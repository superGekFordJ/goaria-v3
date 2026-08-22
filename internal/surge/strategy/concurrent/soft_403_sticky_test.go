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
	"sync"
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

type soft403RoundTripper func(*http.Request) (*http.Response, error)

func (f soft403RoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type blockedSubBatchBody struct {
	blocked     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
	first       bool
}

func (b *blockedSubBatchBody) Read(p []byte) (int, error) {
	if b.first {
		b.first = false
		for i := range p {
			p[i] = 1
		}
		return len(p), nil
	}
	b.once.Do(func() { close(b.blocked) })
	<-b.release
	for i := range p {
		p[i] = 2
	}
	return len(p), io.EOF
}

func (b *blockedSubBatchBody) Close() error {
	return nil
}

type soft403GuardSnapshot struct {
	exhaustionCount           int
	observedVerifiedProgress  int64
	candidateSince            time.Time
	candidateVerifiedProgress int64
}

func snapshotSoft403Guard(d *ConcurrentDownloader) soft403GuardSnapshot {
	d.soft403Guard.mu.Lock()
	defer d.soft403Guard.mu.Unlock()

	return soft403GuardSnapshot{
		exhaustionCount:           d.soft403Guard.exhaustionCount,
		observedVerifiedProgress:  d.soft403Guard.observedVerifiedProgress,
		candidateSince:            d.soft403Guard.candidateSince,
		candidateVerifiedProgress: d.soft403Guard.candidateVerifiedProgress,
	}
}

func setSoft403GuardTestLimits(t *testing.T, limit int, confirmWindow time.Duration) {
	t.Helper()
	previousLimit := soft403StickyExhaustions
	previousWindow := soft403NoProgressConfirmWindow
	soft403StickyExhaustions = limit
	soft403NoProgressConfirmWindow = confirmWindow
	t.Cleanup(func() {
		soft403StickyExhaustions = previousLimit
		soft403NoProgressConfirmWindow = previousWindow
	})
}

func waitForSoft403Condition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for Soft-403 condition")
		case <-ticker.C:
		}
	}
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

	setSoft403GuardTestLimits(t, 2, 200*time.Millisecond)

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

// TestSoft403_Mixed206ProgressRecovers verifies verified body progress recovers.
func TestSoft403_Mixed206ProgressRecovers(t *testing.T) {
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
	guard := snapshotSoft403Guard(d)
	if guard.exhaustionCount != 0 || !guard.candidateSince.IsZero() {
		t.Fatalf("guard = %+v, want no health-cancel pressure", guard)
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

	d.soft403Guard.mu.Lock()
	d.soft403Guard.exhaustionCount = 5
	d.soft403Guard.candidateSince = time.Now()
	d.soft403Guard.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 15*time.Second)
	if err == nil {
		t.Fatal("expected permanent error from 404")
	}
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("expected IsPermanentHTTPError, got: %v", err)
	}
	guard := snapshotSoft403Guard(d)
	if guard.exhaustionCount != 0 || !guard.candidateSince.IsZero() {
		t.Fatalf("guard = %+v after Download, want reset state", guard)
	}
}

func TestSoft403Guard_NoVerifiedProgressConfirmsAfterWindow(t *testing.T) {
	state := progress.New("guard-no-progress", 1024)
	d := NewConcurrentDownloader("guard-no-progress", nil, state, nil)
	d.resetSoft403Guard()
	d.primeSoft403Guard()

	now := time.Unix(100, 0)
	for i := range Soft403StickyExhaustions {
		if d.recordSoft403Exhaustion(now) {
			t.Fatalf("exhaustion %d escalated while arming", i+1)
		}
	}

	guard := snapshotSoft403Guard(d)
	if guard.exhaustionCount != Soft403StickyExhaustions || guard.candidateSince.IsZero() {
		t.Fatalf("guard = %+v, want armed candidate at limit", guard)
	}
	if d.recordSoft403Exhaustion(now.Add(Soft403NoProgressConfirmWindow - time.Nanosecond)) {
		t.Fatal("guard escalated before confirmation deadline")
	}
	if !d.recordSoft403Exhaustion(now.Add(Soft403NoProgressConfirmWindow)) {
		t.Fatal("guard did not escalate at confirmation deadline")
	}
}

