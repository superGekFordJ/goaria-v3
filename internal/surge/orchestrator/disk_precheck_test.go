package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/types"
)

func TestEnqueue_DiskPrecheckReject(t *testing.T) {
	orig := freeDiskBytes
	t.Cleanup(func() { freeDiskBytes = orig })
	freeDiskBytes = func(string) (int64, error) {
		return types.DiskSpaceSafetyBuffer, nil
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	progressCh := make(chan types.DownloadEvent, 10)
	pool := scheduler.New(progressCh, 1)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	destDir := t.TempDir()
	req := &DownloadRequest{
		URL:      ts.URL + "/big.bin",
		Filename: "big.bin",
		Path:     destDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := mgr.Enqueue(ctx, req)
	if !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("Enqueue error = %v, want ErrInsufficientDiskSpace", err)
	}

	surgePath := filepath.Join(destDir, "big.bin") + types.IncompleteSuffix
	if _, err := os.Stat(surgePath); !os.IsNotExist(err) {
		t.Fatalf("expected no .surge working file at %s, stat err=%v", surgePath, err)
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty dest dir after reject, got %d entries", len(entries))
	}
}

func TestEnqueue_DiskPrecheckAllow(t *testing.T) {
	orig := freeDiskBytes
	t.Cleanup(func() { freeDiskBytes = orig })
	freeDiskBytes = func(string) (int64, error) {
		return types.DiskSpaceSafetyBuffer + 10*1024*1024, nil
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	progressCh := make(chan types.DownloadEvent, 10)
	pool := scheduler.New(progressCh, 1)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	destDir := t.TempDir()
	req := &DownloadRequest{
		URL:      ts.URL + "/ok.bin",
		Filename: "ok.bin",
		Path:     destDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	id, finalName, err := mgr.Enqueue(ctx, req)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}
	if finalName != "ok.bin" {
		t.Fatalf("expected ok.bin, got %s", finalName)
	}

	surgePath := filepath.Join(destDir, finalName) + types.IncompleteSuffix
	if _, err := os.Stat(surgePath); err != nil {
		t.Fatalf("expected working file at %s: %v", surgePath, err)
	}
}

func TestEnqueue_DiskPrecheckSkipUnknownSize(t *testing.T) {
	orig := freeDiskBytes
	t.Cleanup(func() { freeDiskBytes = orig })
	called := false
	freeDiskBytes = func(string) (int64, error) {
		called = true
		return 0, nil
	}

	// No Content-Length → probe FileSize 0 / unknown → skip precheck.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	progressCh := make(chan types.DownloadEvent, 10)
	pool := scheduler.New(progressCh, 1)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	destDir := t.TempDir()
	req := &DownloadRequest{
		URL:      ts.URL + "/unknown.bin",
		Filename: "unknown.bin",
		Path:     destDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := mgr.Enqueue(ctx, req)
	if errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatal("unknown size must not return ErrInsufficientDiskSpace")
	}
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if called {
		t.Fatal("freeDiskBytes should not be called when FileSize is unknown")
	}

	surgePath := filepath.Join(destDir, "unknown.bin") + types.IncompleteSuffix
	if _, err := os.Stat(surgePath); err != nil {
		t.Fatalf("expected working file at %s: %v", surgePath, err)
	}
}

func TestEnqueue_DiskPrecheckFailOpen(t *testing.T) {
	orig := freeDiskBytes
	t.Cleanup(func() { freeDiskBytes = orig })
	freeDiskBytes = func(string) (int64, error) {
		return 0, fmt.Errorf("statfs simulated failure")
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	progressCh := make(chan types.DownloadEvent, 10)
	pool := scheduler.New(progressCh, 1)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	destDir := t.TempDir()
	req := &DownloadRequest{
		URL:      ts.URL + "/failopen.bin",
		Filename: "failopen.bin",
		Path:     destDir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := mgr.Enqueue(ctx, req)
	if errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatal("query error must fail-open, not return ErrInsufficientDiskSpace")
	}
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	surgePath := filepath.Join(destDir, "failopen.bin") + types.IncompleteSuffix
	if _, err := os.Stat(surgePath); err != nil {
		t.Fatalf("expected working file at %s: %v", surgePath, err)
	}
}

func TestEnqueue_DiskPrecheckPendingReserveSoftBlock(t *testing.T) {
	orig := freeDiskBytes
	t.Cleanup(func() { freeDiskBytes = orig })

	const fileSize = int64(4 * 1024 * 1024) // 4 MiB
	// Enough free for one file + buffer, not two.
	freeDiskBytes = func(string) (int64, error) {
		return types.DiskSpaceSafetyBuffer + fileSize + 1024, nil
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fileSize))
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	progressCh := make(chan types.DownloadEvent, 10)
	pool := scheduler.New(progressCh, 2)
	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	destDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	id1, _, err := mgr.Enqueue(ctx, &DownloadRequest{
		URL:      ts.URL + "/one.bin",
		Filename: "one.bin",
		Path:     destDir,
	})
	if err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if id1 == "" {
		t.Fatal("expected first id")
	}
	if got := mgr.pendingDiskReserved(); got != fileSize {
		t.Fatalf("pending after first = %d, want %d", got, fileSize)
	}

	_, _, err = mgr.Enqueue(ctx, &DownloadRequest{
		URL:      ts.URL + "/two.bin",
		Filename: "two.bin",
		Path:     destDir,
	})
	if !errors.Is(err, types.ErrInsufficientDiskSpace) {
		t.Fatalf("second Enqueue error = %v, want ErrInsufficientDiskSpace (pending debit)", err)
	}

	mgr.releaseDiskBytes(id1)
	if got := mgr.pendingDiskReserved(); got != 0 {
		t.Fatalf("pending after release = %d, want 0", got)
	}

	id2, _, err := mgr.Enqueue(ctx, &DownloadRequest{
		URL:      ts.URL + "/two.bin",
		Filename: "two.bin",
		Path:     destDir,
	})
	if err != nil {
		t.Fatalf("third Enqueue after release: %v", err)
	}
	if id2 == "" {
		t.Fatal("expected third id")
	}
}

func TestResume_BypassesDiskPrecheck(t *testing.T) {
	orig := freeDiskBytes
	t.Cleanup(func() { freeDiskBytes = orig })
	called := false
	freeDiskBytes = func(string) (int64, error) {
		called = true
		return 0, nil
	}

	state := progress.New("resume-disk", 1000)
	state.Pause()

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"resume-disk": {
			ID:            "resume-disk",
			Filename:      "resume.bin",
			ProgressState: state,
			URL:           "http://example.com/resume",
			DestPath:      filepath.Join(t.TempDir(), "resume.bin"),
			TotalSize:     10 * 1024 * 1024 * 1024,
		},
	})

	eb := NewEventBus()
	mgr := NewLifecycleManager(pool, eb, nil)
	defer mgr.Shutdown()

	if err := mgr.Resume("resume-disk"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if called {
		t.Fatal("Resume must not call freeDiskBytes; it bypasses enqueueResolved")
	}
}
