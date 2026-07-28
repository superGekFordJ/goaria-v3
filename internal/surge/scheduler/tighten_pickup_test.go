package scheduler

import (
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func TestScheduler_TightenOnPickup_InvokedAndShrinks(t *testing.T) {
	ch := make(chan types.DownloadEvent, 100)
	pool := New(ch, 1)
	t.Cleanup(func() { pool.GracefulShutdown() })

	var calls atomic.Int32
	pool.SetTightenOnPickup(func(cfg *types.DownloadRecord) {
		calls.Add(1)
		if cfg.Runtime != nil && cfg.Runtime.Workers > 1 {
			cfg.Runtime.Workers = 1
		}
	})

	entered := make(chan struct{})
	var enterOnce sync.Once
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		w.Header().Set("Content-Length", "1048576")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	id := "tighten-pickup-1"
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		OutputPath:    tmpDir,
		Filename:      "hold.bin",
		DestPath:      filepath.Join(tmpDir, "hold.bin"),
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 9, MinChunkSize: 1024},
	})

	var startedWorkers int
	deadline := time.After(5 * time.Second)
	for startedWorkers == 0 {
		select {
		case msg := <-ch:
			if msg.Type == types.EventStarted {
				startedWorkers = msg.Workers
			}
		case <-deadline:
			t.Fatal("timed out waiting for EventStarted")
		}
	}

	if calls.Load() < 1 {
		t.Fatal("TightenOnPickup was not invoked")
	}
	if startedWorkers != 1 {
		t.Fatalf("EventStarted Workers = %d, want 1 after tighten", startedWorkers)
	}

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		// Hook + EventStarted already proven; server entry is best-effort.
	}
	_ = pool.Cancel(id)
}

func TestScheduler_TightenOnPickup_NilLeavesWorkers(t *testing.T) {
	ch := make(chan types.DownloadEvent, 100)
	pool := New(ch, 1)
	t.Cleanup(func() { pool.GracefulShutdown() })

	entered := make(chan struct{})
	var enterOnce sync.Once
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		w.Header().Set("Content-Length", "1048576")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	id := "tighten-nil-1"
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		OutputPath:    tmpDir,
		Filename:      "hold.bin",
		DestPath:      filepath.Join(tmpDir, "hold.bin"),
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 5, MinChunkSize: 1024},
	})

	var startedWorkers int
	deadline := time.After(5 * time.Second)
	for startedWorkers == 0 {
		select {
		case msg := <-ch:
			if msg.Type == types.EventStarted {
				startedWorkers = msg.Workers
			}
		case <-deadline:
			t.Fatal("timed out waiting for EventStarted")
		}
	}
	if startedWorkers != 5 {
		t.Fatalf("nil hook EventStarted Workers = %d, want 5", startedWorkers)
	}
	_ = pool.Cancel(id)
}

func TestScheduler_TightenOnPickup_RetryInvokesAgain(t *testing.T) {
	ch := make(chan types.DownloadEvent, 100)
	pool := New(ch, 1)
	t.Cleanup(func() { pool.GracefulShutdown() })

	var calls atomic.Int32
	pool.SetTightenOnPickup(func(cfg *types.DownloadRecord) {
		calls.Add(1)
		if cfg.Runtime != nil {
			cfg.Runtime.Workers = 1
		}
	})

	var hits atomic.Int32
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n <= 2 {
			// Non-permanent failure so worker requeues and picks up again.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Length", "1048576")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	id := "tighten-retry-1"
	pool.Add(types.DownloadRecord{
		ID:            id,
		URL:           server.URL,
		OutputPath:    tmpDir,
		Filename:      "hold.bin",
		DestPath:      filepath.Join(tmpDir, "hold.bin"),
		ProgressState: progress.New(id, 0),
		Runtime:       &types.RuntimeConfig{Workers: 9, MinChunkSize: 1024},
	})

	deadline := time.After(8 * time.Second)
	for calls.Load() < 2 {
		select {
		case <-ch:
			// drain progress events (Queued/Started/…)
		case <-deadline:
			t.Fatalf("TightenOnPickup calls = %d, want >= 2 across retry pickup", calls.Load())
		}
	}
	_ = pool.Cancel(id)
}
