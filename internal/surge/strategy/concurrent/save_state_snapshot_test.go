package concurrent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

func TestSaveStateSnapshot_HedgePreferAndPersist(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "hedge.bin")
	state := progress.New("snap-hedge", fileSize)
	state.InitBitmap(fileSize, 100)
	state.Bytes.VerifiedProgress.Store(600)

	downloader := &ConcurrentDownloader{
		ID:          "snap-hedge",
		State:       state,
		Runtime:     &types.RuntimeConfig{Workers: 2, MinChunkSize: 64 * utils.KiB},
		URL:         "http://example.com/hedge.bin",
		activeTasks: make(map[int]*ActiveTask),
	}

	if err := store.AddToMasterList(types.DownloadRecord{
		ID:        "snap-hedge",
		URL:       downloader.URL,
		DestPath:  destPath,
		Status:    "downloading",
		TotalSize: fileSize,
	}); err != nil {
		t.Fatal(err)
	}

	queue := NewTaskQueue()
	sharedOffset := &atomic.Int64{}
	sharedOffset.Store(500)

	queue.Push(types.Task{
		Offset:          500,
		Length:          500,
		SharedMaxOffset: sharedOffset,
	})

	active := &ActiveTask{SharedMaxOffset: sharedOffset}
	active.CurrentOffset.Store(600)
	active.StopAt.Store(1000)
	downloader.activeTasks[0] = active

	if err := downloader.saveStateSnapshot(destPath, fileSize, queue, nil, false); err != nil {
		t.Fatalf("saveStateSnapshot(false): %v", err)
	}

	saved, err := store.LoadState(downloader.URL, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(saved.Tasks) != 1 {
		t.Fatalf("Tasks len=%d, want 1 (fork max-Offset prefer)", len(saved.Tasks))
	}
	if saved.Tasks[0].Offset != 600 || saved.Tasks[0].Length != 400 {
		t.Fatalf("task=%+v, want Offset=600 Length=400", saved.Tasks[0])
	}
	if saved.Tasks[0].SharedMaxOffset != nil {
		t.Fatal("SharedMaxOffset must be cleared on snapshot copies")
	}
	if saved.Downloaded < 600 {
		t.Fatalf("Downloaded=%d, want >=600 (max(VP, computed))", saved.Downloaded)
	}
	if saved.ChunkBitmap == nil {
		t.Fatal("expected ChunkBitmap in snapshot")
	}
	if saved.ActualChunkSize != 100 {
		t.Fatalf("ActualChunkSize=%d, want 100", saved.ActualChunkSize)
	}
	if got := state.TakePendingResumeState(); got == nil || len(got.Tasks) != 1 {
		t.Fatalf("pending Take = %+v, want 1-task record", got)
	}
	if state.TakePendingResumeState() != nil {
		t.Fatal("second pending Take must be nil")
	}
	if state.IsPaused() || state.Pausing.Load() {
		t.Fatal("emit=false must not set pause flags")
	}
}

func TestSaveStateSnapshot_EmitFalse_SkipsCancelDeadline(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * utils.KiB)
	server := testutil.NewMockServerT(t,
		testutil.WithFileSize(fileSize),
		testutil.WithRangeSupport(true),
	)
	defer server.Close()

	destPath := filepath.Join(tmpDir, "cancel_skip.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	state := progress.New("cancel-skip", fileSize)
	d := NewConcurrentDownloader("cancel-skip", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		WorkerBufferSize:          32 * utils.KiB,
	})
	d.URL = server.URL()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := downloadWithTimeout(t, d, ctx, server.URL(), destPath, fileSize, nil, 10*time.Second)
	if err == nil {
		t.Fatal("expected cancel/deadline error")
	}
	if _, loadErr := store.LoadState(server.URL(), destPath); loadErr == nil {
		t.Fatal("cancel path must not persist detail.gob via error snapshot")
	}
	if state.TakePendingResumeState() != nil {
		t.Fatal("cancel path must not stash pending resume state")
	}
}

