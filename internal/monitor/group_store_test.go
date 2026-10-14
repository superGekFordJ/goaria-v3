package monitor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func setupTaskGroupStoreTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "download_groups.json")
	ResetTaskGroupStoreForTest(path, true)
	t.Cleanup(func() {
		ResetTaskGroupStoreForTest("", true)
	})
	return path
}

func TestTaskGroupStore_PersistsLoadsAndHydratesTasks(t *testing.T) {
	path := setupTaskGroupStoreTest(t)
	group := testDownloadGroup("dg-store")

	RegisterTaskGroup("gid-store", group)
	if err := FlushGroupStoreForTest(); err != nil {
		t.Fatalf("flush group store: %v", err)
	}
	ResetTaskGroupStoreForTest(path, true)
	LoadTaskGroups()

	loaded := GetStoredTaskGroup("gid-store")
	if loaded == nil || loaded.ID != group.ID {
		t.Fatalf("expected stored group after reload, got %#v", loaded)
	}
	tasks := []rpc.Task{{GID: "gid-store", Status: "active"}}
	HydrateTaskGroups(tasks)
	if tasks[0].DownloadGroup == nil || tasks[0].DownloadGroup.ID != group.ID {
		t.Fatalf("expected hydrated task group, got %#v", tasks[0].DownloadGroup)
	}
}

func TestTaskGroupStore_RemovesAndClearsGroups(t *testing.T) {
	setupTaskGroupStoreTest(t)
	RegisterTaskGroup("gid-one", testDownloadGroup("dg-one"))
	RegisterTaskGroup("gid-two", testDownloadGroup("dg-two"))

	RemoveTaskGroup("gid-one")
	if got := GetStoredTaskGroup("gid-one"); got != nil {
		t.Fatalf("expected gid-one removed, got %#v", got)
	}
	if got := GetStoredTaskGroup("gid-two"); got == nil {
		t.Fatal("expected gid-two to remain")
	}

	ClearTaskGroups()
	if got := GetStoredTaskGroup("gid-two"); got != nil {
		t.Fatalf("expected all groups cleared, got %#v", got)
	}
}

func TestTaskGroupStore_ListStoredTaskGroupsReturnsDefensiveCopy(t *testing.T) {
	setupTaskGroupStoreTest(t)
	group := testDownloadGroup("dg-list")
	RegisterTaskGroup("gid-list", group)

	snapshot := ListStoredTaskGroups()
	if got := snapshot["gid-list"]; got.ID != group.ID {
		t.Fatalf("expected stored group copy, got %#v", got)
	}

	mutated := snapshot["gid-list"]
	mutated.ID = "dg-mutated"
	snapshot["gid-list"] = mutated
	snapshot["gid-extra"] = testDownloadGroup("dg-extra")
	delete(snapshot, "gid-list")

	stored := GetStoredTaskGroup("gid-list")
	if stored == nil || stored.ID != group.ID {
		t.Fatalf("expected store to retain original group, got %#v", stored)
	}
	if extra := GetStoredTaskGroup("gid-extra"); extra != nil {
		t.Fatalf("expected snapshot map mutation not to add store entry, got %#v", extra)
	}

	second := ListStoredTaskGroups()
	if got := second["gid-list"]; got.ID != group.ID {
		t.Fatalf("expected later snapshot to retain original group, got %#v", got)
	}
}

// newIsolatedGroupStore returns a detached store pointed at a temp file with
// short coalescing windows. Cleanup joins the saver without flushing.
func newIsolatedGroupStore(t *testing.T) (*taskGroupStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "download_groups.json")
	s := newTaskGroupStore()
	s.saveDebounce = 20 * time.Millisecond
	s.saveMaxBurst = 200 * time.Millisecond
	s.path = path
	t.Cleanup(func() { s.stopSaver() })
	return s, path
}

func (s *taskGroupStore) putForTest(gid string, group rpc.DownloadGroup) {
	s.mu.Lock()
	s.groups[gid] = group
	s.triggerSaveLocked()
	s.mu.Unlock()
}

func (s *taskGroupStore) removeForTest(gid string) {
	s.mu.Lock()
	delete(s.groups, gid)
	s.triggerSaveLocked()
	s.mu.Unlock()
}

func (s *taskGroupStore) clearForTest() {
	s.mu.Lock()
	s.groups = make(map[string]rpc.DownloadGroup)
	s.triggerSaveLocked()
	s.mu.Unlock()
}

func readGroupStoreFile(t *testing.T, path string) map[string]rpc.DownloadGroup {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read group store file: %v", err)
	}
	var groups map[string]rpc.DownloadGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		t.Fatalf("group store file is not valid JSON: %v", err)
	}
	return groups
}

