package concurrent

import (
	"context"
	"errors"
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

// setNoProgressGuardTestLimits overrides the residual-requeue fuse limit and
// the frozen-VP dwell window for tests (same override pattern as
// setSoft403GuardTestLimits).
func setNoProgressGuardTestLimits(t *testing.T, limit int, dwell time.Duration) {
	t.Helper()
	prevLimit := noProgressResidualExhaustions
	prevDwell := noProgressDwellWindow
	noProgressResidualExhaustions = limit
	noProgressDwellWindow = dwell
	t.Cleanup(func() {
		noProgressResidualExhaustions = prevLimit
		noProgressDwellWindow = prevDwell
	})
}

// TestNoProgressFuse_ResidualRotationReturnsSentinel: a permanently-failing
// non-permanent status (500) rotates the same residual shard through workers
// forever at 0 VerifiedProgress pre-fix. The fuse must convert that into a
// bounded ErrNoProgress while keeping the residual Push ordering.
func TestNoProgressFuse_ResidualRotationReturnsSentinel(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressGuardTestLimits(t, 3, 50*time.Millisecond)

	fileSize := int64(32 * utils.KiB)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	workingPath := filepath.Join(tmpDir, "np500.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("np500", nil, nil, &types.RuntimeConfig{
		MaxTaskRetries:   1,
		WorkerBufferSize: 32 * utils.KiB,
	})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err = d.worker(ctx, 0, []string{server.URL}, f, queue, fileSize, &http.Client{})
	if !errors.Is(err, types.ErrNoProgress) {
		t.Fatalf("expected ErrNoProgress, got: %v", err)
	}
	// Pre-fuse behavior: residual requeued and popped in a loop until ctx
	// deadline — distinguishable from a real fuse return.
	if ctx.Err() != nil {
		t.Fatalf("worker only returned on ctx timeout — fuse did not fire: %v", ctx.Err())
	}
	if queue.Len() < 1 {
		t.Fatal("expected residual requeue before return (Push ordering preserved)")
	}
	remaining, ok := queue.Pop()
	if !ok {
		t.Fatal("expected residual task on queue")
	}
	if remaining.Offset != 0 || remaining.Length != fileSize {
		t.Fatalf("residual range = [%d+%d), want [0+%d)", remaining.Offset, remaining.Length, fileSize)
	}
}

// TestNoProgressGuard_VPAdvanceResets: any VerifiedProgress delta past the
// primed baseline clears the exhaustion count and raises the baseline.
func TestNoProgressGuard_VPAdvanceResets(t *testing.T) {
	_, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressGuardTestLimits(t, 3, 50*time.Millisecond)

	state := progress.New("np-guard", 1024)
	d := NewConcurrentDownloader("np-guard", nil, state, &types.RuntimeConfig{})

	d.primeNoProgressGuard()
	for i := range 2 {
		if d.recordNoProgressRequeue(time.Now()) {
			t.Fatalf("fuse tripped at %d below limit 3", i+1)
		}
	}

	// VP advance: count clears, baseline moves.
	state.Bytes.VerifiedProgress.Add(1)
	if d.recordNoProgressRequeue(time.Now()) {
		t.Fatal("fuse tripped on the record that observed VP advance — reset missing")
	}
	d.noProgressGuard.mu.Lock()
	got := d.noProgressGuard.exhaustions
	d.noProgressGuard.mu.Unlock()
	if got != 0 {
		t.Fatalf("exhaustions=%d after VP advance, want 0", got)
	}

	// Limit reached → fuse arms; the trip additionally needs the frozen-VP
	// dwell to elapse (rate-pump guard), so the record at the limit returns
	// false and a later record while VP stays frozen trips.
	for i := range 3 {
		if d.recordNoProgressRequeue(time.Now()) {
			t.Fatalf("fuse tripped early at %d of 3", i+1)
		}
	}
	if d.recordNoProgressRequeue(time.Now()) {
		t.Fatal("fuse tripped inside the armed dwell window")
	}
	time.Sleep(80 * time.Millisecond) // > 50ms dwell override
	if !d.recordNoProgressRequeue(time.Now()) {
		t.Fatal("fuse did not trip after frozen-VP dwell at limit")
	}
}

