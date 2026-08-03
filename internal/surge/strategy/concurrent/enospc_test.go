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
	"goaria-v3/internal/surge/store"
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

	state := progress.New("enospc-worker", fileSize)
	d := NewConcurrentDownloader("enospc-worker", nil, state, &types.RuntimeConfig{
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
	d.activeMu.Lock()
	activeLeft := len(d.activeTasks)
	stashed := append([]types.Task(nil), d.abandonedRemaining...)
	d.activeMu.Unlock()
	if activeLeft != 0 {
		t.Fatalf("activeTasks len=%d after ENOSPC return, want 0 (no zombie map entry)", activeLeft)
	}
	if len(stashed) != 1 {
		t.Fatalf("abandonedRemaining len=%d, want 1 (off-queue capture, not live Push)", len(stashed))
	}
	if stashed[0].Offset != 0 || stashed[0].Length != fileSize {
		t.Fatalf("stashed residual=%+v, want Offset=0 Length=%d", stashed[0], fileSize)
	}
	if got := state.ActiveWorkers.Load(); got != 0 {
		t.Fatalf("ActiveWorkers=%d after ENOSPC return, want 0", got)
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

	state := progress.New("enospc-dl", fileSize)
	d := NewConcurrentDownloader("enospc-dl", nil, state, &types.RuntimeConfig{
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
	d.activeMu.Lock()
	activeLeft := len(d.activeTasks)
	d.activeMu.Unlock()
	if activeLeft != 0 {
		t.Fatalf("activeTasks len=%d after Download ENOSPC, want 0", activeLeft)
	}
	if got := state.ActiveWorkers.Load(); got != 0 {
		t.Fatalf("ActiveWorkers=%d after Download ENOSPC, want 0", got)
	}
}

// TestENOSPC_ErrorPath_DownloadPersistsRemainingTasks proves mid-flight disk-full
// still leaves LoadState Tasks via off-queue abandonedRemaining (no live Push).
func TestENOSPC_ErrorPath_DownloadPersistsRemainingTasks(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(128 * utils.KiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	const progressBeforeFail = 16 * utils.KiB
	prev := writeAtFn
	writeAtFn = func(f *os.File, b []byte, off int64) (int, error) {
		if off >= progressBeforeFail {
			return 0, &os.PathError{Op: "write", Path: f.Name(), Err: platformDiskFullErrno()}
		}
		return f.WriteAt(b, off)
	}
	t.Cleanup(func() { writeAtFn = prev })

	destPath := filepath.Join(tmpDir, "enospc_snap.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	url := server.URL()
	if err := store.AddToMasterList(types.DownloadRecord{
		ID:        "enospc-snap",
		URL:       url,
		DestPath:  destPath,
		Status:    "downloading",
		Filename:  filepath.Base(destPath),
		TotalSize: fileSize,
	}); err != nil {
		t.Fatal(err)
	}

	state := progress.New("enospc-snap", fileSize)
	d := NewConcurrentDownloader("enospc-snap", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 2,
		Workers:                   2,
		MinChunkSize:              32 * utils.KiB,
		MaxTaskRetries:            1,
		WorkerBufferSize:          8 * utils.KiB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, url, destPath, fileSize, nil, 20*time.Second)
	if !types.IsInsufficientDiskSpace(err) {
		t.Fatalf("expected IsInsufficientDiskSpace, got: %v", err)
	}

	saved, loadErr := store.LoadState(url, destPath)
	if loadErr != nil {
		t.Fatalf("LoadState after ENOSPC error-path snapshot: %v", loadErr)
	}
	if len(saved.Tasks) == 0 {
		t.Fatal("expected remaining Tasks after ENOSPC (off-queue stash); empty Tasks would leave a resume hole")
	}
	var covered int64
	for _, task := range saved.Tasks {
		covered += task.Length
		if task.SharedMaxOffset != nil {
			t.Fatal("snapshot Tasks must clear SharedMaxOffset")
		}
	}
	if covered+saved.Downloaded < fileSize {
		// Prefer/merge may consolidate; Downloaded+ΣLength should reconstruct the file.
		t.Fatalf("Downloaded(%d)+taskBytes(%d) < fileSize(%d); hole risk", saved.Downloaded, covered, fileSize)
	}
	if pending := state.TakePendingResumeState(); pending == nil || len(pending.Tasks) == 0 {
		t.Fatalf("pending resume stash missing Tasks: %+v", pending)
	}
}
