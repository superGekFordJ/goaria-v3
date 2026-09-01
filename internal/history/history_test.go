package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func setupTest(t *testing.T) string {
	tmpDir := t.TempDir()
	historyFile := filepath.Join(tmpDir, "history.json")
	SetHistoryPath(historyFile)
	SetDebounceDuration(10 * time.Millisecond)
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
		Add(HistoryEntry{GID: fmt.Sprintf("%d", i)})
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

func TestProjectCompletedLengths(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		total         string
		completed     string
		wantTotal     string
		wantCompleted string
	}{
		{
			name:          "complete with zero total and positive completed becomes N/N",
			status:        "complete",
			total:         "0",
			completed:     "1048576",
			wantTotal:     "1048576",
			wantCompleted: "1048576",
		},
		{
			name:          "complete with empty total and positive completed becomes N/N",
			status:        "complete",
			total:         "",
			completed:     "1048576",
			wantTotal:     "1048576",
			wantCompleted: "1048576",
		},
		{
			name:          "complete with negative total and positive completed becomes N/N",
			status:        "complete",
			total:         "-1",
			completed:     "1048576",
			wantTotal:     "1048576",
			wantCompleted: "1048576",
		},
		{
			name:          "complete with zero total and zero completed remains 0/0",
			status:        "complete",
			total:         "0",
			completed:     "0",
			wantTotal:     "0",
			wantCompleted: "0",
		},
		{
			name:          "complete with known positive total preserves mismatch",
			status:        "complete",
			total:         "2048",
			completed:     "1024",
			wantTotal:     "2048",
			wantCompleted: "1024",
		},
		{
			name:          "error with zero total and positive completed remains 0/N",
			status:        "error",
			total:         "0",
			completed:     "512",
			wantTotal:     "0",
			wantCompleted: "512",
		},
		{
			name:          "malformed total string is not used as evidence",
			status:        "complete",
			total:         "not-a-number",
			completed:     "1024",
			wantTotal:     "not-a-number",
			wantCompleted: "1024",
		},
		{
			name:          "malformed completed string is not promoted",
			status:        "complete",
			total:         "0",
			completed:     "bad-bytes",
			wantTotal:     "0",
			wantCompleted: "bad-bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTotal, gotCompleted := ProjectCompletedLengths(tt.status, tt.total, tt.completed)
			if gotTotal != tt.wantTotal || gotCompleted != tt.wantCompleted {
				t.Errorf("ProjectCompletedLengths(%q, %q, %q) = (%q, %q), want (%q, %q)",
					tt.status, tt.total, tt.completed, gotTotal, gotCompleted, tt.wantTotal, tt.wantCompleted)
			}
		})
	}
}

func TestToStoppedTask_ProjectsCompletedLengths(t *testing.T) {
	// Explicit complete 0/N
	t1 := ToStoppedTask(HistoryEntry{
		GID:             "gid-1",
		Status:          "complete",
		TotalLength:     "0",
		CompletedLength: "7520000",
	})
	if t1.TotalLength != "7520000" || t1.CompletedLength != "7520000" {
		t.Errorf("t1 lengths = (%q, %q), want (7520000, 7520000)", t1.TotalLength, t1.CompletedLength)
	}

	// Legacy empty status 0/N -> complete N/N
	t2 := ToStoppedTask(HistoryEntry{
		GID:             "gid-2",
		Status:          "",
		TotalLength:     "0",
		CompletedLength: "1048576",
	})
	if t2.Status != "complete" || t2.TotalLength != "1048576" || t2.CompletedLength != "1048576" {
		t.Errorf("t2 = (%q, %q, %q), want (complete, 1048576, 1048576)", t2.Status, t2.TotalLength, t2.CompletedLength)
	}

	// Error 0/N -> error 0/N
	t3 := ToStoppedTask(HistoryEntry{
		GID:             "gid-3",
		Status:          "error",
		TotalLength:     "0",
		CompletedLength: "500000",
	})
	if t3.Status != "error" || t3.TotalLength != "0" || t3.CompletedLength != "500000" {
		t.Errorf("t3 = (%q, %q, %q), want (error, 0, 500000)", t3.Status, t3.TotalLength, t3.CompletedLength)
	}
}

