package concurrent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// TestDrainWorker_MarksDraining verifies that DrainWorker sets both the
// drainingWorkers map entry and the ActiveTask.Draining flag, and cancels
// the in-flight attempt under activeMu.
func TestDrainWorker_MarksDraining(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	active := &ActiveTask{Cancel: cancel}
	d.activeTasks[0] = active

	ok := d.DrainWorker(0)
	if !ok {
		t.Fatal("DrainWorker should return true for existing worker")
	}

	if !active.Draining.Load() {
		t.Error("ActiveTask.Draining should be true after DrainWorker")
	}

	if _, draining := d.drainingWorkers.Load(0); !draining {
		t.Error("drainingWorkers should contain worker 0")
	}

	select {
	case <-ctx.Done():
		// Interruptive drain cancels the in-flight attempt.
	case <-time.After(time.Second):
		t.Error("DrainWorker must cancel the in-flight attempt context")
	}
}

// TestDrainWorker_MappedNilCancel covers the mapped-nil active-task case:
// an entry registered without a published Cancel still gets flagged — the
// drain is inherited when the next attempt publishes its cancel.
func TestDrainWorker_MappedNilCancel(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	active := &ActiveTask{} // Cancel not yet published
	d.activeTasks[7] = active

	if !d.DrainWorker(7) {
		t.Fatal("DrainWorker should return true for mapped worker without Cancel")
	}
	if !active.Draining.Load() {
		t.Error("Draining flag must be set even when Cancel is nil")
	}
	if _, draining := d.drainingWorkers.Load(7); !draining {
		t.Error("persistent marker must be stored")
	}
}

// TestDrainWorker_NotFound verifies that DrainWorker for a non-existent ID
// still marks the drainingWorkers map (harmless if worker never exists).
func TestDrainWorker_NotFound(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	ok := d.DrainWorker(999)
	if !ok {
		t.Fatal("DrainWorker should return true even for non-existent worker")
	}

	if _, draining := d.drainingWorkers.Load(999); !draining {
		t.Error("drainingWorkers should contain worker 999")
	}
}

// TestDrainWorker_InterruptsInFlightChunk verifies that a drained worker's
// in-flight attempt is cancelled mid-chunk: the worker exits promptly, the
// unfinished range is requeued, and no new task is picked up.
func TestDrainWorker_InterruptsInFlightChunk(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * utils.KiB)
	// Byte latency ensures the worker is still downloading the first chunk
	// when we issue the drain, so the drain flag is checked before the worker
	// can pop the next task.
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithByteLatency(1*time.Microsecond),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "drain_chunk.bin")
	workingPath := destPath + types.IncompleteSuffix

	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("test-id", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   1,
	}
	d := NewConcurrentDownloader("test-id", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	chunkSize := fileSize / 4
	for i := range int64(4) {
		length := chunkSize
		if i == 3 {
			length = fileSize - chunkSize*3
		}
		queue.Push(types.Task{Offset: i * chunkSize, Length: length})
	}

	client := &http.Client{Transport: http.DefaultTransport}

	workerID := int(d.nextWorkerID.Add(1)) - 1
	d.workerWg.Go(func() {
		_ = d.worker(ctx, workerID, []string{server.URL()}, f, queue, fileSize, client)
	})

	waitForActiveTask(t, d, workerID, 2*time.Second)

	d.DrainWorker(workerID)

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		queue.Close()
		t.Fatal("Drained worker did not exit within 30s")
	}

	remaining := queue.Len()
	if remaining < 2 {
		t.Errorf("Expected at least 2 remaining tasks in queue after drain, got %d", remaining)
	}

	d.activeMu.Lock()
	_, exists := d.activeTasks[workerID]
	d.activeMu.Unlock()
	if exists {
		t.Error("Drained worker should not be in activeTasks after exit")
	}

	queue.Close()
	cancel()
}

