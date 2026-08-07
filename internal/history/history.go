package history

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/rpc"
)

// HistoryEntry represents a completed download task
type HistoryEntry struct {
	GID             string             `json:"gid"`
	Title           string             `json:"title"`
	Dir             string             `json:"dir"`
	Path            string             `json:"path"`
	TotalLength     string             `json:"totalLength"`
	CompletedLength string             `json:"completedLength"`
	CompletedAt     int64              `json:"completedAt"`
	Source          string             `json:"source,omitempty"` // magnet/http
	Status          string             `json:"status,omitempty"` // terminal: "complete" | "error"; empty = legacy → complete
	DownloadGroup   *rpc.DownloadGroup `json:"download_group,omitempty"`
}

// ProjectedStoppedStatus resolves the stopped-list status for a history entry.
// Only an explicit stored "error" projects as error; empty/unknown/legacy → complete.
func ProjectedStoppedStatus(entry HistoryEntry) string {
	if entry.Status == "error" {
		return "error"
	}
	return "complete"
}

// ToStoppedTask projects a history entry into a synthetic stopped rpc.Task.
func ToStoppedTask(entry HistoryEntry) rpc.Task {
	var uris []rpc.Uri
	if entry.Source != "" {
		uris = []rpc.Uri{{Uri: entry.Source}}
	} else {
		uris = []rpc.Uri{}
	}
	return rpc.Task{
		GID:             entry.GID,
		Status:          ProjectedStoppedStatus(entry),
		TotalLength:     entry.TotalLength,
		CompletedLength: entry.CompletedLength,
		Dir:             entry.Dir,
		Files:           []rpc.File{{Path: entry.Path, Uris: uris}},
		DownloadGroup:   copyDownloadGroup(entry.DownloadGroup),
	}
}

func copyDownloadGroup(group *rpc.DownloadGroup) *rpc.DownloadGroup {
	if group == nil {
		return nil
	}
	copy := *group
	return &copy
}

func copyHistoryEntry(entry HistoryEntry) HistoryEntry {
	entry.DownloadGroup = copyDownloadGroup(entry.DownloadGroup)
	return entry
}

var (
	entries             []HistoryEntry
	gidIndex            map[string]int
	sourceIndex         map[string]int
	mu                  sync.RWMutex
	historyPath         string
	saveChan            = make(chan struct{}, 1)
	debounceNanos       atomic.Int64
	saveTriggerCount    atomic.Int64
	saverOnce           sync.Once
	SaveEnabled         = true
	hooksMu             sync.RWMutex
	groupRemoveHook     func(string)
	groupRemoveManyHook func([]string)
	groupClearHook      func()
)

func init() {
	debounceNanos.Store(int64(500 * time.Millisecond))
}

// SetHistoryPath overrides the default history file path.
// This is primarily used for testing to isolate test data.
func SetHistoryPath(path string) {
	historyPath = path
}

// SetDebounceDuration configures the debounce interval for batch saving.
// Default is 500ms. This is primarily used for testing.
func SetDebounceDuration(d time.Duration) {
	debounceNanos.Store(int64(d))
}

// SetSaveEnabled sets the SaveEnabled flag under the write lock.
// Use this instead of writing SaveEnabled directly from external packages.
func SetSaveEnabled(v bool) {
	mu.Lock()
	defer mu.Unlock()
	SaveEnabled = v
}

func SetGroupCleanupHooks(remove func(string), removeMany func([]string), clear func()) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	groupRemoveHook = remove
	groupRemoveManyHook = removeMany
	groupClearHook = clear
}

func notifyGroupRemove(gid string) {
	hooksMu.RLock()
	remove := groupRemoveHook
	hooksMu.RUnlock()
	if remove != nil {
		remove(gid)
	}
}

func notifyGroupRemoveMany(gids []string) {
	hooksMu.RLock()
	removeMany := groupRemoveManyHook
	hooksMu.RUnlock()
	if removeMany != nil {
		removeMany(gids)
		return
	}
	for _, gid := range gids {
		notifyGroupRemove(gid)
	}
}

