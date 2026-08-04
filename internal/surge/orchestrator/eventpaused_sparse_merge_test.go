package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func TestEventPaused_RichState_NoUnexpectedBackfill(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "rich_pause.bin")
	url := "http://example.com/rich_pause.bin"
	id := "rich-pause"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:           id,
		URL:          url,
		URLHash:      store.URLHash(url),
		DestPath:     destPath,
		Filename:     "old-name.bin",
		Status:       "downloading",
		TotalSize:    1000,
		Downloaded:   100,
		Workers:      2,
		MinChunkSize: 32 * 1024,
		RateLimit:    1024,
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

	snapshot := &types.DownloadRecord{
		URL:          url,
		ID:           id,
		DestPath:     destPath,
		Filename:     filepath.Base(destPath),
		TotalSize:    1000,
		Downloaded:   600,
		Tasks:        []types.Task{{Offset: 600, Length: 400}},
		Elapsed:      int64(2 * time.Second),
		Workers:      4,
		MinChunkSize: 64 * 1024,
		RateLimit:    2048,
		RateLimitSet: true,
	}

	ch <- types.DownloadEvent{
		Type:         types.EventPaused,
		DownloadID:   id,
		Filename:     filepath.Base(destPath),
		DestPath:     destPath,
		URL:          url,
		Downloaded:   600,
		Workers:      4,
		MinChunkSize: 64 * 1024,
		RateLimit:    2048,
		RateLimitSet: true,
		State:        snapshot,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil || entry.Status != "paused" {
		t.Fatalf("master status=%v, want paused", entry)
	}
	if entry.Downloaded != 600 || entry.TotalSize != 1000 {
		t.Fatalf("master progress Downloaded=%d TotalSize=%d, want 600/1000", entry.Downloaded, entry.TotalSize)
	}
	if entry.Filename != filepath.Base(destPath) {
		t.Fatalf("master Filename=%q, want rich snapshot name", entry.Filename)
	}
	if entry.Workers != 4 || entry.MinChunkSize != 64*1024 {
		t.Fatalf("master Workers=%d MinChunkSize=%d, want rich values", entry.Workers, entry.MinChunkSize)
	}
	if !entry.RateLimitSet || entry.RateLimit != 2048 {
		t.Fatalf("master RateLimit=%d Set=%v, want rich 2048/true", entry.RateLimit, entry.RateLimitSet)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Downloaded != 600 || saved.Workers != 4 || saved.MinChunkSize != 64*1024 {
		t.Fatalf("detail mismatch: Downloaded=%d Workers=%d MinChunkSize=%d", saved.Downloaded, saved.Workers, saved.MinChunkSize)
	}
	if !saved.RateLimitSet || saved.RateLimit != 2048 {
		t.Fatalf("detail RateLimit=%d Set=%v, want 2048/true", saved.RateLimit, saved.RateLimitSet)
	}
}

func TestEventPaused_FirstPauseZero_TaskBackedWins(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "first_zero.bin")
	url := "http://example.com/first_zero.bin"
	id := "first-zero"

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
		Filename:   filepath.Base(destPath),
		TotalSize:  1000,
		Downloaded: 0,
		Tasks:      []types.Task{{Offset: 0, Length: 1000}},
		Elapsed:    int64(time.Second),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventPaused,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		State:      snapshot,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil || entry.Downloaded != 0 {
		t.Fatalf("master Downloaded=%v, want 0 (task-backed first-pause zero, not max→800)", entry)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Downloaded != 0 {
		t.Fatalf("detail Downloaded=%d, want 0", saved.Downloaded)
	}
}

func TestEventPaused_NilState_FallbackPinsBehavior(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "nil_state.bin")
	url := "http://example.com/nil_state.bin"
	id := "nil-state-pause"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:           id,
		URL:          url,
		URLHash:      store.URLHash(url),
		DestPath:     destPath,
		Filename:     filepath.Base(destPath),
		Status:       "downloading",
		TotalSize:    1000,
		Downloaded:   200,
		TimeTaken:    1500,
		RateLimit:    4096,
		RateLimitSet: true,
	})

	seed := &types.DownloadRecord{
		URL:        url,
		ID:         id,
		DestPath:   destPath,
		Filename:   filepath.Base(destPath),
		TotalSize:  1000,
		Downloaded: 200,
		Tasks:      []types.Task{{Offset: 200, Length: 800}},
		Elapsed:    int64(1500 * time.Millisecond),
	}
	if err := store.SaveStateWithOptions(url, destPath, seed, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		t.Fatalf("seed SaveState: %v", err)
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
		Type:         types.EventPaused,
		DownloadID:   id,
		Downloaded:   450,
		RateLimit:    4096,
		RateLimitSet: true,
		State:        nil,
	}
	close(ch)
	<-done

	entry, err := store.GetDownload(id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if entry == nil || entry.Status != "paused" {
		t.Fatalf("master status=%v, want paused", entry)
	}
	if entry.Downloaded != 450 {
		t.Fatalf("master Downloaded=%d, want 450 from nil-State event", entry.Downloaded)
	}
	// Nil-State copies existing entry; RateLimit comes from master, not event rewrite.
	if !entry.RateLimitSet || entry.RateLimit != 4096 {
		t.Fatalf("master RateLimit=%d Set=%v, want preserved master override", entry.RateLimit, entry.RateLimitSet)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Downloaded != 450 {
		t.Fatalf("detail Downloaded=%d, want 450 via advanceRemainingTasks", saved.Downloaded)
	}
	if len(saved.Tasks) != 1 || saved.Tasks[0].Offset != 450 || saved.Tasks[0].Length != 550 {
		t.Fatalf("detail Tasks=%v, want remaining [450,1000)", saved.Tasks)
	}
}

func TestEventPaused_Sparse_MetadataAndRateLimitPreserved(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "sparse_meta.bin")
	url := "http://example.com/sparse_meta.bin"
	id := "sparse-meta"

	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:           id,
		URL:          url,
		URLHash:      store.URLHash(url),
		DestPath:     destPath,
		Filename:     "rich-file.bin",
		Status:       "downloading",
		TotalSize:    5000,
		Downloaded:   2500,
		Workers:      8,
		MinChunkSize: 128 * 1024,
		RateLimit:    8192,
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

	// Sparse State: zeros/empties; event RateLimitSet=false (omission, not intentional clear).
	snapshot := &types.DownloadRecord{
		Elapsed: int64(time.Second),
	}

	ch <- types.DownloadEvent{
		Type:         types.EventPaused,
		DownloadID:   id,
		RateLimit:    0,
		RateLimitSet: false,
		State:        snapshot,
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
	if entry.Filename != "rich-file.bin" {
		t.Fatalf("Filename clobbered: %q", entry.Filename)
	}
	if entry.TotalSize != 5000 {
		t.Fatalf("TotalSize clobbered: %d", entry.TotalSize)
	}
	if entry.Downloaded != 2500 {
		t.Fatalf("Downloaded clobbered: %d (want taskless max→2500)", entry.Downloaded)
	}
	if entry.Workers != 8 || entry.MinChunkSize != 128*1024 {
		t.Fatalf("Workers/MinChunkSize clobbered: %d / %d", entry.Workers, entry.MinChunkSize)
	}
	if !entry.RateLimitSet || entry.RateLimit != 8192 {
		t.Fatalf("RateLimit override lost: RateLimit=%d Set=%v", entry.RateLimit, entry.RateLimitSet)
	}

	saved, err := store.LoadState(url, destPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if saved.Filename != "rich-file.bin" || saved.TotalSize != 5000 || saved.Downloaded != 2500 {
		t.Fatalf("detail metadata wiped: %+v", saved)
	}
	if saved.Workers != 8 || saved.MinChunkSize != 128*1024 {
		t.Fatalf("detail Workers/MinChunkSize wiped: %d / %d", saved.Workers, saved.MinChunkSize)
	}
	if !saved.RateLimitSet || saved.RateLimit != 8192 {
		t.Fatalf("detail RateLimit wiped: %d Set=%v", saved.RateLimit, saved.RateLimitSet)
	}
}

func TestEventPaused_Sparse_IdentityLoadState(t *testing.T) {
	tmpDir := testutil.SetupStateDB(t)
	destPath := filepath.Join(tmpDir, "sparse_id.bin")
	url := "http://example.com/sparse_id.bin"
	id := "sparse-identity"

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

	snapshot := &types.DownloadRecord{
		TotalSize:  1000,
		Downloaded: 400,
		Tasks:      []types.Task{{Offset: 400, Length: 600}},
		Elapsed:    int64(time.Second),
	}

	ch <- types.DownloadEvent{
		Type:       types.EventPaused,
		DownloadID: id,
		Filename:   filepath.Base(destPath),
		DestPath:   destPath,
		URL:        url,
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
