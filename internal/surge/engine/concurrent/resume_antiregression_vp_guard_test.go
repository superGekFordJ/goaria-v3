package concurrent

import (
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/engine/state"
	"goaria-v3/internal/surge/engine/types"
)

// buildCompletedBitmap encodes a 2-bit-per-chunk bitmap marking the first
// numCompleted chunks as ChunkCompleted (status=2). This mirrors the encoding
// used by ProgressState.setChunkState so RecalculateProgress trusts the bitmap.
func buildCompletedBitmap(totalSize, chunkSize int64, numCompleted int) []byte {
	numChunks := int((totalSize + chunkSize - 1) / chunkSize)
	if numChunks <= 0 {
		return nil
	}
	bytesNeeded := (numChunks + 3) / 4
	bm := make([]byte, bytesNeeded)
	for i := 0; i < numCompleted && i < numChunks; i++ {
		byteIndex := i / 4
		bitOffset := (i % 4) * 2
		bm[byteIndex] |= byte(types.ChunkCompleted) << bitOffset
	}
	return bm
}

// saveResumeState writes a .surge resume file + master-list entry so
// setupTasks' LoadState path picks up the provided savedState.
func saveResumeState(t *testing.T, id, url, destPath string, savedState *types.DownloadState, fileSize int64) {
	t.Helper()
	if err := state.AddToMasterList(types.DownloadEntry{
		ID:         id,
		URL:        url,
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "paused",
		TotalSize:  fileSize,
		Downloaded: savedState.Downloaded,
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveState(url, destPath, savedState); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(fileSize); err != nil {
		t.Fatal(err)
	}
}

// TestResume_BitmapTrust_InflatedDownloadedDoesNotOverrideVP verifies that an
// inflated savedState.Downloaded (multi-chunk file, half bitmap complete) can
// no longer override the bitmap-recalculated VP.
func TestResume_BitmapTrust_InflatedDownloadedDoesNotOverrideVP(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(10000)
	chunkSize := int64(1000)
	destPath := filepath.Join(tmpDir, "inflate_multi.bin")

	// Bitmap marks chunks 0-4 complete (5000 bytes verified).
	savedState := &types.DownloadState{
		ID:              "inflate-multi",
		URL:             "http://example.com",
		DestPath:        destPath,
		TotalSize:       fileSize,
		Downloaded:      10000, // inflated to fileSize
		ActualChunkSize: chunkSize,
		ChunkBitmap:     buildCompletedBitmap(fileSize, chunkSize, 5),
		Tasks:           []types.Task{{Offset: 5000, Length: 5000}},
	}
	saveResumeState(t, "inflate-multi", "http://example.com", destPath, savedState, fileSize)

	progState := types.NewProgressState("inflate-multi", fileSize)
	progState.InitBitmap(fileSize, chunkSize)

	d := &ConcurrentDownloader{
		ID:    "inflate-multi",
		URL:   "http://example.com",
		State: progState,
	}

	f, err := os.Open(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	tasks, err := d.setupTasks(destPath, fileSize, chunkSize, f)
	if err != nil {
		t.Fatalf("setupTasks failed: %v", err)
	}

	vp := progState.VerifiedProgress.Load()
	downloaded := progState.Downloaded.Load()

	if vp != 5000 {
		t.Errorf("VP = %d, want 5000 (bitmap truth, not inflated Downloaded)", vp)
	}
	if downloaded != 10000 {
		t.Errorf("Downloaded = %d, want 10000 (restored for UI display)", downloaded)
	}
	if vp >= fileSize {
		t.Errorf("VP %d >= fileSize %d — completionMonitor would false-complete", vp, fileSize)
	}
	if len(tasks) == 0 {
		t.Error("expected remaining tasks for unverified chunks")
	}
}

// TestResume_BitmapTrust_SingleChunkVPZeroInflatedDownloaded is the residual
// risk point: a single-chunk file where the chunk is NOT complete (VP=0) but
// savedState.Downloaded is inflated to fileSize. VP must stay 0 so the
// completionMonitor never false-completes.
func TestResume_BitmapTrust_SingleChunkVPZeroInflatedDownloaded(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(58000000) // 58 MB, single chunk
	chunkSize := int64(58000000)
	destPath := filepath.Join(tmpDir, "inflate_single.bin")

	// Bitmap has len > 0 but no chunk marked complete → VP=0.
	savedState := &types.DownloadState{
		ID:              "inflate-single",
		URL:             "http://example.com",
		DestPath:        destPath,
		TotalSize:       fileSize,
		Downloaded:      58000000, // inflated to fileSize
		ActualChunkSize: chunkSize,
		ChunkBitmap:     buildCompletedBitmap(fileSize, chunkSize, 0), // no completed chunks
		Tasks:           []types.Task{{Offset: 0, Length: fileSize}},
	}
	saveResumeState(t, "inflate-single", "http://example.com", destPath, savedState, fileSize)

	progState := types.NewProgressState("inflate-single", fileSize)
	progState.InitBitmap(fileSize, chunkSize)

	d := &ConcurrentDownloader{
		ID:    "inflate-single",
		URL:   "http://example.com",
		State: progState,
	}

	f, err := os.Open(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	tasks, err := d.setupTasks(destPath, fileSize, chunkSize, f)
	if err != nil {
		t.Fatalf("setupTasks failed: %v", err)
	}

	vp := progState.VerifiedProgress.Load()
	downloaded := progState.Downloaded.Load()

	if vp != 0 {
		t.Errorf("VP = %d, want 0 (single chunk not complete — never inflate to fileSize)", vp)
	}
	if downloaded != 58000000 {
		t.Errorf("Downloaded = %d, want 58000000 (restored for UI display)", downloaded)
	}
	if vp >= fileSize {
		t.Errorf("VP %d >= fileSize %d — completionMonitor would false-complete", vp, fileSize)
	}
	if len(tasks) == 0 {
		t.Error("expected the full-range task to remain for re-download")
	}
}

// TestResume_NoBitmap_LegacyVPEqualsDownloaded verifies the else branch:
// legacy .surge files without a bitmap keep historical behavior
// (VP = Downloaded = savedState.Downloaded).
func TestResume_NoBitmap_LegacyVPEqualsDownloaded(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(10000)
	chunkSize := int64(1000)
	destPath := filepath.Join(tmpDir, "legacy_nobitmap.bin")

	savedState := &types.DownloadState{
		ID:         "legacy-nobitmap",
		URL:        "http://example.com",
		DestPath:   destPath,
		TotalSize:  fileSize,
		Downloaded: 7000,
		Tasks:      []types.Task{{Offset: 7000, Length: 3000}},
	}
	saveResumeState(t, "legacy-nobitmap", "http://example.com", destPath, savedState, fileSize)

	progState := types.NewProgressState("legacy-nobitmap", fileSize)
	progState.InitBitmap(fileSize, chunkSize)

	d := &ConcurrentDownloader{
		ID:    "legacy-nobitmap",
		URL:   "http://example.com",
		State: progState,
	}

	f, err := os.Open(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if _, err := d.setupTasks(destPath, fileSize, chunkSize, f); err != nil {
		t.Fatalf("setupTasks failed: %v", err)
	}

	vp := progState.VerifiedProgress.Load()
	downloaded := progState.Downloaded.Load()

	if vp != 7000 {
		t.Errorf("VP = %d, want 7000 (legacy: VP = savedState.Downloaded)", vp)
	}
	if downloaded != 7000 {
		t.Errorf("Downloaded = %d, want 7000 (legacy: Downloaded = savedState.Downloaded)", downloaded)
	}
}

// TestResume_BitmapTrust_AllZeroBitmapBenignRegression verifies that a bitmap
// with len > 0 but no completed chunks (corrupted or all-pending) enters the
// if branch and trusts RecalculateProgress (VP=0), a benign regression.
func TestResume_BitmapTrust_AllZeroBitmapBenignRegression(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(10000)
	chunkSize := int64(1000)
	destPath := filepath.Join(tmpDir, "allzero_bitmap.bin")

	savedState := &types.DownloadState{
		ID:              "allzero-bitmap",
		URL:             "http://example.com",
		DestPath:        destPath,
		TotalSize:       fileSize,
		Downloaded:      9000, // inflated
		ActualChunkSize: chunkSize,
		ChunkBitmap:     buildCompletedBitmap(fileSize, chunkSize, 0), // all zero, len > 0
		Tasks:           []types.Task{{Offset: 0, Length: fileSize}},
	}
	saveResumeState(t, "allzero-bitmap", "http://example.com", destPath, savedState, fileSize)

	progState := types.NewProgressState("allzero-bitmap", fileSize)
	progState.InitBitmap(fileSize, chunkSize)

	d := &ConcurrentDownloader{
		ID:    "allzero-bitmap",
		URL:   "http://example.com",
		State: progState,
	}

	f, err := os.Open(destPath + types.IncompleteSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if _, err := d.setupTasks(destPath, fileSize, chunkSize, f); err != nil {
		t.Fatalf("setupTasks failed: %v", err)
	}

	vp := progState.VerifiedProgress.Load()
	downloaded := progState.Downloaded.Load()

	if vp != 0 {
		t.Errorf("VP = %d, want 0 (no chunk marked complete — benign regression)", vp)
	}
	if downloaded != 9000 {
		t.Errorf("Downloaded = %d, want 9000 (restored for UI display)", downloaded)
	}
}