// TestNoProgressFuse_HealthCancelCountsAtFrozenVP locks the counting rule:
// health-cancel residual requeues count while VerifiedProgress is frozen —
// a persistent tarpit must not cycle cancel→requeue forever.
func TestNoProgressFuse_HealthCancelCountsAtFrozenVP(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressGuardTestLimits(t, 3, 50*time.Millisecond)

	fileSize := int64(64 * utils.KiB)
	// 206 headers, zero bytes, holds the connection → worker stalls in Read.
	tarpitSrv := newTarpitServer(t, fileSize, 0, 0)

	workingPath := filepath.Join(tmpDir, "np_hc.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("np-hc", fileSize)
	d := NewConcurrentDownloader("np-hc", nil, state, &types.RuntimeConfig{
		MaxTaskRetries:        1,
		WorkerBufferSize:      32 * utils.KiB,
		StallTimeout:          50 * time.Millisecond,
		SlowWorkerGracePeriod: 0,
	})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Periodically run the real health check so the tarpit worker's frozen
	// LastActivity trips stall detection (the resident-monitor cancel path).
	stopHealth := make(chan struct{})
	defer close(stopHealth)
	go func() {
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopHealth:
				return
			case <-ticker.C:
				d.checkWorkerHealth()
			}
		}
	}()

	err = d.worker(ctx, 0, []string{tarpitSrv.URL}, f, queue, fileSize, &http.Client{})
	if !errors.Is(err, types.ErrNoProgress) {
		t.Fatalf("expected ErrNoProgress from health-cancel fuse, got: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("worker only returned on ctx timeout — fuse did not fire: %v", ctx.Err())
	}
	// Fuse-trip cleanup: activeTasks entry dropped, worker counter decremented,
	// residual still queued (Push-then-return ordering preserved).
	d.activeMu.Lock()
	activeLen := len(d.activeTasks)
	d.activeMu.Unlock()
	if activeLen != 0 {
		t.Fatalf("activeTasks still has %d entries after fuse trip", activeLen)
	}
	if got := state.ActiveWorkers.Load(); got != 0 {
		t.Fatalf("ActiveWorkers=%d after fuse trip, want 0", got)
	}
	if queue.Len() < 1 {
		t.Fatal("residual must stay queued on fuse trip")
	}
}

// TestNoProgressFuse_Constant5xxDownloadConverges: Download-level — a wall of
// non-permanent 5xx across multiple shards must converge to ErrNoProgress
// instead of rotating residual shards through workers forever.
func TestNoProgressFuse_Constant5xxDownloadConverges(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressGuardTestLimits(t, 4, 50*time.Millisecond)

	fileSize := int64(256 * utils.KiB)
	var requests atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "np_5xx.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	} else {
		t.Fatal(err)
	}

	state := progress.New("np-5xx", fileSize)
	d := NewConcurrentDownloader("np-5xx", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              64 * utils.KiB,
		MaxTaskRetries:            1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 12*time.Second)
	if !errors.Is(err, types.ErrNoProgress) {
		t.Fatalf("expected ErrNoProgress, got: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("converged only via ctx timeout — fuse did not fire")
	}
	// Bounded, not unbounded: the count is ~limit failures plus the pump rate
	// over one armed dwell window — far below what unbounded rotation produces
	// inside the ctx budget.
	if n := requests.Load(); n > 20000 {
		t.Fatalf("request count %d looks like unbounded rotation", n)
	}
}

// TestPayloadFirst_VerifyStall_HealthCancelThenFuse proves both halves of the
// safety net: the resident health monitor cancels a verify-phase tarpit
// (client.Do blocked before the first WriteAt — previously no cancel path),
// and the no-progress fuse bounds the resulting cancel→requeue rotation.
// hits >= 2 evidences that a second verify request was actually issued.
func TestPayloadFirst_VerifyStall_HealthCancelThenFuse(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressGuardTestLimits(t, 3, 50*time.Millisecond)

	fileSize := int64(256 * 1024)
	var hits atomic.Int64
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-r.Context().Done() // TTFB tarpit: never writes response headers
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "pf_stall.bin")
	d := newPayloadFirstDownloader(t, destPath, fileSize)
	d.Runtime.StallTimeout = 200 * time.Millisecond
	d.Runtime.SlowWorkerGracePeriod = 200 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL, destPath, fileSize, nil, 12*time.Second)
	if !errors.Is(err, types.ErrNoProgress) {
		t.Fatalf("expected ErrNoProgress, got: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("returned only on ctx timeout — resident health monitor / fuse did not fire")
	}
	if got := hits.Load(); got < 2 {
		t.Fatalf("verify-phase hits=%d, want >=2 (cancel→requeue→retry proves resident monitor)", got)
	}
}

