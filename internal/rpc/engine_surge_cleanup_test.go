package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCleanup_ShutdownPausesAllDownloads verifies that Close() triggers
// svc.Shutdown() which runs PauseAll before the event worker exits. After
// Close(), all downloads in the pool must be in "paused" state (not
// "downloading", not "cancelled"), proving that PauseAll executed and its
// events were processed before cleanup tore down the event pipeline.
//
// Under the old ordering (engineCancel → svc.Shutdown), the event worker
// would exit before PauseAll events were consumed, so pause state might not
// be persisted. This test catches that by checking the pool's in-memory
// state after Close().
func TestCleanup_ShutdownPausesAllDownloads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "104857600")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		// Slow write to keep download active until shutdown
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	engine := NewSurgeEngine()

	outputDir := t.TempDir()
	gid, err := engine.AddUri(srv.URL+"/file.bin", AddURIOptions{
		Dir:          outputDir,
		Out:          "file.bin",
		Split:        4,
		MinSplitSize: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("AddUri failed: %v", err)
	}

	// Give the download time to start
	time.Sleep(200 * time.Millisecond)

	// Close must complete without deadlock
	done := make(chan struct{})
	go func() {
		engine.Close()
		close(done)
	}()

	select {
	case <-done:
		// Success — no deadlock
	case <-time.After(10 * time.Second):
		t.Fatal("Close() deadlocked — cleanup ordering may be wrong")
	}

	// Verify the download was paused (not left downloading or cancelled).
	// svc.Shutdown() → GracefulShutdown() → PauseAll() sets state to paused.
	// If engineCancel() ran first, the event worker would have exited before
	// processing pause events, and the state might not reach "paused".
	status := engine.getScheduler().GetStatus(gid)
	if status == nil {
		t.Fatal("expected download status after Close(), got nil — download may have been removed instead of paused")
	}
	if status.Status != "paused" {
		t.Errorf("download status = %q, want \"paused\" (proves PauseAll ran before event worker exit)", status.Status)
	}
}

// TestCleanup_IdempotentClose verifies that calling Close() multiple times
// is safe (shutdownOnce protects against double shutdown).
func TestCleanup_IdempotentClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "104857600")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	engine := NewSurgeEngine()

	outputDir := t.TempDir()
	_, err := engine.AddUri(srv.URL+"/file.bin", AddURIOptions{
		Dir: outputDir,
		Out: "file.bin",
	})
	if err != nil {
		t.Fatalf("AddUri failed: %v", err)
	}

	// First close
	engine.Close()
	// Second close should be a no-op (cleanup is nil after first call? No,
	// but shutdownOnce protects the service)
	engine.Close()
}

// TestCleanup_EmptyEngineClose verifies that Close() on a freshly created
// engine with no downloads completes cleanly.
func TestCleanup_EmptyEngineClose(t *testing.T) {
	engine := NewSurgeEngine()
	engine.Close()
}