// TestDrainWorker_IdleWorker_PublicationGap closes the marker-before-
// publication gap: a worker blocked in queue.Pop() holds no activeTasks
// entry when the drain lands, so only the persistent marker is stored. When
// a task is pushed afterwards the worker registers and publishes its
// per-attempt cancel — the marker check inside that same critical section
// must flag and immediately cancel the attempt. The tarpit body proves the
// cancel landed: without it the worker would block in Read until timeout.
func TestDrainWorker_IdleWorker_PublicationGap(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * utils.KiB)
	// 206 headers, zero body bytes, holds the connection — any attempt the
	// drain fails to inherit blocks forever in resp.Body.Read().
	server := newTarpitServer(t, fileSize, 0, 0)

	destPath := filepath.Join(tmpDir, "drain_idle.bin")
	workingPath := destPath + types.IncompleteSuffix

	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
		MaxTaskRetries:   1,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	defer queue.Close()

	workerID := int(d.nextWorkerID.Add(1)) - 1
	d.workerWg.Go(func() {
		_ = d.worker(ctx, workerID, []string{server.URL}, f, queue, fileSize, &http.Client{})
	})

	time.Sleep(100 * time.Millisecond)

	// Drain while the worker is parked in Pop — no ActiveTask exists yet.
	d.DrainWorker(workerID)

	queue.Push(types.Task{Offset: 0, Length: fileSize})
	queue.Push(types.Task{Offset: 0, Length: fileSize})

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("drained worker blocked in tarpit read — publication did not inherit the marker")
	}

	if remaining := queue.Len(); remaining < 1 {
		t.Errorf("cancelled attempt must requeue its residual, got %d queued", remaining)
	}

	d.activeMu.Lock()
	_, exists := d.activeTasks[workerID]
	d.activeMu.Unlock()
	if exists {
		t.Error("drained worker must be removed from activeTasks on exit")
	}
}

// TestDrainWorker_IdleWorker_QueueClose verifies that an idle drained worker
// exits when the queue is closed (e.g., download completion or cancellation).
func TestDrainWorker_IdleWorker_QueueClose(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()

	workerID := int(d.nextWorkerID.Add(1)) - 1
	d.workerWg.Go(func() {
		_ = d.worker(ctx, workerID, []string{"http://localhost:1"}, nil, queue, 0, &http.Client{})
	})

	time.Sleep(100 * time.Millisecond)

	d.DrainWorker(workerID)
	queue.Close()

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Idle worker did not exit after queue close")
	}
}

