package concurrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// tarpitHandler serves a configurable number of bytes for each Range request
// then holds the connection open (optionally trickling bytes) without sending
// EOF. This simulates a TCP tarpit server that causes workers to block
// indefinitely in resp.Body.Read().
type tarpitHandler struct {
	fileSize     int64
	partialBytes int64
	trickleEvery time.Duration
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

	toSend := min(h.partialBytes, length)
	if toSend > 0 {
		data := make([]byte, toSend)
		n, _ := w.Write(data)
		h.bytesServed.Add(int64(n))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}

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

func TestTarpitCompletion_PartialDataHang(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	tarpitSrv := newTarpitServer(t, fileSize, 256*utils.KiB, 0)
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

	state := progress.New("tarpit-partial", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              128 * utils.KiB,
	}
	d := NewConcurrentDownloader("tarpit-partial", nil, state, runtime)

	ctx := t.Context()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL()}, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
	if got := state.Bytes.Downloaded.Load(); got > fileSize {
		t.Errorf("Downloaded = %d, want <= %d (VP guard/drain should prevent overcount)", got, fileSize)
	}
}

func TestTarpitCompletion_TrickleEvadesStall(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(512 * utils.KiB)
	tarpitSrv := newTarpitServer(t, fileSize, 128*utils.KiB, 100*time.Millisecond)
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

	state := progress.New("tarpit-trickle", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              64 * utils.KiB,
	}
	d := NewConcurrentDownloader("tarpit-trickle", nil, state, runtime)

	ctx := t.Context()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL()}, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
}

func TestTarpitCompletion_RequeueGuard(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
	tarpitSrv := newTarpitServer(t, fileSize, 128*utils.KiB, 0)
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

	state := progress.New("tarpit-guard", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              128 * utils.KiB,
	}
	d := NewConcurrentDownloader("tarpit-guard", nil, state, runtime)

	ctx := t.Context()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL()}, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if got := state.Bytes.Downloaded.Load(); got != fileSize {
		t.Errorf("Downloaded = %d, want %d (requeue guard should prevent re-stick)", got, fileSize)
	}
}

func TestTarpitCompletion_NormalServerUnaffected(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1 * utils.MiB)
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

	state := progress.New("normal-test", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
	}
	d := NewConcurrentDownloader("normal-test", nil, state, runtime)

	ctx := t.Context()

	err := downloadWithTimeout(t, d, ctx, server.URL(), destPath, fileSize, nil, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
	if got := state.Bytes.Downloaded.Load(); got != fileSize {
		t.Errorf("Downloaded = %d, want %d", got, fileSize)
	}
}

func TestRunCompletionMonitor_KillWorkerAt100Percent(t *testing.T) {
	fileSize := int64(1000)
	state := progress.New("cm-test", fileSize)
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

	state.Bytes.VerifiedProgress.Store(fileSize)

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: 500})

	ctx := t.Context()

	done := make(chan struct{})
	go func() {
		d.runCompletionMonitor(ctx, queue, fileSize, 2)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCompletionMonitor did not return within 2s with VerifiedProgress >= fileSize")
	}

	if taskCtx.Err() == nil {
		t.Error("taskCtx should be cancelled after lock-held cancel at 100%")
	}
}

func TestRunCompletionMonitor_NormalCompletionNoKill(t *testing.T) {
	fileSize := int64(1000)
	state := progress.New("cm-normal", fileSize)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		State:       state,
		Runtime:     &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	queue.idleWorkers.Add(2)

	state.Bytes.VerifiedProgress.Store(fileSize)

	ctx := t.Context()

	done := make(chan struct{})
	go func() {
		d.runCompletionMonitor(ctx, queue, fileSize, 2)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCompletionMonitor did not return within 2s on normal completion")
	}
}

func TestRunCompletionMonitor_VPGuard_HangsWhenVPBelowFileSize(t *testing.T) {
	fileSize := int64(1000)
	state := progress.New("cm-vpguard", fileSize)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		State:       state,
		Runtime:     &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	queue.idleWorkers.Add(2)

	state.Bytes.VerifiedProgress.Store(500)

	ctx := t.Context()

	done := make(chan struct{})
	go func() {
		d.runCompletionMonitor(ctx, queue, fileSize, 2)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("runCompletionMonitor returned with VP < fileSize (guard failed)")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestRunCompletionMonitor_DrainsQueueAt100Percent(t *testing.T) {
	fileSize := int64(1000)
	state := progress.New("cm-drain", fileSize)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		State:       state,
		Runtime:     &types.RuntimeConfig{},
	}

	state.Bytes.VerifiedProgress.Store(fileSize)

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: 500})
	queue.Push(types.Task{Offset: 500, Length: 500})

	ctx := t.Context()

	done := make(chan struct{})
	go func() {
		d.runCompletionMonitor(ctx, queue, fileSize, 2)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCompletionMonitor did not return within 2s")
	}

	if queue.Len() != 0 {
		t.Errorf("queue.Len() = %d after 100%% completion, want 0 (queue should be drained)", queue.Len())
	}
}

