package concurrent

// GoAria-only regression tests for VP overcount / false completion / silent hole.
// Covers residual requeue after retry exhaustion, retry StopAt clamp, no-steal-hedged,
// and the downloadTask count clamp that defuses the steal write race.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// staleRequeueHandler models a flaky-then-recovering server:
//   - Requests 1..failCount: send partialBytes of the range, then close
//     (early EOF) — drives the worker retry loop to exhaustion.
//   - Requests > failCount: send recoverBytes of the range, then HOLD the
//     connection open (tarpit) — models the recovered-but-slow server so the
//     completion monitor's VP kill path decides the outcome, not the server.
type staleRequeueHandler struct {
	fileSize     int64
	partialBytes int64
	recoverBytes int64
	failCount    int32
	reqCount     atomic.Int32
}

func (h *staleRequeueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := h.reqCount.Add(1)
	start, end, ok := parseSimpleRange(r.Header.Get("Range"), h.fileSize)
	if !ok {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	length := end - start + 1
	w.Header().Set("Content-Type", "application/octet-stream")
	// No Content-Length: chunked encoding → short read yields io.EOF.
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, h.fileSize))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)

	toSend := h.partialBytes
	if n > h.failCount {
		toSend = h.recoverBytes
	}
	if toSend > length {
		toSend = length
	}
	data := make([]byte, toSend)
	for i := range data {
		data[i] = 0xFF
	}
	_, _ = w.Write(data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if n > h.failCount {
		// Tarpit: hold the connection so the worker blocks in Read until the
		// completion monitor (or test timeout) cancels it.
		<-r.Context().Done()
	}
	// Return closes the chunked stream → io.EOF on the client.
}

// assertNoZeroHole verifies [0,fileSize) contains only 0xFF (no zero holes).
func assertNoZeroHole(t *testing.T, path string, fileSize int64) {
	t.Helper()
	buf, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(buf)) != fileSize {
		t.Fatalf("file size = %d, want %d", len(buf), fileSize)
	}
	for off := int64(0); off < fileSize; off += 64 * utils.KiB {
		if buf[off] != 0xFF {
			t.Fatalf("expected 0xFF at offset %d, got 0x%02X (zero hole)", off, buf[off])
		}
	}
	// Check the final byte too when fileSize isn't a multiple of the stride.
	if fileSize > 0 && buf[fileSize-1] != 0xFF {
		t.Fatalf("expected 0xFF at final offset %d, got 0x%02X", fileSize-1, buf[fileSize-1])
	}
}

