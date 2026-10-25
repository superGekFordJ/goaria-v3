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

func TestIsTaskBackedResumeSnapshot_HLSMatrix(t *testing.T) {
	t.Parallel()

	validResume := func() *types.HLSResumeState {
		segs := make([]types.HLSSegmentState, 9)
		for i := range segs {
			segs[i] = types.HLSSegmentState{Index: i}
		}
		return &types.HLSResumeState{
			ManifestURL:         "https://hls.example.com/index.m3u8",
			MediaPlaylistURL:    "https://hls.example.com/media.m3u8",
			ManifestFingerprint: "fp",
			TotalSegments:       9,
			CompletedBitmap:     []byte{0x0F, 0x00},
			MergedSegments:      4,
			MergedBytes:         500,
			VerifiedBytes:       500,
			Segments:            segs,
		}
	}

	tests := []struct {
		name   string
		mutate func(rec *types.DownloadRecord)
		want   bool
	}{
		{
			name:   "valid",
			mutate: func(rec *types.DownloadRecord) {},
			want:   true,
		},
		{
			name:   "nil_resume",
			mutate: func(rec *types.DownloadRecord) { rec.HLSResume = nil },
			want:   false,
		},
		{
			name: "zero_total_segments",
			mutate: func(rec *types.DownloadRecord) {
				rec.HLSResume.TotalSegments = 0
				rec.HLSResume.Segments = nil
				rec.HLSResume.CompletedBitmap = nil
			},
			want: false,
		},
		{
			name: "bitmap_length_mismatch",
			mutate: func(rec *types.DownloadRecord) {
				rec.HLSResume.CompletedBitmap = []byte{0xFF}
			},
			want: false,
		},
		{
			name: "segments_length_mismatch",
			mutate: func(rec *types.DownloadRecord) {
				rec.HLSResume.Segments = rec.HLSResume.Segments[:8]
			},
			want: false,
		},
		{
			name: "merged_bytes_gt_verified",
			mutate: func(rec *types.DownloadRecord) {
				rec.HLSResume.MergedBytes = 600
			},
			want: false,
		},
		{
			name: "downloaded_ne_verified",
			mutate: func(rec *types.DownloadRecord) {
				rec.Downloaded = 499
			},
			want: false,
		},
		{
			name: "merged_segments_gt_total",
			mutate: func(rec *types.DownloadRecord) {
				rec.HLSResume.MergedSegments = 10
			},
			want: false,
		},
		{
			name: "negative_merged_bytes",
			mutate: func(rec *types.DownloadRecord) {
				rec.HLSResume.MergedBytes = -1
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := types.DownloadRecord{
				Strategy:   types.StrategyHLS,
				Downloaded: 500,
				HLSResume:  validResume(),
			}
			tt.mutate(&rec)
			if got := isTaskBackedResumeSnapshot(rec); got != tt.want {
				t.Fatalf("isTaskBackedResumeSnapshot()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestEventLifecycle_PreservesHLSStrategyAndResumeState(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "video.ts")
	url := "https://hls.example.com/index.m3u8"
	id := "hls-lifecycle"

	segs := make([]types.HLSSegmentState, 9)
	for i := range segs {
		segs[i] = types.HLSSegmentState{Index: i}
	}
	resume := &types.HLSResumeState{
		ManifestURL:         url,
		MediaPlaylistURL:    "https://hls.example.com/media.m3u8",
		ManifestFingerprint: "fp-life",
		TotalSegments:       9,
		CompletedBitmap:     []byte{0x1F, 0x00},
		MergedSegments:      4,
		MergedBytes:         400,
		VerifiedBytes:       500,
		SpillPath:           filepath.Join(tmpDir, ".video.spill"),
		SpillFileSize:       100,
		SpillEntries:        []types.HLSSpillEntry{{SegmentIndex: 4, Offset: 0, Length: 100}},
		Segments:            segs,
	}

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         id,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   destPath,
		Filename:   "video.ts",
		Status:     "downloading",
		Downloaded: 500,
		Strategy:   types.StrategyHLS,
	})
	// Durable detail checkpoint — the authoritative HLS resume state.
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: "video.ts",
		Status: "downloading", Downloaded: 500,
		Strategy: types.StrategyHLS, HLSResume: resume,
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("seed detail: %v", err)
	}

	ch := make(chan types.DownloadEvent, 4)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	// EventStarted must not demote the persisted strategy.
	ch <- types.DownloadEvent{
		Type:       types.EventStarted,
		DownloadID: id,
		URL:        url,
		DestPath:   destPath,
		Filename:   "video.ts",
		State:      &types.DownloadRecord{Strategy: types.StrategyHLS},
	}
	// Sparse pause (m.State == nil) must not advance HLS Downloaded.
	ch <- types.DownloadEvent{
		Type:       types.EventPaused,
		DownloadID: id,
		Downloaded: 9999,
	}
	// Sparse error carrying strategy but no resume must not erase the
	// durable checkpoint.
	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Err:        errors.New("network reset"),
		State:      &types.DownloadRecord{Downloaded: 0},
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil || entry == nil {
		t.Fatalf("GetDownload: %v (nil=%v)", err, entry == nil)
	}
	if entry.Strategy != types.StrategyHLS {
		t.Errorf("master Strategy = %q, want %q", entry.Strategy, types.StrategyHLS)
	}
	if entry.Status != "error" {
		t.Errorf("master Status = %q, want error", entry.Status)
	}
	// The sparse pause Downloaded=9999 must never reach an HLS row; the
	// error snapshot merged to the checkpoint-backed 500.
	if entry.Downloaded != 500 {
		t.Errorf("master Downloaded = %d, want 500", entry.Downloaded)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("LoadState: %v (nil=%v)", err, saved == nil)
	}
	if saved.Strategy != types.StrategyHLS {
		t.Errorf("detail Strategy = %q, want %q", saved.Strategy, types.StrategyHLS)
	}
	if saved.HLSResume == nil {
		t.Fatal("detail HLSResume erased by sparse events")
	}
	if saved.HLSResume.MergedSegments != 4 || saved.HLSResume.VerifiedBytes != 500 ||
		saved.HLSResume.TotalSegments != 9 || len(saved.HLSResume.CompletedBitmap) != 2 {
		t.Errorf("detail HLSResume = %+v, want durable checkpoint", saved.HLSResume)
	}
	if saved.Downloaded != 500 {
		t.Errorf("detail Downloaded = %d, want 500 (sparse pause must not advance)", saved.Downloaded)
	}
}

// hlsCheckpoint builds a strict-valid prefix-only checkpoint: an
// Index-ordered segment table, prefix bitmap, and MergedBytes ==
// VerifiedBytes == verified.
func hlsCheckpoint(url string, verified int64) *types.HLSResumeState {
	segs := make([]types.HLSSegmentState, 4)
	for i := range segs {
		segs[i] = types.HLSSegmentState{Index: i}
	}
	return &types.HLSResumeState{
		ManifestURL:      url,
		MediaPlaylistURL: url,
		TotalSegments:    4,
		CompletedBitmap:  []byte{0x07},
		MergedSegments:   3,
		MergedBytes:      verified,
		VerifiedBytes:    verified,
		Segments:         segs,
	}
}

func seedHLSDetail(t *testing.T, id, url, destPath, filename string, downloaded int64, resume *types.HLSResumeState) {
	t.Helper()
	if err := store.SaveStateWithOptions(url, destPath, &types.DownloadRecord{
		ID: id, URL: url, DestPath: destPath, Filename: filename,
		Status: "downloading", Downloaded: downloaded,
		Strategy: types.StrategyHLS, HLSResume: resume,
	}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("seed detail: %v", err)
	}
}

// A sparse EventError carrying no resume must adopt the durable checkpoint's
// VerifiedBytes even when master Downloaded diverges — otherwise the
// persisted record fails its own Downloaded == VerifiedBytes invariant and
// every later snapshot degrades to taskless max semantics.
func TestEventError_HLSSparseSnapshot_RecoversCheckpointVerifiedBytes(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "hls_ev.ts")
	url := "https://hls.example.com/ev.m3u8"
	id := "hls-ev-diverge"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url),
		DestPath: destPath, Filename: "hls_ev.ts",
		Status: "downloading", Downloaded: 800, Strategy: types.StrategyHLS,
	})
	seedHLSDetail(t, id, url, destPath, "hls_ev.ts", 500, hlsCheckpoint(url, 500))

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   "hls_ev.ts",
		DestPath:   destPath,
		Err:        errors.New("network reset"),
		State:      &types.DownloadRecord{Downloaded: 0},
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil || entry == nil {
		t.Fatalf("GetDownload: %v (nil=%v)", err, entry == nil)
	}
	if entry.Downloaded != 500 {
		t.Fatalf("master Downloaded = %d, want 500 (checkpoint VerifiedBytes, not max→800)", entry.Downloaded)
	}
	if entry.Strategy != types.StrategyHLS {
		t.Fatalf("master Strategy = %q, want %q", entry.Strategy, types.StrategyHLS)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("LoadState: %v (nil=%v)", err, saved == nil)
	}
	if saved.HLSResume == nil || saved.HLSResume.VerifiedBytes != 500 {
		t.Fatalf("detail HLSResume = %+v, want durable checkpoint V=500", saved.HLSResume)
	}
	if saved.Downloaded != 500 {
		t.Fatalf("detail Downloaded = %d, want 500 == VerifiedBytes", saved.Downloaded)
	}
	// The persisted record must still validate as task-backed so the next
	// event keeps checkpoint authority instead of permanent max() demotion.
	if !isTaskBackedResumeSnapshot(*saved) {
		t.Fatal("persisted detail no longer satisfies the task-backed invariants")
	}
}