func waitGroupStoreWg(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestTaskGroupStore_DebounceConcurrentMutations(t *testing.T) {
	s, path := newIsolatedGroupStore(t)
	s.saveDebounce = 50 * time.Millisecond
	s.saveMaxBurst = 5 * time.Second

	// Hold writeMu across the whole burst: a worker that finishes coalescing
	// early still cannot snapshot, while producers only take s.mu and must
	// not wait on writeMu.
	s.writeMu.Lock()

	const producers = 8
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	for i := range producers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			gid := fmt.Sprintf("gid-debounce-%d", i)
			s.putForTest(gid, testDownloadGroup(fmt.Sprintf("dg-debounce-%d", i)))
		}(i)
	}
	close(barrier)

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(15 * time.Second):
		t.Fatal("mutations blocked behind writeMu")
	}
	s.writeMu.Unlock()

	if err := s.performSave(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	s.stopSaver()

	if writes := s.saveWriteCount.Load(); writes != 1 {
		t.Fatalf("expected the controlled burst to coalesce into 1 write, got %d", writes)
	}
	if triggers := s.saveTriggerCount.Load(); triggers != producers {
		t.Fatalf("expected %d triggers, got %d", producers, triggers)
	}

	stored := readGroupStoreFile(t, path)
	if len(stored) != producers {
		t.Fatalf("expected %d persisted groups, got %d", producers, len(stored))
	}
	for i := range producers {
		gid := fmt.Sprintf("gid-debounce-%d", i)
		want := fmt.Sprintf("dg-debounce-%d", i)
		if stored[gid].ID != want {
			t.Fatalf("stored[%s].ID = %q, want %q", gid, stored[gid].ID, want)
		}
	}
}

func TestTaskGroupStore_MaxBurstDoesNotStarve(t *testing.T) {
	s, path := newIsolatedGroupStore(t)
	s.saveDebounce = 100 * time.Millisecond
	s.saveMaxBurst = 150 * time.Millisecond

	// Mutations arrive faster than the idle debounce, so only the max-burst
	// timer can end coalescing; a save must land while the stream still runs.
	before := s.saveWriteCount.Load()
	stopStream := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stopStream:
				return
			default:
			}
			s.putForTest(fmt.Sprintf("gid-burst-%d", i), testDownloadGroup(fmt.Sprintf("dg-burst-%d", i)))
			time.Sleep(10 * time.Millisecond)
		}
	})

	requireCondition(t, func() bool {
		return s.saveWriteCount.Load() > before
	}, 5*time.Second, "expected a save during the continuous mutation stream")
	close(stopStream)
	waitGroupStoreWg(t, &wg, "mutation stream")

	s.putForTest("gid-tail", testDownloadGroup("dg-tail"))
	if err := s.performSave(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	stored := readGroupStoreFile(t, path)
	if stored["gid-tail"].ID != "dg-tail" {
		t.Fatalf("expected tail mutation persisted, got %#v", stored["gid-tail"])
	}
}

