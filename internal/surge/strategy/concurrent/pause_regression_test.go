package concurrent

import (
	"errors"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
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
	progState := progress.New("regress-test", fileSize)
	progState.Bytes.VerifiedProgress.Store(fileSize)
	progState.Bytes.Downloaded.Store(fileSize)

	d := &ConcurrentDownloader{
		ID:          "regress-test",
		State:       progState,
		Runtime:     &types.RuntimeConfig{},
		activeTasks: make(map[int]*ActiveTask),
	}

	stuck := &ActiveTask{
		Task: types.Task{Offset: 886046419, Length: 187695405},
	}
	stuck.CurrentOffset.Store(886046419)
	stuck.StopAt.Store(1073741824)
	d.activeTasks[0] = stuck

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 886046419, Length: 187695405})

	err := d.handlePause(destPath, fileSize, queue, nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("expected ErrPaused, got %v", err)
	}

	if got := progState.Bytes.VerifiedProgress.Load(); got != fileSize {
		t.Errorf("VerifiedProgress = %d, want %d (should not regress from 100%%)", got, fileSize)
	}
	if got := progState.Bytes.Downloaded.Load(); got != fileSize {
		t.Errorf("Downloaded = %d, want %d (should not regress from 100%%)", got, fileSize)
	}
}

// TestPauseRegression_NormalPause_Unaffected verifies that a normal pause
// (no hedge requeue) is unaffected by the max() guard.
func TestPauseRegression_NormalPause_Unaffected(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	halfSize := int64(500)
	destPath := filepath.Join(tmpDir, "normal_pause.bin")
	progState := progress.New("normal-pause", fileSize)
	progState.Bytes.VerifiedProgress.Store(halfSize)
	progState.Bytes.Downloaded.Store(halfSize)

	d := &ConcurrentDownloader{
		ID:      "normal-pause",
		State:   progState,
		Runtime: &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 500, Length: 500})

	err := d.handlePause(destPath, fileSize, queue, nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("expected ErrPaused, got %v", err)
	}

	if got := progState.Bytes.VerifiedProgress.Load(); got != halfSize {
		t.Errorf("VerifiedProgress = %d, want %d (normal pause should be unaffected)", got, halfSize)
	}
}
