package concurrent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func TestPermanentStatus_DownloadTaskMatrix(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	cases := []struct {
		name      string
		status    int
		permanent bool
	}{
		{"401", http.StatusUnauthorized, true},
		{"403", http.StatusForbidden, false},
		{"404", http.StatusNotFound, true},
		{"410", http.StatusGone, true},
		{"416", http.StatusRequestedRangeNotSatisfiable, true},
		{"429", http.StatusTooManyRequests, false},
		{"500", http.StatusInternalServerError, false},
		{"503", http.StatusServiceUnavailable, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			destPath := filepath.Join(tmpDir, "matrix_"+tc.name+".bin")
			f, err := os.Create(destPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()

			d := NewConcurrentDownloader("matrix-"+tc.name, nil, nil, &types.RuntimeConfig{
				MaxTaskRetries: 1,
			})
			task := types.Task{Offset: 0, Length: 1024}
			active := &ActiveTask{Task: task, workerID: 0}
			active.CurrentOffset.Store(task.Offset)
			active.StopAt.Store(task.Offset + task.Length)

			err = d.downloadTask(context.Background(), server.URL, f, active, make([]byte, 32*1024), &http.Client{}, 1024)
			if err == nil {
				t.Fatal("expected error for non-206 status")
			}
			if got := active.LastHTTPStatus.Load(); got != int32(tc.status) {
				t.Fatalf("LastHTTPStatus=%d, want %d", got, tc.status)
			}
			isPerm := types.IsPermanentHTTPError(err)
			if isPerm != tc.permanent {
				t.Fatalf("IsPermanentHTTPError=%v, want %v (err=%v)", isPerm, tc.permanent, err)
			}
			if tc.status >= 400 && tc.status < 500 {
				// Poison path: enough consecutive 4xx should disable hedge.
				for i := 0; i < types.HedgeErrorThreshold; i++ {
					_ = d.downloadTask(context.Background(), server.URL, f, active, make([]byte, 32*1024), &http.Client{}, 1024)
				}
				if !d.hedgeDisabled.Load() {
					t.Fatal("expected recordHedgeError to disable hedge after threshold 4xx")
				}
			}
		})
	}
}

func TestSticky403_NoChurn(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	prev := soft403StickyExhaustions
	soft403StickyExhaustions = 3
	t.Cleanup(func() { soft403StickyExhaustions = prev })

	fileSize := int64(64 * utils.KiB)
	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "sticky403.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("sticky403", fileSize)
	d := NewConcurrentDownloader("sticky403", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		MaxTaskRetries:            3,
		Workers:                   2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 35*time.Second)
	if err == nil {
		t.Fatal("expected permanent HTTP error after soft-403 sticky budget")
	}
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("expected IsPermanentHTTPError, got: %v", err)
	}

	n := requests.Load()
	// Budget=3 exhaustions × MaxTaskRetries(=3) across workers — bounded, not unbounded churn.
	if n < 1 {
		t.Fatalf("expected at least one request, got %d", n)
	}
	if n > 64 {
		t.Fatalf("request count %d looks like residual churn (want bounded)", n)
	}
}

func TestMirrors_403_Failover(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(512 * utils.KiB)

	badServer := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer badServer.Close()

	goodServer := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
		testutil.WithLatency(10*time.Millisecond),
	)
	defer goodServer.Close()

	destPath := filepath.Join(tmpDir, "failover403.bin")
	state := progress.New("failover403", fileSize)
	d := NewConcurrentDownloader("failover403", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 4,
		MaxTaskRetries:            5,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	mirrors := []string{badServer.URL, goodServer.URL()}
	err := d.Download(ctx, badServer.URL, mirrors, mirrors, destPath, fileSize)
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}
	if err := testutil.VerifyFileSize(destPath+types.IncompleteSuffix, fileSize); err != nil {
		t.Error(err)
	}
	if goodServer.Stats().TotalRequests == 0 {
		t.Error("expected good server to handle requests after 403 failover")
	}
}

// TestPermanentHTTP_B1_RequeueThenReturn asserts residual Push still runs on the
// permanent path before return (ordering unit test; not full Pause integration).
// Uses 404 (hard permanent) — mid-chunk 403 is soft until sticky budget.
func TestPermanentHTTP_B1_RequeueThenReturn(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(32 * utils.KiB)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	workingPath := filepath.Join(tmpDir, "b1.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("b1", nil, nil, &types.RuntimeConfig{
		MaxTaskRetries:   1,
		WorkerBufferSize: 32 * utils.KiB,
	})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = d.worker(ctx, 0, []string{server.URL}, f, queue, fileSize, &http.Client{})
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("expected permanent error, got: %v", err)
	}
	if queue.Len() < 1 {
		t.Fatal("B1 requires residual requeue before return; queue empty")
	}
	remaining, ok := queue.Pop()
	if !ok {
		t.Fatal("expected residual task on queue")
	}
	if remaining.Offset != 0 || remaining.Length != fileSize {
		t.Fatalf("residual range = [%d+%d), want [0+%d)", remaining.Offset, remaining.Length, fileSize)
	}
	queue.Close()
}

func TestScaleWorkers_PermanentForward(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(16 * utils.KiB)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	workingPath := filepath.Join(tmpDir, "scale_perm.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("scale-perm", nil, nil, &types.RuntimeConfig{
		MaxTaskRetries:   1,
		WorkerBufferSize: 32 * utils.KiB,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errs := make(chan error, 4)
	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})

	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		cancel:    cancel,
		mirrors:   []string{server.URL},
		file:      f,
		queue:     queue,
		totalSize: fileSize,
		client:    &http.Client{},
		errs:      errs,
	})
	d.workersActive.Store(true)

	if got := d.ScaleWorkers(1); got != 1 {
		t.Fatalf("ScaleWorkers(1)=%d, want 1", got)
	}

	select {
	case err := <-errs:
		if !types.IsPermanentHTTPError(err) {
			t.Fatalf("forwarded err not permanent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for scaled-worker permanent error forward")
	}

	if ctx.Err() == nil {
		t.Fatal("expected cancel to fire after scaled-worker permanent error")
	}

	queue.Close()
	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scaled workers did not exit")
	}
}