func TestTaskGroupStore_DisabledIsSilent(t *testing.T) {
	t.Run("mutations update memory only", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "download_groups.json")
		s := newTaskGroupStore()
		s.enabled = false
		s.path = path
		s.saveDebounce = 10 * time.Millisecond
		s.saveMaxBurst = 50 * time.Millisecond
		t.Cleanup(func() { s.stopSaver() })

		s.putForTest("gid-a", testDownloadGroup("dg-a"))
		s.putForTest("gid-b", testDownloadGroup("dg-b"))
		s.removeForTest("gid-a")
		time.Sleep(5 * s.saveMaxBurst)
		if err := s.performSave(); err != nil {
			t.Fatalf("disabled flush must be nil, got %v", err)
		}
		s.stopSaver()
		if err := s.performSave(); err != nil {
			t.Fatalf("disabled stop must be nil, got %v", err)
		}
		if s.saveWriteCount.Load() != 0 {
			t.Fatalf("disabled store wrote %d times", s.saveWriteCount.Load())
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Fatalf("disabled store touched dir: entries=%v err=%v", entries, err)
		}
	})

	t.Run("disable does not replay pending", func(t *testing.T) {
		s, path := newIsolatedGroupStore(t)

		// Queue a pending save while enabled, then disable before it can run.
		s.writeMu.Lock()
		s.mu.Lock()
		s.groups["gid-pre"] = testDownloadGroup("dg-pre")
		s.triggerSaveLocked()
		s.enabled = false
		s.discardPendingSaveLocked()
		s.mu.Unlock()
		s.writeMu.Unlock()
		time.Sleep(5 * s.saveMaxBurst)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("disabled store persisted queued signal: err=%v", err)
		}

		// Re-enable: the pre-disable pending must not replay; only a new
		// mutation triggers a save of the latest full map.
		s.mu.Lock()
		s.enabled = true
		s.mu.Unlock()
		time.Sleep(5 * s.saveMaxBurst)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("re-enable replayed stale pending: err=%v", err)
		}

		s.putForTest("gid-post", testDownloadGroup("dg-post"))
		if err := s.performSave(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		stored := readGroupStoreFile(t, path)
		if len(stored) != 2 || stored["gid-pre"].ID != "dg-pre" || stored["gid-post"].ID != "dg-post" {
			t.Fatalf("expected latest full map after re-enable, got %#v", stored)
		}
	})

	t.Run("empty collection does not delete existing file", func(t *testing.T) {
		s, path := newIsolatedGroupStore(t)
		if err := os.WriteFile(path, []byte(`{"gid-x":{"id":"dg-x"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.enabled = false
		s.mu.Unlock()
		s.clearForTest()
		if err := s.performSave(); err != nil {
			t.Fatalf("disabled flush must be nil, got %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("disabled store deleted existing file: %v", err)
		}
	})
}

func TestTaskGroupStore_FlushStopAndReset(t *testing.T) {
	t.Run("flush returns latest state synchronously", func(t *testing.T) {
		s, path := newIsolatedGroupStore(t)
		s.putForTest("gid-flush", testDownloadGroup("dg-flush"))
		if err := s.performSave(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		stored := readGroupStoreFile(t, path)
		if stored["gid-flush"].ID != "dg-flush" {
			t.Fatalf("flush did not persist latest value, got %#v", stored)
		}
	})

	t.Run("stop without worker start", func(t *testing.T) {
		s, _ := newIsolatedGroupStore(t)
		done := make(chan error, 1)
		go func() {
			s.stopSaver()
			done <- s.performSave()
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("stop: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("stop without worker never returned")
		}
		s.stopSaver()
		if err := s.performSave(); err != nil {
			t.Fatalf("repeat stop: %v", err)
		}
	})

	t.Run("stop during coalescing flushes pending", func(t *testing.T) {
		s, path := newIsolatedGroupStore(t)
		s.saveDebounce = time.Hour // force mid-coalesce stop
		s.putForTest("gid-stop", testDownloadGroup("dg-stop"))
		s.stopSaver()
		if err := s.performSave(); err != nil {
			t.Fatalf("stop: %v", err)
		}
		stored := readGroupStoreFile(t, path)
		if stored["gid-stop"].ID != "dg-stop" {
			t.Fatalf("stop did not flush pending, got %#v", stored)
		}
	})

	t.Run("reset discards pending and isolates paths", func(t *testing.T) {
		s, pathA := newIsolatedGroupStore(t)
		s.saveDebounce = time.Hour
		s.putForTest("gid-old", testDownloadGroup("dg-old"))
		oldDone := s.doneChan

		pathB := filepath.Join(t.TempDir(), "other.json")
		s.resetForTest(pathB, true)
		select {
		case <-oldDone:
		default:
			t.Fatal("reset did not close the previous worker done channel")
		}
		if _, err := os.Stat(pathA); !os.IsNotExist(err) {
			t.Fatalf("reset flushed pending fixture to old path: err=%v", err)
		}

		s.putForTest("gid-new", testDownloadGroup("dg-new"))
		if err := s.performSave(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		storedB := readGroupStoreFile(t, pathB)
		if len(storedB) != 1 || storedB["gid-new"].ID != "dg-new" {
			t.Fatalf("new path must contain only new content, got %#v", storedB)
		}
		if _, err := os.Stat(pathA); !os.IsNotExist(err) {
			t.Fatalf("old path written after reset: err=%v", err)
		}
	})

	t.Run("empty path reset stays silent", func(t *testing.T) {
		s, _ := newIsolatedGroupStore(t)
		s.putForTest("gid-x", testDownloadGroup("dg-x"))
		if err := s.performSave(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		s.resetForTest("", true)
		s.putForTest("gid-y", testDownloadGroup("dg-y"))
		time.Sleep(5 * s.saveMaxBurst)
		if err := s.performSave(); err != nil {
			t.Fatalf("disabled flush must be nil, got %v", err)
		}
		if s.saveWriteCount.Load() != 0 {
			t.Fatalf("empty-path reset still wrote %d times", s.saveWriteCount.Load())
		}
	})

	t.Run("load is not overwritten by stale pending", func(t *testing.T) {
		s, path := newIsolatedGroupStore(t)
		s.saveDebounce = time.Hour
		s.putForTest("gid-stale", testDownloadGroup("dg-stale"))

		// Simulate an external file arriving before the pending save runs.
		if err := os.WriteFile(path, []byte(`{"gid-disk":{"id":"dg-disk","kind":"batch"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		s.loadFromDisk()

		time.Sleep(50 * time.Millisecond)
		stored := readGroupStoreFile(t, path)
		if _, ok := stored["gid-stale"]; ok {
			t.Fatalf("stale pending rewrote loaded content: %#v", stored)
		}
		if stored["gid-disk"].ID != "dg-disk" {
			t.Fatalf("expected loaded content preserved, got %#v", stored)
		}
	})
}

func TestTaskGroupStore_ConcurrentFlushNoRegression(t *testing.T) {
	s, path := newIsolatedGroupStore(t)
	s.saveDebounce = 30 * time.Millisecond
	s.saveMaxBurst = 100 * time.Millisecond

	const producers = 6
	barrier := make(chan struct{})
	var wg sync.WaitGroup
	for i := range producers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			for j := range 5 {
				s.putForTest(fmt.Sprintf("gid-flush-%d-%d", i, j), testDownloadGroup(fmt.Sprintf("dg-%d-%d", i, j)))
			}
		}(i)
	}
	close(barrier)

	// Concurrent Flush calls race the worker; every one must either write the
	// newest snapshot or observe a clean store — never an older one.
	for range 3 {
		if err := s.performSave(); err != nil {
			t.Fatalf("flush during mutations: %v", err)
		}
	}
	waitGroupStoreWg(t, &wg, "producers")
	if err := s.performSave(); err != nil {
		t.Fatalf("final flush: %v", err)
	}
	s.stopSaver()

	stored := readGroupStoreFile(t, path)
	if len(stored) != producers*5 {
		t.Fatalf("expected %d persisted groups, got %d", producers*5, len(stored))
	}

	// A failed write must not advance the saved counter; fixing the target
	// lets the next Flush retry successfully.
	badDir := t.TempDir()
	badPath := filepath.Join(badDir, "as_dir")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.path = badPath
	s.mu.Unlock()

	s.putForTest("gid-retry", testDownloadGroup("dg-retry"))
	savedBefore := s.saveWriteCount.Load()
	if err := s.performSave(); err == nil {
		t.Fatal("expected write failure against directory target")
	}
	if s.saveWriteCount.Load() != savedBefore {
		t.Fatal("failed write advanced the saved counter")
	}
	if s.lastSavedTrigger.Load() >= s.saveTriggerCount.Load() {
		t.Fatal("failed write cleared dirty state")
	}

	goodPath := filepath.Join(t.TempDir(), "download_groups.json")
	s.mu.Lock()
	s.path = goodPath
	s.mu.Unlock()
	if err := s.performSave(); err != nil {
		t.Fatalf("retry after fixing target: %v", err)
	}
	if s.saveWriteCount.Load() != savedBefore+1 {
		t.Fatalf("expected saved counter +1 after retry, got %d", s.saveWriteCount.Load())
	}
	if stored := readGroupStoreFile(t, goodPath); stored["gid-retry"].ID != "dg-retry" {
		t.Fatalf("retry did not persist latest map, got %#v", stored["gid-retry"])
	}
}

func TestTaskGroupStore_AtomicWriteAndEmptyRemoval(t *testing.T) {
	s, path := newIsolatedGroupStore(t)

	s.putForTest("gid-atomic", testDownloadGroup("dg-atomic"))
	if err := s.performSave(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected store file: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".download-groups-") {
			t.Fatalf("atomic write left temp file %s", entry.Name())
		}
	}

	// A failed rename target (an existing directory) cleans the temp file and
	// leaves the previous valid target untouched.
	dirTarget := filepath.Join(t.TempDir(), "target_dir")
	if err := os.Mkdir(dirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeDownloadGroupStoreFile(dirTarget, []byte(`{}`)); err == nil {
		t.Fatal("expected write against a directory target to fail")
	}
	entries, err = os.ReadDir(filepath.Dir(dirTarget))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".download-groups-") {
			t.Fatalf("failed write left temp file %s", entry.Name())
		}
	}
	if stored := readGroupStoreFile(t, path); stored["gid-atomic"].ID != "dg-atomic" {
		t.Fatalf("failed write clobbered previous target, got %#v", stored)
	}

	// Remove / bulk-remove / clear → empty collection deletes the file.
	s.putForTest("gid-one", testDownloadGroup("dg-one"))
	s.putForTest("gid-two", testDownloadGroup("dg-two"))
	s.removeForTest("gid-one")
	s.mu.Lock()
	delete(s.groups, "gid-two")
	s.triggerSaveLocked()
	s.mu.Unlock()
	s.putForTest("gid-three", testDownloadGroup("dg-three"))
	s.clearForTest()
	if err := s.performSave(); err != nil {
		t.Fatalf("flush after clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty collection did not remove store file: err=%v", err)
	}
}
