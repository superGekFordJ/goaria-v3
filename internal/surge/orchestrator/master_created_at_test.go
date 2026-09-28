package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func runLifecycleEvent(t *testing.T, ev types.DownloadEvent) {
	t.Helper()
	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()
	ch <- ev
	close(ch)
	<-done
}

func masterCreatedAt(t *testing.T, id string) (int64, string) {
	t.Helper()
	got, err := store.GetDownload(id)
	if err != nil || got == nil {
		t.Fatalf("GetDownload(%s): rec=%v err=%v", id, got, err)
	}
	return got.CreatedAt, got.Status
}

func TestEnqueue_StampsMasterCreatedAt(t *testing.T) {
	_ = testutil.SetupStateDB(t)
	ts, _ := newRangeProbeServer(t)
	mgr, _ := newSkipEnqueueManager(t)
	defer mgr.Shutdown()

	before := time.Now().Unix()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	id, _, err := mgr.Enqueue(ctx, &DownloadRequest{
		URL:           ts.URL + "/created.bin",
		Filename:      "created.bin",
		Path:          t.TempDir(),
		FileSize:      1024,
		SupportsRange: new(false),
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	cancelEnqueue(t, mgr, id)

	createdAt, _ := masterCreatedAt(t, id)
	if createdAt < before || createdAt > time.Now().Unix() {
		t.Fatalf("CreatedAt = %d, want within [%d, now]", createdAt, before)
	}
}

func TestLifecycleEvents_PreserveMasterCreatedAt(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "keep.bin")
	url := "http://example.com/keep.bin"
	id := "keep-created-at"
	const createdAt int64 = 1_700_000_000

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:                   id,
		URL:                  url,
		URLHash:              store.URLHash(url),
		DestPath:             destPath,
		Filename:             "keep.bin",
		Status:               "queued",
		CreatedAt:            createdAt,
		TotalSize:            1000,
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
	})

	assertKept := func(step, wantStatus string) {
		t.Helper()
		got, status := masterCreatedAt(t, id)
		if status != wantStatus {
			t.Fatalf("%s: status = %q, want %q", step, status, wantStatus)
		}
		if got != createdAt {
			t.Fatalf("%s: CreatedAt = %d, want %d", step, got, createdAt)
		}
	}

	runLifecycleEvent(t, types.DownloadEvent{
		Type: types.EventStarted, DownloadID: id, URL: url, DestPath: destPath, Filename: "keep.bin", Total: 1000,
	})
	assertKept("started", "downloading")

	runLifecycleEvent(t, types.DownloadEvent{
		Type: types.EventPaused, DownloadID: id, Filename: "keep.bin",
		State: &types.DownloadRecord{
			URL: url, DestPath: destPath, TotalSize: 1000, Downloaded: 100,
			Tasks: []types.Task{{Offset: 100, Length: 900}},
		},
	})
	assertKept("paused", "paused")

	runLifecycleEvent(t, types.DownloadEvent{
		Type: types.EventError, DownloadID: id, Err: errors.New("boom"),
		State: &types.DownloadRecord{
			URL: url, DestPath: destPath, TotalSize: 1000, Downloaded: 200,
			Tasks: []types.Task{{Offset: 200, Length: 800}},
		},
	})
	assertKept("error", "error")

	origRename := renameCompletedFile
	t.Cleanup(func() { renameCompletedFile = origRename })

	renameCompletedFile = func(string, string) error { return errors.New("permission denied") }
	runLifecycleEvent(t, types.DownloadEvent{
		Type: types.EventComplete, DownloadID: id, Total: 1000, Elapsed: 10 * time.Second,
	})
	assertKept("finalize failure", "error")

	renameCompletedFile = func(string, string) error { return nil }
	runLifecycleEvent(t, types.DownloadEvent{
		Type: types.EventComplete, DownloadID: id, Total: 1000, Elapsed: 10 * time.Second,
	})
	assertKept("complete", "completed")
}

func TestLifecycleEvents_AbsentMasterKeepsCreatedAtZero(t *testing.T) {
	_ = testutil.SetupStateDB(t)
	id := "no-master-row"

	runLifecycleEvent(t, types.DownloadEvent{
		Type: types.EventError, DownloadID: id, Err: errors.New("early"),
		URL: "http://example.com/early.bin",
	})
	got, _ := masterCreatedAt(t, id)
	if got != 0 {
		t.Fatalf("CreatedAt = %d, want 0 (not fabricated)", got)
	}
}