func TestTarpitCompletion_DeadSilentTarpitMutexFix(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(512 * utils.KiB)
	tarpitSrv := newTarpitServer(t, fileSize, 0, 0)
	normalSrv := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer normalSrv.Close()

	destPath := filepath.Join(tmpDir, "tarpit_deadsilent.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := progress.New("tarpit-deadsilent", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              64 * utils.KiB,
	}
	d := NewConcurrentDownloader("tarpit-deadsilent", nil, state, runtime)

	ctx := t.Context()

	err := downloadWithTimeout(t, d, ctx, tarpitSrv.URL, destPath, fileSize,
		[]string{normalSrv.URL()}, 10*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if err := testutil.VerifyFileSize(workingPath, fileSize); err != nil {
		t.Error(err)
	}
}

// TestRunCompletionMonitor_MutexCancelKillsAllActive verifies that at
// VP=100% the completion monitor cancels all active task contexts under
// activeMu rather than relying on a worker-ID snapshot helper.
func TestRunCompletionMonitor_MutexCancelKillsAllActive(t *testing.T) {
	fileSize := int64(1000)
	state := progress.New("cm-mutex", fileSize)
	d := &ConcurrentDownloader{
		activeTasks: make(map[int]*ActiveTask),
		State:       state,
		Runtime:     &types.RuntimeConfig{},
	}

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ctx3, cancel3 := context.WithCancel(context.Background())
	defer cancel3()

	d.activeTasks[0] = &ActiveTask{Cancel: cancel1}
	d.activeTasks[1] = &ActiveTask{Cancel: cancel2}
	d.activeTasks[2] = &ActiveTask{Cancel: cancel3}

	state.Bytes.VerifiedProgress.Store(fileSize)

	queue := NewTaskQueue()
	ctx := t.Context()

	done := make(chan struct{})
	go func() {
		d.runCompletionMonitor(ctx, queue, fileSize, 3)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCompletionMonitor did not return within 2s")
	}

	if ctx1.Err() == nil || ctx2.Err() == nil || ctx3.Err() == nil {
		t.Error("not all active task contexts were cancelled at 100%")
	}
}

func TestWorker_VPGuardExitsBeforeRegistration(t *testing.T) {
	fileSize := int64(1000)
	state := progress.New("vp-guard", fileSize)
	runtime := &types.RuntimeConfig{}
	d := NewConcurrentDownloader("vp-guard", nil, state, runtime)

	state.Bytes.VerifiedProgress.Store(fileSize)

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: 500})

	ctx := t.Context()

	client := &http.Client{}
	err := d.worker(ctx, 0, []string{"http://localhost:1"}, nil, queue, fileSize, client)
	if err != nil {
		t.Fatalf("worker returned error: %v", err)
	}

	d.activeMu.Lock()
	registered := len(d.activeTasks)
	d.activeMu.Unlock()
	if registered != 0 {
		t.Errorf("activeTasks has %d entries, want 0 (worker should exit before registration)", registered)
	}

	if got := state.ActiveWorkers.Load(); got != 0 {
		t.Errorf("ActiveWorkers = %d, want 0", got)
	}
}

func TestWorker_ActiveWorkersCountCorrectOnMutexExit(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(512 * utils.KiB)
	normalSrv := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer normalSrv.Close()

	destPath := filepath.Join(tmpDir, "aw_count.bin")
	workingPath := destPath + types.IncompleteSuffix
	if f, err := os.Create(workingPath); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	state := progress.New("aw-count", fileSize)
	runtime := &types.RuntimeConfig{
		MaxConnectionsPerDownload: 4,
		Workers:                   4,
		MinChunkSize:              64 * utils.KiB,
	}
	d := NewConcurrentDownloader("aw-count", nil, state, runtime)

	ctx := t.Context()

	err := downloadWithTimeout(t, d, ctx, normalSrv.URL(), destPath, fileSize, nil, 15*time.Second)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	if got := state.ActiveWorkers.Load(); got != 0 {
		t.Errorf("ActiveWorkers = %d after completion, want 0 (no count leak)", got)
	}
}
