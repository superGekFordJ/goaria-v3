package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func setupTest(t *testing.T) string {
	tmpDir := t.TempDir()
	historyFile := filepath.Join(tmpDir, "history.json")
	SetHistoryPath(historyFile)
	SetDebounceDuration(10 * time.Millisecond)
	SetMaxBurstDuration(50 * time.Millisecond)
	DisableSaveForTest()
	Clear()
	return historyFile
}

func TestRaceCondition(t *testing.T) {
	historyFile := setupTest(t)
	// Enable save for this test
	SetSaveEnabled(true)
	defer DisableSaveForTest()

	gid := "test-gid"
	entry := HistoryEntry{
		GID:   gid,
		Title: "Test Download",
	}

	iterations := 50
	failures := 0

	for i := range iterations {
		Clear()
		Add(entry)
		Remove(gid)

		time.Sleep(30 * time.Millisecond)

		content, err := os.ReadFile(historyFile)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("Iteration %d: Failed to read history file: %v", i, err)
		}

		var loadedEntries []HistoryEntry
		if len(content) > 0 {
			if err := json.Unmarshal(content, &loadedEntries); err != nil {
				t.Fatalf("Iteration %d: Failed to unmarshal history: %v", i, err)
			}
		}

		if len(loadedEntries) != 0 {
			failures++
		}
	}

	if failures > 0 {
		t.Fatalf("Race condition reproduced: %d/%d iterations failed", failures, iterations)
	}
}

func TestPerformance(t *testing.T) {
	setupTest(t)

	start := time.Now()
	n := 1000
	for i := range n {
		Add(HistoryEntry{GID: strconv.Itoa(i)})
	}

	duration := time.Since(start)
	t.Logf("Time to add %d entries: %v", n, duration)
}

func TestAddAndGetAll(t *testing.T) {
	setupTest(t)

	e1 := HistoryEntry{GID: "1", Title: "Task 1"}
	Add(e1)

	all := GetAll()
	if len(all) != 1 {
		t.Errorf("Expected 1 entry, got %d", len(all))
	}
	if all[0].GID != "1" {
		t.Errorf("Expected GID 1, got %s", all[0].GID)
	}

	e1Update := HistoryEntry{GID: "1", Title: "Task 1 Updated"}
	Add(e1Update)

	all = GetAll()
	if len(all) != 1 {
		t.Errorf("Expected 1 entry, got %d", len(all))
	}
	if all[0].Title != "Task 1 Updated" {
		t.Errorf("Expected title 'Task 1 Updated', got '%s'", all[0].Title)
	}

	Add(HistoryEntry{GID: "2", Title: "Task 2"})
	all = GetAll()
	if len(all) != 2 {
		t.Errorf("Expected 2 entries, got %d", len(all))
	}
}

func TestGetMissingByGID(t *testing.T) {
	setupTest(t)

	Add(HistoryEntry{GID: "gid-1", Title: "Task 1"})
	Add(HistoryEntry{GID: "gid-2", Title: "Task 2"})
	Add(HistoryEntry{GID: "gid-3", Title: "Task 3"})

	missing := GetMissingByGID(map[string]struct{}{"gid-2": {}})
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing entries, got %d", len(missing))
	}
	if missing[0].GID != "gid-1" || missing[1].GID != "gid-3" {
		t.Fatalf("expected storage order gid-1,gid-3, got %#v", missing)
	}
}

func TestGetMissingByGID_EmptyExistingReturnsAll(t *testing.T) {
	setupTest(t)

	Add(HistoryEntry{GID: "gid-1", Title: "Task 1"})
	Add(HistoryEntry{GID: "gid-2", Title: "Task 2"})

	for _, existing := range []map[string]struct{}{nil, {}} {
		missing := GetMissingByGID(existing)
		if len(missing) != 2 {
			t.Fatalf("expected all entries for empty existing map, got %d", len(missing))
		}
		if missing[0].GID != "gid-1" || missing[1].GID != "gid-2" {
			t.Fatalf("expected all entries in storage order, got %#v", missing)
		}
	}
}

func TestGetMissingByGID_ReturnsCopies(t *testing.T) {
	setupTest(t)

	Add(HistoryEntry{GID: "gid-1", Title: "Task 1"})
	Add(HistoryEntry{GID: "gid-2", Title: "Task 2"})

	missing := GetMissingByGID(nil)
	if len(missing) != 2 {
		t.Fatalf("expected 2 missing entries, got %d", len(missing))
	}

	missing[0] = HistoryEntry{GID: "mutated", Title: "Mutated"}
	missing[1].Title = "Mutated Task 2"

	entry, ok := Get("gid-1")
	if !ok {
		t.Fatalf("expected gid-1 to remain in history")
	}
	if entry.Title != "Task 1" {
		t.Fatalf("expected gid-1 title to remain unchanged, got %q", entry.Title)
	}

	all := GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 stored entries, got %d", len(all))
	}
	if all[1].Title != "Task 2" {
		t.Fatalf("expected gid-2 title to remain unchanged, got %q", all[1].Title)
	}
}