// Same divergence coverage for EventPaused: a sparse pause must not let
// stale master progress override the checkpoint it just recovered.
func TestEventPaused_HLSSparseSnapshot_RecoversCheckpointVerifiedBytes(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "hls_pv.ts")
	url := "https://hls.example.com/pv.m3u8"
	id := "hls-pv-diverge"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url),
		DestPath: destPath, Filename: "hls_pv.ts",
		Status: "downloading", Downloaded: 800, Strategy: types.StrategyHLS,
	})
	seedHLSDetail(t, id, url, destPath, "hls_pv.ts", 500, hlsCheckpoint(url, 500))

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	ch <- types.DownloadEvent{
		Type:       types.EventPaused,
		DownloadID: id,
		Filename:   "hls_pv.ts",
		DestPath:   destPath,
		State:      &types.DownloadRecord{Downloaded: 0},
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil || entry == nil {
		t.Fatalf("GetDownload: %v (nil=%v)", err, entry == nil)
	}
	if entry.Downloaded != 500 {
		t.Fatalf("master Downloaded = %d, want 500 (checkpoint VerifiedBytes, not max→800)", entry.Downloaded)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("LoadState: %v (nil=%v)", err, saved == nil)
	}
	if saved.HLSResume == nil || saved.HLSResume.VerifiedBytes != 500 || saved.Downloaded != 500 {
		t.Fatalf("detail = Downloaded %d / resume %+v, want 500 + checkpoint", saved.Downloaded, saved.HLSResume)
	}
	if !isTaskBackedResumeSnapshot(*saved) {
		t.Fatal("persisted detail no longer satisfies the task-backed invariants")
	}
}

