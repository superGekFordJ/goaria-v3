package history

import (
	"encoding/json"
	"log"
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
	entries          []HistoryEntry
	gidIndex         map[string]int
	mu               sync.RWMutex
	historyPath      string
	saveChan         = make(chan struct{}, 1)
	debounceInterval = 500 * time.Millisecond
	saverOnce        sync.Once
	SaveEnabled      = true
)

// SetHistoryPath overrides the default history file path.
// This is primarily used for testing to isolate test data.
func SetHistoryPath(path string) {
	historyPath = path
}

// SetDebounceDuration configures the debounce interval for batch saving.
// Default is 500ms. This is primarily used for testing.
func SetDebounceDuration(d time.Duration) {
	debounceInterval = d
}

// DisableSaveForTest disables disk persistence for testing.
// This prevents test data from being written to the filesystem.
func DisableSaveForTest() {
	mu.Lock()
	defer mu.Unlock()
	SaveEnabled = false
}

// GetHistoryPath returns the path to history.json
func GetHistoryPath() string {
	if historyPath != "" {
		return historyPath
	}
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
	gidIndex = make(map[string]int)
	data, err := os.ReadFile(GetHistoryPath())
	if err == nil {
		_ = json.Unmarshal(data, &entries)
		for i, e := range entries {
			gidIndex[e.GID] = i
		}
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

	if gidIndex == nil {
		gidIndex = make(map[string]int)
	}

	// Check if already exists
	if i, ok := gidIndex[entry.GID]; ok {
		// Update existing entry
		entries[i] = entry
		triggerSave()
		return
	}

	// Add new entry
	entry.CompletedAt = time.Now().Unix()
	entries = append(entries, entry)
	gidIndex[entry.GID] = len(entries) - 1
	triggerSave()
}

// Remove removes an entry by GID
func Remove(gid string) {
	mu.Lock()
	defer mu.Unlock()

	if gidIndex == nil {
		return
	}

	if i, ok := gidIndex[gid]; ok {
		entries = append(entries[:i], entries[i+1:]...)
		delete(gidIndex, gid)
		// Update indices for shifted elements
		for j := i; j < len(entries); j++ {
			gidIndex[entries[j].GID] = j
		}
		triggerSave()
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

// ContainsSource checks if a history entry with the given source exists.
// This is more efficient than GetAll() as it avoids copying the slice.
func ContainsSource(source string) bool {
	mu.RLock()
	defer mu.RUnlock()

	for _, e := range entries {
		if e.Source == source {
			return true
		}
	}
	return false
}

// Get returns a single history entry by GID (O(1) lookup)
func Get(gid string) (HistoryEntry, bool) {
	mu.RLock()
	defer mu.RUnlock()

	if gidIndex == nil {
		return HistoryEntry{}, false
	}
	if i, ok := gidIndex[gid]; ok {
		return entries[i], true
	}
	return HistoryEntry{}, false
}

// Clear removes all history entries
func Clear() {
	mu.Lock()
	defer mu.Unlock()

	entries = []HistoryEntry{}
	gidIndex = make(map[string]int)
	triggerSave()
}

func triggerSave() {
	saverOnce.Do(func() {
		go saveLoop()
	})
	select {
	case saveChan <- struct{}{}:
	default:
	}
}

func saveLoop() {
	for {
		<-saveChan
		time.Sleep(debounceInterval)
		// drain extra signals
		select {
		case <-saveChan:
		default:
		}

		doSave()
	}
}

// doSave performs the actual save operation
func doSave() {
	mu.RLock()
	if !SaveEnabled {
		mu.RUnlock()
		return
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	mu.RUnlock()

	if err != nil {
		log.Printf("[history] Failed to marshal entries: %v", err)
		return
	}

	if err := os.WriteFile(GetHistoryPath(), data, 0644); err != nil {
		log.Printf("[history] Failed to write file: %v", err)
	}
}
