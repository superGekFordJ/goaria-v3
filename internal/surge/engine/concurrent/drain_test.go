package concurrent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine"
	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/utils"
)

// TestDrainWorker_MarksDraining verifies that DrainWorker sets both the
// drainingWorkers map entry and the ActiveTask.Draining flag.
func TestDrainWorker_MarksDraining(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	_, cancel := context.WithCancel(context.Background())
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

// TestDrainWorker_ExitsAfterCurrentChunk verifies that a drained worker
// completes its current chunk but does not pick up new tasks from the queue.
func TestDrainWorker_ExitsAfterCurrentChunk(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(4 * utils.MiB)
	// Moderate byte latency ensures the worker is still downloading the first
	// chunk when we issue the drain, so the drain flag is checked before
	// the worker can pop the next task. 2µs/byte ≈ 2s per 1MB chunk.
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithByteLatency(2*time.Microsecond),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "drain_chunk.bin")
	workingPath := destPath + types.IncompleteSuffix

	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := types.NewProgressState("test-id", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   1,
	}
	d := NewConcurrentDownloader("test-id", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	chunkSize := fileSize / 4
	for i := int64(0); i < 4; i++ {
		length := chunkSize
		if i == 3 {
			length = fileSize - chunkSize*3
		}
		queue.Push(types.Task{Offset: i * chunkSize, Length: length})
	}

	client := &http.Client{Transport: http.DefaultTransport}

	workerID := int(d.nextWorkerID.Add(1)) - 1
	d.workerWg.Add(1)
	go func() {
		defer d.workerWg.Done()
		_ = d.worker(ctx, workerID, []string{server.URL()}, f, queue, fileSize, client)
	}()

	// Wait for worker to pick up the first task
	waitForActiveTask(t, d, workerID, 2*time.Second)

	// Drain the worker — it should finish the current chunk then exit
	d.DrainWorker(workerID)

	// Wait for the worker to exit (it should exit after completing current chunk)
	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Worker exited successfully
	case <-time.After(30 * time.Second):
		cancel()
		queue.Close()
		t.Fatal("Drained worker did not exit within 30s")
	}

	// The queue should still have remaining tasks (worker only did 1 chunk).
	// With 4 tasks and drain after the first, at least 2 should remain
	// (the worker completes 1, drain prevents popping the rest).
	remaining := queue.Len()
	if remaining < 2 {
		t.Errorf("Expected at least 2 remaining tasks in queue after drain, got %d", remaining)
	}

	// Verify the worker is no longer in activeTasks
	d.activeMu.Lock()
	_, exists := d.activeTasks[workerID]
	d.activeMu.Unlock()
	if exists {
		t.Error("Drained worker should not be in activeTasks after exit")
	}

	// Clean up remaining tasks
	queue.Close()
	cancel()
}

// TestDrainWorker_IdleWorker_DesignLimit documents and verifies the design
// limitation of draining an idle worker: a worker blocked on queue.Pop()
// cannot be interrupted by the drain flag. If a task arrives after drain,
// the worker will execute it (completing the in-flight task) and then exit
// on the next loop iteration when the drain check fires.
//
// This is acceptable because:
// 1. The worker still exits (no permanent leak)
// 2. The task is completed (no data loss)
// 3. The TCP connection still returns to the idle pool after the task
//
// A true "interrupt blocked Pop" would require a StopWaiting() method on
// TaskQueue, which adds complexity not justified for v1.
func TestDrainWorker_IdleWorker_DesignLimit(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithByteLatency(20*time.Microsecond),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "drain_idle.bin")
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

	workerID := int(d.nextWorkerID.Add(1)) - 1
	d.workerWg.Add(1)
	go func() {
		defer d.workerWg.Done()
		_ = d.worker(ctx, workerID, []string{server.URL()}, f, queue, fileSize, &http.Client{})
	}()

	// Wait for worker to be idle (waiting on Pop)
	time.Sleep(100 * time.Millisecond)

	// Drain the idle worker
	d.DrainWorker(workerID)

	// Push a task — the idle worker is blocked on Pop, so it will receive
	// this task, execute it, then on the next loop iteration the drain
	// check fires and the worker exits.
	queue.Push(types.Task{Offset: 0, Length: fileSize})

	// Push a second task that should NOT be picked up after drain
	queue.Push(types.Task{Offset: 0, Length: 1 * utils.MiB})

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Worker exited after completing the one task
	case <-time.After(30 * time.Second):
		cancel()
		queue.Close()
		t.Fatal("Idle drained worker did not exit after task completion")
	}

	// The second task should still be in the queue (drain prevented pickup)
	remaining := queue.Len()
	if remaining < 1 {
		t.Errorf("Expected at least 1 remaining task (drain should prevent 2nd pickup), got %d", remaining)
	}

	queue.Close()
}