// A task-backed HLS pause at zero bytes is authoritative: the valid
// checkpoint keeps 0 rather than inheriting the master's stale progress.
func TestEventPaused_HLSZeroCheckpoint_IsAuthoritative(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "hls_zero.ts")
	url := "https://hls.example.com/zero.m3u8"
	id := "hls-zero"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID: id, URL: url, URLHash: store.URLHash(url),
		DestPath: destPath, Filename: "hls_zero.ts",
		Status: "downloading", Downloaded: 500, Strategy: types.StrategyHLS,
	})
	seedHLSDetail(t, id, url, destPath, "hls_zero.ts", 500, hlsCheckpoint(url, 500))

	zeroSegs := make([]types.HLSSegmentState, 4)
	for i := range zeroSegs {
		zeroSegs[i] = types.HLSSegmentState{Index: i}
	}
	zeroResume := &types.HLSResumeState{
		ManifestURL:     url,
		TotalSegments:   4,
		CompletedBitmap: []byte{0x00},
		VerifiedBytes:   0,
		Segments:        zeroSegs,
	}

	ch := make(chan types.DownloadEvent, 1)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	ch <- types.DownloadEvent{
		Type:       types.EventPaused,
		DownloadID: id,
		Filename:   "hls_zero.ts",
		DestPath:   destPath,
		State: &types.DownloadRecord{
			URL: url, DestPath: destPath, Filename: "hls_zero.ts",
			Downloaded: 0, Strategy: types.StrategyHLS, HLSResume: zeroResume,
		},
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil || entry == nil {
		t.Fatalf("GetDownload: %v (nil=%v)", err, entry == nil)
	}
	if entry.Downloaded != 0 {
		t.Fatalf("master Downloaded = %d, want 0 (task-backed zero wins over 500)", entry.Downloaded)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("LoadState: %v (nil=%v)", err, saved == nil)
	}
	if saved.HLSResume == nil || saved.HLSResume.VerifiedBytes != 0 || saved.Downloaded != 0 {
		t.Fatalf("detail = Downloaded %d / resume %+v, want zero checkpoint", saved.Downloaded, saved.HLSResume)
	}
	if !isTaskBackedResumeSnapshot(*saved) {
		t.Fatal("zero checkpoint detail must still satisfy the task-backed invariants")
	}
}

