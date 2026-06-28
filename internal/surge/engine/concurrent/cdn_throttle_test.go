package concurrent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/testutil"
)

// TestCheckWorkerHealth_PublishesSessionFields verifies the new CDN fingerprint
// fields (WorkerStartUnix/SessionBytes/HTTPStatus) are populated from the
// per-worker session map and ActiveTask.LastHTTPStatus.
func TestCheckWorkerHealth_PublishesSessionFields(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := types.NewProgressState("sess-test", 1024*1024)
	d.State = state

	now := time.Now()
	active := &ActiveTask{
		Task:        types.Task{Offset: 4096, Length: 1024},
		StartTime:   now,
		WindowStart: now,
		workerID:    7,
	}
	active.CurrentOffset.Store(4608)
	active.StopAt.Store(5120)
	active.LastActivity.Store(now.UnixNano())
	active.LastHTTPStatus.Store(206)

	d.activeTasks[7] = active

	startUnix := now.Add(-30 * time.Second).UnixNano()
	sess := &workerSession{startUnix: startUnix}
	sess.sessionBytes.Store(5 * 1024 * 1024)
	d.workerSessions.Store(7, sess)

	d.checkWorkerHealth()

	stats := state.GetWorkerStats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(stats))
	}
	s := stats[0]
	if s.HTTPStatus != 206 {
		t.Errorf("HTTPStatus = %d, want 206", s.HTTPStatus)
	}
	if s.WorkerStartUnix != startUnix {
		t.Errorf("WorkerStartUnix = %d, want %d", s.WorkerStartUnix, startUnix)
	}
	if s.SessionBytes != 5*1024*1024 {
		t.Errorf("SessionBytes = %d, want 5MB", s.SessionBytes)
	}
}

// TestCheckWorkerHealth_SessionMissingFillsZero verifies a worker absent from
// the session map (extreme race while exiting) yields zero session fields
// without a nil deref.
func TestCheckWorkerHealth_SessionMissingFillsZero(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)
	state := types.NewProgressState("miss-test", 1024)
	d.State = state

	now := time.Now()
	active := &ActiveTask{
		Task:        types.Task{Offset: 0, Length: 1024},
		StartTime:   now,
		WindowStart: now,
	}
	active.LastActivity.Store(now.UnixNano())
	d.activeTasks[3] = active
	// No workerSessions entry for worker 3.

	d.checkWorkerHealth()

	stats := state.GetWorkerStats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(stats))
	}
	if stats[0].WorkerStartUnix != 0 || stats[0].SessionBytes != 0 {
		t.Errorf("expected zero session fields, got start=%d bytes=%d",
			stats[0].WorkerStartUnix, stats[0].SessionBytes)
	}
}

// TestWorkerSession_SurvivesChunkSwitch verifies that WorkerStartUnix stays
// constant and SessionBytes is sourced from the per-worker session (not the
// per-chunk ActiveTask) across simulated chunk switches where ActiveTask is
// rebuilt.
func TestWorkerSession_SurvivesChunkSwitch(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)
	state := types.NewProgressState("chunk-test", 1024*1024)
	d.State = state

	startUnix := time.Now().Add(-60 * time.Second).UnixNano()
	sess := &workerSession{startUnix: startUnix}
	sess.sessionBytes.Store(10 * 1024 * 1024)
	d.workerSessions.Store(1, sess)

	// Chunk 1: build a fresh ActiveTask (simulating worker.go per-chunk creation).
	now := time.Now()
	a1 := &ActiveTask{Task: types.Task{Offset: 0, Length: 1024}, StartTime: now, WindowStart: now, workerID: 1}
	a1.CurrentOffset.Store(1024)
	a1.StopAt.Store(1024)
	a1.LastActivity.Store(now.UnixNano())
	d.activeTasks[1] = a1
	d.checkWorkerHealth()
	first := state.GetWorkerStats()[0]

	// Chunk 2: ActiveTask is rebuilt (new StartTime, offset reset) but session persists.
	now2 := time.Now()
	a2 := &ActiveTask{Task: types.Task{Offset: 1024, Length: 1024}, StartTime: now2, WindowStart: now2, workerID: 1}
	a2.CurrentOffset.Store(1024)
	a2.StopAt.Store(2048)
	a2.LastActivity.Store(now2.UnixNano())
	d.activeTasks[1] = a2
	// Session bytes grew (worker downloaded more between chunks).
	sess.sessionBytes.Store(20 * 1024 * 1024)
	d.checkWorkerHealth()
	second := state.GetWorkerStats()[0]

	if first.WorkerStartUnix != second.WorkerStartUnix {
		t.Errorf("WorkerStartUnix changed across chunks: %d -> %d (must be connection-granularity)",
			first.WorkerStartUnix, second.WorkerStartUnix)
	}
	if second.SessionBytes != 20*1024*1024 {
		t.Errorf("SessionBytes = %d, want 20MB (session-sourced, not per-chunk)", second.SessionBytes)
	}
}

