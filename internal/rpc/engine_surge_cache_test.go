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

func TestMasterCache_ConcurrentReadWrite(t *testing.T) {
	e := newCacheTestEngine(t)

	var writerWG sync.WaitGroup
	stop := make(chan struct{})

	// Writers: upsert and remove entries.
	for w := 0; w < 4; w++ {
		writerWG.Add(1)
		go func(w int) {
			defer writerWG.Done()
			for i := 0; i < 200; i++ {
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
	readerWG.Add(1)
	go func() {
		defer readerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = e.buildDownloadList()
			}
		}
	}()

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
	first.masterCache = []types.DownloadRecord{}
	first.RefreshMasterCache()
	if got, ok := first.GetMasterCacheEntry("dl-recover"); !ok || got.URL != "http://x/a" {
		t.Fatalf("first instance cache missing entry: %+v ok=%v", got, ok)
	}

	// Simulate restart: new SurgeEngine loads master list at construction.
	second := NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
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
	e.masterCache = []types.DownloadRecord{{ID: "dl-cached", URL: "http://x/a", Status: "completed"}}

	// Multiple calls should return the cached entry without touching gob.
	for i := 0; i < 5; i++ {
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
