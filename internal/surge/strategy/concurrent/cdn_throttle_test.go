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

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// TestCheckWorkerHealth_PublishesSessionFields verifies CDN fingerprint
// fields (WorkerStartUnix/SessionBytes/HTTPStatus) are populated from the
// per-worker session map and ActiveTask.LastHTTPStatus.
func TestCheckWorkerHealth_PublishesSessionFields(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)

	state := progress.New("sess-test", 1024*1024)
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

// TestWorkerSession_SurvivesChunkSwitch verifies that WorkerStartUnix stays
// constant and SessionBytes is sourced from the per-worker session across
// simulated chunk switches where ActiveTask is rebuilt.
func TestWorkerSession_SurvivesChunkSwitch(t *testing.T) {
	var d ConcurrentDownloader
	d.activeTasks = make(map[int]*ActiveTask)
	state := progress.New("chunk-test", 1024*1024)
	d.State = state

	startUnix := time.Now().Add(-60 * time.Second).UnixNano()
	sess := &workerSession{startUnix: startUnix}
	sess.sessionBytes.Store(10 * 1024 * 1024)
	d.workerSessions.Store(1, sess)

	now := time.Now()
	a1 := &ActiveTask{Task: types.Task{Offset: 0, Length: 1024}, StartTime: now, WindowStart: now, workerID: 1}
	a1.CurrentOffset.Store(1024)
	a1.StopAt.Store(1024)
	a1.LastActivity.Store(now.UnixNano())
	d.activeTasks[1] = a1
	d.checkWorkerHealth()
	first := state.GetWorkerStats()[0]

	now2 := time.Now()
	a2 := &ActiveTask{Task: types.Task{Offset: 1024, Length: 1024}, StartTime: now2, WindowStart: now2, workerID: 1}
	a2.CurrentOffset.Store(1024)
	a2.StopAt.Store(2048)
	a2.LastActivity.Store(now2.UnixNano())
	d.activeTasks[1] = a2
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

// TestSetSlowWorkerThreshold_Zero_StallStillFires verifies stall detection
// remains armed after a threshold override of 0.
func TestSetSlowWorkerThreshold_Zero_StallStillFires(t *testing.T) {
	runtime := &types.RuntimeConfig{
		SlowWorkerThreshold:   0.5,
		SlowWorkerGracePeriod: 0,
		StallTimeout:          1 * time.Second,
	}
	state := progress.New("stall-test", 1000)
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
	active.LastActivity.Store(now.Add(-2 * time.Second).UnixNano())
	active.CurrentOffset.Store(2 * 1024 * 1024)
	d.activeTasks[0] = active

	d.checkWorkerHealth()

	select {
	case <-stallCtx.Done():
	default:
		t.Error("stalled worker should still be cancelled by stall detection")
	}
}

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
	default:
		t.Error("KillWorker did not cancel the target task context")
	}

	if d.KillWorker(99) {
		t.Error("KillWorker(99) returned true for a worker with no active task")
	}
}

// TestKillWorker_WorkerStaysAlive verifies KillWorker cancels the in-flight
// chunk, the worker goroutine does NOT exit, remaining bytes are requeued,
// and the worker continues to drain the queue on a fresh connection.
func TestKillWorker_WorkerStaysAlive(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(2 * 1024 * 1024)
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

	workingPath := filepath.Join(tmpDir, "kill_stay.bin") + types.IncompleteSuffix
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("kill-stay", fileSize)
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

	waitForActiveTask(t, d, workerID, 2*time.Second)

	if !d.KillWorker(workerID) {
		close(release)
		cancel()
		queue.Close()
		t.Fatal("KillWorker returned false while worker was active")
	}
	close(release)
	queue.Close()

	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("worker did not complete remaining work after Kill (goroutine should stay alive)")
	}

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

func TestProgressState_KillAndSlowThresholdBridge(t *testing.T) {
	state := progress.New("bridge-test", 1024)

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

	var thr atomic.Value
	thr.Store(float64(-1))
	state.SetSetSlowThresholdFn(func(v float64) { thr.Store(v) })
	state.SetSlowWorkerThreshold(0)
	if got := thr.Load().(float64); got != 0 {
		t.Errorf("slow threshold bridge got %v, want 0", got)
	}

	state.SessionReset()
	if state.KillWorker(1) {
		t.Error("KillWorker should return false after SessionReset cleared the fn")
	}
	state.SetSlowWorkerThreshold(0.5)
}

func TestProgressState_KillAndSlowThresholdBridge_Concurrent(t *testing.T) {
	state := progress.New("race-bridge", 1024)
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
