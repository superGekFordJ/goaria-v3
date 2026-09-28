package orchestrator

import (
	"errors"
	"path/filepath"
	"testing"

	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// Duplicate IDs inside one ResumeBatch must not double-enqueue: the first
// occurrence resumes normally, repeats report ErrAlreadyActive, and the pool
// ends up with exactly one queued entry.
func TestResumeBatch_DuplicateIDsGetAlreadyActive(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)

	const id = "dup-resume"
	if err := store.AddToMasterList(types.DownloadRecord{
		ID:       id,
		URL:      "http://example.com/dup.bin",
		DestPath: filepath.Join(tmpDir, "dup.bin"),
		Filename: "dup.bin",
		Status:   "paused",
	}); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{})
	mgr := NewLifecycleManager(pool, nil, nil)
	defer mgr.Shutdown()

	errs := mgr.ResumeBatch([]string{id, id})
	if len(errs) != 2 {
		t.Fatalf("len(errs) = %d, want 2", len(errs))
	}
	if errs[0] != nil {
		t.Fatalf("errs[0] = %v, want nil", errs[0])
	}
	if !errors.Is(errs[1], types.ErrAlreadyActive) {
		t.Fatalf("errs[1] = %v, want ErrAlreadyActive", errs[1])
	}

	if all := pool.GetAll(); len(all) != 1 {
		t.Fatalf("pool holds %d entries, want exactly 1", len(all))
	}
}
