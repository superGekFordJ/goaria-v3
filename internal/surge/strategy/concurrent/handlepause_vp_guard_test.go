package concurrent

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
)

// TestHandlePause_RemainingZeroButVPLessThanFileSize_SkipsSaveNotFinalize
// verifies the no-bitmap gap case: remainingBytes == 0 with VP < fileSize and
// no bitmap to rebuild from → handlePause must not persist a Tasks=nil record
// (resume would treat it as fresh) and must not finalize. It returns ErrPaused
// without emitting EventPaused and leaves VP untouched.
func TestHandlePause_RemainingZeroButVPLessThanFileSize_SkipsSaveNotFinalize(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "vp_guard.bin")
	progState := progress.New("vp-guard", fileSize)
	progState.Bytes.VerifiedProgress.Store(500) // VP < fileSize

	progressCh := make(chan types.DownloadEvent, 1)
	d := &ConcurrentDownloader{
		ID:           "vp-guard",
		State:        progState,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
	}

	queue := NewTaskQueue()
	// No tasks → remainingBytes == 0, but VP=500 < fileSize=1000 and no
	// bitmap → nothing resumable, skip persistence.

	err := d.handlePause(destPath, fileSize, queue, nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("expected ErrPaused, got %v", err)
	}
	if len(progressCh) != 0 {
		t.Fatal("no-bitmap gap must not emit EventPaused")
	}
	if snapshot := progState.TakePendingResumeState(); snapshot != nil {
		t.Fatalf("unexpected pending resume snapshot: %+v", snapshot)
	}
	if got := progState.Bytes.VerifiedProgress.Load(); got != 500 {
		t.Fatalf("VP = %d, want 500 (must not raise to fileSize)", got)
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

// TestHandlePause_RebuildsTasksFromBitmap verifies the bitmap-gap case:
// remainingBytes == 0 with VP < fileSize while the persisted bitmap still
// holds unverified chunks → rebuild resumable ranges from the bitmap so the
// saved record's Tasks stay non-empty (resume keys off len(Tasks) > 0).
func TestHandlePause_RebuildsTasksFromBitmap(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	const fileSize, chunkSize int64 = 1000, 250
	destPath := filepath.Join(tmpDir, "bitmap-resume.bin")
	state := progress.New("bitmap-resume", fileSize)
	state.InitBitmap(fileSize, chunkSize)
	state.UpdateChunkStatus(0, chunkSize, types.ChunkCompleted)
	state.UpdateChunkStatus(2*chunkSize, chunkSize, types.ChunkCompleted)
	// VP=500, chunks 0 and 2 completed; chunks 1 and 3 unverified.

	progressCh := make(chan types.DownloadEvent, 1)
	d := &ConcurrentDownloader{
		ID:           "bitmap-resume",
		State:        state,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
	}

	want := []types.Task{
		{Offset: chunkSize, Length: chunkSize},
		{Offset: 3 * chunkSize, Length: chunkSize},
	}

	err := d.handlePause(destPath, fileSize, NewTaskQueue(), nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("handlePause = %v, want ErrPaused", err)
	}
	var ev types.DownloadEvent
	select {
	case ev = <-progressCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for EventPaused")
	}
	if ev.State == nil {
		t.Fatal("expected pause State on EventPaused")
	}
	if !reflect.DeepEqual(ev.State.Tasks, want) {
		t.Fatalf("resume tasks = %+v, want %+v", ev.State.Tasks, want)
	}
	if ev.State.Downloaded != 500 {
		t.Fatalf("Downloaded = %d, want 500", ev.State.Downloaded)
	}

	// emit=false path rebuilds from the same bitmap and stashes pending state.
	if err := d.saveStateSnapshot(destPath, fileSize, NewTaskQueue(), nil, false); err != nil {
		t.Fatalf("saveStateSnapshot(false) = %v, want nil", err)
	}
	snapshot := state.TakePendingResumeState()
	if snapshot == nil {
		t.Fatal("missing pending resume snapshot")
	}
	if !reflect.DeepEqual(snapshot.Tasks, want) {
		t.Fatalf("pending resume tasks = %+v, want %+v", snapshot.Tasks, want)
	}
}

// TestHandlePause_CompleteBitmapDoesNotSaveIncompleteState verifies the
// complete-bitmap gap case: all chunks marked completed while VP lags
// (status-store/VP-add window) → finalize heals VP instead of persisting an
// incomplete record.
func TestHandlePause_CompleteBitmapDoesNotSaveIncompleteState(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	const fileSize, chunkSize int64 = 1000, 250
	destPath := filepath.Join(tmpDir, "bitmap-complete.bin")
	state := progress.New("bitmap-complete", fileSize)
	state.InitBitmap(fileSize, chunkSize)
	for offset := int64(0); offset < fileSize; offset += chunkSize {
		state.SetChunkState(int(offset/chunkSize), types.ChunkCompleted)
	}
	// Bitmap fully complete but VP stays 0 — simulates the lag window.

	progressCh := make(chan types.DownloadEvent, 1)
	d := &ConcurrentDownloader{
		ID:           "bitmap-complete",
		State:        state,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
	}

	err := d.handlePause(destPath, fileSize, NewTaskQueue(), nil)
	if err != nil {
		t.Fatalf("handlePause = %v, want nil", err)
	}
	if len(progressCh) != 0 {
		t.Fatal("complete-bitmap finalize must not emit EventPaused")
	}
	if snapshot := state.TakePendingResumeState(); snapshot != nil {
		t.Fatalf("unexpected incomplete resume snapshot: %+v", snapshot)
	}
	if got := state.Bytes.VerifiedProgress.Load(); got != fileSize {
		t.Fatalf("VerifiedProgress = %d, want %d", got, fileSize)
	}
	if state.IsPaused() {
		t.Error("state should not be paused — should be finalized as completed")
	}
}

// TestHandlePause_BitmapRebuildFloorsDownloadedAtVerifiedProgress verifies
// the strict inequality in the Downloaded floor: chunk-granular rebuild can
// overstate remaining bytes, so the record must never report less than
// VerifiedProgress on disk.
func TestHandlePause_BitmapRebuildFloorsDownloadedAtVerifiedProgress(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	const fileSize, chunkSize int64 = 1000, 250
	destPath := filepath.Join(tmpDir, "bitmap-floor.bin")
	state := progress.New("bitmap-floor", fileSize)
	state.InitBitmap(fileSize, chunkSize)
	// Bitmap-complete chunks without VP credit (status-store/VP-add window):
	// rebuild leaves 500 bytes remaining while VP already stands at 700.
	state.SetChunkState(0, types.ChunkCompleted)
	state.SetChunkState(2, types.ChunkCompleted)
	state.Bytes.VerifiedProgress.Store(700)

	progressCh := make(chan types.DownloadEvent, 1)
	d := &ConcurrentDownloader{
		ID:           "bitmap-floor",
		State:        state,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
	}

	err := d.handlePause(destPath, fileSize, NewTaskQueue(), nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("handlePause = %v, want ErrPaused", err)
	}
	var ev types.DownloadEvent
	select {
	case ev = <-progressCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for EventPaused")
	}
	if ev.State == nil {
		t.Fatal("expected pause State on EventPaused")
	}
	want := []types.Task{
		{Offset: chunkSize, Length: chunkSize},
		{Offset: 3 * chunkSize, Length: chunkSize},
	}
	if !reflect.DeepEqual(ev.State.Tasks, want) {
		t.Fatalf("resume tasks = %+v, want %+v", ev.State.Tasks, want)
	}
	if ev.State.Downloaded != 700 {
		t.Fatalf("Downloaded = %d, want 700 (VP floor over rebuilt remaining)", ev.State.Downloaded)
	}
}

// TestHandlePause_BitmapRebuildCapsTailChunkAtFileSize verifies the tail
// clamp: when fileSize % chunkSize != 0, the last unverified range must be
// capped at fileSize, not extended to a full chunk.
func TestHandlePause_BitmapRebuildCapsTailChunkAtFileSize(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	const fileSize, chunkSize int64 = 1100, 250 // 4 full chunks + 100-byte tail
	destPath := filepath.Join(tmpDir, "bitmap-tail.bin")
	state := progress.New("bitmap-tail", fileSize)
	state.InitBitmap(fileSize, chunkSize)
	state.UpdateChunkStatus(0, chunkSize, types.ChunkCompleted)
	state.UpdateChunkStatus(chunkSize, chunkSize, types.ChunkCompleted)
	state.UpdateChunkStatus(3*chunkSize, chunkSize, types.ChunkCompleted)
	// VP=750; chunks 2 and the 100-byte tail chunk stay unverified.

	progressCh := make(chan types.DownloadEvent, 1)
	d := &ConcurrentDownloader{
		ID:           "bitmap-tail",
		State:        state,
		ProgressChan: progressCh,
		Runtime:      &types.RuntimeConfig{},
	}

	err := d.handlePause(destPath, fileSize, NewTaskQueue(), nil)
	if !errors.Is(err, types.ErrPaused) {
		t.Fatalf("handlePause = %v, want ErrPaused", err)
	}
	var ev types.DownloadEvent
	select {
	case ev = <-progressCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for EventPaused")
	}
	if ev.State == nil {
		t.Fatal("expected pause State on EventPaused")
	}
	want := []types.Task{
		{Offset: 2 * chunkSize, Length: chunkSize},
		{Offset: 4 * chunkSize, Length: fileSize - 4*chunkSize}, // capped: 100
	}
	if !reflect.DeepEqual(ev.State.Tasks, want) {
		t.Fatalf("resume tasks = %+v, want %+v", ev.State.Tasks, want)
	}
	if ev.State.Downloaded != 750 {
		t.Fatalf("Downloaded = %d, want 750", ev.State.Downloaded)
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