func TestSaveStateSnapshot_ErrorPath_DownloadPersistsRemainingTasks(t *testing.T) {
	tmpDir, cleanup := initTestState(t)
	defer cleanup()

	fileSize := int64(64 * utils.KiB)
	// Hard-permanent 404: worker burns retries then residual-Pushes before return.
	// ENOSPC coverage is in enospc_test.go (off-queue abandonedRemaining, no live Push).
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	destPath := filepath.Join(tmpDir, "perm_snap.bin")
	if f, err := os.Create(destPath + types.IncompleteSuffix); err == nil {
		_ = f.Close()
	}

	url := server.URL
	if err := store.AddToMasterList(types.DownloadRecord{
		ID:        "perm-snap",
		URL:       url,
		DestPath:  destPath,
		Status:    "downloading",
		Filename:  filepath.Base(destPath),
		TotalSize: fileSize,
	}); err != nil {
		t.Fatal(err)
	}

	state := progress.New("perm-snap", fileSize)
	d := NewConcurrentDownloader("perm-snap", nil, state, &types.RuntimeConfig{
		MaxConnectionsPerDownload: 1,
		Workers:                   1,
		MaxTaskRetries:            1,
		WorkerBufferSize:          32 * utils.KiB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := downloadWithTimeout(t, d, ctx, url, destPath, fileSize, nil, 20*time.Second)
	if !types.IsPermanentHTTPError(err) {
		t.Fatalf("expected IsPermanentHTTPError, got: %v", err)
	}

	saved, loadErr := store.LoadState(url, destPath)
	if loadErr != nil {
		t.Fatalf("LoadState after error-path snapshot: %v", loadErr)
	}
	if len(saved.Tasks) == 0 {
		t.Fatal("expected remaining Tasks so resume SupportsRange would be true")
	}
	if pending := state.TakePendingResumeState(); pending == nil || len(pending.Tasks) == 0 {
		t.Fatalf("pending resume stash missing Tasks: %+v", pending)
	}
}

func TestSaveStateSnapshot_PersistsHeadersClone(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)

	fileSize := int64(1000)
	destPath := filepath.Join(tmpDir, "hdr-snap.bin")
	state := progress.New("hdr-snap", fileSize)
	state.InitBitmap(fileSize, 100)
	state.Bytes.VerifiedProgress.Store(400)

	downloader := &ConcurrentDownloader{
		ID:      "hdr-snap",
		State:   state,
		Runtime: &types.RuntimeConfig{Workers: 1, MinChunkSize: 64 * utils.KiB},
		URL:     "http://example.com/hdr-snap.bin",
		Headers: map[string]string{
			"Cookie":        "session=abc",
			"Authorization": "Bearer tok",
		},
		activeTasks: make(map[int]*ActiveTask),
	}

	if err := store.AddToMasterList(types.DownloadRecord{
		ID:        "hdr-snap",
		URL:       downloader.URL,
		DestPath:  destPath,
		Status:    "downloading",
		TotalSize: fileSize,
	}); err != nil {
		t.Fatal(err)
	}

	queue := NewTaskQueue()
	queue.Push(types.Task{Offset: 400, Length: 600})

	if err := downloader.saveStateSnapshot(destPath, fileSize, queue, nil, false); err != nil {
		t.Fatalf("saveStateSnapshot: %v", err)
	}

	saved, err := store.LoadState(downloader.URL, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Headers["Cookie"] != "session=abc" || saved.Headers["Authorization"] != "Bearer tok" {
		t.Fatalf("persisted Headers = %v, want Cookie+Authorization", saved.Headers)
	}

	// Mutating the live map after the snapshot must not corrupt the stashed
	// record (maps.Clone on the snapshot).
	downloader.Headers["Cookie"] = "session=MUTATED"
	pending := state.TakePendingResumeState()
	if pending == nil {
		t.Fatal("pending resume stash missing")
	}
	if got := pending.Headers["Cookie"]; got != "session=abc" {
		t.Fatalf("snapshot Headers aliased live map: Cookie=%q, want session=abc", got)
	}
}

func TestPersistRangeSupportedBeforeWrite_PersistsHeaders(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)

	fileSize := int64(64 * utils.KiB)
	destPath := filepath.Join(tmpDir, "pf-hdr.bin")
	url := "http://example.com/pf-hdr.bin"
	if err := store.AddToMasterList(types.DownloadRecord{
		ID:       "pf-hdr",
		URL:      url,
		DestPath: destPath,
		Status:   "downloading",
		Filename: filepath.Base(destPath),
	}); err != nil {
		t.Fatal(err)
	}

	d := &ConcurrentDownloader{
		ID:       "pf-hdr",
		URL:      url,
		DestPath: destPath,
		Runtime:  &types.RuntimeConfig{Workers: 1, MinChunkSize: 64 * utils.KiB},
		Headers:  map[string]string{"Cookie": "session=pf"},
	}
	d.payloadFirstSession.Store(true)
	d.pfPlannedTasks = []types.Task{{Offset: 0, Length: fileSize}}
	d.pfFileSize = fileSize
	d.pfCandidateMirrors = []string{url}
	d.pfChunkSize = fileSize

	if err := d.persistRangeSupportedBeforeWrite(); err != nil {
		t.Fatalf("persistRangeSupportedBeforeWrite: %v", err)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Headers["Cookie"] != "session=pf" {
		t.Fatalf("persisted Headers = %v, want Cookie", saved.Headers)
	}
	if saved.RangeAcquisitionMode != types.RangeAcquireRangeSupported {
		t.Fatalf("mode = %q, want range_supported", saved.RangeAcquisitionMode)
	}
}
