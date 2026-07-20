package concurrent

import (
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/engine/state"
	"goaria-v3/internal/surge/engine/types"
)

// TestPauseRegression_HedgeKillRequeue_CompletedDoesNotRegress verifies that
// when VerifiedProgress is at 100% (hedge partner wrote all bytes) but
// activeTasks contains a stuck worker with partial progress, handlePause does
// not regress computedDownloaded below VerifiedProgress.
func TestPauseRegression_HedgeKillRequeue_CompletedDoesNotRegress(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1073741824) // 1GB — matches the real-world regression
	destPath := filepath.Join(tmpDir, "regression.bin")
	progState := types.NewProgressState("regress-test", fileSize)
	// Simulate hedge partner completed all bytes (VerifiedProgress = 100%).
	progState.VerifiedProgress.Store(fileSize)
	progState.Downloaded.Store(fileSize)

	d := &ConcurrentDownloader{
		ID:          "regress-test",
		State:       progState,
		Runtime:     &types.RuntimeConfig{},
		activeTasks: make(map[int]*ActiveTask),
	}

	// Simulate a stuck worker with partial progress (CurrentOffset < StopAt).
	// This is the requeued hedged task that was picked up but not downloaded.
	stuck := &ActiveTask{
		Task: types.Task{Offset: 886046419, Length: 187695405},
	}
	stuck.CurrentOffset.Store(886046419)
	stuck.StopAt.Store(1073741824)
	d.activeTasks[0] = stuck

	queue := NewTaskQueue()
	// Simulate a requeued hedged task in the queue.
	queue.Push(types.Task{Offset: 886046419, Length: 187695405})

	err := d.handlePause(destPath, fileSize, queue, nil)
	if err != types.ErrPaused {
		t.Fatalf("expected ErrPaused, got %v", err)
	}

	// computedDownloaded should be fileSize (not regressed to 886046419).
	if got := progState.VerifiedProgress.Load(); got != fileSize {
		t.Errorf("VerifiedProgress = %d, want %d (should not regress from 100%%)", got, fileSize)
	}
	if got := progState.Downloaded.Load(); got != fileSize {
		t.Errorf("Downloaded = %d, want %d (should not regress from 100%%)", got, fileSize)
	}
}

// TestPauseRegression_NormalPause_Unaffected verifies that a normal pause
// (no hedge requeue) is unaffected by the max() guard: computedDownloaded
// equals fileSize - remainingBytes, and VerifiedProgress matches.
func TestPauseRegression_NormalPause_Unaffected(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	halfSize := int64(500)
	destPath := filepath.Join(tmpDir, "normal_pause.bin")
	progState := types.NewProgressState("normal-pause", fileSize)
	// Normal 50% progress: VerifiedProgress = 500.
	progState.VerifiedProgress.Store(halfSize)
	progState.Downloaded.Store(halfSize)

	d := &ConcurrentDownloader{
		ID:      "normal-pause",
		State:   progState,
		Runtime: &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	// remainingBytes = 500 → computedDownloaded = 500.
	queue.Push(types.Task{Offset: 500, Length: 500})

	err := d.handlePause(destPath, fileSize, queue, nil)
	if err != types.ErrPaused {
		t.Fatalf("expected ErrPaused, got %v", err)
	}

	// computedDownloaded = 500 = VerifiedProgress, max() is a no-op.
	if got := progState.VerifiedProgress.Load(); got != halfSize {
		t.Errorf("VerifiedProgress = %d, want %d (normal pause should be unaffected)", got, halfSize)
	}
}

// TestPauseRegression_RemainingZeroEarlyExit_Unaffected verifies that when
// remainingBytes == 0, handlePause takes the early-exit path (finalizes as
// completed) and does not reach the max() guard.
func TestPauseRegression_RemainingZeroEarlyExit_Unaffected(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "zero_remaining.bin")
	progState := types.NewProgressState("zero-test", fileSize)
	progState.VerifiedProgress.Store(fileSize)
	progState.Downloaded.Store(fileSize)

	d := &ConcurrentDownloader{
		ID:      "zero-test",
		State:   progState,
		Runtime: &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	// No tasks → remainingBytes == 0 → early exit path.

	err := d.handlePause(destPath, fileSize, queue, nil)
	if err != nil {
		t.Fatalf("expected nil error on completion boundary, got %v", err)
	}

	// Should be finalized as completed (not paused).
	if progState.IsPaused() {
		t.Error("state should not be paused on completion boundary")
	}
	if got := progState.VerifiedProgress.Load(); got != fileSize {
		t.Errorf("VerifiedProgress = %d, want %d", got, fileSize)
	}
}

// TestPauseRegression_ResumeVerifiedProgressUnaffected verifies that when
// resuming from a saved state where Downloaded = fileSize but the bitmap has
// no completed chunks, setupTasks trusts the bitmap (VP=0) and restores
// Downloaded for UI display only. VP is never inflated by savedState.Downloaded.
func TestPauseRegression_ResumeVerifiedProgressUnaffected(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	chunkSize := int64(100)
	destPath := filepath.Join(tmpDir, "resume_regress.bin")

	// Build a saved state: Downloaded = fileSize (100%), but bitmap has no
	// completed chunks (inconsistent state from task-loss path inflation).
	savedBitmap := make([]byte, 4) // 10 chunks → 3 bytes, round up
	savedState := &types.DownloadState{
		ID:              "resume-test",
		URL:             "http://example.com",
		DestPath:        destPath,
		TotalSize:       fileSize,
		Downloaded:      fileSize, // inflated — handlePause max() guard saved this
		ActualChunkSize: chunkSize,
		ChunkBitmap:     savedBitmap,
		// Tasks contain requeued hedged bytes (already on disk).
		Tasks: []types.Task{{Offset: 0, Length: 500}},
	}
	if err := state.AddToMasterList(types.DownloadEntry{
		ID:         "resume-test",
		URL:        "http://example.com",
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "paused",
		TotalSize:  fileSize,
		Downloaded: fileSize,
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveState("http://example.com", destPath, savedState); err != nil {
		t.Fatal(err)
	}

	f, err := createFile(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	progState := types.NewProgressState("resume-test", fileSize)
	// InitBitmap must happen before setupTasks (as in Download).
	progState.InitBitmap(fileSize, chunkSize)

	d := &ConcurrentDownloader{
		ID:    "resume-test",
		URL:   "http://example.com",
		State: progState,
	}

	tasks, err := d.setupTasks(destPath, fileSize, chunkSize, f)
	if err != nil {
		t.Fatalf("setupTasks failed: %v", err)
	}

	// Tasks should still contain the requeued hedged bytes (for I/O alignment).
	if len(tasks) == 0 {
		t.Fatal("expected tasks from saved state, got none")
	}

	// FORK-PATCH: VP strictly follows bitmap truth (no completed chunks → 0).
	// savedState.Downloaded is inflated and must NOT override VP.
	if got := progState.VerifiedProgress.Load(); got != 0 {
		t.Errorf("VerifiedProgress = %d, want 0 (bitmap has no completed chunks)", got)
	}
	// Downloaded is restored to max(savedState.Downloaded, VP) for UI display.
	if got := progState.Downloaded.Load(); got != fileSize {
		t.Errorf("Downloaded = %d, want %d (restored for UI display)", got, fileSize)
	}
}

// createFile is a helper that creates a file for testing.
func createFile(path string) (*os.File, error) {
	return os.Create(path)
}
