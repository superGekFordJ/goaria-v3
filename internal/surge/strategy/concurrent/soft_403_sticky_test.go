package concurrent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
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

func serveRange206(w http.ResponseWriter, r *http.Request, blob []byte) {
	rng := r.Header.Get("Range")
	if rng == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(blob)
		return
	}
	var start, end int64
	n, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end)
	if err != nil || n != 2 || start < 0 || end >= int64(len(blob)) || start > end {
		http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(blob)))
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.CopyN(w, bytes.NewReader(blob[start:end+1]), end-start+1)
}

func TestSoft403StickyExhaustions_Constant(t *testing.T) {
	if Soft403StickyExhaustions != 16 {
		t.Fatalf("Soft403StickyExhaustions=%d, want 16", Soft403StickyExhaustions)
	}
}

// TestProbe_Intermittent403_Recovers: first failBudget GETs return 403, then 206.
// Soft residual + sticky headroom must complete (Alist/115 soft-throttle shape).
func TestProbe_Intermittent403_Recovers(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * utils.KiB)
	blob := make([]byte, fileSize)
	for i := range blob {
		blob[i] = byte(i)
	}

	const failBudget = 8
	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n <= failBudget {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveRange206(w, r, blob)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "intermittent403_ok.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("probe-403-ok", fileSize)
	d := NewConcurrentDownloader("probe-403-ok", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		MaxTaskRetries:            3,
		Workers:                   2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 50*time.Second)
	if err != nil {
		t.Fatalf("intermittent 403 should recover via soft residual: %v", err)
	}
	vp := state.Bytes.VerifiedProgress.Load()
	if vp < fileSize {
		t.Fatalf("VerifiedProgress=%d, want >= %d", vp, fileSize)
	}
	t.Logf("intermittent 403 recovered: VP=%d requests=%d", vp, requests.Load())
}

// TestProbe_Intermittent500_ResidualContinueRecovers: 500 stays non-permanent;
// residual continue still succeeds.
func TestProbe_Intermittent500_ResidualContinueRecovers(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * utils.KiB)
	blob := make([]byte, fileSize)
	for i := range blob {
		blob[i] = byte(i)
	}

	const failBudget = 8
	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n <= failBudget {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		serveRange206(w, r, blob)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "intermittent500_ok.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("probe-500-ok", fileSize)
	d := NewConcurrentDownloader("probe-500-ok", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		MaxTaskRetries:            3,
		Workers:                   2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 35*time.Second)
	if err != nil {
		t.Fatalf("intermittent 500 should recover via residual continue: %v", err)
	}
	vp := state.Bytes.VerifiedProgress.Load()
	if vp < fileSize {
		t.Fatalf("VerifiedProgress=%d, want >= %d", vp, fileSize)
	}
}

// TestProbe_Intermittent403_WithinBurnBudget_Succeeds: fewer than MaxTaskRetries
// consecutive 403s still clear within the burn.
func TestProbe_Intermittent403_WithinBurnBudget_Succeeds(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(32 * utils.KiB)
	blob := make([]byte, fileSize)
	for i := range blob {
		blob[i] = byte(i)
	}

	const failBudget = 2
	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n <= failBudget {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveRange206(w, r, blob)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "intermittent403_burn_ok.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("probe-403-burn-ok", fileSize)
	d := NewConcurrentDownloader("probe-403-burn-ok", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		MaxTaskRetries:            3,
		Workers:                   1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 25*time.Second)
	if err != nil {
		t.Fatalf("403 within burn budget should succeed: %v", err)
	}
}

// TestProbe_RedirectThenIntermittent403_Recovers: Alist-like openlist→CDN
// redirect; CDN returns 403 for first failBudget hops, then 206.
func TestProbe_RedirectThenIntermittent403_Recovers(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(48 * utils.KiB)
	blob := make([]byte, fileSize)
	for i := range blob {
		blob[i] = byte(i % 251)
	}

	const failBudget = 8
	var cdnHits atomic.Int64
	var redirects atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/cdn/", func(w http.ResponseWriter, r *http.Request) {
		n := cdnHits.Add(1)
		if n <= failBudget {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveRange206(w, r, blob)
	})
	mux.HandleFunc("/alist/", func(w http.ResponseWriter, r *http.Request) {
		redirects.Add(1)
		http.Redirect(w, r, "/cdn/file.bin", http.StatusFound)
	})

	server := testutil.NewHTTPServerT(t, mux)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "redirect_403_ok.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("probe-redir-403-ok", fileSize)
	d := NewConcurrentDownloader("probe-redir-403-ok", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		MaxTaskRetries:            3,
		Workers:                   2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL+"/alist/file", destPath, fileSize, nil, 50*time.Second)
	if err != nil {
		t.Fatalf("redirect+intermittent CDN 403 should recover: %v", err)
	}
	if redirects.Load() < 1 {
		t.Fatal("expected at least one Alist redirect")
	}
	vp := state.Bytes.VerifiedProgress.Load()
	if vp < fileSize {
		t.Fatalf("VerifiedProgress=%d, want >= %d", vp, fileSize)
	}
}

