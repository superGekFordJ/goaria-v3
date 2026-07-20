package types

import (
	"testing"
)

// assertVPSumChunkProgress verifies the invariant VP == sum(ChunkProgress).
func assertVPSumChunkProgress(t *testing.T, ps *ProgressState) {
	t.Helper()
	vp := ps.VerifiedProgress.Load()
	var sum int64
	for i := 0; i < ps.BitmapWidth; i++ {
		sum += ps.ChunkProgress[i]
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
	ps := NewProgressState("partial-chunk", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// No bitmap completion marks (all Pending).
	// remaining: chunk 0 fully, chunk 1 partial (10 of 25 downloaded), chunk 2
	// not in remaining (fully on disk), chunk 3 fully.
	remaining := []Task{
		{Offset: 0, Length: 25},
		{Offset: 25, Length: 15},
		{Offset: 75, Length: 25},
	}

	ps.RecalculateProgress(remaining)

	// chunk 0: 25-25=0 → Pending
	// chunk 1: 25-15=10 → Downloading
	// chunk 2: 25-0=25 → Completed (full, no remaining coverage)
	// chunk 3: 25-25=0 → Pending
	if vp := ps.VerifiedProgress.Load(); vp != 35 {
		t.Errorf("VP = %d, want 35", vp)
	}
	if ps.ChunkProgress[1] != 10 {
		t.Errorf("ChunkProgress[1] = %d, want 10 (partial)", ps.ChunkProgress[1])
	}
	if ps.GetChunkState(2) != ChunkCompleted {
		t.Errorf("chunk 2 state = %v, want Completed (no remaining coverage)", ps.GetChunkState(2))
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_PartialChunkAndBitmapMixed verifies partial chunk
// progress combined with bitmap ChunkCompleted chunks.
func TestRecalculateProgress_PartialChunkAndBitmapMixed(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := NewProgressState("mixed", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// bitmap: chunk 0,2 = Completed
	ps.SetChunkState(0, ChunkCompleted)
	ps.SetChunkState(2, ChunkCompleted)

	remaining := []Task{
		{Offset: 0, Length: 25},
		{Offset: 25, Length: 15},
		{Offset: 75, Length: 25},
	}

	ps.RecalculateProgress(remaining)

	// chunk 0: 25-25=0 → Step 3 bitmap trust restores to 25 → Completed
	// chunk 1: 25-15=10 → Downloading
	// chunk 2: 25-0=25 → Completed (Step 3 no-op, already full)
	// chunk 3: 25-25=0 → Pending
	if vp := ps.VerifiedProgress.Load(); vp != 60 {
		t.Errorf("VP = %d, want 60", vp)
	}
	if ps.ChunkProgress[0] != 25 {
		t.Errorf("ChunkProgress[0] = %d, want 25 (bitmap trust)", ps.ChunkProgress[0])
	}
	if ps.ChunkProgress[1] != 10 {
		t.Errorf("ChunkProgress[1] = %d, want 10 (partial)", ps.ChunkProgress[1])
	}
	if ps.ChunkProgress[2] != 25 {
		t.Errorf("ChunkProgress[2] = %d, want 25 (full, no remaining)", ps.ChunkProgress[2])
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_CrossChunkBoundaryRemainingTask verifies precise
// overlap calculation when a remaining task spans multiple chunks.
func TestRecalculateProgress_CrossChunkBoundaryRemainingTask(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := NewProgressState("cross-chunk", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// remaining: offset 10, length 40 → covers chunk 0 [10,25) + chunk 1 [25,50)
	remaining := []Task{
		{Offset: 10, Length: 40},
	}

	ps.RecalculateProgress(remaining)

	// chunk 0: 25 - (25-10) = 25-15 = 10 → Downloading
	// chunk 1: 25 - (50-25) = 25-25 = 0 → Pending
	// chunk 2: 25-0 = 25 → Completed
	// chunk 3: 25-0 = 25 → Completed
	if vp := ps.VerifiedProgress.Load(); vp != 60 {
		t.Errorf("VP = %d, want 60", vp)
	}
	if ps.ChunkProgress[0] != 10 {
		t.Errorf("ChunkProgress[0] = %d, want 10 (partial from cross-chunk)", ps.ChunkProgress[0])
	}
	if ps.ChunkProgress[1] != 0 {
		t.Errorf("ChunkProgress[1] = %d, want 0 (fully covered)", ps.ChunkProgress[1])
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_EmptyRemaining_VPEqualsFileSize verifies that empty
// remainingTasks means all bytes are on disk → VP = fileSize.
func TestRecalculateProgress_EmptyRemaining_VPEqualsFileSize(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := NewProgressState("empty-remaining", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	ps.RecalculateProgress(nil)

	if vp := ps.VerifiedProgress.Load(); vp != totalSize {
		t.Errorf("VP = %d, want %d (fileSize)", vp, totalSize)
	}
	for i := 0; i < ps.BitmapWidth; i++ {
		if ps.GetChunkState(i) != ChunkCompleted {
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
	ps := NewProgressState("single-full", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	remaining := []Task{{Offset: 0, Length: 50}}

	ps.RecalculateProgress(remaining)

	if vp := ps.VerifiedProgress.Load(); vp != 0 {
		t.Errorf("VP = %d, want 0", vp)
	}
	if ps.ChunkProgress[0] != 0 {
		t.Errorf("ChunkProgress[0] = %d, want 0", ps.ChunkProgress[0])
	}
	if ps.GetChunkState(0) != ChunkPending {
		t.Errorf("chunk 0 state = %v, want Pending", ps.GetChunkState(0))
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_SingleChunkPartialDownloaded verifies a single-chunk
// file with partial remaining → VP reflects on-disk bytes.
func TestRecalculateProgress_SingleChunkPartialDownloaded(t *testing.T) {
	totalSize := int64(50)
	chunkSize := int64(50)
	ps := NewProgressState("single-partial", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// 40 of 50 bytes on disk, 10 remaining
	remaining := []Task{{Offset: 40, Length: 10}}

	ps.RecalculateProgress(remaining)

	if vp := ps.VerifiedProgress.Load(); vp != 40 {
		t.Errorf("VP = %d, want 40 (partial on-disk)", vp)
	}
	if ps.ChunkProgress[0] != 40 {
		t.Errorf("ChunkProgress[0] = %d, want 40", ps.ChunkProgress[0])
	}
	if ps.GetChunkState(0) != ChunkDownloading {
		t.Errorf("chunk 0 state = %v, want Downloading", ps.GetChunkState(0))
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_MultiPauseResumeCycle simulates two pause-resume
// cycles to verify no cumulative error in VP reconstruction.
func TestRecalculateProgress_MultiPauseResumeCycle(t *testing.T) {
	totalSize := int64(100)
	chunkSize := int64(25)
	ps := NewProgressState("multi-cycle", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// First pause: chunks 0,1 completed, chunk 2 partial (15/25), chunk 3 not started
	ps.SetChunkState(0, ChunkCompleted)
	ps.SetChunkState(1, ChunkCompleted)

	// First resume: remaining covers chunk 2 partial (10 left) + chunk 3 full
	remaining1 := []Task{
		{Offset: 65, Length: 10},
		{Offset: 75, Length: 25},
	}
	ps.RecalculateProgress(remaining1)

	// chunk 0,1 = 25 (Completed), chunk 2 = 25-10=15, chunk 3 = 25-25=0
	// VP = 25+25+15+0 = 65
	if vp := ps.VerifiedProgress.Load(); vp != 65 {
		t.Errorf("First resume VP = %d, want 65", vp)
	}
	if ps.ChunkProgress[2] != 15 {
		t.Errorf("First resume ChunkProgress[2] = %d, want 15", ps.ChunkProgress[2])
	}
	assertVPSumChunkProgress(t, ps)

	// Second pause: worker downloaded 5 more bytes of chunk 2 → 20/25
	// remaining: chunk 2 has 5 left + chunk 3 full
	remaining2 := []Task{
		{Offset: 70, Length: 5},
		{Offset: 75, Length: 25},
	}
	ps.RecalculateProgress(remaining2)

	// chunk 0,1 = 25 (Completed), chunk 2 = 25-5=20, chunk 3 = 25-25=0
	// VP = 25+25+20+0 = 70
	if vp := ps.VerifiedProgress.Load(); vp != 70 {
		t.Errorf("Second resume VP = %d, want 70", vp)
	}
	if ps.ChunkProgress[2] != 20 {
		t.Errorf("Second resume ChunkProgress[2] = %d, want 20", ps.ChunkProgress[2])
	}
	assertVPSumChunkProgress(t, ps)
}

// TestRecalculateProgress_UpdateChunkStatusCompletionChain verifies the full
// completion chain: RecalculateProgress initializes partial progress, then
// UpdateChunkStatus accumulates the remaining bytes to reach full → ChunkCompleted.
func TestRecalculateProgress_UpdateChunkStatusCompletionChain(t *testing.T) {
	totalSize := int64(50)
	chunkSize := int64(50)
	ps := NewProgressState("completion-chain", totalSize)
	ps.InitBitmap(totalSize, chunkSize)

	// Partial: 40 of 50 bytes on disk, 10 remaining
	remaining := []Task{{Offset: 40, Length: 10}}
	ps.RecalculateProgress(remaining)

	if ps.ChunkProgress[0] != 40 {
		t.Fatalf("After RecalculateProgress: ChunkProgress[0] = %d, want 40", ps.ChunkProgress[0])
	}
	if vp := ps.VerifiedProgress.Load(); vp != 40 {
		t.Fatalf("After RecalculateProgress: VP = %d, want 40", vp)
	}

	// Simulate worker downloading the remaining 10 bytes
	ps.UpdateChunkStatus(40, 10, ChunkCompleted)

	if ps.ChunkProgress[0] != 50 {
		t.Errorf("After UpdateChunkStatus: ChunkProgress[0] = %d, want 50 (full)", ps.ChunkProgress[0])
	}
	if ps.GetChunkState(0) != ChunkCompleted {
		t.Errorf("After UpdateChunkStatus: chunk 0 state = %v, want Completed", ps.GetChunkState(0))
	}
	if vp := ps.VerifiedProgress.Load(); vp != 50 {
		t.Errorf("After UpdateChunkStatus: VP = %d, want 50 (fileSize)", vp)
	}
	assertVPSumChunkProgress(t, ps)
}