// With no master row and no event State the minimal error record must still
// inherit persisted discriminators — a Strategy="" row would switch off the
// checkpoint recovery guards for the next event on the same task.
func TestEventError_MinimalRecord_RescuesStrategyFromDetail(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "hls_min.ts")
	url := "https://hls.example.com/min.m3u8"
	id := "hls-minimal"

	// Detail only — no master row — as after an early pre-enqueue failure.
	seedHLSDetail(t, id, url, destPath, "hls_min.ts", 500, hlsCheckpoint(url, 500))

	ch := make(chan types.DownloadEvent, 2)
	mgr := NewLifecycleManager(nil, nil, nil)
	defer mgr.Shutdown()
	done := make(chan struct{})
	go func() {
		mgr.StartEventWorker(ch)
		close(done)
	}()

	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   "hls_min.ts",
		URL:        url,
		DestPath:   destPath,
		Err:        errors.New("early failure"),
	}
	// A later sparse event must still classify this task as HLS: the
	// rescued strategy enables checkpoint recovery instead of erasing it.
	ch <- types.DownloadEvent{
		Type:       types.EventError,
		DownloadID: id,
		Filename:   "hls_min.ts",
		DestPath:   destPath,
		Err:        errors.New("second failure"),
		State:      &types.DownloadRecord{Downloaded: 0},
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil || entry == nil {
		t.Fatalf("GetDownload: %v (nil=%v)", err, entry == nil)
	}
	if entry.Strategy != types.StrategyHLS {
		t.Fatalf("minimal master row Strategy = %q, want %q", entry.Strategy, types.StrategyHLS)
	}
	if entry.Downloaded != 500 {
		t.Fatalf("master Downloaded = %d, want 500 (checkpoint adopted on second event)", entry.Downloaded)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil || saved == nil {
		t.Fatalf("LoadState: %v (nil=%v)", err, saved == nil)
	}
	if saved.HLSResume == nil || saved.HLSResume.VerifiedBytes != 500 {
		t.Fatalf("detail HLSResume = %+v — checkpoint must survive the minimal-record path", saved.HLSResume)
	}
}
