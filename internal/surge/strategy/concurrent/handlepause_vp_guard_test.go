package concurrent

import (
	"errors"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// TestHandlePause_RemainingZeroButVPLessThanFileSize_SavesStateNotFinalize
// verifies that when remainingBytes == 0 but VP < fileSize, handlePause falls
// through to the standard pause path (returns ErrPaused) instead of finalizing
// an incomplete file as completed.
func TestHandlePause_RemainingZeroButVPLessThanFileSize_SavesStateNotFinalize(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "vp_guard.bin")
	progState := progress.New("vp-guard", fileSize)
	progState.Bytes.VerifiedProgress.Store(500) // VP < fileSize

	d := &ConcurrentDownloader{
		ID:      "vp-guard",
		State:   progState,
		Runtime: &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	// No tasks → remainingBytes == 0, but VP=500 < fileSize=1000.

	err := d.handlePause(destPath, fileSize, queue, nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("expected ErrPaused (save state for resume), got %v", err)
	}
}

// TestHandlePause_RemainingZeroAndVPEqualsFileSize_Finalizes verifies the
// normal completion boundary: VP == fileSize → finalize as completed.
func TestHandlePause_RemainingZeroAndVPEqualsFileSize_Finalizes(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "vp_equal.bin")
	progState := progress.New("vp-equal", fileSize)
	progState.Bytes.VerifiedProgress.Store(fileSize)

	d := &ConcurrentDownloader{
		ID:      "vp-equal",
		State:   progState,
		Runtime: &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()

	err := d.handlePause(destPath, fileSize, queue, nil)
	if err != nil {
		t.Fatalf("expected nil (finalize), got %v", err)
	}
	if progState.IsPaused() {
		t.Error("state should not be paused — should be finalized as completed")
	}
}

// TestHandlePause_RemainingZeroAndVPGreaterThanFileSize_Finalizes verifies
// the >= boundary: VP > fileSize (defensive) still finalizes.
func TestHandlePause_RemainingZeroAndVPGreaterThanFileSize_Finalizes(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "vp_greater.bin")
	progState := progress.New("vp-greater", fileSize)
	progState.Bytes.VerifiedProgress.Store(1001) // VP > fileSize

	d := &ConcurrentDownloader{
		ID:      "vp-greater",
		State:   progState,
		Runtime: &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()

	err := d.handlePause(destPath, fileSize, queue, nil)
	if err != nil {
		t.Fatalf("expected nil (finalize), got %v", err)
	}
	if progState.IsPaused() {
		t.Error("state should not be paused — should be finalized as completed")
	}
}

// TestHandlePause_NilState_RemainingZero_NoPanic verifies the defensive
// d.State == nil check: handlePause returns nil without panicking.
func TestHandlePause_NilState_RemainingZero_NoPanic(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "nil_state.bin")
	d := &ConcurrentDownloader{
		ID:    "nil-state",
		State: nil,
	}

	queue := NewTaskQueue()

	err := d.handlePause(destPath, fileSize, queue, nil)
	if err != nil {
		t.Fatalf("expected nil for nil-state completion boundary, got %v", err)
	}
}

// TestSaveStateSnapshot_NilState_RemainingTasksNoPanic locks helper nil-safety
// when remaining work exists (call sites usually gate State already).
func TestSaveStateSnapshot_NilState_RemainingTasksNoPanic(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "nil_state_remain.bin")
	d := &ConcurrentDownloader{
		ID:    "nil-state-remain",
		State: nil,
	}

	queueFalse := NewTaskQueue()
	queueFalse.Push(types.Task{Offset: 0, Length: fileSize})
	if err := d.saveStateSnapshot(destPath, fileSize, queueFalse, nil, false); err != nil {
		t.Fatalf("emit=false nil State: %v", err)
	}

	queueTrue := NewTaskQueue()
	queueTrue.Push(types.Task{Offset: 0, Length: fileSize})
	err := d.saveStateSnapshot(destPath, fileSize, queueTrue, nil, true)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("emit=true nil State: got %v, want ErrPaused", err)
	}
}
