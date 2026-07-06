package concurrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/testutil"
)

// tarpitHandler serves a configurable number of bytes for each Range request
// then holds the connection open (optionally trickling bytes) without sending
// EOF. This simulates a TCP tarpit server that causes workers to block
// indefinitely in resp.Body.Read().
type tarpitHandler struct {
	fileSize     int64
	partialBytes int64         // bytes to send before holding
	trickleEvery time.Duration // 0 = pure hold; >0 = send 1 byte per interval
	bytesServed  atomic.Int64
}

func (h *tarpitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start, end, ok := parseSimpleRange(r.Header.Get("Range"), h.fileSize)
	if !ok {
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	length := end - start + 1
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, h.fileSize))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)

	toSend := h.partialBytes
	if toSend > length {
		toSend = length
	}
	if toSend > 0 {
		data := make([]byte, toSend)
		n, _ := w.Write(data)
		h.bytesServed.Add(int64(n))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

	// Hold the connection until the client disconnects.
	if h.trickleEvery > 0 {
		oneByte := []byte{0x41}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(h.trickleEvery):
				n, _ := w.Write(oneByte)
				h.bytesServed.Add(int64(n))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	} else {
		<-r.Context().Done()
	}
}

// parseSimpleRange parses "bytes=start-end" and returns start, end.
func parseSimpleRange(rangeHeader string, fileSize int64) (int64, int64, bool) {
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimPrefix(rangeHeader, "bytes=")
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if start < 0 || end >= fileSize || start > end {
		return 0, 0, false
	}
	return start, end, true
}

// newTarpitServer creates an httptest.Server with the given tarpit handler.
func newTarpitServer(t *testing.T, fileSize, partialBytes int64, trickleEvery time.Duration) *httptest.Server {
	t.Helper()
	handler := &tarpitHandler{
		fileSize:     fileSize,
		partialBytes: partialBytes,
		trickleEvery: trickleEvery,
	}
	srv := testutil.NewHTTPServerT(t, handler)
	t.Cleanup(srv.Close)
	return srv
}

// downloadWithTimeout runs Download in a goroutine and fails the test if it
// does not complete within the timeout.
func downloadWithTimeout(t *testing.T, d *ConcurrentDownloader, ctx context.Context, url, destPath string, fileSize int64, activeMirrors []string, timeout time.Duration) error {
	t.Helper()
	type result struct{ err error }
	done := make(chan result, 1)
	go func() {
		done <- result{err: d.Download(ctx, url, nil, activeMirrors, destPath, fileSize)}
	}()
	select {
	case r := <-done:
		return r.err
	case <-time.After(timeout):
		t.Fatalf("Download did not complete within %v (tarpit hang)", timeout)
		return nil
	}
}

// TestTarpitCompletion_PartialDataHang verifies that when a tarpit server
// sends partial data then holds the connection, the completion monitor kills
// the stuck worker once all bytes are accounted for (via the normal mirror
// partner), and Download returns promptly.
func TestTarpitCompletion_PartialDataHang(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * types.MB)
	// Tarpit server: sends 256KB then holds (no EOF).
	tarpitSrv := newTarpitServer(t, fileSize, 256*types.KB, 0)
	// Normal server completes the same range so the hedge partner can finish.
	normalSrv := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer normalSrv.Close()

	destPath := filepath.Join(tmpDir, "tarpit_partial.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := types.NewProgressState("tarpit-partial", fileSize)
	// 2 workers: worker 0 → tarpit (stuck), worker 1 → normal (completes).
	// End-game hedge creates a duplicate of worker 0's task; worker 1 picks
	// it up from the normal mirror and finishes, pushing Downloaded to 100%.
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              128 * types.KB,
	}
	d := NewConcurrentDownloader("tarpit-partial", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL()}, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
}

// TestTarpitCompletion_TrickleEvadesStall verifies that a trickle tarpit
// (1 byte per 100ms) evades stall detection but the completion monitor still
// kills the stuck worker once all bytes are downloaded via the normal mirror.
func TestTarpitCompletion_TrickleEvadesStall(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(512 * types.KB)
	// Trickle: send 128KB then 1 byte per 100ms (evades 3s stall detection).
	tarpitSrv := newTarpitServer(t, fileSize, 128*types.KB, 100*time.Millisecond)
	normalSrv := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer normalSrv.Close()

	destPath := filepath.Join(tmpDir, "tarpit_trickle.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := types.NewProgressState("tarpit-trickle", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              64 * types.KB,
	}
	d := NewConcurrentDownloader("tarpit-trickle", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL()}, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
}