// TestDrainWorker_InterruptsBlockedRead drains a worker parked in a real
// blocked HTTP body read (206 headers + partial bytes, then a tarpit hold).
// The interruptive drain must unwind the attempt promptly: the written
// prefix stays committed (residual starts after it), the residual range is
// requeued un-shrunk, the worker is removed from activeTasks exactly once,
// and ActiveWorkers returns to zero.
func TestDrainWorker_InterruptsBlockedRead(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	const partial = int64(64 * utils.KiB)
	fileSize := int64(256 * utils.KiB)
	server := newTarpitServer(t, fileSize, partial, 0)

	workingPath := filepath.Join(tmpDir, "drain_block.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("drain-block", fileSize)
	d := NewConcurrentDownloader("drain-block", nil, state, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
		MaxTaskRetries:   1,
	})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	workerID := int(d.nextWorkerID.Add(1)) - 1
	done := make(chan error, 1)
	d.workerWg.Go(func() {
		done <- d.worker(ctx, workerID, []string{server.URL}, f, queue, fileSize, &http.Client{})
	})

	waitForActiveTask(t, d, workerID, 5*time.Second)

	// Wait until the partial prefix is committed and the body read is parked.
	deadline := time.Now().Add(5 * time.Second)
	for {
		d.activeMu.Lock()
		at := d.activeTasks[workerID]
		d.activeMu.Unlock()
		if at == nil {
			t.Fatal("active task disappeared before drain")
		}
		if at.CurrentOffset.Load() >= partial {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker never committed the partial prefix (offset=%d)", at.CurrentOffset.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	d.DrainWorker(workerID)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("drained worker returned non-nil error: %v", err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("interruptive drain did not unwind the blocked body read")
	}

	// Residual coverage: only the committed prefix is skipped — the drained
	// range restarts exactly at the written offset.
	remaining, ok := queue.Pop()
	if !ok {
		t.Fatal("expected the unfinished range to be requeued")
	}
	if remaining.Offset != partial || remaining.Length != fileSize-partial {
		t.Fatalf("residual = [%d+%d), want [%d+%d)", remaining.Offset, remaining.Length, partial, fileSize-partial)
	}

	// Exactly-once cleanup: one map delete, one ActiveWorkers decrement.
	d.activeMu.Lock()
	_, exists := d.activeTasks[workerID]
	d.activeMu.Unlock()
	if exists {
		t.Error("drained worker must be removed from activeTasks on exit")
	}
	if got := state.ActiveWorkers.Load(); got != 0 {
		t.Errorf("ActiveWorkers=%d after drain exit, want 0", got)
	}
}

// TestDrainWorker_NoProgressFuseExempt verifies an intentional drain's
// residual requeue is not counted by the no-progress fuse — even with the
// fuse armed to trip on a single event, the drained worker exits cleanly.
func TestDrainWorker_NoProgressFuseExempt(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressGuardTestLimits(t, 1, 50*time.Millisecond)

	fileSize := int64(64 * utils.KiB)
	server := newTarpitServer(t, fileSize, 8*utils.KiB, 0)

	workingPath := filepath.Join(tmpDir, "drain_fuse.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("drain-fuse", fileSize)
	d := NewConcurrentDownloader("drain-fuse", nil, state, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
		MaxTaskRetries:   1,
	})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	workerID := int(d.nextWorkerID.Add(1)) - 1
	done := make(chan error, 1)
	d.workerWg.Go(func() {
		done <- d.worker(ctx, workerID, []string{server.URL}, f, queue, fileSize, &http.Client{})
	})

	waitForActiveTask(t, d, workerID, 5*time.Second)
	d.DrainWorker(workerID)

	select {
	case err := <-done:
		if errors.Is(err, types.ErrNoProgress) {
			t.Fatal("intentional drain must not trip the no-progress fuse")
		}
		if err != nil {
			t.Fatalf("drained worker returned non-nil error: %v", err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("drained worker did not exit")
	}

	d.noProgressGuard.mu.Lock()
	exhaustions := d.noProgressGuard.exhaustions
	d.noProgressGuard.mu.Unlock()
	if exhaustions != 0 {
		t.Fatalf("drain residual counted by the fuse: exhaustions=%d, want 0", exhaustions)
	}
}

// TestGracePeriod_DownloadVolumeCheck verifies that the grace period
// now also considers download volume (<1MB) in addition to time.
func TestGracePeriod_DownloadVolumeCheck(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,
	}
	state := progress.New("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx := t.Context()

	now := time.Now()

	w0Ctx, w0Cancel := context.WithCancel(ctx)
	w1Ctx, w1Cancel := context.WithCancel(ctx)

	w0 := &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 10 * utils.MiB},
		StartTime: now.Add(-10 * time.Second),
		Speed:     10 * 1024 * 1024,
		Cancel:    w0Cancel,
	}
	w0.CurrentOffset.Store(2 * utils.MiB)

	w1 := &ActiveTask{
		Task:      types.Task{Offset: 10 * utils.MiB, Length: 10 * utils.MiB},
		StartTime: now.Add(-10 * time.Second),
		Speed:     100 * 1024,
		Cancel:    w1Cancel,
	}
	w1.CurrentOffset.Store(10*utils.MiB + 100*utils.KiB)

	d.activeTasks[0] = w0
	d.activeTasks[1] = w1

	d.checkWorkerHealth()

	select {
	case <-w0Ctx.Done():
		t.Error("Worker 0 should NOT have been cancelled (fast worker)")
	default:
	}

	select {
	case <-w1Ctx.Done():
		t.Error("Worker 1 should NOT have been cancelled (download volume < 1MB grace)")
	default:
	}
}

// TestStallDetection_NotBlockedByVolumeGrace verifies that stall detection
// fires even when downloadedBytes < 1MB.
func TestStallDetection_NotBlockedByVolumeGrace(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,
		StallTimeout:          1 * time.Second,
	}
	state := progress.New("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx := t.Context()

	now := time.Now()

	stalledCtx, stalledCancel := context.WithCancel(ctx)

	active := &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 10 * utils.MiB},
		StartTime: now.Add(-30 * time.Second),
		Cancel:    stalledCancel,
	}
	active.LastActivity.Store(now.Add(-30 * time.Second).UnixNano())
	active.Speed = 0
	active.CurrentOffset.Store(0)

	d.activeTasks[0] = active

	d.checkWorkerHealth()

	select {
	case <-stalledCtx.Done():
	default:
		t.Error("Stalled worker with <1MB downloaded should still be cancelled by stall detection")
	}
}

