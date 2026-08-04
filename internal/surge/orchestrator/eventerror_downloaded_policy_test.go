package orchestrator

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func TestIsTaskBackedResumeSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rec  types.DownloadRecord
		want bool
	}{
		{
			name: "valid_single_range",
			rec: types.DownloadRecord{
				TotalSize: 1000,
				Tasks:     []types.Task{{Offset: 600, Length: 400}},
			},
			want: true,
		},
		{
			name: "multiple_valid_ranges",
			rec: types.DownloadRecord{
				TotalSize: 1000,
				Tasks: []types.Task{
					{Offset: 100, Length: 100},
					{Offset: 600, Length: 400},
				},
			},
			want: true,
		},
		{
			name: "empty_Tasks",
			rec: types.DownloadRecord{
				TotalSize: 1000,
				Tasks:     nil,
			},
			want: false,
		},
		{
			name: "Length_le_0",
			rec: types.DownloadRecord{
				TotalSize: 1000,
				Tasks:     []types.Task{{Offset: 0, Length: 0}},
			},
			want: false,
		},
		{
			name: "Offset_lt_0",
			rec: types.DownloadRecord{
				TotalSize: 1000,
				Tasks:     []types.Task{{Offset: -1, Length: 10}},
			},
			want: false,
		},
		{
			name: "end_past_TotalSize",
			rec: types.DownloadRecord{
				TotalSize: 1000,
				Tasks:     []types.Task{{Offset: 900, Length: 200}},
			},
			want: false,
		},
		{
			name: "TotalSize_0_with_Tasks",
			rec: types.DownloadRecord{
				TotalSize: 0,
				Tasks:     []types.Task{{Offset: 0, Length: 100}},
			},
			want: false,
		},
		{
			name: "overflow_large_Offset_Length",
			rec: types.DownloadRecord{
				TotalSize: math.MaxInt64,
				Tasks:     []types.Task{{Offset: math.MaxInt64, Length: math.MaxInt64}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isTaskBackedResumeSnapshot(tt.rec); got != tt.want {
				t.Fatalf("isTaskBackedResumeSnapshot()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestEventError_Downloaded_TaskBacked_LowerWins(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "tb_lower.bin")
	url := "http://example.com/tb_lower.bin"
	id := "tb-lower"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         id,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "downloading",
		TotalSize:  1000,
		Downloaded: 800,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	snapshot := &types.DownloadRecord{
		URL:        url,
		ID:         id,
		DestPath:   destPath,
		TotalSize:  1000,
		Downloaded: 600,
		Tasks:      []types.Task{{Offset: 600, Length: 400}},
		Filename:   filepath.Base(destPath),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		DestPath:   destPath,
		Err:        errors.New("disk full"),
		State:      snapshot,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil || entry.Downloaded != 600 {
		t.Fatalf("master Downloaded=%v, want 600 (task-backed lower wins, not max→800)", entry)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Downloaded != 600 {
		t.Fatalf("detail Downloaded=%d, want 600", saved.Downloaded)
	}
}

func TestEventError_Downloaded_TaskBacked_ZeroWins(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "tb_zero.bin")
	url := "http://example.com/tb_zero.bin"
	id := "tb-zero"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         id,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "downloading",
		TotalSize:  1000,
		Downloaded: 800,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	snapshot := &types.DownloadRecord{
		URL:        url,
		ID:         id,
		DestPath:   destPath,
		TotalSize:  1000,
		Downloaded: 0,
		Tasks:      []types.Task{{Offset: 0, Length: 1000}},
		Filename:   filepath.Base(destPath),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		DestPath:   destPath,
		Err:        errors.New("disk full"),
		State:      snapshot,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil || entry.Downloaded != 0 {
		t.Fatalf("master Downloaded=%v, want 0 (task-backed zero wins, not backfill→800)", entry)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Downloaded != 0 {
		t.Fatalf("detail Downloaded=%d, want 0", saved.Downloaded)
	}
}

func TestEventError_Downloaded_Taskless_MaxPreservesMaster(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "taskless.bin")
	url := "http://example.com/taskless.bin"
	id := "taskless-max"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         id,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "downloading",
		TotalSize:  1000,
		Downloaded: 800,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	snapshot := &types.DownloadRecord{
		URL:        url,
		ID:         id,
		DestPath:   destPath,
		TotalSize:  1000,
		Downloaded: 100,
		Filename:   filepath.Base(destPath),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		DestPath:   destPath,
		Err:        errors.New("stale"),
		State:      snapshot,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil || entry.Downloaded != 800 {
		t.Fatalf("master Downloaded=%v, want 800 (taskless max)", entry)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Downloaded != 800 {
		t.Fatalf("detail Downloaded=%d, want 800", saved.Downloaded)
	}
}

func TestEventError_Identity_SparseSnapshot_LoadState(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "identity.bin")
	url := "http://example.com/identity.bin"
	id := "identity-sparse"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         id,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "downloading",
		TotalSize:  1000,
		Downloaded: 400,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	// Sparse identity: blank URL/DestPath/ID on snapshot; event carries DownloadID.
	snapshot := &types.DownloadRecord{
		TotalSize:  1000,
		Downloaded: 400,
		Tasks:      []types.Task{{Offset: 400, Length: 600}},
		Elapsed:    int64(time.Second),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		DestPath:   destPath,
		URL:        url,
		Err:        errors.New("disk full"),
		State:      snapshot,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil {
		t.Fatal("master entry missing")
	}
	if entry.URL != url || entry.DestPath != destPath {
		t.Fatalf("master resume key blanked: URL=%q DestPath=%q", entry.URL, entry.DestPath)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState(originalURL, originalDest): %v", err)
	}
	if saved.ID != id {
		t.Fatalf("detail ID=%q, want event DownloadID %q", saved.ID, id)
	}
	if saved.URL != url || saved.DestPath != destPath {
		t.Fatalf("detail identity blanked: URL=%q DestPath=%q", saved.URL, saved.DestPath)
	}
}

func TestEventError_Filename_NilExisting_SaveStateSkipped(t *testing.T) {
	_ = testutil.SetupStateDB(t)
	id := "filename-h4"

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	snapshot := &types.DownloadRecord{
		ID:         id,
		Filename:   "from-snapshot.bin",
		TotalSize:  500,
		Downloaded: 100,
		Elapsed:    int64(time.Second),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		// Empty event filename + empty url/dest → SaveState skipped; entry must
		// still receive snapshot.Filename (H4).
		Err:   errors.New("boom"),
		State: snapshot,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil {
		t.Fatal("master entry missing after AddToMasterList")
	}
	if entry.Filename != "from-snapshot.bin" {
		t.Fatalf("Filename=%q, want from-snapshot.bin (snapshot wins when event empty)", entry.Filename)
	}
}

func TestEventError_Identity_DownloadIDWinsOverSnapshotID(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "id_precedence.bin")
	url := "http://example.com/id_precedence.bin"
	masterID := "id-A"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         masterID,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		Status:     "downloading",
		TotalSize:  1000,
		Downloaded: 200,
	})

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	snapshot := &types.DownloadRecord{
		URL:        url,
		ID:         "id-B", // mismatch vs event DownloadID
		DestPath:   destPath,
		TotalSize:  1000,
		Downloaded: 200,
		Tasks:      []types.Task{{Offset: 200, Length: 800}},
		Filename:   filepath.Base(destPath),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: masterID,
		Filename:   filepath.Base(destPath),
		DestPath:   destPath,
		URL:        url,
		Err:        errors.New("boom"),
		State:      snapshot,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(masterID)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil || entry.ID != masterID {
		t.Fatalf("master ID=%v, want %q (event DownloadID wins)", entry, masterID)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.ID != masterID {
		t.Fatalf("detail ID=%q, want %q (not snapshot id-B)", saved.ID, masterID)
	}
}