// TestDrainWorker_IdleWorker_QueueClose verifies that an idle drained worker
// exits when the queue is closed (e.g., download completion or cancellation).
// This is the normal exit path for idle workers — drain flag is not needed
// since queue.Close() wakes up all blocked workers.
func TestDrainWorker_IdleWorker_QueueClose(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()

	workerID := int(d.nextWorkerID.Add(1)) - 1
	d.workerWg.Add(1)
	go func() {
		defer d.workerWg.Done()
		_ = d.worker(ctx, workerID, []string{"http://localhost:1"}, nil, queue, 0, &http.Client{})
	}()

	// Wait for worker to be idle (waiting on Pop)
	time.Sleep(100 * time.Millisecond)

	// Mark as draining (for completeness — the drain flag is not what
	// causes exit here, queue.Close() is)
	d.DrainWorker(workerID)

	// Close the queue to wake up the worker from Pop
	queue.Close()

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Worker exited via queue close
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Idle worker did not exit after queue close")
	}
}

// TestGracePeriod_DownloadVolumeCheck verifies that the grace period
// now also considers download volume (<1MB) in addition to time.
func TestGracePeriod_DownloadVolumeCheck(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0, // No time-based grace — rely on volume check
	}
	state := types.NewProgressState("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Now()

	w0Ctx, w0Cancel := context.WithCancel(ctx)
	w1Ctx, w1Cancel := context.WithCancel(ctx)

	// Worker 0: 10 MB/s, started 10s ago, downloaded >1MB → eligible for slow check
	w0 := &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 10 * utils.MiB},
		StartTime: now.Add(-10 * time.Second),
		Speed:     10 * 1024 * 1024,
		Cancel:    w0Cancel,
	}
	w0.CurrentOffset.Store(2 * utils.MiB) // Downloaded 2MB > 1MB

	// Worker 1: 0.1 MB/s, started 10s ago, but downloaded <1MB → protected by volume grace
	w1 := &ActiveTask{
		Task:      types.Task{Offset: 10 * utils.MiB, Length: 10 * utils.MiB},
		StartTime: now.Add(-10 * time.Second),
		Speed:     100 * 1024,
		Cancel:    w1Cancel,
	}
	w1.CurrentOffset.Store(10*utils.MiB + 100*utils.KiB) // Only 100KB downloaded < 1MB

	d.activeTasks[0] = w0
	d.activeTasks[1] = w1

	d.checkWorkerHealth()

	// Worker 0 should NOT be cancelled (it's the fast one)
	select {
	case <-w0Ctx.Done():
		t.Error("Worker 0 should NOT have been cancelled (fast worker)")
	default:
	}

	// Worker 1 should NOT be cancelled despite being slow — protected by volume grace
	select {
	case <-w1Ctx.Done():
		t.Error("Worker 1 should NOT have been cancelled (download volume < 1MB grace)")
	default:
	}
}