// TestSlowThresholdOrDefault verifies the override sentinel: unset returns the
// default; set(0) returns 0 (distinguishable from unset).
func TestSlowThresholdOrDefault(t *testing.T) {
	var d ConcurrentDownloader
	if got := d.slowThresholdOrDefault(0.30); got != 0.30 {
		t.Errorf("unset: got %v, want 0.30 default", got)
	}
	d.SetSlowWorkerThreshold(0)
	if got := d.slowThresholdOrDefault(0.30); got != 0 {
		t.Errorf("set(0): got %v, want 0 (takeover disables relative slow cancel)", got)
	}
	d.SetSlowWorkerThreshold(0.5)
	if got := d.slowThresholdOrDefault(0.30); got != 0.5 {
		t.Errorf("set(0.5): got %v, want 0.5", got)
	}
}

// TestSetSlowWorkerThreshold_Zero_DisablesRelativeSlow verifies that a runtime
// override of 0 disables the engine's relative slow-speed cancel while stall
// detection stays armed.
func TestSetSlowWorkerThreshold_Zero_DisablesRelativeSlow(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,
	}
	state := types.NewProgressState("thr-test", 1000)
	d := NewConcurrentDownloader("thr-test", nil, state, runtime)
	d.SetSlowWorkerThreshold(0) // policy takeover

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()

	// Slow worker (would be cancelled by relative check if threshold > 0).
	_, slowCancel := context.WithCancel(ctx)
	d.activeTasks[0] = &ActiveTask{StartTime: now.Add(-10 * time.Second), Speed: 1 * 1024 * 1024, Cancel: slowCancel}
	d.activeTasks[0].CurrentOffset.Store(2 * 1024 * 1024) // past volume grace
	// Fast worker so meanSpeed > 0.
	_, fastCancel := context.WithCancel(ctx)
	d.activeTasks[1] = &ActiveTask{StartTime: now.Add(-10 * time.Second), Speed: 10 * 1024 * 1024, Cancel: fastCancel}
	d.activeTasks[1].CurrentOffset.Store(2 * 1024 * 1024)

	d.checkWorkerHealth()

	// Slow worker should NOT be cancelled (relative slow disabled by override 0).
	// Verify via a fresh context we can't reuse slowCancel's ctx; use a done check.
	slowCtx, slowCancel2 := context.WithCancel(ctx)
	defer slowCancel2()
	d.activeTasks[0].Cancel = slowCancel2
	// Re-run to be sure: the relative check must not fire.
	d.checkWorkerHealth()
	select {
	case <-slowCtx.Done():
		t.Error("slow worker should NOT be cancelled when threshold override is 0")
	default:
		// success
	}
}

// TestSetSlowWorkerThreshold_Zero_StallStillFires verifies stall detection
// remains armed after a threshold override of 0.
func TestSetSlowWorkerThreshold_Zero_StallStillFires(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,
		StallTimeout:          1 * time.Second,
	}
	state := types.NewProgressState("stall-test", 1000)
	d := NewConcurrentDownloader("stall-test", nil, state, runtime)
	d.SetSlowWorkerThreshold(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()

	stallCtx, stallCancel := context.WithCancel(ctx)
	active := &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 10 * 1024 * 1024},
		StartTime: now.Add(-10 * time.Second),
		Cancel:    stallCancel,
	}
	active.LastActivity.Store(now.Add(-2 * time.Second).UnixNano()) // exceeds 1s stall
	active.CurrentOffset.Store(2 * 1024 * 1024)
	d.activeTasks[0] = active

	d.checkWorkerHealth()

	select {
	case <-stallCtx.Done():
		// success: stall detection fired despite threshold override 0
	default:
		t.Error("stalled worker should still be cancelled by stall detection")
	}
}

// TestKillWorker_CancelsActiveTask verifies KillWorker cancels the targeted
// worker's task context and returns false for a worker with no active task.
func TestKillWorker_CancelsActiveTask(t *testing.T) {
	d := NewConcurrentDownloader("kill-test", nil, nil, &types.RuntimeConfig{})
	d.activeTasks = make(map[int]*ActiveTask)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	targetCtx, targetCancel := context.WithCancel(ctx)
	d.activeTasks[5] = &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 1024},
		StartTime: time.Now(),
		Cancel:    targetCancel,
	}

	if !d.KillWorker(5) {
		t.Fatal("KillWorker(5) returned false, want true")
	}
	select {
	case <-targetCtx.Done():
		// success: task context cancelled
	default:
		t.Error("KillWorker did not cancel the target task context")
	}

	// Already-cancelled / drained worker: KillWorker still returns true while the
	// entry exists (cancel is idempotent). A missing worker returns false.
	if d.KillWorker(99) {
		t.Error("KillWorker(99) returned true for a worker with no active task")
	}
}