// TestScaleWorkers_Up verifies that ScaleWorkers with positive delta
// spawns new workers and increments nextWorkerID.
func TestScaleWorkers_Up(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "scale_up.bin")
	workingPath := destPath + types.IncompleteSuffix

	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		mirrors:   []string{server.URL()},
		file:      f,
		queue:     queue,
		totalSize: fileSize,
		client:    &http.Client{},
	})
	d.workersActive.Store(true)

	beforeID := d.nextWorkerID.Load()

	result := d.ScaleWorkers(2)

	if result != 2 {
		t.Fatalf("ScaleWorkers(2) returned %d, want 2", result)
	}

	afterID := d.nextWorkerID.Load()
	if afterID-beforeID != 2 {
		t.Errorf("nextWorkerID incremented by %d, want 2", afterID-beforeID)
	}

	queue.Close()

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Scaled workers did not exit after queue close")
	}
}

// TestScaleWorkers_Down verifies that ScaleWorkers with negative delta
// drains the slowest worker and interrupts its in-flight attempt.
func TestScaleWorkers_Down(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.activeTasks[0] = &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 100},
		StartTime: time.Now().Add(-10 * time.Second),
		Cancel:    cancel,
	}
	d.activeTasks[0].Speed = 10 * 1024 * 1024

	w1Ctx, w1Cancel := context.WithCancel(ctx)
	d.activeTasks[1] = &ActiveTask{
		Task:      types.Task{Offset: 100, Length: 100},
		StartTime: time.Now().Add(-10 * time.Second),
		Cancel:    w1Cancel,
	}
	d.activeTasks[1].Speed = 5 * 1024 * 1024

	w2Ctx, w2Cancel := context.WithCancel(ctx)
	d.activeTasks[2] = &ActiveTask{
		Task:      types.Task{Offset: 200, Length: 100},
		StartTime: time.Now().Add(-10 * time.Second),
		Cancel:    w2Cancel,
	}
	d.activeTasks[2].Speed = 100 * 1024

	d.workerDepsPtr.Store(&workerDeps{ctx: ctx})
	d.workersActive.Store(true)

	result := d.ScaleWorkers(-1)

	if result != -1 {
		t.Fatalf("ScaleWorkers(-1) returned %d, want -1", result)
	}

	if !d.activeTasks[2].Draining.Load() {
		t.Error("Slowest worker (ID 2) should be marked as draining")
	}

	if d.activeTasks[0].Draining.Load() {
		t.Error("Worker 0 should NOT be draining")
	}
	if d.activeTasks[1].Draining.Load() {
		t.Error("Worker 1 should NOT be draining")
	}

	select {
	case <-w2Ctx.Done():
		// Interruptive drain cancels the in-flight attempt under activeMu.
	case <-time.After(time.Second):
		t.Error("Drained worker context should be cancelled (interruptive drain)")
	}

	if err := w1Ctx.Err(); err != nil {
		t.Errorf("unselected worker context should remain live, got %v", err)
	}
}

// TestScaleWorkers_Zero verifies that ScaleWorkers(0) is a no-op.
func TestScaleWorkers_Zero(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})
	d.workerDepsPtr.Store(&workerDeps{ctx: context.Background()})
	d.workersActive.Store(true)

	result := d.ScaleWorkers(0)
	if result != 0 {
		t.Errorf("ScaleWorkers(0) returned %d, want 0", result)
	}
}

// TestScaleWorkers_NilContext verifies that ScaleWorkers returns 0
// when workerCtx is nil (no download in progress).
func TestScaleWorkers_NilContext(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	result := d.ScaleWorkers(1)
	if result != 0 {
		t.Errorf("ScaleWorkers(1) with nil ctx returned %d, want 0", result)
	}
}

// TestDrain_ConcurrentAccess verifies that concurrent calls to
// DrainWorker, ScaleWorkers, and activeTasks iteration are race-free.
func TestDrain_ConcurrentAccess(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	ctx := t.Context()

	for i := range 5 {
		_, ccancel := context.WithCancel(ctx)
		d.activeTasks[i] = &ActiveTask{
			Task:      types.Task{Offset: int64(i * 100), Length: 100},
			StartTime: time.Now().Add(-10 * time.Second),
			Cancel:    ccancel,
		}
		d.activeTasks[i].Speed = float64(1000 * (i + 1))
	}

	d.workerDepsPtr.Store(&workerDeps{ctx: ctx})
	d.workersActive.Store(true)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			d.DrainWorker(n % 5)
			d.activeMu.Lock()
			for id := range d.activeTasks {
				_ = id
			}
			d.activeMu.Unlock()
			d.ScaleWorkers(-1)
		}(i)
	}
	wg.Wait()
}