func TestRemove(t *testing.T) {
	setupTest(t)

	Add(HistoryEntry{GID: "1"})
	Add(HistoryEntry{GID: "2"})
	Add(HistoryEntry{GID: "3"})

	Remove("2")

	all := GetAll()
	if len(all) != 2 {
		t.Errorf("Expected 2 entries, got %d", len(all))
	}
	if all[0].GID != "1" || all[1].GID != "3" {
		t.Errorf("Unexpected entries after remove: %v", all)
	}

	Remove("999")
	if len(GetAll()) != 2 {
		t.Errorf("Count changed after removing non-existent")
	}
}

func TestRemoveKeepingGroupStore_SkipsGroupHook(t *testing.T) {
	setupTest(t)

	hooked := false
	SetGroupCleanupHooks(func(string) { hooked = true }, nil, nil)
	t.Cleanup(func() { SetGroupCleanupHooks(nil, nil, nil) })

	Add(HistoryEntry{GID: "keep-group", Path: "/tmp/a.bin"})
	RemoveKeepingGroupStore("keep-group")

	if _, ok := Get("keep-group"); ok {
		t.Fatal("expected entry removed")
	}
	if hooked {
		t.Fatal("expected RemoveKeepingGroupStore to skip group cleanup hook")
	}
}

func TestRemoveMany(t *testing.T) {
	setupTest(t)

	Add(HistoryEntry{GID: "gid-1", Source: "source-a"})
	Add(HistoryEntry{GID: "gid-2", Source: "source-shared"})
	Add(HistoryEntry{GID: "gid-3", Source: "source-b"})
	Add(HistoryEntry{GID: "gid-4", Source: "source-shared"})
	Add(HistoryEntry{GID: "gid-5", Source: "source-c"})

	RemoveMany([]string{"gid-1", "gid-3", "gid-5"})

	all := GetAll()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries after RemoveMany, got %d", len(all))
	}
	if all[0].GID != "gid-2" || all[1].GID != "gid-4" {
		t.Fatalf("expected survivors to preserve order gid-2,gid-4, got %#v", all)
	}
	if gidIndex["gid-2"] != 0 || gidIndex["gid-4"] != 1 {
		t.Fatalf("unexpected gidIndex after RemoveMany: %#v", gidIndex)
	}
	if _, ok := gidIndex["gid-1"]; ok {
		t.Fatalf("expected gid-1 to be removed from gidIndex")
	}
	if !ContainsSource("source-shared") {
		t.Fatalf("expected shared source to remain present")
	}
	if ContainsSource("source-a") || ContainsSource("source-b") || ContainsSource("source-c") {
		t.Fatalf("expected removed sources to be absent, got %#v", sourceIndex)
	}
}

func TestRemoveMany_IdempotentDuplicateEmptyMissing(t *testing.T) {
	setupTest(t)

	Add(HistoryEntry{GID: "gid-1", Source: "source-a"})
	Add(HistoryEntry{GID: "gid-2", Source: "source-a"})
	Add(HistoryEntry{GID: "gid-3", Source: "source-b"})

	RemoveMany([]string{"", "gid-missing", "gid-1", "gid-1", "gid-3", "gid-3"})

	all := GetAll()
	if len(all) != 1 || all[0].GID != "gid-2" {
		t.Fatalf("expected only gid-2 to remain, got %#v", all)
	}
	if got := sourceIndex["source-a"]; got != 1 {
		t.Fatalf("expected source-a count 1 after removing one shared-source entry, got %d", got)
	}
	if _, ok := sourceIndex["source-b"]; ok {
		t.Fatalf("expected source-b to be removed from sourceIndex")
	}

	RemoveMany([]string{"gid-1", "gid-3", "gid-missing", ""})
	all = GetAll()
	if len(all) != 1 || all[0].GID != "gid-2" {
		t.Fatalf("expected no mutation after idempotent second remove, got %#v", all)
	}
	if got := sourceIndex["source-a"]; got != 1 {
		t.Fatalf("expected source-a count to remain 1, got %d", got)
	}
}

func TestRemoveMany_NoOpDoesNotMutateIndexesOrSave(t *testing.T) {
	setupTest(t)

	Add(HistoryEntry{GID: "gid-1", Source: "source-a"})
	Add(HistoryEntry{GID: "gid-2", Source: "source-b"})
	before := GetAll()
	beforeGIDIndex := map[string]int{"gid-1": gidIndex["gid-1"], "gid-2": gidIndex["gid-2"]}
	beforeSourceIndex := map[string]int{"source-a": sourceIndex["source-a"], "source-b": sourceIndex["source-b"]}

	beforeSaveTriggers := saveTriggerCount.Load()

	RemoveMany(nil)
	RemoveMany([]string{"", "gid-missing"})

	if got := saveTriggerCount.Load() - beforeSaveTriggers; got != 0 {
		t.Fatalf("expected no save triggers for no-op RemoveMany calls, got %d", got)
	}
	after := GetAll()
	if len(after) != len(before) || after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("expected entries to remain unchanged, got %#v", after)
	}
	for gid, want := range beforeGIDIndex {
		if got := gidIndex[gid]; got != want {
			t.Fatalf("expected gidIndex[%q]=%d, got %d", gid, want, got)
		}
	}
	for source, want := range beforeSourceIndex {
		if got := sourceIndex[source]; got != want {
			t.Fatalf("expected sourceIndex[%q]=%d, got %d", source, want, got)
		}
	}
}