// TestKillWorker_WorkerStaysAlive verifies the core CDN-throttle contract:
// KillWorker cancels the in-flight chunk, the worker goroutine does NOT exit,
// remaining bytes are requeued, and the worker continues to drain the queue on
// a fresh connection.
func TestKillWorker_WorkerStaysAlive(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(2 * 1024 * 1024) // 2MB, two 1MB chunks
	var reqCount atomic.Int32
	release := make(chan struct{})
	handler := func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		rangeHdr := r.Header.Get("Range")
		start, end := int64(0), fileSize-1
		if rangeHdr != "" {
			fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
		w.WriteHeader(http.StatusPartialContent)
		// First request: write a little then block until killed or request cancelled.
		if reqCount.Load() == 1 {
			_, _ = w.Write(make([]byte, 1024))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.CopyN(w, &zeroReader{}, end-start+1)
	}
	server := testutil.NewMockServerT(t, testutil.WithHandler(handler))
	defer server.Close()
	// release is closed after the kill so the blocking first handler returns
	// (the server cannot detect the client disconnect while the handler is
	// blocked on a channel rather than doing I/O).

	workingPath := filepath.Join(tmpDir, "kill_stay.bin") + types.IncompleteSuffix
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := types.NewProgressState("kill-stay", fileSize)
	d := NewConcurrentDownloader("kill-stay", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		WorkerBufferSize:          32 * 1024,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: 1024 * 1024})
	queue.Push(types.Task{Offset: 1024 * 1024, Length: 1024 * 1024})

	workerID := int(d.nextWorkerID.Add(1)) - 1
	d.workerWg.Add(1)
	go func() {
		defer d.workerWg.Done()
		_ = d.worker(ctx, workerID, []string{server.URL()}, f, queue, fileSize, &http.Client{})
	}()

	// Wait for the worker to be mid-download on the first (blocking) chunk.
	waitForActiveTask(t, d, workerID, 2*time.Second)

	// Kill the in-flight chunk — socket destroyed, remaining requeued, worker
	// must NOT exit.
	if !d.KillWorker(workerID) {
		close(release)
		cancel()
		queue.Close()
		t.Fatal("KillWorker returned false while worker was active")
	}
	// Unblock the first (blocking) handler so its goroutine can return; its
	// post-select writes will fail on the already-cancelled connection, which
	// is harmless.
	close(release)
	// Close the queue so the worker can exit after draining all remaining work
	// (Push still works after Close; Pop returns false only once empty).
	queue.Close()

	// The worker should stay alive and drain the queue (requeued remaining +
	// second chunk). If it had exited on Kill, workerWg would complete with the
	// queue still holding tasks and the file incomplete.
	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success: worker stayed alive and finished all remaining work
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("worker did not complete remaining work after Kill (goroutine should stay alive)")
	}

	// Queue should be fully drained (all tasks processed after the kill).
	if remaining := queue.Len(); remaining != 0 {
		t.Errorf("expected empty queue after worker drained all work, got %d", remaining)
	}
}

type zeroReader struct{}

func (z *zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// TestProgressState_KillAndSlowThresholdBridge verifies the fn-pointer bridges
// route KillWorker/SetSlowWorkerThreshold to the registered callbacks and are
// cleared by SessionReset.
func TestProgressState_KillAndSlowThresholdBridge(t *testing.T) {
	state := types.NewProgressState("bridge-test", 1024)

	// Kill bridge.
	var killed atomic.Int32
	state.SetKillWorkerFn(func(id int) bool {
		killed.Store(int32(id))
		return true
	})
	if !state.KillWorker(42) {
		t.Fatal("KillWorker returned false with fn registered")
	}
	if killed.Load() != 42 {
		t.Errorf("kill bridge routed to %d, want 42", killed.Load())
	}

	// Slow threshold bridge.
	var thr atomic.Value
	thr.Store(float64(-1))
	state.SetSetSlowThresholdFn(func(v float64) { thr.Store(v) })
	state.SetSlowWorkerThreshold(0)
	if got := thr.Load().(float64); got != 0 {
		t.Errorf("slow threshold bridge got %v, want 0", got)
	}

	// SessionReset clears both.
	state.SessionReset()
	if state.KillWorker(1) {
		t.Error("KillWorker should return false after SessionReset cleared the fn")
	}
	state.SetSlowWorkerThreshold(0.5) // no-op, fn is nil
}

// TestProgressState_KillAndSlowThresholdBridge_Concurrent exercises concurrent
// access to the new bridges (registered/unregistered) under -race.
func TestProgressState_KillAndSlowThresholdBridge_Concurrent(t *testing.T) {
	state := types.NewProgressState("race-bridge", 1024)
	state.SetKillWorkerFn(func(id int) bool { return true })
	state.SetSetSlowThresholdFn(func(v float64) {})

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = state.KillWorker(i)
				state.SetSlowWorkerThreshold(0)
			}
		}()
	}
	wg.Wait()
}
