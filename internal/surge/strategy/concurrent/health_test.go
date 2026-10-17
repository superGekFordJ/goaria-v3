package concurrent

import (
	"context"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func TestHealth_LastManStanding(t *testing.T) {
	// 1. Setup mock state with high historical speed
	// Say we downloaded 100MB in 10s => 10MB/s global average
	// FORK-PATCH: use 200MB total so VP (100MB) < total, avoiding the
	// GetProgress clamp safety net that would mask the speed check.
	state := progress.New("test", 200*1024*1024)
	state.Bytes.VerifiedProgress.Store(100 * 1024 * 1024)

	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0, // Instant check
	}

	d := NewConcurrentDownloader("test", nil, state, runtime)

	// 2. Add one active task that is SLOW
	// Global is 10MB/s (100MB / 10s)
	// Worker is 1MB/s (should be < 0.5 * 10 = 5MB/s).

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Now()

	// Hack: Set State.StartTime to 10s ago
	// This is safe here because we are single-threaded in setup
	state.Session.SetStartTimeForTest(now.Add(-10 * time.Second))

	active := &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 10 * 1024 * 1024},
		StartTime: now.Add(-10 * time.Second), // Started long ago
		Speed:     1 * 1024 * 1024,            // 1 MB/s
		Cancel:    cancel,
	}
	active.CurrentOffset.Store(2 * 1024 * 1024) // >1MB, past volume grace

	d.activeTasks[0] = active

	// 3. Run Check
	d.checkWorkerHealth()

	// 4. Verify Cancellation
	select {
	case <-ctx.Done():
		// Success: context cancelled
	default:
		t.Errorf("Worker should have been cancelled (Global Speed ~10MB/s, Worker 1MB/s)")
	}
}

func TestHealth_MultipleWorkers(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,
	}
	state := progress.New("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx := t.Context()

	now := time.Now()

	// 1. Setup multiple workers
	// Worker 0: 10 MB/s
	// Worker 1: 10 MB/s
	// Worker 2: 1 MB/s (Slow)
	// Mean = 7 MB/s. Threshold = 3.5 MB/s. Worker 2 < 3.5 => Cancel.

	w0Ctx, w0Cancel := context.WithCancel(ctx)
	w1Ctx, w1Cancel := context.WithCancel(ctx)
	w2Ctx, w2Cancel := context.WithCancel(ctx)

	d.activeTasks[0] = &ActiveTask{StartTime: now.Add(-10 * time.Second), Speed: 10 * 1024 * 1024, Cancel: w0Cancel}
	d.activeTasks[0].CurrentOffset.Store(2 * 1024 * 1024) // >1MB, past volume grace
	d.activeTasks[1] = &ActiveTask{StartTime: now.Add(-10 * time.Second), Speed: 10 * 1024 * 1024, Cancel: w1Cancel}
	d.activeTasks[1].CurrentOffset.Store(2 * 1024 * 1024) // >1MB, past volume grace
	d.activeTasks[2] = &ActiveTask{StartTime: now.Add(-10 * time.Second), Speed: 1 * 1024 * 1024, Cancel: w2Cancel}
	d.activeTasks[2].CurrentOffset.Store(2 * 1024 * 1024) // >1MB, past volume grace

	d.checkWorkerHealth()

	// Verify Worker 2 cancelled
	select {
	case <-w2Ctx.Done():
		// Success
	default:
		t.Error("Worker 2 should have been cancelled")
	}

	// Verify others NOT cancelled
	select {
	case <-w0Ctx.Done():
		t.Error("Worker 0 should NOT have been cancelled")
	default:
	}
	select {
	case <-w1Ctx.Done():
		t.Error("Worker 1 should NOT have been cancelled")
	default:
	}
}

func TestHealth_GracePeriod(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 5 * time.Second,
	}
	state := progress.New("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx := t.Context()

	now := time.Now()

	// 1. Setup workers
	// Worker 0: 10 MB/s (Old)
	// Worker 1: 0.1 MB/s (New, within grace period) -> Should NOT cancel despite being slow

	w0Ctx, w0Cancel := context.WithCancel(ctx)
	w1Ctx, w1Cancel := context.WithCancel(ctx)

	d.activeTasks[0] = &ActiveTask{StartTime: now.Add(-10 * time.Second), Speed: 10 * 1024 * 1024, Cancel: w0Cancel}
	d.activeTasks[0].CurrentOffset.Store(2 * 1024 * 1024) // >1MB, past volume grace
	d.activeTasks[1] = &ActiveTask{StartTime: now.Add(-1 * time.Second), Speed: 100 * 1024, Cancel: w1Cancel}

	d.checkWorkerHealth()

	// Verify Worker 1 NOT cancelled due to grace period
	select {
	case <-w1Ctx.Done():
		t.Error("Worker 1 should NOT have been cancelled (Grace Period)")
	default:
		// Success
	}

	// Verify Worker 0 NOT cancelled (Fast enough)
	select {
	case <-w0Ctx.Done():
		t.Error("Worker 0 should NOT have been cancelled")
	default:
	}
}

