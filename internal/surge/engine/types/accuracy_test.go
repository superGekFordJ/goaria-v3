package types_test

import (
	"testing"

	"goaria-v3/internal/surge/engine/types"
)

func TestChunkAccuracy(t *testing.T) {
	state := types.NewProgressState("test", 100*1024*1024) // 100MB

	// Init 200 chunks -> 500KB per chunk
	// 10 MB total, 1 MB chunks
	state.InitBitmap(10*1024*1024, 1024*1024)

	// Simulate downloading a small part of the first chunk (e.g. 1KB)
	// UpdateChunkStatus(offset=0, length=1024, status=ChunkCompleted)
	// Update first 500KB (half of first chunk)
	state.UpdateChunkStatus(0, 500*1024, types.ChunkDownloading)

	// Verify
	if state.GetChunkState(0) != types.ChunkDownloading {
		t.Errorf("Expected chunk 0 to be Downloading")
	}

	// Calculate percentage
	// Calculate visual percentage
	activeCount := 0
	bitmap, width, _, _, _ := state.GetBitmap()

	// Helpers to decode bitmap manually for test verification
	getComp := func(idx int) bool {
		byteIndex := idx / 4
		bitOffset := (idx % 4) * 2
		val := (bitmap[byteIndex] >> bitOffset) & 3
		return types.ChunkStatus(val) == types.ChunkDownloading || types.ChunkStatus(val) == types.ChunkCompleted
	}

	for i := 0; i < width; i++ {
		if getComp(i) {
			activeCount++
		}
	}

	pct := float64(activeCount) / float64(width)

	// We expect 1 chunk out of 10 to be active (10%)
	if pct < 0.09 || pct > 0.11 {
		t.Errorf("Expected ~10%% visual activity (1 chunk active), got %.2f%%", pct*100)
	}
	t.Logf("Visual Completion: %.2f%%", pct*100)
}

func TestRestoreBitmap(t *testing.T) {
	state := types.NewProgressState("test-restore", 100*1024*1024) // 100MB

	// Create a bitmap manually
	// 100MB / 1MB chunks = 100 chunks.
	// 2 bits per chunk -> 200 bits -> 25 bytes.
	bitmap := make([]byte, 25)

	// Mark chunk 0 as Completed (10 -> 2)
	// Byte 0: 00 00 00 10 = 0x02 (if index 0 is first 2 bits)
	// Logic is: (status << bitOffset). Index 0 -> Offset 0.
	// val = 2 << 0 = 2.
	bitmap[0] = 0x02

	// Restore
	state.RestoreBitmap(bitmap, 1024*1024) // 1MB chunk size

	// Verify
	if state.ActualChunkSize != 1024*1024 {
		t.Errorf("Expected ActualChunkSize 1MB, got %d", state.ActualChunkSize)
	}

	if state.BitmapWidth != 100 {
		t.Errorf("Expected BitmapWidth 100, got %d", state.BitmapWidth)
	}

	if state.GetChunkState(0) != types.ChunkCompleted {
		t.Errorf("Expected chunk 0 to be completed")
	}

	if state.GetChunkState(1) != types.ChunkPending {
		t.Errorf("Expected chunk 1 to be pending")
	}
}