func notifyGroupClear() {
	hooksMu.RLock()
	clear := groupClearHook
	hooksMu.RUnlock()
	if clear != nil {
		clear()
	}
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
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "history.json")
}

// Load reads history from disk
func Load() {
	mu.Lock()
	defer mu.Unlock()

	entries = []HistoryEntry{}
	gidIndex = make(map[string]int)
	sourceIndex = make(map[string]int)
	data, err := os.ReadFile(GetHistoryPath())
	if err == nil {
		_ = json.Unmarshal(data, &entries)
		for i, e := range entries {
			gidIndex[e.GID] = i
			if e.Source != "" {
				sourceIndex[e.Source]++
			}
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
	return os.WriteFile(GetHistoryPath(), data, 0o644)
}

// Add adds a new entry to history (deduplicates by GID)
func Add(entry HistoryEntry) {
	mu.Lock()
	defer mu.Unlock()

	if gidIndex == nil {
		gidIndex = make(map[string]int)
	}
	if sourceIndex == nil {
		sourceIndex = make(map[string]int)
	}

	// Check if already exists
	if i, ok := gidIndex[entry.GID]; ok {
		if entry.DownloadGroup == nil && entries[i].DownloadGroup != nil {
			entry.DownloadGroup = copyDownloadGroup(entries[i].DownloadGroup)
		}
		// Update sourceIndex if source changes
		oldSource := entries[i].Source
		if oldSource != entry.Source {
			if oldSource != "" {
				sourceIndex[oldSource]--
				if sourceIndex[oldSource] <= 0 {
					delete(sourceIndex, oldSource)
				}
			}
			if entry.Source != "" {
				sourceIndex[entry.Source]++
			}
		}

		// Update existing entry
		entries[i] = copyHistoryEntry(entry)
		triggerSaveLocked()
		return
	}

	// Add new entry
	entry.CompletedAt = time.Now().Unix()
	entries = append(entries, copyHistoryEntry(entry))
	gidIndex[entry.GID] = len(entries) - 1
	if entry.Source != "" {
		sourceIndex[entry.Source]++
	}
	triggerSaveLocked()
}

// Remove removes an entry by GID
func Remove(gid string) {
	mu.Lock()
	defer mu.Unlock()

	if gidIndex == nil {
		return
	}

	if i, ok := gidIndex[gid]; ok {
		notifyGroupRemove(gid)
		// Update sourceIndex
		oldSource := entries[i].Source
		if oldSource != "" {
			sourceIndex[oldSource]--
			if sourceIndex[oldSource] <= 0 {
				delete(sourceIndex, oldSource)
			}
		}

		entries = append(entries[:i], entries[i+1:]...)
		delete(gidIndex, gid)
		// Update indices for shifted elements
		for j := i; j < len(entries); j++ {
			gidIndex[entries[j].GID] = j
		}
		triggerSaveLocked()
	}
}

// RemoveMany removes history entries for the given GIDs in one compaction pass.
func RemoveMany(gids []string) {
	if len(gids) == 0 {
		return
	}

	removeSet := make(map[string]struct{}, len(gids))
	for _, gid := range gids {
		if gid == "" {
			continue
		}
		removeSet[gid] = struct{}{}
	}
	if len(removeSet) == 0 {
		return
	}

	mu.Lock()
	defer mu.Unlock()

	if len(entries) == 0 || len(gidIndex) == 0 {
		return
	}

	hasRemoval := false
	for gid := range removeSet {
		if _, ok := gidIndex[gid]; ok {
			hasRemoval = true
			break
		}
	}
	if !hasRemoval {
		return
	}

	removed := 0
	removedGIDs := make([]string, 0, len(removeSet))
	compacted := entries[:0]
	for _, entry := range entries {
		if _, ok := removeSet[entry.GID]; ok {
			removedGIDs = append(removedGIDs, entry.GID)
			removed++
			continue
		}
		compacted = append(compacted, entry)
	}
	if removed == 0 {
		return
	}
	notifyGroupRemoveMany(removedGIDs)

	for i := len(compacted); i < len(entries); i++ {
		entries[i] = HistoryEntry{}
	}
	entries = compacted
	gidIndex = make(map[string]int, len(entries))
	sourceIndex = make(map[string]int, len(entries))
	for i, entry := range entries {
		gidIndex[entry.GID] = i
		if entry.Source != "" {
			sourceIndex[entry.Source]++
		}
	}
	triggerSaveLocked()
}

// GetAll returns all history entries
func GetAll() []HistoryEntry {
	mu.RLock()
	defer mu.RUnlock()

	result := make([]HistoryEntry, len(entries))
	for i, entry := range entries {
		result[i] = copyHistoryEntry(entry)
	}
	return result
}

// GetMissingByGID returns history entries whose GIDs are absent from existingGIDs.
func GetMissingByGID(existingGIDs map[string]struct{}) []HistoryEntry {
	mu.RLock()
	defer mu.RUnlock()

	result := make([]HistoryEntry, 0)
	for _, entry := range entries {
		if _, exists := existingGIDs[entry.GID]; exists {
			continue
		}
		result = append(result, copyHistoryEntry(entry))
	}
	return result
}

// ContainsSource checks if a history entry with the given source exists.
// This is more efficient than GetAll() as it avoids copying the slice.
func ContainsSource(source string) bool {
	mu.RLock()
	defer mu.RUnlock()

	if sourceIndex == nil {
		return false
	}
	return sourceIndex[source] > 0
}

// ContainsSources returns the queried sources currently present in history.
func ContainsSources(sources []string) map[string]bool {
	mu.RLock()
	defer mu.RUnlock()

	result := make(map[string]bool)
	if len(sources) == 0 || sourceIndex == nil {
		return result
	}

	for _, source := range sources {
		if source == "" {
			continue
		}
		if sourceIndex[source] > 0 {
			result[source] = true
		}
	}
	return result
}

// Get returns a single history entry by GID (O(1) lookup)
func Get(gid string) (HistoryEntry, bool) {
	mu.RLock()
	defer mu.RUnlock()

	if gidIndex == nil {
		return HistoryEntry{}, false
	}
	if i, ok := gidIndex[gid]; ok {
		return copyHistoryEntry(entries[i]), true
	}
	return HistoryEntry{}, false
}

func UpdateDownloadGroupName(groupKey, name, status string) int {
	groupKey = strings.TrimSpace(groupKey)
	name = strings.TrimSpace(name)
	status = strings.TrimSpace(status)
	if groupKey == "" || name == "" || !rpc.IsDownloadGroupNameStatus(status) {
		return 0
	}

	mu.Lock()
	defer mu.Unlock()

	changed := 0
	for i := range entries {
		group := entries[i].DownloadGroup
		if group == nil || group.ID != groupKey {
			continue
		}
		if group.Name == name && group.NameStatus == status {
			continue
		}
		updated := *group
		updated.Name = name
		updated.NameStatus = status
		entries[i].DownloadGroup = &updated
		changed++
	}
	if changed > 0 {
		triggerSaveLocked()
	}
	return changed
}

// Clear removes all history entries
func Clear() {
	mu.Lock()
	defer mu.Unlock()

	entries = []HistoryEntry{}
	gidIndex = make(map[string]int)
	sourceIndex = make(map[string]int)
	notifyGroupClear()
	triggerSaveLocked()
}

func triggerSaveLocked() {
	saveTriggerCount.Add(1)
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
		time.Sleep(time.Duration(debounceNanos.Load()))
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

	if err := os.WriteFile(GetHistoryPath(), data, 0o644); err != nil {
		log.Printf("[history] Failed to write file: %v", err)
	}
}