func TestHealth_StallDetection(t *testing.T) {
	// A worker that has received no data for StallTimeout should be cancelled
	// regardless of speed comparison
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,               // Instant check
		StallTimeout:          1 * time.Second, // Short timeout for test
	}
	state := progress.New("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx := t.Context()

	now := time.Now()

	// Worker with last activity 2 seconds ago (exceeds 1s StallTimeout)
	stalledCtx, stalledCancel := context.WithCancel(ctx)
	active := &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 10 * 1024 * 1024},
		StartTime: now.Add(-10 * time.Second),
		Cancel:    stalledCancel,
	}
	active.LastActivity.Store(now.Add(-2 * time.Second).UnixNano()) // Stalled for 2s
	active.Speed = 5 * 1024 * 1024                                  // 5 MB/s (fast speed, but stalled)
	active.CurrentOffset.Store(2 * 1024 * 1024)                     // >1MB, past volume grace
	d.activeTasks[0] = active

	d.checkWorkerHealth()

	// Verify stalled worker was cancelled
	select {
	case <-stalledCtx.Done():
		// Success: stall detected and cancelled
	default:
		t.Error("Stalled worker should have been cancelled")
	}
}

func TestHealth_ZeroStallTimeoutDisablesStallDetection(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,
		StallTimeout:          0, // Disabled
	}
	state := progress.New("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx := t.Context()

	now := time.Now()

	stalledCtx, stalledCancel := context.WithCancel(ctx)
	active := &ActiveTask{
		StartTime: now.Add(-10 * time.Second),
		Cancel:    stalledCancel,
	}
	active.LastActivity.Store(now.Add(-2 * time.Second).UnixNano()) // Stalled for 2s
	active.Speed = 5 * 1024 * 1024
	active.CurrentOffset.Store(2 * 1024 * 1024) // >1MB, past volume grace
	d.activeTasks[0] = active

	d.checkWorkerHealth()

	// Verify stalled worker was NOT cancelled
	select {
	case <-stalledCtx.Done():
		t.Error("Stalled worker should NOT have been cancelled since stall detection is disabled")
	default:
		// Success
	}
}

func TestHealth_ZeroSlowWorkerThresholdDisablesSlowCheck(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0, // Disabled
		SlowWorkerGracePeriod: 0,
	}
	state := progress.New("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx := t.Context()

	now := time.Now()

	_, w0Cancel := context.WithCancel(ctx)
	w1Ctx, w1Cancel := context.WithCancel(ctx)

	d.activeTasks[0] = &ActiveTask{StartTime: now.Add(-10 * time.Second), Speed: 10 * 1024 * 1024, Cancel: w0Cancel}
	d.activeTasks[0].CurrentOffset.Store(2 * 1024 * 1024) // >1MB, past volume grace
	d.activeTasks[1] = &ActiveTask{StartTime: now.Add(-10 * time.Second), Speed: 1 * 1024 * 1024, Cancel: w1Cancel}
	d.activeTasks[1].CurrentOffset.Store(2 * 1024 * 1024) // >1MB, past volume grace

	d.checkWorkerHealth()

	// Verify slow worker (Worker 1) was NOT cancelled
	select {
	case <-w1Ctx.Done():
		t.Error("Worker 1 should NOT have been cancelled since slow worker checks are disabled")
	default:
		// Success
	}
}

func TestHealth_PublishesWorkerStats(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 5 * time.Second, // skip cancel; only assert telemetry
	}
	state := progress.New("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	active := &ActiveTask{
		Task:      types.Task{Offset: 100, Length: 1000},
		StartTime: time.Now(),
		Speed:     1024,
	}
	active.CurrentOffset.Store(300)
	active.StopAt.Store(1100)
	active.RetryCount.Store(2)
	active.LastHTTPStatus.Store(206)
	d.activeTasks[7] = active

	d.checkWorkerHealth()

	stats := state.GetWorkerStats()
	if len(stats) != 1 {
		t.Fatalf("GetWorkerStats len = %d, want 1", len(stats))
	}
	s := stats[0]
	if s.WorkerID != 7 {
		t.Errorf("WorkerID = %d, want 7", s.WorkerID)
	}
	if s.RetryCount != 2 {
		t.Errorf("RetryCount = %d, want 2", s.RetryCount)
	}
	if s.HTTPStatus != 206 {
		t.Errorf("HTTPStatus = %d, want 206", s.HTTPStatus)
	}
	if s.ChunkStart != 100 || s.ChunkOffset != 300 {
		t.Errorf("chunk fields ChunkStart=%d ChunkOffset=%d", s.ChunkStart, s.ChunkOffset)
	}
	if s.ChunkLength != 1000 {
		t.Errorf("ChunkLength = %d, want 1000", s.ChunkLength)
	}
	if s.WorkerStartUnix != 0 || s.SessionBytes != 0 {
		t.Errorf("missing session should be zero: start=%d bytes=%d", s.WorkerStartUnix, s.SessionBytes)
	}
}

