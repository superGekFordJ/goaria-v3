package service

import (
	"os"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// TestLocalDownloadService_Purge_RemovesSpillArena pins Purge's full sweep:
// a stopped HLS row's final file, .surge working file and the hidden
// .<id>.spill arena all leave the disk in one call.
func TestLocalDownloadService_Purge_RemovesSpillArena(t *testing.T) {
	svc, ts, tmpDir, _ := setupTestService(t)
	defer ts.Close()
	t.Cleanup(func() { _ = svc.Shutdown() })

	const id = "purge-hls-id"
	destPath := filepath.Join(tmpDir, "clip.ts")
	targets := []string{
		destPath,
		destPath + types.IncompleteSuffix,
		filepath.Join(tmpDir, "."+id+".spill"),
	}
	for _, p := range targets {
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	if err := store.AddToMasterList(types.DownloadRecord{
		ID:       id,
		URL:      "https://example.com/clip.m3u8",
		DestPath: destPath,
		Filename: "clip.ts",
		Status:   "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	if err := svc.Purge(id); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	for _, p := range targets {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived Purge: %v", filepath.Base(p), err)
		}
	}
}

// TestLocalDownloadService_Purge_IllegalSpillID surfaces — rather than
// silently skipping — an ID that cannot name a legal arena file: the
// aggregated error still reports the refused deletion.
func TestLocalDownloadService_Purge_IllegalSpillID(t *testing.T) {
	svc, ts, tmpDir, _ := setupTestService(t)
	defer ts.Close()
	t.Cleanup(func() { _ = svc.Shutdown() })

	const id = "purge/../escape"
	destPath := filepath.Join(tmpDir, "clip.ts")
	if err := os.WriteFile(destPath, []byte("data"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := store.AddToMasterList(types.DownloadRecord{
		ID:       id,
		URL:      "https://example.com/clip.m3u8",
		DestPath: destPath,
		Filename: "clip.ts",
		Status:   "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	if err := svc.Purge(id); err == nil {
		t.Fatal("Purge succeeded despite an illegal spill ID")
	}
	// The plain file deletion still ran — the error is about the refused
	// arena name, not an early abort.
	if _, err := os.Stat(destPath); !os.IsNotExist(err) {
		t.Fatalf("final file survived Purge: %v", err)
	}
}