// waitForActiveTask waits until the given worker ID appears in activeTasks
// or times out.
func waitForActiveTask(t *testing.T, d *ConcurrentDownloader, workerID int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		d.activeMu.Lock()
		_, exists := d.activeTasks[workerID]
		d.activeMu.Unlock()
		if exists {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Worker %d did not appear in activeTasks within %v", workerID, timeout)
}

// TestScaleWorkers_WaitGroupReuseProtection verifies that ScaleWorkers
// returns 0 (no Add) after workersActive is set to false (post-Wait).
func TestScaleWorkers_WaitGroupReuseProtection(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	ctx := t.Context()

	d.workerDepsPtr.Store(&workerDeps{ctx: ctx})
	d.workersActive.Store(false)

	result := d.ScaleWorkers(1)
	if result != 0 {
		t.Errorf("ScaleWorkers(1) after workersActive=false returned %d, want 0", result)
	}
}

// TestScaleWorkers_UpDuringActiveWorkers verifies that while workers
// are still running (Wait() not yet returned), workersActive stays true and
// ScaleWorkers(delta>0) can Add new workers instead of being blocked.
func TestScaleWorkers_UpDuringActiveWorkers(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "scale_up_active.bin")
	workingPath := destPath + types.IncompleteSuffix

	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		mirrors:   []string{server.URL()},
		file:      f,
		queue:     queue,
		totalSize: fileSize,
		client:    &http.Client{},
	})

	d.workersActive.Store(true)

	d.workerWg.Go(func() {
		time.Sleep(200 * time.Millisecond)
	})

	result := d.ScaleWorkers(1)
	if result != 1 {
		t.Fatalf("ScaleWorkers(1) during active workers returned %d, want 1", result)
	}

	queue.Close()

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("workers did not exit after queue close")
	}
}

// TestConnErrorDetection_503 verifies that a 503 response from the server
// increments consecutiveConnErrors on ProgressState.
func TestConnErrorDetection_503(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	server := testutil.NewMockServerT(t,
		testutil.WithHandler(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "conn_503.bin")
	workingPath := destPath + types.IncompleteSuffix

	if f, err := os.Create(workingPath); err == nil {
		_ = f.Close()
	}

	state := progress.New("conn-503-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		MaxTaskRetries:            1,
	}

	d := NewConcurrentDownloader("conn-503-test", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		for {
			if state.GetConnErrors() > 0 {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	_ = d.Download(ctx, server.URL(), nil, nil, destPath, fileSize)

	if got := state.GetConnErrors(); got == 0 {
		t.Error("expected consecutiveConnErrors > 0 after 503 responses, got 0")
	}
}

// TestConnErrorDetection_429 verifies that a 429 response from the server
// increments consecutiveConnErrors on ProgressState.
func TestConnErrorDetection_429(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	server := testutil.NewMockServerT(t,
		testutil.WithHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		}),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "conn_429.bin")
	workingPath := destPath + types.IncompleteSuffix

	if f, err := os.Create(workingPath); err == nil {
		_ = f.Close()
	}

	state := progress.New("conn-429-test", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerDownload: 1}

	d := NewConcurrentDownloader("conn-429-test", nil, state, runtime)
	d.hostLimiter = transport.NewHostRateLimiter()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	go func() {
		for {
			if state.GetConnErrors() > 0 {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()

	_ = d.Download(ctx, server.URL(), nil, nil, destPath, fileSize)

	if got := state.GetConnErrors(); got == 0 {
		t.Error("expected consecutiveConnErrors > 0 after 429 responses, got 0")
	}
}

// TestScaleWorkers_ConcurrentScaleUp verifies that concurrent ScaleWorkers
// calls with workersActive=true are race-free.
func TestScaleWorkers_ConcurrentScaleUp(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "concurrent_scale.bin")
	workingPath := destPath + types.IncompleteSuffix

	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		mirrors:   []string{server.URL()},
		file:      f,
		queue:     queue,
		totalSize: fileSize,
		client:    &http.Client{},
	})
	d.workersActive.Store(true)

	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			d.ScaleWorkers(1)
		})
	}
	wg.Wait()

	queue.Close()

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Workers did not exit after queue close")
	}
}
