package rpc

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

func newCacheTestEngine(t *testing.T) *SurgeEngine {
	t.Helper()
	testutil.SetupStateDB(t)
	pool := scheduler.NewSchedulerForTesting(nil)
	e := NewSurgeEngineForTesting(pool)
	t.Cleanup(e.Close)
	e.masterCache = []types.DownloadRecord{}
	return e
}

func TestMasterCache_UpsertAndRemove(t *testing.T) {
	e := newCacheTestEngine(t)

	entry := types.DownloadRecord{ID: "dl-1", URL: "http://x/a", Status: "completed"}
	e.UpsertMasterCacheEntry(entry)

	list, err := e.getDownloadList()
	if err != nil {
		t.Fatalf("getDownloadList: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == "dl-1" {
			found = true
			if s.Status != "completed" {
				t.Errorf("status = %q, want completed", s.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected dl-1 in buildDownloadList output")
	}

	// Upsert replaces existing entry (same ID).
	e.UpsertMasterCacheEntry(types.DownloadRecord{ID: "dl-1", URL: "http://x/a", Status: "error"})
	got, ok := e.GetMasterCacheEntry("dl-1")
	if !ok {
		t.Fatal("expected dl-1 in cache")
	}
	if got.Status != "error" {
		t.Errorf("after upsert status = %q, want error", got.Status)
	}

	e.RemoveMasterCacheEntry("dl-1")
	if _, ok := e.GetMasterCacheEntry("dl-1"); ok {
		t.Fatal("expected dl-1 removed from cache")
	}
}

// Write-side isolation is symmetric with the read-side clones: mutating the
// caller's slices after Upsert must never reach the stored mirror row.
func TestMasterCache_UpsertClonesWriteSide(t *testing.T) {
	e := newCacheTestEngine(t)

	entry := types.DownloadRecord{
		ID:      "dl-w",
		Status:  "queued",
		Mirrors: []string{"http://m1"},
		HLSResume: &types.HLSResumeState{
			CompletedBitmap: []byte{0x0f},
			Segments:        []types.HLSSegmentState{{Index: 0, URI: "http://seg/0"}},
		},
	}
	e.UpsertMasterCacheEntry(entry)

	entry.Mirrors[0] = "http://mutated"
	entry.HLSResume.CompletedBitmap[0] = 0
	entry.HLSResume.Segments[0].URI = "http://mutated"

	got, ok := e.GetMasterCacheEntry("dl-w")
	if !ok {
		t.Fatal("entry missing after upsert")
	}
	if got.Mirrors[0] != "http://m1" ||
		got.HLSResume.CompletedBitmap[0] != 0x0f ||
		got.HLSResume.Segments[0].URI != "http://seg/0" {
		t.Fatalf("caller mutation leaked into cache: %+v", got)
	}
}

func TestMasterCache_ConcurrentReadWrite(t *testing.T) {
	e := newCacheTestEngine(t)

	var writerWG sync.WaitGroup
	stop := make(chan struct{})

	// Writers: upsert and remove entries.
	for w := range 4 {
		writerWG.Add(1)
		go func(w int) {
			defer writerWG.Done()
			for i := range 200 {
				id := "dl-" + string(rune('A'+w)) + "-" + string(rune('0'+i%10))
				e.UpsertMasterCacheEntry(types.DownloadRecord{ID: id, Status: "completed"})
				if i%5 == 0 {
					e.RemoveMasterCacheEntry(id)
				}
			}
		}(w)
	}

	// Readers: buildDownloadList under RLock until writers finish.
	var readerWG sync.WaitGroup
	readerWG.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = e.buildDownloadList()
			}
		}
	})

	writerWG.Wait()
	close(stop)
	readerWG.Wait()
}

func TestMasterCache_CrashRecovery(t *testing.T) {
	testutil.SetupStateDB(t)

	// First instance: seed master.gob via state and load into cache.
	entry := types.DownloadRecord{ID: "dl-recover", URL: "http://x/a", Status: "completed", TotalSize: 100}
	if err := store.AddToMasterList(entry); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	first := NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
	t.Cleanup(first.Close)
	first.masterCache = []types.DownloadRecord{}
	first.RefreshMasterCache()
	if got, ok := first.GetMasterCacheEntry("dl-recover"); !ok || got.URL != "http://x/a" {
		t.Fatalf("first instance cache missing entry: %+v ok=%v", got, ok)
	}

	// Simulate restart: new SurgeEngine loads master list at construction.
	second := NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
	t.Cleanup(second.Close)
	second.masterCache = loadMasterEntriesForTest(t)
	if got, ok := second.GetMasterCacheEntry("dl-recover"); !ok || got.Status != "completed" {
		t.Fatalf("second instance (restart) cache missing entry: %+v ok=%v", got, ok)
	}
}

