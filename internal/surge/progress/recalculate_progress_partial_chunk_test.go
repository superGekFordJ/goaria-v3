package progress

import (
	"testing"

	"goaria-v3/internal/surge/types"
)

// assertVPSumChunkProgress verifies the invariant VP == sum(ChunkProgress).
func assertVPSumChunkProgress(t *testing.T, ps *DownloadProgress) {
	t.Helper()
	vp := ps.Bytes.VerifiedProgress.Load()
	_, width, _, _, chunkProgress := ps.GetBitmapSnapshot(true)
	var sum int64
	for i := 0; i < width && i < len(chunkProgress); i++ {
		sum += chunkProgress[i]
	}
	if vp != sum {
		t.Errorf("VP=%d != sum(ChunkProgress)=%d (invariant broken)", vp, sum)
	}
}

// TestRecalculateProgress_PartialChunk_VPIncludesPartialProgress verifies that
// a partial chunk (partially downloaded, not ChunkCompleted) has its on-disk
// bytes correctly counted in VP via the full-minus-remaining calculation.
func TestRecalculateProgress_PartialChunk_VPIncludesPartialProgress(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := New("partial-chunk", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// No bitmap completion marks (all Pending).
	// remaining: chunk 0 fully, chunk 1 partial (10 of 25 downloaded), chunk 2
	// not in remaining (fully on disk), chunk 3 fully.
	remaining := []types.Task{
		{Offset: 0, Length: 25},
		{Offset: 25, Length: 15},
		{Offset: 75, Length: 25},
	}

	ps.RecalculateProgress(remaining)

	// chunk 0: 25-25=0 → Pending
	// chunk 1: 25-15=10 → Downloading
	// chunk 2: 25-0=25 → Completed (full, no remaining coverage)
	// chunk 3: 25-25=0 → Pending
	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 35 {
		t.Errorf("VP = %d, want 35", vp)
	}
	_, _, _, _, chunkProgress := ps.GetBitmapSnapshot(true)
	if chunkProgress[1] != 10 {
		t.Errorf("ChunkProgress[1] = %d, want 10 (partial)", chunkProgress[1])
	}
	if ps.GetChunkState(2) != types.ChunkCompleted {
		t.Errorf("chunk 2 state = %v, want Completed (no remaining coverage)", ps.GetChunkState(2))
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_PartialChunkAndBitmapMixed verifies partial chunk
// progress combined with bitmap ChunkCompleted chunks.
func TestRecalculateProgress_PartialChunkAndBitmapMixed(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := New("mixed", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// bitmap: chunk 0,2 = Completed
	ps.SetChunkState(0, types.ChunkCompleted)
	ps.SetChunkState(2, types.ChunkCompleted)

	remaining := []types.Task{
		{Offset: 0, Length: 25},
		{Offset: 25, Length: 15},
		{Offset: 75, Length: 25},
	}

	ps.RecalculateProgress(remaining)

	// chunk 0: 25-25=0 → Step 3 bitmap trust restores to 25 → Completed
	// chunk 1: 25-15=10 → Downloading
	// chunk 2: 25-0=25 → Completed (Step 3 no-op, already full)
	// chunk 3: 25-25=0 → Pending
	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 60 {
		t.Errorf("VP = %d, want 60", vp)
	}
	_, _, _, _, chunkProgress := ps.GetBitmapSnapshot(true)
	if chunkProgress[0] != 25 {
		t.Errorf("ChunkProgress[0] = %d, want 25 (bitmap trust)", chunkProgress[0])
	}
	if chunkProgress[1] != 10 {
		t.Errorf("ChunkProgress[1] = %d, want 10 (partial)", chunkProgress[1])
	}
	if chunkProgress[2] != 25 {
		t.Errorf("ChunkProgress[2] = %d, want 25 (full, no remaining)", chunkProgress[2])
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_CrossChunkBoundaryRemainingTask verifies precise
// overlap calculation when a remaining task spans multiple chunks.
func TestRecalculateProgress_CrossChunkBoundaryRemainingTask(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := New("cross-chunk", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// remaining: offset 10, length 40 → covers chunk 0 [10,25) + chunk 1 [25,50)
	remaining := []types.Task{
		{Offset: 10, Length: 40},
	}

	ps.RecalculateProgress(remaining)

	// chunk 0: 25 - (25-10) = 25-15 = 10 → Downloading
	// chunk 1: 25 - (50-25) = 25-25 = 0 → Pending
	// chunk 2: 25-0 = 25 → Completed
	// chunk 3: 25-0 = 25 → Completed
	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 60 {
		t.Errorf("VP = %d, want 60", vp)
	}
	_, _, _, _, chunkProgress := ps.GetBitmapSnapshot(true)
	if chunkProgress[0] != 10 {
		t.Errorf("ChunkProgress[0] = %d, want 10 (partial from cross-chunk)", chunkProgress[0])
	}
	if chunkProgress[1] != 0 {
		t.Errorf("ChunkProgress[1] = %d, want 0 (fully covered)", chunkProgress[1])
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_EmptyRemaining_VPEqualsFileSize verifies that empty
// remainingTasks means all bytes are on disk → VP = fileSize.
func TestRecalculateProgress_EmptyRemaining_VPEqualsFileSize(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := New("empty-remaining", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	ps.RecalculateProgress(nil)

	if vp := ps.Bytes.VerifiedProgress.Load(); vp != totalSize {
		t.Errorf("VP = %d, want %d (fileSize)", vp, totalSize)
	}
	_, width, _, _, _ := ps.GetBitmapSnapshot(true)
	for i := range width {
		if ps.GetChunkState(i) != types.ChunkCompleted {
			t.Errorf("chunk %d state = %v, want Completed (no remaining)", i, ps.GetChunkState(i))
		}
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_SingleChunkFullyRemaining verifies a single-chunk
// file with full remaining → VP=0, Pending.
func TestRecalculateProgress_SingleChunkFullyRemaining(t *testing.T) {
	totalSize := int64(50)
	chunkSize := int64(50)
	ps := New("single-full", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	remaining := []types.Task{{Offset: 0, Length: 50}}

	ps.RecalculateProgress(remaining)

	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 0 {
		t.Errorf("VP = %d, want 0", vp)
	}
	_, _, _, _, chunkProgress := ps.GetBitmapSnapshot(true)
	if chunkProgress[0] != 0 {
		t.Errorf("ChunkProgress[0] = %d, want 0", chunkProgress[0])
	}
	if ps.GetChunkState(0) != types.ChunkPending {
		t.Errorf("chunk 0 state = %v, want Pending", ps.GetChunkState(0))
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_SingleChunkPartialDownloaded verifies a single-chunk
// file with partial remaining → VP reflects on-disk bytes.
func TestRecalculateProgress_SingleChunkPartialDownloaded(t *testing.T) {
	totalSize := int64(50)
	chunkSize := int64(50)
	ps := New("single-partial", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// 40 of 50 bytes on disk, 10 remaining
	remaining := []types.Task{{Offset: 40, Length: 10}}

	ps.RecalculateProgress(remaining)

	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 40 {
		t.Errorf("VP = %d, want 40 (partial on-disk)", vp)
	}
	_, _, _, _, chunkProgress := ps.GetBitmapSnapshot(true)
	if chunkProgress[0] != 40 {
		t.Errorf("ChunkProgress[0] = %d, want 40", chunkProgress[0])
	}
	if ps.GetChunkState(0) != types.ChunkDownloading {
		t.Errorf("chunk 0 state = %v, want Downloading", ps.GetChunkState(0))
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_MultiPauseResumeCycle simulates two pause-resume
// cycles to verify no cumulative error in VP reconstruction.
func TestRecalculateProgress_MultiPauseResumeCycle(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := New("multi-cycle", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// First pause: chunks 0,1 completed, chunk 2 partial (15/25), chunk 3 not started
	ps.SetChunkState(0, types.ChunkCompleted)
	ps.SetChunkState(1, types.ChunkCompleted)

	// First resume: remaining covers chunk 2 partial (10 left) + chunk 3 full
	remaining1 := []types.Task{
		{Offset: 65, Length: 10},
		{Offset: 75, Length: 25},
	}
	ps.RecalculateProgress(remaining1)

	// chunk 0,1 = 25 (Completed), chunk 2 = 25-10=15, chunk 3 = 25-25=0
	// VP = 25+25+15+0 = 65
	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 65 {
		t.Errorf("First resume VP = %d, want 65", vp)
	}
	_, _, _, _, chunkProgress := ps.GetBitmapSnapshot(true)
	if chunkProgress[2] != 15 {
		t.Errorf("First resume ChunkProgress[2] = %d, want 15", chunkProgress[2])
	}
	assertVPSumChunkProgress(t, ps)

	// Second pause: worker downloaded 5 more bytes of chunk 2 → 20/25
	// remaining: chunk 2 has 5 left + chunk 3 full
	remaining2 := []types.Task{
		{Offset: 70, Length: 5},
		{Offset: 75, Length: 25},
	}
	ps.RecalculateProgress(remaining2)

	// chunk 0,1 = 25 (Completed), chunk 2 = 25-5=20, chunk 3 = 25-25=0
	// VP = 25+25+20+0 = 70
	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 70 {
		t.Errorf("Second resume VP = %d, want 70", vp)
	}
	_, _, _, _, chunkProgress2 := ps.GetBitmapSnapshot(true)
	if chunkProgress2[2] != 20 {
		t.Errorf("Second resume ChunkProgress[2] = %d, want 20", chunkProgress2[2])
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_UpdateChunkStatusCompletionChain verifies the full
// completion chain: RecalculateProgress initializes partial progress, then
// UpdateChunkStatus accumulates the remaining bytes to reach full → ChunkCompleted.
func TestRecalculateProgress_UpdateChunkStatusCompletionChain(t *testing.T) {
	totalSize := int64(50)
	chunkSize := int64(50)
	ps := New("completion-chain", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// Partial: 40 of 50 bytes on disk, 10 remaining
	remaining := []types.Task{{Offset: 40, Length: 10}}
	ps.RecalculateProgress(remaining)

	_, _, _, _, chunkProgress := ps.GetBitmapSnapshot(true)
	if chunkProgress[0] != 40 {
		t.Fatalf("After RecalculateProgress: ChunkProgress[0] = %d, want 40", chunkProgress[0])
	}
	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 40 {
		t.Fatalf("After RecalculateProgress: VP = %d, want 40", vp)
	}

	// Simulate worker downloading the remaining 10 bytes
	ps.UpdateChunkStatus(40, 10, types.ChunkCompleted)

	_, _, _, _, chunkProgress2 := ps.GetBitmapSnapshot(true)
	if chunkProgress2[0] != 50 {
		t.Errorf("After UpdateChunkStatus: ChunkProgress[0] = %d, want 50 (full)", chunkProgress2[0])
	}
	if ps.GetChunkState(0) != types.ChunkCompleted {
		t.Errorf("After UpdateChunkStatus: chunk 0 state = %v, want Completed", ps.GetChunkState(0))
	}
	if vp := ps.Bytes.VerifiedProgress.Load(); vp != 50 {
		t.Errorf("After UpdateChunkStatus: VP = %d, want 50 (fileSize)", vp)
	}
	assertVPSumChunkProgress(t, ps)
}