// TestHealth_SingleWorkerOverrideHandoff pins the len(activeTasks)==1 gate on
// the zero-override fallback: the CDN detector's threshold takeover must not
// be bypassed while multiple tasks are active — even when only one reports a
// positive speed sample — but a leftover override=0 must not starve the
// engine's globalSpeed fallback once a single task remains.
func TestHealth_SingleWorkerOverrideHandoff(t *testing.T) {
	now := time.Now()
	// ~10 MiB/s global session speed (100 MiB verified over ~10s).
	state := progress.New("test", 200*1024*1024)
	state.Bytes.VerifiedProgress.Store(100 * 1024 * 1024)
	state.Session.SetStartTimeForTest(now.Add(-10 * time.Second))
	d := NewConcurrentDownloader("test", nil, state, &types.RuntimeConfig{
		SlowWorkerThreshold:   0.30,
		SlowWorkerGracePeriod: 0,
	})
	d.SetSlowWorkerThreshold(0)

	parent := t.Context()
	// newActive returns a worker past grace/volume/stall guards, isolated to
	// the relative-speed check. The returned ctx fires when health cancels it.
	newActive := func(speed float64) (*ActiveTask, context.Context) {
		wCtx, wCancel := context.WithCancel(parent)
		at := &ActiveTask{
			Task:      types.Task{Offset: 0, Length: 10 * utils.MiB},
			StartTime: now.Add(-10 * time.Second),
			Speed:     speed,
			Cancel:    wCancel,
		}
		at.CurrentOffset.Store(2 * utils.MiB) // >1MiB volume grace
		at.StopAt.Store(10 * utils.MiB)
		at.LastActivity.Store(now.UnixNano())
		return at, wCtx
	}
	cancelled := func(c context.Context) bool {
		select {
		case <-c.Done():
			return true
		default:
			return false
		}
	}

	slow, slowCtx := newActive(25 * 1024) // 25 KiB/s << 0.30 * ~10MiB/s
	zero, zeroCtx := newActive(0)         // connected but no speed sample yet

	t.Run("multi_active_override_zero_blocks_cancel", func(t *testing.T) {
		// speedCount==1 (only slow reports speed) but len(activeTasks)==2 —
		// override=0 stays authoritative, no engine-side cancel.
		d.activeTasks[0] = slow
		d.activeTasks[1] = zero
		d.checkWorkerHealth()
		if cancelled(slowCtx) {
			t.Error("override=0 must hold with 2 active tasks even when speedCount==1")
		}
		if cancelled(zeroCtx) {
			t.Error("zero-speed worker must not be cancelled")
		}
	})

	t.Run("single_active_override_zero_falls_back_to_runtime", func(t *testing.T) {
		// Second worker done — a single active task remains; the leftover
		// override=0 must fall back to the Runtime threshold.
		d.activeMu.Lock()
		delete(d.activeTasks, 1)
		d.activeMu.Unlock()
		d.checkWorkerHealth()
		if !cancelled(slowCtx) {
			t.Error("single active task with leftover override=0 must fall back to Runtime threshold")
		}
	})

	t.Run("single_active_positive_override_respected", func(t *testing.T) {
		// A positive override still beats the RuntimeConfig threshold on the
		// single-active path: 4MiB/s is above 0.30*global (3MiB) but below
		// 0.50*global (5MiB) — only the override can explain a cancel.
		d2 := NewConcurrentDownloader("test-pos", nil, state, &types.RuntimeConfig{
			SlowWorkerThreshold:   0.30,
			SlowWorkerGracePeriod: 0,
		})
		d2.SetSlowWorkerThreshold(0.50)
		at, wCtx := newActive(4 * utils.MiB)
		d2.activeTasks[0] = at
		d2.checkWorkerHealth()
		if !cancelled(wCtx) {
			t.Error("positive override=0.5 must govern the single-active relative check")
		}
	})

	t.Run("single_active_runtime_zero_stays_disabled", func(t *testing.T) {
		// An explicit Runtime threshold of 0 is a legitimate disable even in
		// the single-active fallback — nothing is hardcoded back.
		d3 := NewConcurrentDownloader("test-zero", nil, state, &types.RuntimeConfig{
			SlowWorkerThreshold:   0,
			SlowWorkerGracePeriod: 0,
		})
		d3.SetSlowWorkerThreshold(0)
		at, wCtx := newActive(25 * 1024)
		d3.activeTasks[0] = at
		d3.checkWorkerHealth()
		if cancelled(wCtx) {
			t.Error("Runtime threshold=0 must stay disabled even for the last worker")
		}
	})
}