func TestRemoveMany_TriggersSaveOnceForActualDeletion(t *testing.T) {
	setupTest(t)

	Add(HistoryEntry{GID: "gid-1"})
	Add(HistoryEntry{GID: "gid-2"})
	Add(HistoryEntry{GID: "gid-3"})

	beforeSaveTriggers := saveTriggerCount.Load()

	RemoveMany([]string{"gid-1", "gid-3", "gid-3", "gid-missing"})

	if got := saveTriggerCount.Load() - beforeSaveTriggers; got != 1 {
		t.Fatalf("expected one save trigger for actual RemoveMany deletion, got %d", got)
	}
	all := GetAll()
	if len(all) != 1 || all[0].GID != "gid-2" {
		t.Fatalf("expected only gid-2 to remain, got %#v", all)
	}
}

func TestLoad(t *testing.T) {
	historyFile := setupTest(t)
	SetSaveEnabled(true)
	defer DisableSaveForTest()

	Clear()
	entry := HistoryEntry{GID: "1", Title: "Persisted", Source: "https://example.com/file.iso"}
	Add(entry)

	// Wait for file to be created (debounced save)
	time.Sleep(50 * time.Millisecond)

	if _, err := os.Stat(historyFile); os.IsNotExist(err) {
		t.Fatalf("History file not created at %s", historyFile)
	}

	bytes, _ := os.ReadFile(historyFile)
	var fromDisk []HistoryEntry
	if err := json.Unmarshal(bytes, &fromDisk); err != nil {
		t.Fatalf("Failed to unmarshal history from disk: %v", err)
	}
	if len(fromDisk) != 1 || fromDisk[0].GID != "1" {
		t.Errorf("File content incorrect: %v", fromDisk)
	}

	// Reset memory state
	mu.Lock()
	entries = []HistoryEntry{}
	gidIndex = make(map[string]int)
	sourceIndex = make(map[string]int)
	mu.Unlock()

	Load()
	all := GetAll()
	if len(all) != 1 || all[0].GID != "1" {
		t.Errorf("Load failed to restore entries. Got %v", all)
	}
	if !ContainsSource(entry.Source) {
		t.Errorf("Load failed to rebuild source index for %q", entry.Source)
	}
}

func TestContainsSource(t *testing.T) {
	setupTest(t)

	source1 := "http://example.com/1"
	source2 := "http://example.com/2"

	Add(HistoryEntry{GID: "1", Source: source1})
	Add(HistoryEntry{GID: "2", Source: source1}) // Same source, different GID
	Add(HistoryEntry{GID: "3", Source: source2})

	if !ContainsSource(source1) {
		t.Errorf("Expected to contain source1")
	}
	if !ContainsSource(source2) {
		t.Errorf("Expected to contain source2")
	}
	if ContainsSource("http://non-existent") {
		t.Errorf("Did not expect to contain non-existent source")
	}

	// Remove one entry with source1
	Remove("1")
	if !ContainsSource(source1) {
		t.Errorf("Expected to still contain source1 after one removal")
	}

	// Remove other entry with source1
	Remove("2")
	if ContainsSource(source1) {
		t.Errorf("Expected to not contain source1 after all removals")
	}

	// Test update
	Add(HistoryEntry{GID: "3", Source: source1}) // Change GID 3 from source2 to source1
	if ContainsSource(source2) {
		t.Errorf("Expected to not contain source2 after update")
	}
	if !ContainsSource(source1) {
		t.Errorf("Expected to contain source1 after update")
	}

	Clear()
	if ContainsSource(source1) {
		t.Errorf("Expected to not contain source1 after Clear")
	}
}

