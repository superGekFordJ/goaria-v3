package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HistoryEntry represents a completed download task
type HistoryEntry struct {
	GID             string `json:"gid"`
	Title           string `json:"title"`
	Dir             string `json:"dir"`
	Path            string `json:"path"`
	TotalLength     string `json:"totalLength"`
	CompletedLength string `json:"completedLength"`
	CompletedAt     int64  `json:"completedAt"`
	Source          string `json:"source,omitempty"` // magnet/http
}

var (
	entries []HistoryEntry
	mu      sync.RWMutex
)

// GetHistoryPath returns the path to history.json
func GetHistoryPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".goaria")
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "history.json")
}

// Load reads history from disk
func Load() {
	mu.Lock()
	defer mu.Unlock()

	entries = []HistoryEntry{}
	data, err := os.ReadFile(GetHistoryPath())
	if err == nil {
		_ = json.Unmarshal(data, &entries)
	}
}

// Save writes history to disk
func Save() error {
	mu.Lock()
	defer mu.Unlock()

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(GetHistoryPath(), data, 0644)
}

// Add adds a new entry to history (deduplicates by GID)
func Add(entry HistoryEntry) {
	mu.Lock()
	defer mu.Unlock()

	// Check if already exists
	for i, e := range entries {
		if e.GID == entry.GID {
			// Update existing entry
			entries[i] = entry
			go saveAsync()
			return
		}
	}

	// Add new entry
	entry.CompletedAt = time.Now().Unix()
	entries = append(entries, entry)
	go saveAsync()
}

// Remove removes an entry by GID
func Remove(gid string) {
	mu.Lock()
	defer mu.Unlock()

	for i, e := range entries {
		if e.GID == gid {
			entries = append(entries[:i], entries[i+1:]...)
			go saveAsync()
			return
		}
	}
}

// GetAll returns all history entries
func GetAll() []HistoryEntry {
	mu.RLock()
	defer mu.RUnlock()

	result := make([]HistoryEntry, len(entries))
	copy(result, entries)
	return result
}

// Clear removes all history entries
func Clear() {
	mu.Lock()
	defer mu.Unlock()

	entries = []HistoryEntry{}
	go saveAsync()
}

// saveAsync saves history in background
func saveAsync() {
	mu.RLock()
	data, err := json.MarshalIndent(entries, "", "  ")
	mu.RUnlock()

	if err == nil {
		_ = os.WriteFile(GetHistoryPath(), data, 0644)
	}
}