// TestSoft403_StickyAll403_EscalatesAfterBudget: all-403 ends permanent after
// sticky budget; request count stays bounded.
func TestSoft403_StickyAll403_EscalatesAfterBudget(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	prev := soft403StickyExhaustions
	soft403StickyExhaustions = 2
	t.Cleanup(func() { soft403StickyExhaustions = prev })

	fileSize := int64(32 * utils.KiB)
	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "sticky_all403.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("soft403-sticky", fileSize)
	d := NewConcurrentDownloader("soft403-sticky", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		MaxTaskRetries:            2,
		Workers:                   1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 25*time.Second)
	if err == nil {
		t.Fatal("expected permanent after soft-403 sticky budget")
	}
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("want IsPermanentHTTPError, got: %v", err)
	}
	n := requests.Load()
	// limit=2, MaxTaskRetries=2, 1 worker → minimum 2*2=4 requests before escalate.
	if n < 4 {
		t.Fatalf("expected at least limit*MaxTaskRetries=%d requests before escalate, got %d", 2*2, n)
	}
	if n > 32 {
		t.Fatalf("request count %d looks like unbounded churn", n)
	}
}

// TestSoft403_Mixed206ResetsSticky: intervening 206 clears sticky counter so
// a later burst of 403s does not escalate early.
func TestSoft403_Mixed206ResetsSticky(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	prev := soft403StickyExhaustions
	soft403StickyExhaustions = 3
	t.Cleanup(func() { soft403StickyExhaustions = prev })

	fileSize := int64(48 * utils.KiB)
	blob := make([]byte, fileSize)
	for i := range blob {
		blob[i] = byte(i)
	}

	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		// First few 403, then sustained 206 — mixed progress must reset sticky.
		if n <= 4 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveRange206(w, r, blob)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "mixed206_403.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("soft403-mixed", fileSize)
	d := NewConcurrentDownloader("soft403-mixed", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		MaxTaskRetries:            3,
		Workers:                   2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 50*time.Second)
	if err != nil {
		t.Fatalf("mixed 206/403 should complete after sticky reset: %v", err)
	}
	if state.Bytes.VerifiedProgress.Load() < fileSize {
		t.Fatalf("VerifiedProgress=%d, want >= %d", state.Bytes.VerifiedProgress.Load(), fileSize)
	}
}

// TestSoft403_Hard404_ImmediatePermanent: mid-chunk 404 stays immediate permanent
// (no sticky delay).
func TestSoft403_Hard404_ImmediatePermanent(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	prev := soft403StickyExhaustions
	soft403StickyExhaustions = 16
	t.Cleanup(func() { soft403StickyExhaustions = prev })

	fileSize := int64(16 * utils.KiB)
	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "hard404.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("hard404", fileSize)
	d := NewConcurrentDownloader("hard404", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		MaxTaskRetries:            1,
		Workers:                   1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 8*time.Second)
	if err == nil {
		t.Fatal("expected immediate permanent on 404")
	}
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("want IsPermanentHTTPError, got: %v", err)
	}
	n := requests.Load()
	if n > 4 {
		t.Fatalf("404 should escalate after one burn, got %d requests", n)
	}
}