func TestContainsSources(t *testing.T) {
	setupTest(t)

	sharedSource := "https://example.com/shared.iso"
	otherSource := "https://example.com/other.iso"
	missingSource := "https://example.com/missing.iso"

	Add(HistoryEntry{GID: "gid-shared-1", Source: sharedSource})
	Add(HistoryEntry{GID: "gid-shared-2", Source: sharedSource})
	Add(HistoryEntry{GID: "gid-other", Source: otherSource})
	Add(HistoryEntry{GID: "gid-empty", Source: ""})

	got := ContainsSources([]string{sharedSource, otherSource, sharedSource, "", missingSource})
	if len(got) != 2 {
		t.Fatalf("expected two present sources, got %#v", got)
	}
	if !got[sharedSource] {
		t.Fatalf("expected shared source to be present")
	}
	if !got[otherSource] {
		t.Fatalf("expected other source to be present")
	}
	if got[""] {
		t.Fatalf("expected empty source to be absent")
	}
	if got[missingSource] {
		t.Fatalf("expected missing source to be absent")
	}

	Remove("gid-shared-1")
	got = ContainsSources([]string{sharedSource, otherSource})
	if !got[sharedSource] {
		t.Fatalf("expected shared source to remain present after one removal")
	}
	if !got[otherSource] {
		t.Fatalf("expected other source to remain present")
	}

	Remove("gid-shared-2")
	got = ContainsSources([]string{sharedSource, otherSource})
	if got[sharedSource] {
		t.Fatalf("expected shared source to be absent after all removals")
	}
	if !got[otherSource] {
		t.Fatalf("expected other source to remain present after shared removals")
	}

	Clear()
	got = ContainsSources([]string{sharedSource, otherSource})
	if len(got) != 0 {
		t.Fatalf("expected no sources after Clear, got %#v", got)
	}
	if got := ContainsSources(nil); len(got) != 0 {
		t.Fatalf("expected nil input to return empty map, got %#v", got)
	}
}

func historyTestDownloadGroup(id string) *rpc.DownloadGroup {
	return &rpc.DownloadGroup{
		ID:         id,
		Kind:       "collection",
		Name:       "Collection 2026-05-07 15-04-05",
		FolderName: "Collection 2026-05-07 15-04-05 " + id,
		Dir:        filepath.Join("downloads", id),
		ItemCount:  2,
		CreatedAt:  1778166245,
	}
}

func TestHistoryDownloadGroupAddLoadUpdateAndRemoveMany(t *testing.T) {
	historyFile := setupTest(t)
	SetSaveEnabled(true)
	defer DisableSaveForTest()

	group := historyTestDownloadGroup("dg-history")
	Add(HistoryEntry{GID: "gid-group", Title: "Grouped", Source: "https://example.com/group.bin", DownloadGroup: group})
	Add(HistoryEntry{GID: "gid-other", Title: "Other", Source: "https://example.com/other.bin"})

	if entry, ok := Get("gid-group"); !ok || entry.DownloadGroup == nil || entry.DownloadGroup.ID != group.ID {
		t.Fatalf("expected group after Add, got %#v ok=%v", entry, ok)
	}

	Add(HistoryEntry{GID: "gid-group", Title: "Grouped Updated", Source: "https://example.com/group-updated.bin"})
	entry, ok := Get("gid-group")
	if !ok || entry.DownloadGroup == nil || entry.DownloadGroup.ID != group.ID {
		t.Fatalf("expected update without group to preserve existing group, got %#v ok=%v", entry, ok)
	}
	if ContainsSource("https://example.com/group.bin") || !ContainsSource("https://example.com/group-updated.bin") {
		t.Fatalf("sourceIndex not updated correctly after grouped update: %#v", sourceIndex)
	}

	if err := Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	mu.Lock()
	entries = nil
	gidIndex = nil
	sourceIndex = nil
	mu.Unlock()
	Load()
	entry, ok = Get("gid-group")
	if !ok || entry.DownloadGroup == nil || entry.DownloadGroup.ID != group.ID {
		t.Fatalf("expected group after Load, got %#v ok=%v", entry, ok)
	}

	RemoveMany([]string{"gid-group"})
	if _, ok := Get("gid-group"); ok {
		t.Fatal("expected grouped entry removed")
	}
	if ContainsSource("https://example.com/group-updated.bin") {
		t.Fatal("expected grouped source removed from sourceIndex")
	}
	if !ContainsSource("https://example.com/other.bin") {
		t.Fatal("expected unrelated source to remain")
	}

	if _, err := os.Stat(historyFile); err != nil {
		t.Fatalf("expected history file to remain readable: %v", err)
	}

	Clear()
	if len(GetAll()) != 0 || len(sourceIndex) != 0 {
		t.Fatalf("expected Clear to remove entries and sourceIndex, entries=%#v sourceIndex=%#v", GetAll(), sourceIndex)
	}
}

func TestHistoryDownloadGroupNameStatusUpdatePreservesSourceIndex(t *testing.T) {
	setupTest(t)
	beforeSaveTriggers := saveTriggerCount.Load()
	group := historyTestDownloadGroup("dg-history-name")
	Add(HistoryEntry{GID: "gid-name", Title: "Grouped", Source: "https://example.com/group.bin", DownloadGroup: group})
	afterAddTriggers := saveTriggerCount.Load()

	changed := UpdateDownloadGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)

	if changed != 1 {
		t.Fatalf("expected one updated history entry, got %d", changed)
	}
	entry, ok := Get("gid-name")
	if !ok || entry.DownloadGroup == nil || entry.DownloadGroup.Name != "Project Alpha" || entry.DownloadGroup.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected updated history group, got %#v ok=%v", entry, ok)
	}
	if !ContainsSource("https://example.com/group.bin") {
		t.Fatal("expected sourceIndex preserved after name update")
	}
	if got := saveTriggerCount.Load() - afterAddTriggers; got != 1 {
		t.Fatalf("expected one save trigger for changed name/status, got %d (before setup delta %d)", got, afterAddTriggers-beforeSaveTriggers)
	}
	if noop := UpdateDownloadGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable); noop != 0 {
		t.Fatalf("expected no-op update count 0, got %d", noop)
	}
}