// TestNoProgressFuse_ExhaustedTaskRotatesMirror locks the mirror-rotation fix:
// with a 1-try budget a worker that exhausts retries on a bad mirror must
// rotate its mirror preference, not stay pinned and pump residual requeues
// on the failing mirror forever.
func TestNoProgressFuse_ExhaustedTaskRotatesMirror(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	const taskLen = int64(32 * utils.KiB)
	totalSize := int64(64 * utils.KiB)

	var badHits, goodHits atomic.Int64
	badSrv := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer badSrv.Close()

	blob := make([]byte, taskLen)
	for i := range blob {
		blob[i] = byte(i)
	}
	goodSrv := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		serveRange206(w, r, blob)
	}))
	defer goodSrv.Close()

	workingPath := filepath.Join(tmpDir, "np_rot.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("np-rot", nil, nil, &types.RuntimeConfig{
		MaxTaskRetries:   1,
		WorkerBufferSize: 32 * utils.KiB,
	})

	queue := NewTaskQueue()
	// Task length < totalSize so the 200 response is rejected as range-ignored.
	queue.Push(types.Task{Offset: 0, Length: taskLen})
	queue.Close() // worker exits via Pop once the residual is consumed

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = d.worker(ctx, 0, []string{badSrv.URL, goodSrv.URL}, f, queue, totalSize, &http.Client{})
	if err != nil {
		t.Fatalf("worker should complete via rotated mirror, got: %v", err)
	}
	if badHits.Load() < 1 || goodHits.Load() < 1 {
		t.Fatalf("bad=%d good=%d, want >=1 each (rotation after exhaustion)", badHits.Load(), goodHits.Load())
	}
}

// TestNoProgressFuse_Soft403ChannelWins locks the dedicated-channel priority:
// events whose last status is 403 are evaluated by the sticky-403 channel and
// must not also feed the no-progress fuse — otherwise a fast 403 pump trips
// the (retryable) fuse before the slower dwell-window channel can return
// ErrPermanentHTTP.
func TestNoProgressFuse_Soft403ChannelWins(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	// Fuse would arm+trip far earlier than the sticky window if 403 events
	// were counted — the skip must let soft403 decide first.
	setNoProgressGuardTestLimits(t, 2, 10*time.Millisecond)
	setSoft403GuardTestLimits(t, 2, 60*time.Millisecond)

	fileSize := int64(32 * utils.KiB)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	workingPath := filepath.Join(tmpDir, "np_403.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	state := progress.New("np-403", fileSize)
	d := NewConcurrentDownloader("np-403", nil, state, &types.RuntimeConfig{
		MaxTaskRetries:   1,
		WorkerBufferSize: 32 * utils.KiB,
	})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})
	defer queue.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err = d.worker(ctx, 0, []string{server.URL}, f, queue, fileSize, &http.Client{})
	if errors.Is(err, types.ErrNoProgress) {
		t.Fatalf("fuse must not preempt the sticky-403 channel, got: %v", err)
	}
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("expected sticky-403 ErrPermanentHTTP, got: %v", err)
	}
}

// TestNoProgressGuard_ResetOnDownloadEntry: Download() resets and re-primes
// the guard, so counters from a previous Download() cannot leak into a new
// session (baseline is re-captured post-restore).
func TestNoProgressGuard_ResetOnDownloadEntry(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * utils.KiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "np_reset.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	} else {
		t.Fatal(err)
	}

	state := progress.New("np-reset", fileSize)
	d := NewConcurrentDownloader("np-reset", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
	})

	d.noProgressGuard.mu.Lock()
	d.noProgressGuard.primed = true
	d.noProgressGuard.baselineVP = 999
	d.noProgressGuard.exhaustions = 7
	d.noProgressGuard.candidateSince = time.Now()
	d.noProgressGuard.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := d.Download(ctx, server.URL(), nil, nil, destPath, fileSize); err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	d.noProgressGuard.mu.Lock()
	exhaustions := d.noProgressGuard.exhaustions
	baselineVP := d.noProgressGuard.baselineVP
	primed := d.noProgressGuard.primed
	candidateZero := d.noProgressGuard.candidateSince.IsZero()
	d.noProgressGuard.mu.Unlock()
	if exhaustions != 0 || baselineVP != 0 || !candidateZero || !primed {
		t.Fatalf("guard state exhaustions=%d baseline=%d primed=%v candidateZero=%v after Download, want reset+primed baseline 0",
			exhaustions, baselineVP, primed, candidateZero)
	}
}