// TestSoft403_B1_EscalateAfterBudget confirms sticky escalate still Push-then-return.
func TestSoft403_B1_EscalateAfterBudget(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	prev := soft403StickyExhaustions
	soft403StickyExhaustions = 2
	t.Cleanup(func() { soft403StickyExhaustions = prev })

	fileSize := int64(16 * utils.KiB)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	workingPath := filepath.Join(tmpDir, "soft403_b1.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("soft403-b1", nil, nil, &types.RuntimeConfig{
		MaxTaskRetries:   1,
		WorkerBufferSize: 16 * utils.KiB,
	})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = d.worker(ctx, 0, []string{server.URL}, f, queue, fileSize, &http.Client{})
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("expected permanent after sticky budget, got: %v", err)
	}
	if queue.Len() < 1 {
		t.Fatal("sticky escalate B1 requires residual requeue before return")
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

// TestSoft403_Hard401_DownloadTaskImmediate: downloadTask wraps 401 immediately.
func TestSoft403_Hard401_DownloadTaskImmediate(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "hard401.bin")
	f, err := os.Create(destPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("hard401", nil, nil, &types.RuntimeConfig{MaxTaskRetries: 1})
	task := types.Task{Offset: 0, Length: 1024}
	active := &ActiveTask{Task: task, workerID: 0}
	active.CurrentOffset.Store(task.Offset)
	active.StopAt.Store(task.Offset + task.Length)

	err = d.downloadTask(context.Background(), server.URL, f, active, make([]byte, 32*1024), &http.Client{}, 1024)
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("401 must be immediate permanent, got: %v", err)
	}
}

// TestSoft403_HealthCancel_DoesNotIncrementSticky verifies that a health-cancelled
// 403 attempt does not increment the sticky counter. The first request returns 403
// (setting LastHTTPStatus); the second holds until the task is externally cancelled
// via activeTask.Cancel (simulating health-check cancellation). The health-cancel
// block sets lastErr=nil before the post-exhaustion block, so the counter stays 0.
func TestSoft403_HealthCancel_DoesNotIncrementSticky(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	prev := soft403StickyExhaustions
	soft403StickyExhaustions = 1
	t.Cleanup(func() { soft403StickyExhaustions = prev })

	fileSize := int64(32 * utils.KiB)
	blob := make([]byte, fileSize)
	for i := range blob {
		blob[i] = byte(i)
	}

	var requests atomic.Int64
	secondStarted := make(chan struct{})
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if n == 2 {
			close(secondStarted)
			<-r.Context().Done()
			return
		}
		serveRange206(w, r, blob)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "hc403.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("hc403", fileSize)
	d := NewConcurrentDownloader("hc403", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		MaxTaskRetries:            3,
		Workers:                   1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Cancel the active task once the second request reaches the handler,
	// simulating a health-check cancel while LastHTTPStatus is still 403
	// from the first attempt.
	go func() {
		<-secondStarted
		d.activeMu.Lock()
		for _, at := range d.activeTasks {
			if at.Cancel != nil {
				at.Cancel()
			}
		}
		d.activeMu.Unlock()
	}()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 30*time.Second)
	if err != nil {
		t.Fatalf("download should complete after health-cancel + 206: %v", err)
	}
	if got := d.soft403NoProgressExhaustions.Load(); got != 0 {
		t.Fatalf("sticky counter = %d, want 0 (health-cancel must not increment)", got)
	}
}

// TestSoft403_DownloadEntry_ResetsStickyCounter verifies that Download entry
// resets the sticky counter to 0. A 404 (hard permanent) does not increment the
// counter, so any non-zero value after Download proves the entry reset was skipped.
func TestSoft403_DownloadEntry_ResetsStickyCounter(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(16 * utils.KiB)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "entry_reset.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("entry-reset", fileSize)
	d := NewConcurrentDownloader("entry-reset", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		MaxTaskRetries:            1,
		Workers:                   1,
	})

	// Pre-warm the counter to a non-zero value (simulating prior sticky pressure).
	d.soft403NoProgressExhaustions.Store(5)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 15*time.Second)
	if err == nil {
		t.Fatal("expected permanent error from 404")
	}
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("expected IsPermanentHTTPError, got: %v", err)
	}
	// 404 is hard permanent — it returns before the 403 sticky check, so the
	// counter must be 0 (reset at Download entry, not incremented by 404).
	if got := d.soft403NoProgressExhaustions.Load(); got != 0 {
		t.Fatalf("sticky counter = %d after Download, want 0 (entry reset)", got)
	}
}