func TestRestoreBitmap_ShortBitmapRecoversWithoutPanic(t *testing.T) {
	const (
		totalSize = 100 * 1024 * 1024
		chunkSize = 1 * 1024 * 1024
	)

	state := types.NewProgressState("test-short-restore", totalSize)
	malformed := []byte{0x02} // Too short: only enough storage for 4 chunks.
	expectedBytes := 25       // 100 chunks * 2 bits = 25 bytes.

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RestoreBitmap/RecalculateProgress panicked with short bitmap: %v", r)
		}
	}()

	state.RestoreBitmap(malformed, chunkSize)

	bitmap, width, _, actualChunkSize, _ := state.GetBitmap()
	if width != 100 {
		t.Fatalf("BitmapWidth = %d, want 100", width)
	}
	if actualChunkSize != chunkSize {
		t.Fatalf("ActualChunkSize = %d, want %d", actualChunkSize, chunkSize)
	}
	if len(bitmap) != expectedBytes {
		t.Fatalf("bitmap len = %d, want %d after normalization", len(bitmap), expectedBytes)
	}

	if got := state.GetChunkState(0); got != types.ChunkCompleted {
		t.Fatalf("chunk 0 state = %v, want Completed after copying available bits", got)
	}
	if got := state.GetChunkState(99); got != types.ChunkPending {
		t.Fatalf("chunk 99 state = %v, want Pending", got)
	}

	// remainingTasks covers the entire file so that non-bitmap-verified chunks
	// (1-99) are fully subtracted → Pending. Chunk 0 is bitmap-verified
	// Completed → bitmap trust restores it to full despite remaining coverage.
	state.RecalculateProgress([]types.Task{{Offset: 0, Length: totalSize}})

	// FORK-PATCH: RecalculateProgress trusts the restored bitmap's
	// ChunkCompleted chunks: chunk 0 stays Completed even though a remaining
	// task covers it (bitmap indicates bytes on disk). Non-verified chunks
	// are fully subtracted by remaining → Pending.
	if got := state.GetChunkState(0); got != types.ChunkCompleted {
		t.Fatalf("chunk 0 state after recalc = %v, want Completed (bitmap trust)", got)
	}
	if got := state.GetChunkState(1); got != types.ChunkPending {
		t.Fatalf("chunk 1 state after recalc = %v, want Pending (fully covered by remaining, no bitmap)", got)
	}
	if got := state.GetChunkState(99); got != types.ChunkPending {
		t.Fatalf("chunk 99 state after recalc = %v, want Pending (fully covered by remaining, no bitmap)", got)
	}
}

func TestRecalculateProgress(t *testing.T) {
	// 30MB total, 10MB chunks -> 3 chunks
	state := types.NewProgressState("test-recalc", 30*1024*1024)
	chunkSize := int64(10 * 1024 * 1024)
	state.InitBitmap(30*1024*1024, chunkSize)

	// Simulate remaining tasks (Resume scenario)
	// Chunk 0: Missing first 5MB (Offset 0, Len 5MB) -> 5MB downloaded
	// Chunk 1: Missing all 10MB (Offset 10MB, Len 10MB) -> 0MB downloaded
	// Chunk 2: Missing nothing -> 10MB downloaded

	tasks := []types.Task{
		{Offset: 0, Length: 5 * 1024 * 1024},
		{Offset: 10 * 1024 * 1024, Length: 10 * 1024 * 1024},
	}

	state.RecalculateProgress(tasks)

	// FORK-PATCH: RecalculateProgress uses full-minus-remaining calculation.
	// Fresh InitBitmap creates all-pending bitmap (no ChunkCompleted entries).
	// Chunk 0: 10MB full − 5MB remaining = 5MB → Downloading
	// Chunk 1: 10MB full − 10MB remaining = 0 → Pending
	// Chunk 2: 10MB full − 0 remaining = 10MB → Completed (full, no remaining coverage)
	if state.GetChunkState(0) != types.ChunkDownloading {
		t.Errorf("Expected Chunk 0 to be Downloading (partial: 5MB remaining), got %v", state.GetChunkState(0))
	}
	if state.ChunkProgress[0] != 5*1024*1024 {
		t.Errorf("Expected ChunkProgress[0] = 5MB, got %d", state.ChunkProgress[0])
	}
	if state.GetChunkState(1) != types.ChunkPending {
		t.Errorf("Expected Chunk 1 to be Pending (fully covered by remaining), got %v", state.GetChunkState(1))
	}
	if state.ChunkProgress[1] != 0 {
		t.Errorf("Expected ChunkProgress[1] = 0, got %d", state.ChunkProgress[1])
	}
	if state.GetChunkState(2) != types.ChunkCompleted {
		t.Errorf("Expected Chunk 2 to be Completed (no remaining coverage), got %v", state.GetChunkState(2))
	}
	if state.ChunkProgress[2] != 10*1024*1024 {
		t.Errorf("Expected ChunkProgress[2] = 10MB, got %d", state.ChunkProgress[2])
	}
	// VP = 5MB + 0 + 10MB = 15MB
	if vp := state.VerifiedProgress.Load(); vp != 15*1024*1024 {
		t.Errorf("Expected VP = 15MB, got %d", vp)
	}
}