// TestStallDetection_NotBlockedByVolumeGrace verifies that stall detection
// fires even when downloadedBytes < 1MB. A dead connection that received
// little or no data must still be cancelled by the stall check, regardless
// of the volume grace that protects the slow speed check.
func TestStallDetection_NotBlockedByVolumeGrace(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,               // No time grace
		StallTimeout:          1 * time.Second, // Short stall timeout
	}
	state := types.NewProgressState("test", 1000)
	d := NewConcurrentDownloader("test", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Now()

	stalledCtx, stalledCancel := context.WithCancel(ctx)

	// Worker: started 30s ago, downloaded 0 bytes (<1MB), stalled for 30s.
	// Volume grace would protect it from slow speed check, but stall
	// detection must still fire.
	active := &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 10 * utils.MiB},
		StartTime: now.Add(-30 * time.Second),
		Cancel:    stalledCancel,
	}
	active.LastActivity.Store(now.Add(-30 * time.Second).UnixNano()) // Stalled for 30s
	active.Speed = 0
	active.CurrentOffset.Store(0) // 0 bytes downloaded, <1MB

	d.activeTasks[0] = active

	d.checkWorkerHealth()

	// Worker should be cancelled by stall detection despite volume grace
	select {
	case <-stalledCtx.Done():
		// Success: stall detection fired despite <1MB downloaded
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
	// FORK-PATCH: Use workerDepsPtr instead of individual fields
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

	// Close the queue so the spawned workers exit quickly
	queue.Close()

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Workers exited
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Scaled workers did not exit after queue close")
	}
}

// TestScaleWorkers_Down verifies that ScaleWorkers with negative delta
// drains the slowest worker.
func TestScaleWorkers_Down(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up three active tasks with different speeds
	d.activeTasks[0] = &ActiveTask{
		Task:      types.Task{Offset: 0, Length: 100},
		StartTime: time.Now().Add(-10 * time.Second),
		Cancel:    cancel,
	}
	d.activeTasks[0].Speed = 10 * 1024 * 1024 // 10 MB/s (fast)

	_, w1Cancel := context.WithCancel(ctx)
	d.activeTasks[1] = &ActiveTask{
		Task:      types.Task{Offset: 100, Length: 100},
		StartTime: time.Now().Add(-10 * time.Second),
		Cancel:    w1Cancel,
	}
	d.activeTasks[1].Speed = 5 * 1024 * 1024 // 5 MB/s (medium)

	w2Ctx, w2Cancel := context.WithCancel(ctx)
	d.activeTasks[2] = &ActiveTask{
		Task:      types.Task{Offset: 200, Length: 100},
		StartTime: time.Now().Add(-10 * time.Second),
		Cancel:    w2Cancel,
	}
	d.activeTasks[2].Speed = 100 * 1024 // 100 KB/s (slowest)

	d.workerDepsPtr.Store(&workerDeps{ctx: ctx})
	d.workersActive.Store(true)

	result := d.ScaleWorkers(-1)

	if result != -1 {
		t.Fatalf("ScaleWorkers(-1) returned %d, want -1", result)
	}

	// The slowest worker (ID 2) should be marked as draining
	if !d.activeTasks[2].Draining.Load() {
		t.Error("Slowest worker (ID 2) should be marked as draining")
	}

	// Other workers should NOT be draining
	if d.activeTasks[0].Draining.Load() {
		t.Error("Worker 0 should NOT be draining")
	}
	if d.activeTasks[1].Draining.Load() {
		t.Error("Worker 1 should NOT be draining")
	}

	// Verify w2Ctx is NOT cancelled (drain ≠ cancel)
	select {
	case <-w2Ctx.Done():
		t.Error("Drained worker context should NOT be cancelled (drain is graceful)")
	default:
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
	// workerDepsPtr is nil (not stored), so ScaleWorkers should return 0

	result := d.ScaleWorkers(1)
	if result != 0 {
		t.Errorf("ScaleWorkers(1) with nil ctx returned %d, want 0", result)
	}
}

// TestDrain_ConcurrentAccess verifies that concurrent calls to
// DrainWorker, ScaleWorkers, and activeWorkerIDs are race-free.
func TestDrain_ConcurrentAccess(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Pre-populate activeTasks
	for i := 0; i < 5; i++ {
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
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			d.DrainWorker(n % 5)
			d.activeWorkerIDs()
			d.ScaleWorkers(-1)
		}(i)
	}
	wg.Wait()

	// If we get here without a race detector failure, the test passes
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
// FORK-PATCH: WaitGroup reuse protection
func TestScaleWorkers_WaitGroupReuseProtection(t *testing.T) {
	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d.workerDepsPtr.Store(&workerDeps{ctx: ctx})
	d.workersActive.Store(false) // Simulate post-Wait state (workersActive set after Wait() returns)

	result := d.ScaleWorkers(1)
	if result != 0 {
		t.Errorf("ScaleWorkers(1) after workersActive=false returned %d, want 0", result)
	}
}

// TestScaleWorkers_UpDuringActiveWorkers verifies the core fix: while workers
// are still running (Wait() not yet returned), workersActive stays true and
// ScaleWorkers(delta>0) can Add new workers instead of being blocked.
// FORK-PATCH: workersActive must remain true until Wait() returns.
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

	// Simulate executeWorkers start: workersActive=true.
	d.workersActive.Store(true)

	// Simulate a long-running worker still in flight (Wait() not returned).
	d.workerWg.Add(1)
	go func() {
		defer d.workerWg.Done()
		time.Sleep(200 * time.Millisecond)
	}()

	// While the worker is still running, ScaleUp must succeed (returns 1).
	result := d.ScaleWorkers(1)
	if result != 1 {
		t.Fatalf("ScaleWorkers(1) during active workers returned %d, want 1", result)
	}

	// Close the queue so the ScaleWorkers-spawned worker exits cleanly.
	queue.Close()

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All workers exited without panic.
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("workers did not exit after queue close")
	}
}

