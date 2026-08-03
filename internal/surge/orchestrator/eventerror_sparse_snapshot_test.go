package orchestrator

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// TestEventError_SparseSnapshot_PreservesMasterMetadata verifies that when an
// EventError arrives with a sparse snapshot (zero/empty Filename, TotalSize,
// Downloaded, Workers, MinChunkSize) but an existing master record has rich
// metadata, the master list retains the rich values after both AddToMasterList
// and SaveStateWithOptions have run.
//
// The fork applies field-level fallbacks to `entry` only (not `snapshot`), then
// SaveStateWithOptions unconditionally overwrites the master list's Filename,
// TotalSize, Downloaded, Workers, MinChunkSize with the snapshot's sparse
// values — clobbering the backfilled values. This test reads the master list
// from disk (post-SaveStateWithOptions) to catch that clobber.
func TestEventError_SparseSnapshot_PreservesMasterMetadata(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "report.pdf")
	url := "http://example.com/report.pdf"
	id := "sparse-err"

	// Seed a master record with rich metadata.
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:           id,
		URL:          url,
		URLHash:      store.URLHash(url),
		DestPath:     destPath,
		Filename:     "report.pdf",
		Status:       "downloading",
		TotalSize:    1000000,
		Downloaded:   500000,
		Workers:      4,
		MinChunkSize: 1048576,
		RateLimit:    1000000,
		RateLimitSet: true,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	go mgr.StartEventWorker(ch)

	// Sparse snapshot: valid identity but zero/empty metadata fields.
	snapshot := &types.DownloadRecord{
		URL:      url,
		ID:       id,
		DestPath: destPath,
		Elapsed:  int64(5 * time.Second),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		DestPath:   destPath,
		Err:        errors.New("disk full"),
		State:      snapshot,
	}
	close(ch)

	// Wait for the worker to finish processing (both AddToMasterList and
	// SaveStateWithOptions). Poll until status becomes "error".
	deadline := time.Now().Add(3 * time.Second)
	for {
		entry, err := store.GetDownload(id)
		if err == nil && entry != nil && entry.Status == "error" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Status=error")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Give SaveStateWithOptions a moment to run after AddToMasterList.
	// AddToMasterList sets status=error; SaveStateWithOptions runs next and
	// clobbers the master list. We must read from disk to see the final state.
	time.Sleep(100 * time.Millisecond)

	// Reload the master list from disk to get the post-SaveStateWithOptions state.
	list, err := store.LoadMasterList()
	if err != nil {
		t.Fatalf("LoadMasterList: %v", err)
	}

	var record *types.DownloadRecord
	for i := range list.Downloads {
		if list.Downloads[i].ID == id {
			record = &list.Downloads[i]
			break
		}
	}
	if record == nil {
		t.Fatal("master list does not contain record after EventError")
	}

	// Assert that the rich metadata from the seeded record is preserved.
	if record.Filename != "report.pdf" {
		t.Errorf("Filename=%q, want %q (clobbered by SaveStateWithOptions)",
			record.Filename, "report.pdf")
	}
	if record.TotalSize != 1000000 {
		t.Errorf("TotalSize=%d, want %d (clobbered by SaveStateWithOptions)",
			record.TotalSize, 1000000)
	}
	if record.Downloaded != 500000 {
		t.Errorf("Downloaded=%d, want %d (clobbered by SaveStateWithOptions)",
			record.Downloaded, 500000)
	}
	if record.Workers != 4 {
		t.Errorf("Workers=%d, want %d (clobbered by SaveStateWithOptions)",
			record.Workers, 4)
	}
	if record.MinChunkSize != 1048576 {
		t.Errorf("MinChunkSize=%d, want %d (clobbered by SaveStateWithOptions)",
			record.MinChunkSize, 1048576)
	}
}
