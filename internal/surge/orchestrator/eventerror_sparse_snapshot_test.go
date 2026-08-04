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
// metadata, those rich values survive on the master list after the worker
// finishes.
//
// Taskless snapshots (empty Tasks) apply max(existing, snapshot) for
// Downloaded; Filename / TotalSize / Workers / MinChunkSize still sparse-
// backfill from the master before SaveStateWithOptions.
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
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

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
	<-done

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
	if record.Status != "error" {
		t.Fatalf("Status=%q, want error", record.Status)
	}

	// Assert that the rich metadata from the seeded record is preserved.
	if record.Filename != "report.pdf" {
		t.Errorf("Filename=%q, want %q (expected snapshot backfill from master)",
			record.Filename, "report.pdf")
	}
	if record.TotalSize != 1000000 {
		t.Errorf("TotalSize=%d, want %d (expected snapshot backfill from master)",
			record.TotalSize, 1000000)
	}
	if record.Downloaded != 500000 {
		t.Errorf("Downloaded=%d, want %d (expected snapshot backfill from master)",
			record.Downloaded, 500000)
	}
	if record.Workers != 4 {
		t.Errorf("Workers=%d, want %d (expected snapshot backfill from master)",
			record.Workers, 4)
	}
	if record.MinChunkSize != 1048576 {
		t.Errorf("MinChunkSize=%d, want %d (expected snapshot backfill from master)",
			record.MinChunkSize, 1048576)
	}
}
