package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

	for i := 0; i < iterations; i++ {
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
	for i := 0; i < n; i++ {
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
