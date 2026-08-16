package scheduler

import (
	"errors"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// TestAbandonConcurrentResumeForSingleFallback locks the Truncate+single
// path: pending stash must clear and Layer1 detail.gob must not revive
// abandoned concurrent Tasks after zero-progress fallback.
func TestAbandonConcurrentResumeForSingleFallback(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "fallback.bin")
	url := "http://example.com/fallback.bin"
	id := "fallback-id"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:        id,
		URL:       url,
		URLHash:   store.URLHash(url),
		DestPath:  destPath,
		Filename:  filepath.Base(destPath),
		Status:    "downloading",
		TotalSize: 1000,
	})

	stale := &types.DownloadRecord{
		ID:         id,
		URL:        url,
		DestPath:   destPath,
		TotalSize:  1000,
		Downloaded: 0,
		Tasks:      []types.Task{{Offset: 0, Length: 1000}},
		Filename:   filepath.Base(destPath),
	}
	if err := store.SaveStateWithOptions(url, destPath, stale, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("SaveStateWithOptions: %v", err)
	}

	ps := progress.New(id, 1000)
	ps.SetPendingResumeState(stale)

	if !shouldFallbackToSingle(errors.New("boom"), 0, "") {
		t.Fatal("expected fallback eligibility for zero-progress non-disk error")
	}

	abandonConcurrentResumeForSingleFallback(ps, id)

	if got := ps.TakePendingResumeState(); got != nil {
		t.Fatalf("pending after abandon = %+v, want nil (EventError.State must stay nil)", got)
	}

	saved, err := store.LoadState(url, destPath)
	if err == nil && saved != nil && len(saved.Tasks) > 0 {
		t.Fatalf("LoadState revived abandoned concurrent Tasks: %+v", saved.Tasks)
	}
}