func TestHistoryDownloadGroupSnapshotsAreDefensiveCopies(t *testing.T) {
	setupTest(t)
	group := historyTestDownloadGroup("dg-history-defensive")
	Add(HistoryEntry{GID: "gid-defensive", Title: "Grouped", Source: "https://example.com/group.bin", DownloadGroup: group})

	all := GetAll()
	all[0].DownloadGroup.Name = "mutated outside history"
	entry, ok := Get("gid-defensive")
	if !ok || entry.DownloadGroup == nil || entry.DownloadGroup.Name == "mutated outside history" {
		t.Fatalf("expected GetAll to return defensive group copy, got entry=%#v ok=%v", entry, ok)
	}

	entry.DownloadGroup.Name = "mutated get"
	again, ok := Get("gid-defensive")
	if !ok || again.DownloadGroup == nil || again.DownloadGroup.Name == "mutated get" {
		t.Fatalf("expected Get to return defensive group copy, got entry=%#v ok=%v", again, ok)
	}
}

func TestHistoryDownloadGroupNameUpdateReplacesSharedPointers(t *testing.T) {
	setupTest(t)
	group := historyTestDownloadGroup("dg-history-replace")
	Add(HistoryEntry{GID: "gid-replace", Title: "Grouped", Source: "https://example.com/group.bin", DownloadGroup: group})
	before := GetAll()[0].DownloadGroup

	UpdateDownloadGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)
	after := GetAll()[0].DownloadGroup

	if before == nil || after == nil {
		t.Fatalf("expected group pointers before/after, before=%#v after=%#v", before, after)
	}
	if before == after {
		t.Fatal("expected history name update to replace pointer instead of mutating it in place")
	}
	if before.Name == "Project Alpha" || before.NameStatus == rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected old history snapshot pointer unchanged, got %#v", before)
	}
	if after.Name != "Project Alpha" || after.NameStatus != rpc.DownloadGroupNameStatusStable {
		t.Fatalf("expected updated history pointer, got %#v", after)
	}
}

func TestHistoryDownloadGroupNameUpdateConcurrentReaders(t *testing.T) {
	setupTest(t)
	group := historyTestDownloadGroup("dg-history-race")
	Add(HistoryEntry{GID: "gid-race", Title: "Grouped", Source: "https://example.com/group.bin", DownloadGroup: group})

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 100 {
				_ = GetAll()
				UpdateDownloadGroupName(group.ID, "Project Alpha", rpc.DownloadGroupNameStatusStable)
				UpdateDownloadGroupName(group.ID, group.Name, rpc.DownloadGroupNameStatusFallback)
			}
		})
	}
	wg.Wait()
}

