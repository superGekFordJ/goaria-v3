package store

import (
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/types"
)

// spillName is the canonical hidden arena basename: ".<raw id>.spill".
func spillName(id string) string { return "." + id + ".spill" }

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestValidateIntegrity_SpillSurvivesWithValidPausedRow pins the protection
// rule: a surviving non-completed row owns its spill arena, so the sweep
// must leave it alone while the .surge check passes.
func TestValidateIntegrity_SpillSurvivesWithValidPausedRow(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	destPath := filepath.Join(tmpDir, "live.ts")
	writeFile(t, destPath+types.IncompleteSuffix, []byte("merged-bytes"))
	spill := filepath.Join(tmpDir, spillName("integrity-live"))
	writeFile(t, spill, []byte("spill-ledger"))

	if err := AddToMasterList(types.DownloadRecord{
		ID:       "integrity-live",
		URL:      "https://example.com/live.m3u8",
		DestPath: destPath,
		Filename: "live.ts",
		Status:   "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	removed, err := ValidateIntegrity()
	if err != nil {
		t.Fatalf("ValidateIntegrity: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 for a valid paused row", removed)
	}
	if _, err := os.Stat(spill); err != nil {
		t.Fatalf("protected spill arena swept: %v", err)
	}
}

// TestValidateIntegrity_SpillSweptWhenRowDropped drops the paused row (its
// .surge is gone) and proves the now-ownerless arena joins the orphan sweep
// in the same pass.
func TestValidateIntegrity_SpillSweptWhenRowDropped(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	destPath := filepath.Join(tmpDir, "dead.ts")
	spill := filepath.Join(tmpDir, spillName("integrity-dead"))
	writeFile(t, spill, []byte("spill-ledger"))

	if err := AddToMasterList(types.DownloadRecord{
		ID:       "integrity-dead",
		URL:      "https://example.com/dead.m3u8",
		DestPath: destPath,
		Filename: "dead.ts",
		Status:   "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	removed, err := ValidateIntegrity()
	if err != nil {
		t.Fatalf("ValidateIntegrity: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (missing .surge drops the row)", removed)
	}
	if _, err := os.Stat(spill); !os.IsNotExist(err) {
		t.Fatalf("orphaned spill arena survived the sweep: %v", err)
	}
}

// TestValidateIntegrity_OrphanSpillSwept sweeps stray arenas in a candidate
// directory — IDs with no row at all and IDs whose row completed — while a
// surviving paused row's own arena is untouched.
func TestValidateIntegrity_OrphanSpillSwept(t *testing.T) {
	tmpDir := setupTestDB(t)
	defer func() { _ = os.RemoveAll(tmpDir) }()
	defer CloseDB()

	// One live row makes tmpDir a candidate directory for the sweep.
	liveDest := filepath.Join(tmpDir, "live.ts")
	writeFile(t, liveDest+types.IncompleteSuffix, []byte("merged-bytes"))
	if err := AddToMasterList(types.DownloadRecord{
		ID:       "integrity-anchor",
		URL:      "https://example.com/live.m3u8",
		DestPath: liveDest,
		Filename: "live.ts",
		Status:   "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList anchor: %v", err)
	}
	liveSpill := filepath.Join(tmpDir, spillName("integrity-anchor"))
	writeFile(t, liveSpill, []byte("owned-arena"))

	// A completed row's arena is unprotected — completed downloads promote
	// to their final name and never resume a spill ledger.
	doneDest := filepath.Join(tmpDir, "done.ts")
	if err := AddToMasterList(types.DownloadRecord{
		ID:       "integrity-done",
		URL:      "https://example.com/done.m3u8",
		DestPath: doneDest,
		Filename: "done.ts",
		Status:   "completed",
	}); err != nil {
		t.Fatalf("AddToMasterList completed: %v", err)
	}
	doneSpill := filepath.Join(tmpDir, spillName("integrity-done"))
	writeFile(t, doneSpill, []byte("stale-arena"))

	// An arena whose ID was never in the master list.
	straySpill := filepath.Join(tmpDir, spillName("integrity-stray"))
	writeFile(t, straySpill, []byte("stray-arena"))

	// Near-miss basenames must not be touched: no leading dot, empty id.
	writeFile(t, filepath.Join(tmpDir, "notaspill.spill"), []byte("keep"))
	writeFile(t, filepath.Join(tmpDir, ".spill"), []byte("keep"))

	removed, err := ValidateIntegrity()
	if err != nil {
		t.Fatalf("ValidateIntegrity: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 (all rows valid)", removed)
	}
	for _, p := range []string{doneSpill, straySpill} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("orphan spill %s survived: %v", filepath.Base(p), err)
		}
	}
	for _, p := range []string{liveSpill, filepath.Join(tmpDir, "notaspill.spill"), filepath.Join(tmpDir, ".spill")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("protected file %s swept: %v", filepath.Base(p), err)
		}
	}
}