func TestMasterCache_FileCorruption(t *testing.T) {
	tempDir := testutil.SetupStateDB(t)

	// Seed a valid entry first so the master.gob path exists.
	if err := store.AddToMasterList(types.DownloadRecord{ID: "dl-1", Status: "completed"}); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	// Corrupt the master.gob file (lives at baseDir/master.gob).
	masterPath := filepath.Join(tempDir, "master.gob")
	if err := os.WriteFile(masterPath, []byte("not-valid-gob"), 0o644); err != nil {
		t.Fatalf("write corrupt master.gob: %v", err)
	}

	e := NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
	t.Cleanup(e.Close)
	e.masterCache = []types.DownloadRecord{{ID: "stale"}}
	// RefreshMasterCache logs the error and leaves cache unchanged (best-effort).
	e.RefreshMasterCache()

	// Cache should not have been replaced with nil; stale entry remains.
	if got, ok := e.GetMasterCacheEntry("stale"); !ok {
		t.Fatalf("expected stale entry preserved after corrupt refresh, got %+v", got)
	}
}

func TestMasterCache_GetDownloadListNoGobDecode(t *testing.T) {
	testutil.SetupStateDB(t)
	if err := store.AddToMasterList(types.DownloadRecord{ID: "dl-cached", URL: "http://x/a", Status: "completed"}); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	e := NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
	t.Cleanup(e.Close)
	e.masterCache = []types.DownloadRecord{{ID: "dl-cached", URL: "http://x/a", Status: "completed"}}

	// Multiple calls should return the cached entry without touching gob.
	for i := range 5 {
		list, err := e.getDownloadList()
		if err != nil {
			t.Fatalf("call %d getDownloadList: %v", i, err)
		}
		found := false
		for _, s := range list {
			if s.ID == "dl-cached" {
				found = true
			}
		}
		if !found {
			t.Fatalf("call %d: dl-cached missing from list", i)
		}
	}
}

func TestMasterCache_NonEventWriteConsistency(t *testing.T) {
	testutil.SetupStateDB(t)

	e := NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
	t.Cleanup(e.Close)
	e.masterCache = []types.DownloadRecord{}

	// Simulate a non-event-driven write directly to store (e.g. removeDownloadsByStatus peer).
	if err := store.AddToMasterList(types.DownloadRecord{ID: "dl-nonevt", URL: "http://x/a", Status: "completed"}); err != nil {
		t.Fatalf("AddToMasterList: %v", err)
	}

	// Cache does not yet know about it.
	if _, ok := e.GetMasterCacheEntry("dl-nonevt"); ok {
		t.Fatal("expected entry absent before refresh")
	}

	// Periodic refresh syncs the cache.
	e.RefreshMasterCache()
	if got, ok := e.GetMasterCacheEntry("dl-nonevt"); !ok || got.URL != "http://x/a" {
		t.Fatalf("after refresh: %+v ok=%v", got, ok)
	}
}