// TestTarpitCompletion_RequeueGuard verifies that when a worker is killed at
// 100%, the requeue guard prevents requeuing already-downloaded bytes. After
// completion, Downloaded should equal fileSize (not stuck or re-sticked).
func TestTarpitCompletion_RequeueGuard(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * types.MB)
	tarpitSrv := newTarpitServer(t, fileSize, 128*types.KB, 0)
	normalSrv := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer normalSrv.Close()

	destPath := filepath.Join(tmpDir, "tarpit_guard.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := types.NewProgressState("tarpit-guard", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              128 * types.KB,
	}
	d := NewConcurrentDownloader("tarpit-guard", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL()}, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if got := state.Downloaded.Load(); got != fileSize {
		t.Errorf("Downloaded = %d, want %d (requeue guard should prevent re-stick)", got, fileSize)
	}
}

// TestTarpitCompletion_NormalServerUnaffected verifies that a normal server
// (sends full data + EOF) completes via the idle-workers path, not the
// KillWorker path.
func TestTarpitCompletion_NormalServerUnaffected(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * types.MB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "normal_server.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := types.NewProgressState("normal-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
	}
	d := NewConcurrentDownloader("normal-test", nil, state, runtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL(), destPath, fileSize, nil, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
	if got := state.Downloaded.Load(); got != fileSize {
		t.Errorf("Downloaded = %d, want %d", got, fileSize)
	}
}

// TestRunCompletionMonitor_KillWorkerAt100Percent verifies the completion
// monitor logic directly: when Downloaded >= fileSize, it kills active workers
// and closes the queue even if queue.Len() > 0 (requeued hedged task).
func TestRunCompletionMonitor_KillWorkerAt100Percent(t *testing.T) {
	fileSize := int64(1000)
	state := types.NewProgressState("cm-test", fileSize)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		State:       state,
		Runtime:     &types.RuntimeConfig{},
	}

	taskCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	active := &ActiveTask{Cancel: cancel}
	active.CurrentOffset.Store(0)
	active.StopAt.Store(500)
	d.activeTasks[1] = active

	// FORK-PATCH: Mark all bytes as verified (completion monitor uses VerifiedProgress).
	state.VerifiedProgress.Store(fileSize)

	queue := NewTaskQueue()
	// Queue is NOT empty (simulates requeued hedged task).
	queue.Push(types.Task{Offset: 0, Length: 500})

	ctx, monCancel := context.WithCancel(context.Background())
	defer monCancel()

	done := make(chan struct{})
	go func() {
		d.runCompletionMonitor(ctx, queue, fileSize, 2)
		close(done)
	}()

	select {
	case <-done:
		// Monitor returned — worker should have been killed (Cancel called).
	case <-time.After(2 * time.Second):
		t.Fatal("runCompletionMonitor did not return within 2s with VerifiedProgress >= fileSize")
	}

	// Verify taskCtx was cancelled by KillWorker.
	if taskCtx.Err() == nil {
		t.Error("taskCtx should be cancelled after KillWorker at 100%")
	}
}

// TestRunCompletionMonitor_NormalCompletionNoKill verifies that when workers
// complete normally (idle workers == numConns), the completion monitor closes
// the queue without calling KillWorker.
func TestRunCompletionMonitor_NormalCompletionNoKill(t *testing.T) {
	fileSize := int64(1000)
	state := types.NewProgressState("cm-normal", fileSize)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		State:       state,
		Runtime:     &types.RuntimeConfig{},
	}

	// No active tasks, Downloaded < fileSize.
	queue := NewTaskQueue()
	// Simulate all workers idle.
	queue.idleWorkers.Add(2)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.runCompletionMonitor(ctx, queue, fileSize, 2)
		close(done)
	}()

	select {
	case <-done:
		// Normal completion path (idle workers == numConns).
	case <-time.After(2 * time.Second):
		t.Fatal("runCompletionMonitor did not return within 2s on normal completion")
	}
}