func TestSoft403Guard_FinalRecheckSeesVerifiedProgress(t *testing.T) {
	state := progress.New("guard-final-recheck", 1024)
	d := NewConcurrentDownloader("guard-final-recheck", nil, state, nil)
	d.resetSoft403Guard()
	d.primeSoft403Guard()

	now := time.Unix(150, 0)
	for i := range Soft403StickyExhaustions {
		if d.recordSoft403Exhaustion(now) {
			t.Fatalf("exhaustion %d escalated while arming", i+1)
		}
	}
	if d.soft403Guard.recordWithProgress(
		now.Add(Soft403NoProgressConfirmWindow),
		state.Bytes.VerifiedProgress.Load(),
		func() int64 {
			state.Bytes.VerifiedProgress.Store(1)
			return state.Bytes.VerifiedProgress.Load()
		},
		Soft403StickyExhaustions,
		Soft403NoProgressConfirmWindow,
	) {
		t.Fatal("final verified-progress recheck escalated")
	}
	guard := snapshotSoft403Guard(d)
	if guard.exhaustionCount != 1 || !guard.candidateSince.IsZero() || guard.observedVerifiedProgress != 1 {
		t.Fatalf("guard after final recheck = %+v, want fresh epoch", guard)
	}
}

func TestSoft403Guard_ProgressChangesClearCandidate(t *testing.T) {
	state := progress.New("guard-progress", 1024)
	d := NewConcurrentDownloader("guard-progress", nil, state, nil)
	d.resetSoft403Guard()
	d.primeSoft403Guard()

	now := time.Unix(200, 0)
	for i := range Soft403StickyExhaustions {
		if d.recordSoft403Exhaustion(now) {
			t.Fatalf("exhaustion %d escalated while arming", i+1)
		}
	}

	state.Bytes.VerifiedProgress.Store(128)
	if d.recordSoft403Exhaustion(now.Add(time.Second)) {
		t.Fatal("verified progress advance escalated")
	}
	guard := snapshotSoft403Guard(d)
	if guard.exhaustionCount != 1 || !guard.candidateSince.IsZero() || guard.observedVerifiedProgress != 128 {
		t.Fatalf("guard after advance = %+v, want fresh epoch", guard)
	}

	for i := 1; i < Soft403StickyExhaustions; i++ {
		if d.recordSoft403Exhaustion(now.Add(time.Duration(i+1) * time.Second)) {
			t.Fatalf("new epoch escalated before candidate at exhaustion %d", i+1)
		}
	}
	guard = snapshotSoft403Guard(d)
	if guard.candidateSince.IsZero() || guard.candidateVerifiedProgress != 128 {
		t.Fatalf("guard = %+v, want candidate based on advanced VP", guard)
	}

	state.Bytes.VerifiedProgress.Store(64)
	if d.recordSoft403Exhaustion(now.Add(100 * time.Second)) {
		t.Fatal("verified progress decrease escalated")
	}
	guard = snapshotSoft403Guard(d)
	if guard.exhaustionCount != 1 || !guard.candidateSince.IsZero() || guard.observedVerifiedProgress != 64 {
		t.Fatalf("guard after decrease = %+v, want rebased fresh epoch", guard)
	}
}

