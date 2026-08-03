package concurrent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// TestENOSPC_NoInPlaceRetryNoResidualPush locks the stricter-than-PermanentHTTP
// invariant: disk-full fails on first WriteAt, does not burn MaxTaskRetries,
// and does not residual-Push (B1 is HTTP-only).
func TestENOSPC_NoInPlaceRetryNoResidualPush(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(32 * utils.KiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	var writeAttempts atomic.Int64
	prev := writeAtFn
	writeAtFn = func(f *os.File, b []byte, off int64) (int, error) {
		writeAttempts.Add(1)
		return 0, &os.PathError{Op: "write", Path: f.Name(), Err: platformDiskFullErrno()}
	}
	t.Cleanup(func() { writeAtFn = prev })

	workingPath := filepath.Join(tmpDir, "enospc_worker.surge")
	f, err := os.Create(workingPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	d := NewConcurrentDownloader("enospc-worker", nil, nil, &types.RuntimeConfig{
		MaxTaskRetries:   3,
		WorkerBufferSize: 32 * utils.KiB,
	})

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 0, Length: fileSize})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = d.worker(ctx, 0, []string{server.URL()}, f, queue, fileSize, &http.Client{})
	if !types.IsInsufficientDiskSpace(err) {
		t.Fatalf("expected IsInsufficientDiskSpace, got: %v", err)
	}
	if n := writeAttempts.Load(); n != 1 {
		t.Fatalf("write attempts = %d, want 1 (no in-place retry burn)", n)
	}
	if queue.Len() != 0 {
		t.Fatalf("expected no residual Push on disk-space path, queue len=%d", queue.Len())
	}
	queue.Close()
}

// TestENOSPC_DownloadEndsImmediately asserts Download surfaces disk-space
// without hanging on worker/scheduler-style retry churn.
func TestENOSPC_DownloadEndsImmediately(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * utils.KiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	var writeAttempts atomic.Int64
	prev := writeAtFn
	writeAtFn = func(f *os.File, b []byte, off int64) (int, error) {
		writeAttempts.Add(1)
		return 0, &os.PathError{Op: "write", Path: f.Name(), Err: platformDiskFullErrno()}
	}
	t.Cleanup(func() { writeAtFn = prev })

	destPath := filepath.Join(tmpDir, "enospc_dl.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	d := NewConcurrentDownloader("enospc-dl", nil, nil, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		MaxTaskRetries:            3,
		Workers:                   2,
		WorkerBufferSize:          32 * utils.KiB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL(), destPath, fileSize, nil, 20*time.Second)
	if !types.IsInsufficientDiskSpace(err) {
		t.Fatalf("expected IsInsufficientDiskSpace, got: %v", err)
	}
	if n := writeAttempts.Load(); n < 1 {
		t.Fatal("expected at least one WriteAt failure")
	}
	// Bounded: workers may race a write each, but must not burn MaxTaskRetries×workers.
	if n := writeAttempts.Load(); n > 4 {
		t.Fatalf("write attempts = %d, want ≤4 (no retry storm)", n)
	}
}