func TestHistoryLoadOldEntriesWithoutDownloadGroup(t *testing.T) {
	historyFile := setupTest(t)
	oldJSON := `[{"gid":"old-gid","title":"Old","source":"https://example.com/old.bin"}]`
	if err := os.WriteFile(historyFile, []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	Load()
	entry, ok := Get("old-gid")
	if !ok {
		t.Fatal("expected old entry to load")
	}
	if entry.DownloadGroup != nil {
		t.Fatalf("expected old entry group nil, got %#v", entry.DownloadGroup)
	}
	if !ContainsSource("https://example.com/old.bin") {
		t.Fatal("expected sourceIndex rebuilt for old entry")
	}
}

func TestProjectedStoppedStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "error", status: "error", want: "error"},
		{name: "complete", status: "complete", want: "complete"},
		{name: "empty legacy", status: "", want: "complete"},
		{name: "unknown", status: "paused", want: "complete"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ProjectedStoppedStatus(HistoryEntry{Status: tc.status})
			if got != tc.want {
				t.Fatalf("ProjectedStoppedStatus(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

func TestToStoppedTask_ProjectsStatus(t *testing.T) {
	errorTask := ToStoppedTask(HistoryEntry{
		GID:             "gid-err",
		Dir:             "/tmp",
		Path:            "/tmp/err.bin",
		TotalLength:     "10",
		CompletedLength: "5",
		Source:          "https://example.com/err.bin",
		Status:          "error",
	})
	if errorTask.Status != "error" {
		t.Fatalf("expected error status, got %q", errorTask.Status)
	}
	if errorTask.GID != "gid-err" || errorTask.Dir != "/tmp" {
		t.Fatalf("unexpected projected fields: %#v", errorTask)
	}
	if len(errorTask.Files) != 1 || errorTask.Files[0].Path != "/tmp/err.bin" {
		t.Fatalf("unexpected files: %#v", errorTask.Files)
	}

	legacyTask := ToStoppedTask(HistoryEntry{
		GID:    "gid-legacy",
		Path:   "/tmp/legacy.bin",
		Status: "",
	})
	if legacyTask.Status != "complete" {
		t.Fatalf("expected legacy empty status to project complete, got %q", legacyTask.Status)
	}
}

func TestHistoryStatusRoundTrip(t *testing.T) {
	historyFile := setupTest(t)
	SetSaveEnabled(true)
	defer DisableSaveForTest()

	Add(HistoryEntry{GID: "gid-err", Path: "/tmp/a.bin", Status: "error"})
	Add(HistoryEntry{GID: "gid-ok", Path: "/tmp/b.bin", Status: "complete"})

	// Force flush of debounced saver.
	time.Sleep(50 * time.Millisecond)
	if err := Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	Clear()
	Load()

	errEntry, ok := Get("gid-err")
	if !ok {
		t.Fatal("expected gid-err after reload")
	}
	if errEntry.Status != "error" {
		t.Fatalf("expected status error, got %q", errEntry.Status)
	}
	okEntry, ok := Get("gid-ok")
	if !ok {
		t.Fatal("expected gid-ok after reload")
	}
	if okEntry.Status != "complete" {
		t.Fatalf("expected status complete, got %q", okEntry.Status)
	}

	data, err := os.ReadFile(historyFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	_ = raw
}

func TestHistoryLoadLegacyWithoutStatus(t *testing.T) {
	historyFile := setupTest(t)
	oldJSON := `[{"gid":"legacy-gid","title":"Old","path":"/tmp/old.bin","source":"https://example.com/old.bin"}]`
	if err := os.WriteFile(historyFile, []byte(oldJSON), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	Load()
	entry, ok := Get("legacy-gid")
	if !ok {
		t.Fatal("expected legacy entry to load")
	}
	if entry.Status != "" {
		t.Fatalf("expected empty status for legacy JSON, got %q", entry.Status)
	}
	if ProjectedStoppedStatus(entry) != "complete" {
		t.Fatalf("expected legacy projection complete, got %q", ProjectedStoppedStatus(entry))
	}
}

func TestToStoppedTask_ProjectsFields(t *testing.T) {
	// Complete task direct field mapping
	t1 := ToStoppedTask(HistoryEntry{
		GID:             "gid-1",
		Status:          "complete",
		TotalLength:     "7520000",
		CompletedLength: "7520000",
		Path:            "/tmp/file.zip",
		Source:          "http://example.com/file.zip",
	})
	if t1.Status != "complete" || t1.TotalLength != "7520000" || t1.CompletedLength != "7520000" {
		t.Errorf("t1 = (%q, %q, %q), want (complete, 7520000, 7520000)", t1.Status, t1.TotalLength, t1.CompletedLength)
	}
	if len(t1.Files) != 1 || t1.Files[0].Path != "/tmp/file.zip" || len(t1.Files[0].Uris) != 1 || t1.Files[0].Uris[0].Uri != "http://example.com/file.zip" {
		t.Errorf("t1 files/uris mismatch: %+v", t1.Files)
	}

	// Legacy empty status -> complete
	t2 := ToStoppedTask(HistoryEntry{
		GID:             "gid-2",
		Status:          "",
		TotalLength:     "1048576",
		CompletedLength: "1048576",
	})
	if t2.Status != "complete" || t2.TotalLength != "1048576" || t2.CompletedLength != "1048576" {
		t.Errorf("t2 = (%q, %q, %q), want (complete, 1048576, 1048576)", t2.Status, t2.TotalLength, t2.CompletedLength)
	}

	// Error -> error
	t3 := ToStoppedTask(HistoryEntry{
		GID:             "gid-3",
		Status:          "error",
		TotalLength:     "1048576",
		CompletedLength: "500000",
	})
	if t3.Status != "error" || t3.TotalLength != "1048576" || t3.CompletedLength != "500000" {
		t.Errorf("t3 = (%q, %q, %q), want (error, 1048576, 500000)", t3.Status, t3.TotalLength, t3.CompletedLength)
	}
}

func TestAddMergeKeepsTimingOnZeroInput(t *testing.T) {
	setupTest(t)
	Add(HistoryEntry{GID: "t1", Title: "a", DurationMs: 5000, AvgSpeed: 200, PeakSpeed: 300})
	first, _ := Get("t1")
	if first.CompletedAt == 0 {
		t.Fatal("new entry must stamp CompletedAt")
	}

	// Restart replay: same GID, no timing.
	Add(HistoryEntry{GID: "t1", Title: "a-replayed"})
	got, _ := Get("t1")
	if got.Title != "a-replayed" {
		t.Fatalf("Title = %q, non-timing fields must still be replaced", got.Title)
	}
	if got.CompletedAt != first.CompletedAt || got.DurationMs != 5000 || got.AvgSpeed != 200 || got.PeakSpeed != 300 {
		t.Fatalf("zero input erased timing: %+v", got)
	}

	Add(HistoryEntry{GID: "t1", CompletedAt: 42, DurationMs: 1, AvgSpeed: 2, PeakSpeed: 3})
	got, _ = Get("t1")
	if got.CompletedAt != 42 || got.DurationMs != 1 || got.AvgSpeed != 2 || got.PeakSpeed != 3 {
		t.Fatalf("non-zero input must overwrite: %+v", got)
	}
}

func TestTimingFieldsJSONShape(t *testing.T) {
	data, err := json.Marshal(HistoryEntry{GID: "j", DurationMs: 1500, AvgSpeed: 10, PeakSpeed: 20})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	for _, key := range []string{"durationMs", "avgSpeed", "peakSpeed"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing camelCase key %q in %s", key, data)
		}
	}

	data, err = json.Marshal(HistoryEntry{GID: "z"})
	if err != nil {
		t.Fatal(err)
	}
	raw = map[string]any{}
	_ = json.Unmarshal(data, &raw)
	for _, key := range []string{"durationMs", "avgSpeed", "peakSpeed"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("zero %q must be omitted: %s", key, data)
		}
	}
}

func TestLoadLegacyFileWithoutTimingFields(t *testing.T) {
	historyFile := setupTest(t)
	legacy := `[{"gid":"old","title":"old.iso","dir":"/d","path":"/d/old.iso","totalLength":"10","completedLength":"10","completedAt":1700000000}]`
	if err := os.WriteFile(historyFile, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	Load()
	got, ok := Get("old")
	if !ok || got.CompletedAt != 1700000000 {
		t.Fatalf("legacy load failed: %+v ok=%v", got, ok)
	}
	if got.DurationMs != 0 || got.AvgSpeed != 0 || got.PeakSpeed != 0 {
		t.Fatalf("legacy entry grew timing: %+v", got)
	}
}

func TestHistory_AtomicWrite_PreservesTargetAndCleansTemp(t *testing.T) {
	historyFile := setupTest(t)
	dir := filepath.Dir(historyFile)

	// 1. Direct atomicWrite verification
	testData := []byte(`[{"gid":"atom-1","title":"Atomic"}]`)
	if err := atomicWrite(historyFile, testData); err != nil {
		t.Fatalf("atomicWrite failed: %v", err)
	}

	content, err := os.ReadFile(historyFile)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(content) != string(testData) {
		t.Fatalf("content mismatch: got %s, want %s", string(content), string(testData))
	}

	// Verify no .history-*.tmp files exist in directory
	matches, err := filepath.Glob(filepath.Join(dir, ".history-*.tmp"))
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 leftover temp files, found %v", matches)
	}

	// 2. Orphaned temp file cleanup on Load()
	orphan1 := filepath.Join(dir, ".history-12345.tmp")
	orphan2 := filepath.Join(dir, ".history-67890.tmp")
	if err := os.WriteFile(orphan1, []byte("junk1"), 0o644); err != nil {
		t.Fatalf("failed to create orphan1: %v", err)
	}
	if err := os.WriteFile(orphan2, []byte("junk2"), 0o644); err != nil {
		t.Fatalf("failed to create orphan2: %v", err)
	}

	Load()

	matchesAfterLoad, err := filepath.Glob(filepath.Join(dir, ".history-*.tmp"))
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if len(matchesAfterLoad) != 0 {
		t.Fatalf("expected orphan files to be cleaned up by Load(), found %v", matchesAfterLoad)
	}

	// Ensure valid target file remained intact and readable
	entry, ok := Get("atom-1")
	if !ok || entry.Title != "Atomic" {
		t.Fatalf("expected entry atom-1 restored by Load(), got %#v", entry)
	}
}

func TestHistory_Flush_SynchronousDurability(t *testing.T) {
	historyFile := setupTest(t)
	SetSaveEnabled(true)
	defer DisableSaveForTest()

	// 1. When SaveEnabled is true, Flush writes synchronously
	entry := HistoryEntry{GID: "flush-1", Title: "Flush Item"}
	Add(entry)

	if err := Flush(); err != nil {
		t.Fatalf("Flush() failed: %v", err)
	}

	data, err := os.ReadFile(historyFile)
	if err != nil {
		t.Fatalf("failed to read flushed file: %v", err)
	}

	var loaded []HistoryEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("flushed content is not valid JSON: %v", err)
	}
	if len(loaded) != 1 || loaded[0].GID != "flush-1" {
		t.Fatalf("expected 1 flushed entry with GID flush-1, got %#v", loaded)
	}

	// 2. When SaveEnabled is false, Flush is a no-op and respects SaveEnabled
	SetSaveEnabled(false)
	Add(HistoryEntry{GID: "flush-2", Title: "Unsaved Item"})

	if err := Flush(); err != nil {
		t.Fatalf("Flush() with SaveEnabled=false should succeed (no-op), got: %v", err)
	}

	// Target file should still only contain the first entry
	dataAfterDisabled, err := os.ReadFile(historyFile)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	var loadedAfter []HistoryEntry
	if err := json.Unmarshal(dataAfterDisabled, &loadedAfter); err != nil {
		t.Fatalf("content is not valid JSON: %v", err)
	}
	if len(loadedAfter) != 1 || loadedAfter[0].GID != "flush-1" {
		t.Fatalf("expected file to remain unchanged when SaveEnabled=false, got %#v", loadedAfter)
	}
}

func TestHistory_AdaptiveDebounce_BurstCoalescing(t *testing.T) {
	historyFile := setupTest(t)
	SetSaveEnabled(true)
	defer DisableSaveForTest()

	// Debounce 30ms, MaxBurst 200ms
	SetDebounceDuration(30 * time.Millisecond)
	SetMaxBurstDuration(200 * time.Millisecond)

	beforeWrites := saveWriteCount.Load()

	// Rapidly add 20 entries in tight loop (< 5ms)
	for i := range 20 {
		Add(HistoryEntry{GID: strconv.Itoa(1000 + i), Title: "Burst " + strconv.Itoa(i)})
	}

	// Wait for idle window to settle (30ms debounce + safety margin)
	time.Sleep(80 * time.Millisecond)

	delta := saveWriteCount.Load() - beforeWrites
	if delta != 1 {
		t.Fatalf("expected exactly 1 coalesced write for 20 rapid modifications, got %d", delta)
	}

	// Verify on disk that all 20 entries were persisted in that single write
	data, err := os.ReadFile(historyFile)
	if err != nil {
		t.Fatalf("failed to read history file: %v", err)
	}
	var loaded []HistoryEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if len(loaded) != 20 {
		t.Fatalf("expected all 20 entries on disk, got %d", len(loaded))
	}
}

func TestHistory_AdaptiveDebounce_MaxBurstHardCap(t *testing.T) {
	_ = setupTest(t)
	SetSaveEnabled(true)
	defer DisableSaveForTest()

	SetDebounceDuration(25 * time.Millisecond)
	SetMaxBurstDuration(50 * time.Millisecond)

	beforeWrites := saveWriteCount.Load()

	// Feed an Add() every 10ms for 80ms (continuously resetting idleTimer)
	done := make(chan struct{})
	var midWrites atomic.Int64

	go func() {
		for i := range 8 {
			Add(HistoryEntry{GID: strconv.Itoa(3000 + i), Title: "Stream " + strconv.Itoa(i)})
			time.Sleep(10 * time.Millisecond)
			if i == 5 { // at ~60ms, stream is still active
				midWrites.Store(saveWriteCount.Load() - beforeWrites)
			}
		}
		close(done)
	}()

	<-done

	if midWrites.Load() < 1 {
		t.Fatalf("expected hard cap to force at least 1 write before 60ms during continuous stream, got %d", midWrites.Load())
	}

	// Allow final idle window to settle
	time.Sleep(50 * time.Millisecond)

	totalWrites := saveWriteCount.Load() - beforeWrites
	if totalWrites < 2 {
		t.Fatalf("expected at least 2 writes (1 hard cap + 1 trailing idle), got %d", totalWrites)
	}
}

func TestHistory_ConcurrentWrites_NoSnapshotRegression(t *testing.T) {
	historyFile := setupTest(t)
	SetSaveEnabled(true)
	defer DisableSaveForTest()

	SetDebounceDuration(10 * time.Millisecond)
	SetMaxBurstDuration(30 * time.Millisecond)

	const goroutines = 10
	const opsPerGoroutine = 30
	var wg sync.WaitGroup

	for g := range goroutines {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()
			for i := range opsPerGoroutine {
				gid := strconv.Itoa(routineID*1000 + i)
				Add(HistoryEntry{GID: gid, Title: "Concurrent " + gid})
				if i%5 == 0 {
					_ = Flush()
				}
				if i%10 == 0 {
					RemoveMany([]string{strconv.Itoa(routineID*1000 + (i - 2))})
				}
			}
		}(g)
	}

	wg.Wait()

	// Final flush to ensure all surviving entries are written to disk
	if err := Flush(); err != nil {
		t.Fatalf("final Flush failed: %v", err)
	}

	data, err := os.ReadFile(historyFile)
	if err != nil {
		t.Fatalf("failed to read history file: %v", err)
	}

	var loaded []HistoryEntry
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("history file corrupted, not valid JSON: %v", err)
	}

	// In-memory entries must match on-disk entries
	inMem := GetAll()
	if len(loaded) != len(inMem) {
		t.Fatalf("entry count mismatch: disk has %d, memory has %d", len(loaded), len(inMem))
	}

	diskMap := make(map[string]bool, len(loaded))
	for _, e := range loaded {
		diskMap[e.GID] = true
	}
	for _, e := range inMem {
		if !diskMap[e.GID] {
			t.Fatalf("entry %s present in memory but missing on disk", e.GID)
		}
	}
}