// TestVPOvercount_StaleRequeueDoesNotCreateHole is the inverted regression for
// the "stall → natural recovery → corrupted file" incident. After the fix the
// residual requeue uses RemainingTask() so already-counted bytes are not
// re-downloaded and re-counted; the file must be complete with VP == fileSize.
func TestVPOvercount_StaleRequeueDoesNotCreateHole(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(8 * utils.MiB)
	handler := &staleRequeueHandler{
		fileSize:     fileSize,
		partialBytes: 2 * utils.MiB,
		recoverBytes: 4 * utils.MiB,
		failCount:    2,
	}
	srv := testutil.NewHTTPServerT(t, handler)
	t.Cleanup(srv.Close)

	destPath := filepath.Join(tmpDir, "stale_requeue.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := progress.New("stale-requeue", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		MinChunkSize:              8 * utils.MiB,
		MaxTaskRetries:            2,
	}
	d := NewConcurrentDownloader("stale-requeue", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, srv.URL, destPath, fileSize, nil, 30*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	vp := state.Bytes.VerifiedProgress.Load()
	if vp != fileSize {
		t.Fatalf("VP=%d, want %d (fileSize)", vp, fileSize)
	}
	assertNoZeroHole(t, workingPath, fileSize)
}

// TestVPOvercount_MaxRetries1_StaleRequeue covers the edge case where
// MaxTaskRetries=1: the first failure breaks immediately and resumeOnRetryOffset
// never runs. The residual must still be recovered from activeTask via
// RemainingTask() rather than the stale task end.
func TestVPOvercount_MaxRetries1_StaleRequeue(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(4 * utils.MiB)
	handler := &staleRequeueHandler{
		fileSize:     fileSize,
		partialBytes: 1 * utils.MiB,
		recoverBytes: fileSize, // recovered requests send the full remaining range
		failCount:    1,
	}
	srv := testutil.NewHTTPServerT(t, handler)
	t.Cleanup(srv.Close)

	destPath := filepath.Join(tmpDir, "maxretry1.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := progress.New("maxretry1", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		MinChunkSize:              4 * utils.MiB,
		MaxTaskRetries:            1,
	}
	d := NewConcurrentDownloader("maxretry1", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, srv.URL, destPath, fileSize, nil, 30*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if got := state.Bytes.VerifiedProgress.Load(); got != fileSize {
		t.Fatalf("VP=%d, want %d", got, fileSize)
	}
	assertNoZeroHole(t, workingPath, fileSize)
}

// TestVPOvercount_RetryRespectsStolenStopAt verifies that after StealWork
// reduces StopAt, a retry clamps the task end to the reduced StopAt so the
// stolen range is not resurrected and double-counted with an independent
// dedup pointer.
func TestVPOvercount_RetryRespectsStolenStopAt(t *testing.T) {
	task := types.Task{Offset: 0, Length: 100 * utils.MiB}
	active := &ActiveTask{Task: task}
	active.CurrentOffset.Store(30 * utils.MiB) // wrote [0,30M) before failing
	// StealWork reduced StopAt: stolen task owns [50M,100M) with an
	// independent SharedMaxOffset.
	active.StopAt.Store(50 * utils.MiB)

	var d ConcurrentDownloader
	d.resumeOnRetryOffset(&task, active)

	// The next attempt re-stores StopAt = task.Offset + task.Length.
	retryStopAt := task.Offset + task.Length
	stolenStart := int64(50 * utils.MiB)
	if retryStopAt > stolenStart {
		t.Fatalf("retry StopAt=%d resurrects stolen range [%d,%d) — overlap %d bytes",
			retryStopAt, stolenStart, 100*utils.MiB, retryStopAt-stolenStart)
	}
	if task.Offset+task.Length > active.StopAt.Load() {
		t.Fatalf("task end=%d exceeds active StopAt=%d", task.Offset+task.Length, active.StopAt.Load())
	}
	// Non-hedged task carries nil SharedMaxOffset — no dedup, unchanged.
	if task.SharedMaxOffset != nil {
		t.Fatalf("non-hedged task should carry nil SharedMaxOffset, got non-nil")
	}
}

// TestRetryStopAt_ClampedToActiveStopAt is the minimal unit check that
// resumeOnRetryOffset clamps the task end to activeTask.StopAt when StopAt is
// below the original task end.
func TestRetryStopAt_ClampedToActiveStopAt(t *testing.T) {
	task := types.Task{Offset: 0, Length: 80 * utils.MiB}
	active := &ActiveTask{Task: task}
	active.CurrentOffset.Store(20 * utils.MiB)
	active.StopAt.Store(50 * utils.MiB) // steal reduced StopAt below original end 80M

	var d ConcurrentDownloader
	d.resumeOnRetryOffset(&task, active)

	if task.Offset+task.Length > active.StopAt.Load() {
		t.Fatalf("task end=%d not clamped to StopAt=%d", task.Offset+task.Length, active.StopAt.Load())
	}
	if task.Offset != 20*utils.MiB {
		t.Fatalf("task.Offset=%d, want 20M (current)", task.Offset)
	}
}

// TestVPOvercount_RetryNoProgressStillClamps covers the edge case where a
// retry made no progress (current == task.Offset, e.g. connection failed
// before any write). The unconditional clamp must still shrink task.Length to
// StopAt so the stolen range is not resurrected.
func TestVPOvercount_RetryNoProgressStillClamps(t *testing.T) {
	task := types.Task{Offset: 10 * utils.MiB, Length: 80 * utils.MiB}
	active := &ActiveTask{Task: task}
	// No progress: current == task.Offset.
	active.CurrentOffset.Store(10 * utils.MiB)
	// Steal reduced StopAt below the original end (10M + 80M = 90M).
	active.StopAt.Store(40 * utils.MiB)

	var d ConcurrentDownloader
	d.resumeOnRetryOffset(&task, active)

	if task.Offset+task.Length > active.StopAt.Load() {
		t.Fatalf("task end=%d not clamped to StopAt=%d (no-progress case)",
			task.Offset+task.Length, active.StopAt.Load())
	}
	if task.Offset != 10*utils.MiB {
		t.Fatalf("task.Offset=%d, want 10M (no progress)", task.Offset)
	}
	wantLen := int64(40*utils.MiB) - int64(10*utils.MiB)
	if task.Length != wantLen {
		t.Fatalf("task.Length=%d, want %d (clamped even with no progress)", task.Length, wantLen)
	}
}

// TestVPOvercount_StealWorkSkipsHedgedVictim verifies StealWork declines to
// steal from a hedged worker, preventing the hedge partner and the stolen
// worker from double-counting the stolen range with separate dedup pointers.
func TestVPOvercount_StealWorkSkipsHedgedVictim(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{MinChunkSize: 2 * utils.MiB},
	}

	shared := &atomic.Int64{}
	shared.Store(0)
	victim := &ActiveTask{Task: types.Task{Offset: 0, Length: 20 * utils.MiB}}
	victim.CurrentOffset.Store(0)
	victim.StopAt.Store(20 * utils.MiB)
	victim.Hedged.Store(1)
	victim.SharedMaxOffset = shared
	d.activeTasks[0] = victim

	queue := NewTaskQueue()
	defer queue.Close()

	if d.StealWork(queue) {
		t.Fatal("StealWork should skip hedged victim")
	}
	if queue.Len() != 0 {
		t.Fatalf("queue should be empty, len=%d", queue.Len())
	}
}

// TestVPOvercount_AllCandidatesHedged_NoSteal verifies that when every active
// task is hedged, StealWork returns false without panicking or deadlocking.
func TestVPOvercount_AllCandidatesHedged_NoSteal(t *testing.T) {
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		Runtime:     &types.RuntimeConfig{MinChunkSize: 2 * utils.MiB},
	}

	shared := &atomic.Int64{}
	shared.Store(0)
	for i := 0; i < 2; i++ {
		victim := &ActiveTask{Task: types.Task{Offset: 0, Length: 20 * utils.MiB}}
		victim.CurrentOffset.Store(0)
		victim.StopAt.Store(20 * utils.MiB)
		victim.Hedged.Store(1)
		victim.SharedMaxOffset = shared
		d.activeTasks[i] = victim
	}

	queue := NewTaskQueue()
	defer queue.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if d.StealWork(queue) {
			t.Error("StealWork should decline when all candidates are hedged")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StealWork deadlocked with all candidates hedged")
	}
	if queue.Len() != 0 {
		t.Fatalf("queue should be empty, len=%d", queue.Len())
	}
}

