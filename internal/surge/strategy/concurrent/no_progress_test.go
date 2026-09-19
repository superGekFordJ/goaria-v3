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

// setNoProgressLimit overrides the residual-requeue fuse limit for tests
// (same override pattern as setSoft403GuardTestLimits).
func setNoProgressLimit(t *testing.T, limit int) {
	t.Helper()
	prev := noProgressResidualExhaustions
	noProgressResidualExhaustions = limit
	t.Cleanup(func() { noProgressResidualExhaustions = prev })
}

// TestNoProgressFuse_ResidualRotationReturnsSentinel: a permanently-failing
// non-permanent status (500) rotates the same residual shard through workers
// forever at 0 VerifiedProgress pre-fix. The fuse must convert that into a
// bounded ErrNoProgress while keeping the residual Push ordering.
func TestNoProgressFuse_ResidualRotationReturnsSentinel(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressLimit(t, 3)

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

	setNoProgressLimit(t, 3)

	state := progress.New("np-guard", 1024)
	d := NewConcurrentDownloader("np-guard", nil, state, &types.RuntimeConfig{})

	d.primeNoProgressGuard()
	for i := range 2 {
		if d.recordNoProgressRequeue() {
			t.Fatalf("fuse tripped at %d below limit 3", i+1)
		}
	}

	// VP advance: count clears, baseline moves.
	state.Bytes.VerifiedProgress.Add(1)
	if d.recordNoProgressRequeue() {
		t.Fatal("fuse tripped on the record that observed VP advance — reset missing")
	}
	d.noProgressGuard.mu.Lock()
	got := d.noProgressGuard.exhaustions
	d.noProgressGuard.mu.Unlock()
	if got != 0 {
		t.Fatalf("exhaustions=%d after VP advance, want 0", got)
	}

	var tripped bool
	for i := range 3 {
		tripped = d.recordNoProgressRequeue()
		if i < 2 && tripped {
			t.Fatalf("fuse tripped early at %d of 3", i+1)
		}
	}
	if !tripped {
		t.Fatal("fuse did not trip at limit 3 with frozen VP")
	}
}

// TestNoProgressFuse_HealthCancelCountsAtFrozenVP locks the counting rule:
// health-cancel residual requeues count while VerifiedProgress is frozen —
// a persistent tarpit must not cycle cancel→requeue forever.
func TestNoProgressFuse_HealthCancelCountsAtFrozenVP(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressLimit(t, 3)

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
}

// TestNoProgressFuse_Constant5xxDownloadConverges: Download-level — a wall of
// non-permanent 5xx across multiple shards must converge to ErrNoProgress
// instead of rotating residual shards through workers forever.
func TestNoProgressFuse_Constant5xxDownloadConverges(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	setNoProgressLimit(t, 4)

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
	if n := requests.Load(); n > 64 {
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

	setNoProgressLimit(t, 3)

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