// TestIsConnLimitError verifies detection of server connection limit errors.
// FORK-PATCH
func TestIsConnLimitError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("server returned 503"), true},
		{fmt.Errorf("rate limited (429)"), true},
		{fmt.Errorf("connection refused"), true},
		{fmt.Errorf("connection reset by peer"), true},
		{fmt.Errorf("unexpected status: 404"), false},
		{fmt.Errorf("i/o timeout"), false},
		{nil, false},
	}

	for _, tt := range tests {
		got := isConnLimitError(tt.err)
		if got != tt.want {
			t.Errorf("isConnLimitError(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

// TestConnErrorDetection_503 verifies that a 503 response from the server
// increments consecutiveConnErrors on ProgressState.
// FORK-PATCH
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

	state := types.NewProgressState("conn-503-test", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerDownload: 1}

	d := NewConcurrentDownloader("conn-503-test", nil, state, runtime)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_ = d.Download(ctx, server.URL(), nil, nil, destPath, fileSize)

	if got := state.GetConnErrors(); got == 0 {
		t.Error("expected consecutiveConnErrors > 0 after 503 responses, got 0")
	}
}

// TestConnErrorDetection_429 verifies that a 429 response from the server
// increments consecutiveConnErrors on ProgressState.
// FORK-PATCH
func TestConnErrorDetection_429(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	server := testutil.NewMockServerT(t,
		testutil.WithHandler(func(w http.ResponseWriter, r *http.Request) {
			// FORK-PATCH: Retry-After: 0 clamps to RateLimitMinBackoff (500ms),
			// allowing the rlRetries budget to exhaust within the test timeout.
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

	state := types.NewProgressState("conn-429-test", fileSize)
	runtime := &types.RuntimeConfig{MaxConnectionsPerDownload: 1}

	d := NewConcurrentDownloader("conn-429-test", nil, state, runtime)
	d.hostLimiter = engine.NewHostRateLimiter()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_ = d.Download(ctx, server.URL(), nil, nil, destPath, fileSize)

	if got := state.GetConnErrors(); got == 0 {
		t.Error("expected consecutiveConnErrors > 0 after 429 responses, got 0")
	}
}

// TestScaleWorkers_ConcurrentScaleUp verifies that concurrent ScaleWorkers
// calls with workersActive=true are race-free.
// FORK-PATCH
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
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.ScaleWorkers(1)
		}()
	}
	wg.Wait()

	// Close queue so workers exit
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