func TestSoft403Guard_RestoredBaselineAndSessionReset(t *testing.T) {
	state := progress.New("guard-resume", 1024)
	state.Bytes.VerifiedProgress.Store(512)
	d := NewConcurrentDownloader("guard-resume", nil, state, nil)
	d.resetSoft403Guard()
	d.primeSoft403Guard()

	now := time.Unix(300, 0)
	guard := snapshotSoft403Guard(d)
	if guard.observedVerifiedProgress != 512 || guard.exhaustionCount != 0 || !guard.candidateSince.IsZero() {
		t.Fatalf("guard after restored baseline = %+v", guard)
	}
	for i := range Soft403StickyExhaustions {
		if d.recordSoft403Exhaustion(now) {
			t.Fatalf("exhaustion %d escalated while arming", i+1)
		}
	}
	if !d.recordSoft403Exhaustion(now.Add(Soft403NoProgressConfirmWindow)) {
		t.Fatal("historical resume VP incorrectly counted as fresh progress")
	}

	d.resetSoft403Guard()
	d.primeSoft403Guard()
	guard = snapshotSoft403Guard(d)
	if guard.observedVerifiedProgress != 512 || guard.exhaustionCount != 0 || !guard.candidateSince.IsZero() {
		t.Fatalf("guard after new session = %+v, want fresh state", guard)
	}
}

func TestSoft403Guard_NilStateFallbackEscalatesAtLimit(t *testing.T) {
	d := NewConcurrentDownloader("guard-nil", nil, nil, nil)
	d.resetSoft403Guard()

	now := time.Unix(400, 0)
	for i := range Soft403StickyExhaustions - 1 {
		if d.recordSoft403Exhaustion(now) {
			t.Fatalf("fallback escalated at exhaustion %d", i+1)
		}
	}
	if !d.recordSoft403Exhaustion(now) {
		t.Fatal("fallback did not escalate at limit")
	}
}

func TestSoft403Guard_ConcurrentRecord(t *testing.T) {
	state := progress.New("guard-race", 1024)
	d := NewConcurrentDownloader("guard-race", nil, state, nil)
	d.resetSoft403Guard()
	d.primeSoft403Guard()

	now := time.Unix(500, 0)
	results := make(chan bool, Soft403StickyExhaustions*2)
	var wg sync.WaitGroup
	for range Soft403StickyExhaustions * 2 {
		wg.Go(func() {
			results <- d.recordSoft403Exhaustion(now)
		})
	}
	wg.Wait()
	close(results)
	for escalated := range results {
		if escalated {
			t.Fatal("concurrent arm call escalated before confirmation")
		}
	}

	guard := snapshotSoft403Guard(d)
	if guard.exhaustionCount != Soft403StickyExhaustions || guard.candidateSince.IsZero() {
		t.Fatalf("guard after concurrent calls = %+v", guard)
	}
	if !d.recordSoft403Exhaustion(now.Add(Soft403NoProgressConfirmWindow)) {
		t.Fatal("concurrent guard did not confirm after deadline")
	}
}

