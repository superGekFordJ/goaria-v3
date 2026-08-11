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

func TestEventComplete_FinalizationError_PersistsErrorString(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "finalize_fail.bin")
	url := "http://example.com/finalize_fail.bin"
	id := "finalize-fail"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:        id,
		URL:       url,
		URLHash:   store.URLHash(url),
		DestPath:  destPath,
		Filename:  filepath.Base(destPath),
		Status:    "downloading",
		TotalSize: 1000,
	})

	// Override renameCompletedFile to simulate finalization failure.
	// The error must NOT be syscall.EXDEV (which triggers the copy fallback),
	// and os.Stat(finalPath) must fail (file does not exist) so
	// finalizeCompletedFile returns the error directly.
	origRename := renameCompletedFile
	renameCompletedFile = func(src, dst string) error {
		return errors.New("permission denied")
	}
	t.Cleanup(func() { renameCompletedFile = origRename })

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	ch <- types.DownloadEvent{
		Type:       types.EventComplete,
		DownloadID: id,
		Total:      1000,
		Elapsed:    10 * time.Second,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil {
		t.Fatal("expected error record, got nil")
	}
	if entry.Status != "error" {
		t.Fatalf("Status=%q, want error", entry.Status)
	}
	if entry.Error != "permission denied" {
		t.Fatalf("master Error=%q, want %q", entry.Error, "permission denied")
	}
}
