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
	// Hard-permanent 404: worker burns retries then residual-Pushes before return,
	// so Download's emit=false snapshot still sees remaining Tasks (unlike ENOSPC
	// which intentionally skips residual Push).
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