func TestMasterCache_MergePatternPreservesFields(t *testing.T) {
	e := newCacheTestEngine(t)

	// Seed a full entry with rich metadata.
	full := types.DownloadRecord{
		ID:           "dl-merge",
		URL:          "http://x/a",
		URLHash:      "hash-a",
		DestPath:     "/out/a",
		Filename:     "a.bin",
		Status:       "paused",
		TotalSize:    1000,
		Downloaded:   500,
		Mirrors:      []string{"http://m1", "http://m2"},
		Workers:      8,
		MinChunkSize: 4 * 1024 * 1024,
	}
	e.UpsertMasterCacheEntry(full)

	// Simulate a DownloadResumedMsg (only ID+Filename) using merge mode.
	existing, ok := e.GetMasterCacheEntry("dl-merge")
	if !ok {
		t.Fatal("expected existing entry")
	}
	merged := existing
	merged.Status = "downloading"
	e.UpsertMasterCacheEntry(merged)

	got, ok := e.GetMasterCacheEntry("dl-merge")
	if !ok {
		t.Fatal("expected merged entry")
	}
	if got.Status != "downloading" {
		t.Errorf("status = %q, want downloading", got.Status)
	}
	if got.URL != "http://x/a" {
		t.Errorf("URL = %q, want preserved http://x/a", got.URL)
	}
	if got.DestPath != "/out/a" {
		t.Errorf("DestPath = %q, want preserved /out/a", got.DestPath)
	}
	if len(got.Mirrors) != 2 {
		t.Errorf("Mirrors len = %d, want preserved 2", len(got.Mirrors))
	}
	if got.Workers != 8 {
		t.Errorf("Workers = %d, want preserved 8", got.Workers)
	}
	if got.MinChunkSize != 4*1024*1024 {
		t.Errorf("MinChunkSize = %d, want preserved", got.MinChunkSize)
	}
}

// loadMasterEntriesForTest mirrors NewSurgeEngine's startup load for tests
// that construct SurgeEngine manually without going through NewSurgeEngine.
func loadMasterEntriesForTest(t *testing.T) []types.DownloadRecord {
	t.Helper()
	list, err := store.LoadMasterList()
	if err != nil {
		return []types.DownloadRecord{}
	}
	return list.Downloads
}

// An old disk read must not clobber event writes that landed between the
// LoadMasterList and the install: upserts survive, removes are not
// resurrected, remove+re-add keeps the new entry. With no interleaved
// mutation a later refresh still picks up fresh disk changes.
func TestMasterCache_RefreshConcurrentMutation(t *testing.T) {
	testutil.SetupStateDB(t)
	e := newCacheTestEngine(t)

	seed := []types.DownloadRecord{
		{ID: "keep", Status: "queued", TotalSize: 1},
		{ID: "victim", Status: "queued", TotalSize: 1},
		{ID: "readd", Status: "queued", TotalSize: 1},
	}
	for _, rec := range seed {
		if err := store.AddToMasterList(rec); err != nil {
			t.Fatal(err)
		}
	}
	e.SetMasterCacheForTesting(seed)

	// The hook fires after the disk read and before install; all mutations
	// bump the mirror revision so the install merges instead of replacing.
	e.SetMasterRefreshHookForTesting(func() {
		e.UpsertMasterCacheEntry(types.DownloadRecord{ID: "keep", Status: "downloading", TotalSize: 7, Downloaded: 3})
		e.RemoveMasterCacheEntry("victim")
		e.RemoveMasterCacheEntry("readd")
		e.UpsertMasterCacheEntry(types.DownloadRecord{ID: "readd", Status: "paused", TotalSize: 9})
		e.UpsertMasterCacheEntry(types.DownloadRecord{ID: "newevent", Status: "queued", TotalSize: 2})
	})
	e.RefreshMasterCache()
	e.SetMasterRefreshHookForTesting(nil)

	if got, ok := e.GetMasterCacheEntry("keep"); !ok || got.Status != "downloading" || got.TotalSize != 7 || got.Downloaded != 3 {
		t.Fatalf("accepted event row clobbered by old disk read: %#v", got)
	}
	if _, ok := e.GetMasterCacheEntry("victim"); ok {
		t.Fatal("old disk read resurrected a mid-read remove")
	}
	if got, ok := e.GetMasterCacheEntry("readd"); !ok || got.Status != "paused" || got.TotalSize != 9 {
		t.Fatalf("remove+re-add must keep the new entry: %#v", got)
	}
	if got, ok := e.GetMasterCacheEntry("newevent"); !ok || got.TotalSize != 2 {
		t.Fatalf("mid-read upsert lost by refresh install: %#v", got)
	}

	// Uninterleaved refresh still reloads disk state.
	if err := store.AddToMasterList(types.DownloadRecord{ID: "later", Status: "completed", TotalSize: 5}); err != nil {
		t.Fatal(err)
	}
	e.RefreshMasterCache()
	if got, ok := e.GetMasterCacheEntry("later"); !ok || got.Status != "completed" {
		t.Fatalf("fresh disk change not refreshed: %#v", got)
	}
}