func TestSoft403_Empty206DoesNotResetProgressGuard(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	state := progress.New("empty-206", 1024)
	d := NewConcurrentDownloader("empty-206", nil, state, &types.RuntimeConfig{WorkerBufferSize: 1024})
	d.resetSoft403Guard()
	d.primeSoft403Guard()

	now := time.Unix(600, 0)
	for i := range Soft403StickyExhaustions {
		if d.recordSoft403Exhaustion(now) {
			t.Fatalf("exhaustion %d escalated while arming", i+1)
		}
	}
	before := snapshotSoft403Guard(d)

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.Header().Set("Content-Range", "bytes 0-1023/1024")
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer server.Close()

	f, err := os.Create(filepath.Join(tmpDir, "empty206.surge"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	active := &ActiveTask{Task: types.Task{Offset: 0, Length: 1024}}
	active.StopAt.Store(1024)
	if err := d.downloadTask(context.Background(), server.URL, f, active, make([]byte, 1024), &http.Client{}, 1024); err == nil {
		t.Fatal("expected empty 206 body to fail")
	}
	if state.Bytes.VerifiedProgress.Load() != 0 {
		t.Fatalf("VerifiedProgress=%d, want 0", state.Bytes.VerifiedProgress.Load())
	}
	after := snapshotSoft403Guard(d)
	if after.exhaustionCount != before.exhaustionCount || after.candidateSince != before.candidateSince || after.candidateVerifiedProgress != before.candidateVerifiedProgress {
		t.Fatalf("empty 206 changed guard: before=%+v after=%+v", before, after)
	}
	if !d.recordSoft403Exhaustion(now.Add(Soft403NoProgressConfirmWindow)) {
		t.Fatal("empty 206 body prevented no-progress confirmation")
	}
}

func TestSoft403_StaleStatusDoesNotClassifyTransportFailure(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	setSoft403GuardTestLimits(t, 1, Soft403NoProgressConfirmWindow)

	fileSize := int64(1024)
	blob := bytes.Repeat([]byte{1}, int(fileSize))
	var calls atomic.Int32
	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})

	client := &http.Client{Transport: soft403RoundTripper(func(r *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Request:    r,
			}, nil
		case 2:
			return nil, fmt.Errorf("pre-response transport failure")
		default:
			queue.Close()
			return &http.Response{
				StatusCode:    http.StatusPartialContent,
				Header:        make(http.Header),
				Body:          io.NopCloser(bytes.NewReader(blob)),
				ContentLength: fileSize,
				Request:       r,
			}, nil
		}
	})}

	f, err := os.Create(filepath.Join(tmpDir, "stale-status.surge"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("stale-status", nil, nil, &types.RuntimeConfig{
		MaxTaskRetries:   2,
		WorkerBufferSize: int(fileSize),
	})
	if err := d.worker(context.Background(), 0, []string{"http://example.test"}, f, queue, fileSize, client); err != nil {
		t.Fatalf("stale 403 status classified final transport failure as terminal: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("requests=%d, want 3", calls.Load())
	}
}

func TestSoft403_BlockedReadPublishesSubBatch(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	setSoft403GuardTestLimits(t, 1, Soft403NoProgressConfirmWindow)

	const subBatch = 64 * 1024
	fileSize := int64(subBatch + 1)
	state := progress.New("blocked-read", fileSize)
	state.InitBitmap(fileSize, fileSize)
	d := NewConcurrentDownloader("blocked-read", nil, state, &types.RuntimeConfig{WorkerBufferSize: subBatch})
	d.resetSoft403Guard()
	d.primeSoft403Guard()

	now := time.Unix(700, 0)
	if d.recordSoft403Exhaustion(now) {
		t.Fatal("candidate armed permanently")
	}

	body := &blockedSubBatchBody{
		blocked: make(chan struct{}),
		release: make(chan struct{}),
		first:   true,
	}
	t.Cleanup(func() { body.releaseOnce.Do(func() { close(body.release) }) })
	client := &http.Client{Transport: soft403RoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       body,
			Request:    r,
		}, nil
	})}

	file, err := os.Create(filepath.Join(tmpDir, "blocked-read.surge"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	active := &ActiveTask{Task: types.Task{Offset: 0, Length: fileSize}}
	active.StopAt.Store(fileSize)
	done := make(chan error, 1)
	go func() {
		done <- d.downloadTask(context.Background(), "http://blocked-read.test", file, active, make([]byte, subBatch), client, fileSize)
	}()

	select {
	case <-body.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("body did not reach blocked read")
	}
	if got := state.Bytes.VerifiedProgress.Load(); got != subBatch {
		t.Fatalf("VerifiedProgress=%d while next read blocked, want %d", got, subBatch)
	}
	if d.recordSoft403Exhaustion(now.Add(Soft403NoProgressConfirmWindow)) {
		t.Fatal("blocked read caused a false Soft-403 permanent decision")
	}

	body.releaseOnce.Do(func() { close(body.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked-read download failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked-read download did not finish")
	}
	if got := state.Bytes.VerifiedProgress.Load(); got != fileSize {
		t.Fatalf("VerifiedProgress=%d, want %d", got, fileSize)
	}
}

func TestSoft403_SlowVerifiedBodyPreventsFalseTerminal(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()
	setSoft403GuardTestLimits(t, 2, 2*time.Second)

	blockSize := int64(types.WorkerBatchSize)
	rangeSize := 4 * blockSize
	fileSize := 2 * rangeSize
	block := bytes.Repeat([]byte{7}, int(blockSize))
	advanceBody := make(chan struct{})
	allowForbidden := make(chan struct{})
	healthyStarted := make(chan struct{})
	healthyFinished := make(chan struct{})
	var healthyStartedOnce sync.Once
	var healthyFinishedOnce sync.Once
	var forbiddenRequests atomic.Int64

	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || start < 0 || end < start {
			http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if start != 0 {
			select {
			case <-allowForbidden:
				forbiddenRequests.Add(1)
				w.WriteHeader(http.StatusForbidden)
			case <-r.Context().Done():
			}
			return
		}

		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", rangeSize-1, fileSize))
		w.Header().Set("Content-Length", strconv.FormatInt(rangeSize, 10))
		w.WriteHeader(http.StatusPartialContent)
		flusher, _ := w.(http.Flusher)
		for part := range int64(4) {
			if part > 0 {
				select {
				case <-advanceBody:
				case <-r.Context().Done():
					return
				}
			}
			if _, err := w.Write(block); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			if part == 0 {
				healthyStartedOnce.Do(func() { close(healthyStarted) })
			}
		}
		healthyFinishedOnce.Do(func() { close(healthyFinished) })
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "slow-verified-body.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	} else {
		t.Fatal(err)
	}

	state := progress.New("slow-verified-body", fileSize)
	d := NewConcurrentDownloader("slow-verified-body", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              rangeSize,
		WorkerBufferSize:          int(blockSize / 2),
		MaxTaskRetries:            2,
		DialHedgeCount:            0,
		SlowWorkerThreshold:       0,
		SlowWorkerGracePeriod:     0,
		StallTimeout:              0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- d.Download(ctx, server.URL, nil, nil, destPath, fileSize)
	}()

	select {
	case <-healthyStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy range did not start")
	}
	waitForSoft403Condition(t, 5*time.Second, func() bool {
		return state.Bytes.VerifiedProgress.Load() >= blockSize
	})
	close(allowForbidden)
	lastProgress := state.Bytes.VerifiedProgress.Load()

	for completedBlocks := int64(1); completedBlocks < 4; completedBlocks++ {
		waitForSoft403Condition(t, 5*time.Second, func() bool {
			guard := snapshotSoft403Guard(d)
			return !guard.candidateSince.IsZero() && guard.candidateVerifiedProgress >= lastProgress
		})
		select {
		case err := <-result:
			t.Fatalf("download terminated while healthy body was held: %v", err)
		default:
		}
		if completedBlocks < 3 {
			select {
			case <-healthyFinished:
				t.Fatal("healthy body finished before all synchronized progress pulses")
			default:
			}
		}
		select {
		case advanceBody <- struct{}{}:
		case err := <-result:
			t.Fatalf("download terminated before healthy progress pulse: %v", err)
		}
		waitForSoft403Condition(t, 5*time.Second, func() bool {
			return state.Bytes.VerifiedProgress.Load() > lastProgress
		})
		lastProgress = state.Bytes.VerifiedProgress.Load()
	}

	select {
	case <-healthyFinished:
	case <-time.After(5 * time.Second):
		t.Fatal("healthy body did not finish")
	}
	select {
	case err := <-result:
		if !types.IsPermanentHTTPError(err) {
			t.Fatalf("want confirmed permanent error after healthy body finished, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("persistent 403 range did not confirm after healthy body finished")
	}
	if forbiddenRequests.Load() < 2 {
		t.Fatalf("forbidden requests=%d, want persistent failures", forbiddenRequests.Load())
	}
}
