package concurrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// FORK-PATCH: ScaleUp lightweight prewarm (Scheme B) coverage.

func TestScaleWorkers_Prewarm_InvokedBeforeSpawn(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	var prewarmHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			prewarmHits.Add(1)
		}
		w.Header().Set("Content-Range", "bytes 0-0/1024")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "scale_prewarm_before.bin")
	f, err := os.Create(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
		DialHedgeCount:   0, // ScaleUp must ignore initial-download hedge gate
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		mirrors:   []string{server.URL},
		file:      f,
		queue:     queue,
		totalSize: 1024,
		client:    server.Client(),
	})
	d.workersActive.Store(true)

	added := d.ScaleWorkers(1)
	if added != 1 {
		t.Fatalf("ScaleWorkers(1) returned %d, want 1", added)
	}
	if prewarmHits.Load() < 1 {
		t.Fatalf("expected ≥1 prewarm Range bytes=0-0 hit, got %d", prewarmHits.Load())
	}

	queue.Close()
	waitScaledWorkers(t, d, cancel, 5*time.Second)
}

func TestScaleWorkers_Prewarm_SkippedWhenIsResume(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	var prewarmHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			prewarmHits.Add(1)
		}
		w.Header().Set("Content-Range", "bytes 0-0/1024")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "scale_prewarm_resume.bin")
	f, err := os.Create(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
		DialHedgeCount:   0,
	})
	d.isResume.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		mirrors:   []string{server.URL},
		file:      f,
		queue:     queue,
		totalSize: 1024,
		client:    server.Client(),
	})
	d.workersActive.Store(true)

	added := d.ScaleWorkers(1)
	if added != 1 {
		t.Fatalf("ScaleWorkers(1) returned %d, want 1", added)
	}
	if prewarmHits.Load() != 0 {
		t.Fatalf("resume ScaleUp prewarm hits = %d, want 0", prewarmHits.Load())
	}

	queue.Close()
	waitScaledWorkers(t, d, cancel, 5*time.Second)
}

func TestScaleWorkers_Prewarm_BudgetTimeoutStillSpawns(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Range", "bytes 0-0/1024")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "scale_prewarm_budget.bin")
	f, err := os.Create(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
		DialHedgeCount:   0,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		mirrors:   []string{server.URL},
		file:      f,
		queue:     queue,
		totalSize: 1024,
		client:    server.Client(),
	})
	d.workersActive.Store(true)

	start := time.Now()
	added := d.ScaleWorkers(1)
	elapsed := time.Since(start)

	if added != 1 {
		t.Fatalf("ScaleWorkers(1) returned %d, want 1", added)
	}
	// Must return near scalePrewarmBudget, not DialTimeout (10s).
	slack := 500 * time.Millisecond
	if elapsed > scalePrewarmBudget+slack {
		t.Fatalf("ScaleWorkers took %v, want ≤ %v (budget+slack); likely blocked on DialTimeout", elapsed, scalePrewarmBudget+slack)
	}
	if elapsed < scalePrewarmBudget/2 {
		// Slow handler should force waiting most of the budget; allow some scheduling noise.
		t.Logf("warning: returned unusually fast (%v); server may not have stalled", elapsed)
	}

	queue.Close()
	waitScaledWorkers(t, d, cancel, 5*time.Second)
}

func TestPrewarmConnectionsBounded_Cap128(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Range", "bytes 0-0/1024")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer server.Close()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{})
	ctx := context.Background()
	ready := d.prewarmConnectionsBounded(ctx, server.Client(), 200, []string{server.URL}, scalePrewarmBudget)
	if ready > 128 {
		t.Fatalf("ready count %d exceeds cap 128", ready)
	}
	// Allow brief drain of in-flight after cancel; hard cap is on starts.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hits.Load() <= 128 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := hits.Load(); got > 128 {
		t.Fatalf("observed %d pings, want ≤128", got)
	}
}

func TestScaleWorkers_Prewarm_WorkersActiveFalse_NoPrewarm(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		DialHedgeCount: 0,
	})

	ctx := t.Context()

	d.workerDepsPtr.Store(&workerDeps{
		ctx:     ctx,
		mirrors: []string{server.URL},
		client:  server.Client(),
	})
	d.workersActive.Store(false)

	added := d.ScaleWorkers(1)
	if added != 0 {
		t.Fatalf("ScaleWorkers(1) with workersActive=false returned %d, want 0", added)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected zero HTTP hits, got %d", hits.Load())
	}
}

func TestScaleWorkers_Prewarm_BatchSingleWait(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	var prewarmHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			prewarmHits.Add(1)
			time.Sleep(200 * time.Millisecond)
		}
		w.Header().Set("Content-Range", "bytes 0-0/1024")
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "scale_prewarm_batch.bin")
	f, err := os.Create(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("test", nil, nil, &types.RuntimeConfig{
		WorkerBufferSize: 32 * utils.KiB,
		DialHedgeCount:   0,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := NewTaskQueue()
	d.workerDepsPtr.Store(&workerDeps{
		ctx:       ctx,
		mirrors:   []string{server.URL},
		file:      f,
		queue:     queue,
		totalSize: 1024,
		client:    server.Client(),
	})
	d.workersActive.Store(true)

	const delta = 3
	start := time.Now()
	added := d.ScaleWorkers(delta)
	elapsed := time.Since(start)

	if added != delta {
		t.Fatalf("ScaleWorkers(%d) returned %d, want %d", delta, added, delta)
	}
	// One batched wait: wall ≈ one budget window, not delta×budget.
	if elapsed >= time.Duration(delta)*scalePrewarmBudget {
		t.Fatalf("elapsed %v looks like %d sequential budgets (budget=%v)", elapsed, delta, scalePrewarmBudget)
	}
	if elapsed > scalePrewarmBudget+500*time.Millisecond {
		t.Fatalf("elapsed %v exceeds single budget+slack", elapsed)
	}
	if got := prewarmHits.Load(); got > int64(delta) || got > 128 {
		t.Fatalf("prewarm hits %d, want ≤%d and ≤128", got, delta)
	}
	if prewarmHits.Load() < 1 {
		t.Fatalf("expected prewarm hits, got 0")
	}

	queue.Close()
	waitScaledWorkers(t, d, cancel, 5*time.Second)
}

func waitScaledWorkers(t *testing.T, d *ConcurrentDownloader, cancel context.CancelFunc, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		d.workerWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		cancel()
		t.Fatalf("scaled workers did not exit within %v", timeout)
	}
}