// signalLimiter blocks WaitN until gate is closed. It also closes arrived once
// WaitN is entered, so a test can synchronize on the worker having passed the
// pre-write StopAt truncation check (which runs before the limiter wait).
type signalLimiter struct {
	arrivedOnce sync.Once
	arrived     chan struct{}
	gate        chan struct{}
}

func newSignalLimiter() *signalLimiter {
	return &signalLimiter{
		arrived: make(chan struct{}),
		gate:    make(chan struct{}),
	}
}

func (s *signalLimiter) WaitN(ctx context.Context, n int64) error {
	s.arrivedOnce.Do(func() { close(s.arrived) })
	select {
	case <-s.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fullRangeHandler serves the complete requested range as 0xFF then closes
// (chunked → io.EOF). Used to drive a single large read in downloadTask.
type fullRangeHandler struct {
	fileSize int64
}

func (h *fullRangeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start, end, ok := parseSimpleRange(r.Header.Get("Range"), h.fileSize)
	if !ok {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	length := end - start + 1
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, h.fileSize))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)
	data := make([]byte, length)
	for i := range data {
		data[i] = 0xFF
	}
	_, _ = w.Write(data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	// Return closes the chunked stream → io.EOF on the client.
}

// TestVPOvercount_DownloadTaskCountClampedToStopAt verifies the Task 4 clamp:
// when StopAt is reduced between the read-loop's pre-write truncation and the
// post-write count, newlyWritten is clamped so VP does not overcount bytes
// past the reduced StopAt. A blocking limiter widens the race window so the
// reduction lands deterministically after the truncation check.
func TestVPOvercount_DownloadTaskCountClampedToStopAt(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(256 * utils.KiB)
	handler := &fullRangeHandler{fileSize: fileSize}
	srv := testutil.NewHTTPServerT(t, handler)
	t.Cleanup(srv.Close)

	workingPath := filepath.Join(tmpDir, "clamp.bin")
	f, err := os.OpenFile(workingPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("clamp", fileSize)
	state.InitBitmap(fileSize, fileSize) // single bitmap chunk covering [0,fileSize)

	runtime := &types.RuntimeConfig{}
	limiter := newSignalLimiter()
	d := &ConcurrentDownloader{
		State:   state,
		Runtime: runtime,
		Limiter: limiter,
	}

	task := types.Task{Offset: 0, Length: fileSize}
	activeTask := &ActiveTask{Task: task, workerID: 0}
	activeTask.CurrentOffset.Store(0)
	activeTask.StopAt.Store(fileSize)

	buf := make([]byte, fileSize)
	client := &http.Client{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- d.downloadTask(ctx, srv.URL, f, activeTask, buf, client, fileSize)
	}()

	// Wait until the worker is blocked in the limiter (past the pre-write
	// truncation check), then reduce StopAt to model a concurrent steal.
	<-limiter.arrived
	half := fileSize / 2
	activeTask.StopAt.Store(half)
	close(limiter.gate)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("downloadTask returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("downloadTask did not complete")
	}

	vp := state.Bytes.VerifiedProgress.Load()
	// Without the clamp VP would equal fileSize (full overcount of the half
	// written past the reduced StopAt). With the clamp VP must not exceed the
	// reduced StopAt boundary.
	if vp > half {
		t.Fatalf("VP=%d exceeds reduced StopAt=%d — count not clamped (overcount=%d)",
			vp, half, vp-half)
	}
	if vp <= 0 {
		t.Fatalf("VP=%d, expected non-zero clamped count", vp)
	}
}

// TestVPOvercount_DownloadTaskCountClampedMultiChunk verifies that the Task 4
// clamp attributes bytes to the correct chunk when the reduced StopAt falls in
// a different chunk than the write start. The single-chunk test 8 cannot expose
// the pendingStart mis-attribution bug: with multiple chunks, a clamped
// pendingStart pointing past clampStopAt would credit the wrong chunk, leaving
// the first chunk permanently incomplete (VP < fileSize → hang).
func TestVPOvercount_DownloadTaskCountClampedMultiChunk(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	// Two chunks: chunk 0 = [0, 128KiB), chunk 1 = [128KiB, 256KiB).
	chunkSize := int64(128 * utils.KiB)
	fileSize := 2 * chunkSize
	handler := &fullRangeHandler{fileSize: fileSize}
	srv := testutil.NewHTTPServerT(t, handler)
	t.Cleanup(srv.Close)

	workingPath := filepath.Join(tmpDir, "clamp_multi.bin")
	f, err := os.OpenFile(workingPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("clamp-multi", fileSize)
	// Use chunkSize as ActualChunkSize so the file spans 2 chunks.
	state.InitBitmap(fileSize, chunkSize)

	runtime := &types.RuntimeConfig{}
	limiter := newSignalLimiter()
	d := &ConcurrentDownloader{
		State:   state,
		Runtime: runtime,
		Limiter: limiter,
	}

	task := types.Task{Offset: 0, Length: fileSize}
	activeTask := &ActiveTask{Task: task, workerID: 0}
	activeTask.CurrentOffset.Store(0)
	activeTask.StopAt.Store(fileSize)

	buf := make([]byte, fileSize)
	client := &http.Client{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- d.downloadTask(ctx, srv.URL, f, activeTask, buf, client, fileSize)
	}()

	// Wait until the worker is blocked in the limiter (past the pre-write
	// truncation check), then reduce StopAt to a boundary inside chunk 0.
	// The write starts at offset 0 (chunk 0) and extends to fileSize; the
	// clamp must attribute bytes to [0, stopAt) in chunk 0, not to chunk 1.
	<-limiter.arrived
	stopAt := chunkSize / 2 // 64KiB — inside chunk 0
	activeTask.StopAt.Store(stopAt)
	close(limiter.gate)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("downloadTask returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("downloadTask did not complete")
	}

	vp := state.Bytes.VerifiedProgress.Load()
	// VP must equal the reduced StopAt — bytes [0, stopAt) attributed to
	// chunk 0. If pendingStart were mis-attributed to chunk 1, chunk 0 would
	// stay at 0 and chunk 1 would get credit it shouldn't, leaving VP correct
	// in total but chunk 0 incomplete. Check chunk 0 progress directly.
	if vp != stopAt {
		t.Fatalf("VP=%d, want %d (reduced StopAt) — chunk attribution wrong",
			vp, stopAt)
	}

	// Verify chunk 0 received the credit (not chunk 1). No concurrent writes
	// after downloadTask returns, so snapshot read is safe.
	_, _, _, _, chunkProgress := state.GetBitmapSnapshot(true)
	if len(chunkProgress) < 2 {
		t.Fatalf("expected >=2 chunk progress entries, got %d", len(chunkProgress))
	}
	chunk0Progress := chunkProgress[0]
	chunk1Progress := chunkProgress[1]

	if chunk0Progress != stopAt {
		t.Fatalf("chunk 0 progress=%d, want %d — bytes mis-attributed to wrong chunk",
			chunk0Progress, stopAt)
	}
	if chunk1Progress != 0 {
		t.Fatalf("chunk 1 progress=%d, want 0 — over-boundary bytes leaked into chunk 1",
			chunk1Progress)
	}

	// Verify CurrentOffset stores the clamped value, not the raw over-boundary
	// offset. This is the core of the offset-clamp fix: StealWork reads
	// finalCurrent from CurrentOffset, so a clamped value lets the stolen
	// worker start from max(newStopAt, clampedOffset) = newStopAt.
	if got := activeTask.CurrentOffset.Load(); got != stopAt {
		t.Fatalf("CurrentOffset = %d, want %d (clamped to StopAt)", got, stopAt)
	}
}
