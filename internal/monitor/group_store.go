package monitor

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

var groupStore = newTaskGroupStore()

func init() {
	history.SetGroupCleanupHooks(RemoveTaskGroup, RemoveTaskGroups, ClearTaskGroups)
}

type taskGroupStore struct {
	mu      sync.RWMutex
	path    string
	groups  map[string]rpc.DownloadGroup
	enabled bool
}

func newTaskGroupStore() *taskGroupStore {
	return &taskGroupStore{groups: make(map[string]rpc.DownloadGroup), enabled: true}
}

func defaultTaskGroupStorePath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".goaria")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "download_groups.json")
}

func SetTaskGroupStorePath(path string) {
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	groupStore.path = path
	groupStore.groups = make(map[string]rpc.DownloadGroup)
}

func ResetTaskGroupStoreForTest(path string, enabled bool) {
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	groupStore.path = path
	groupStore.groups = make(map[string]rpc.DownloadGroup)
	groupStore.enabled = enabled
}

func SetTaskGroupStoreEnabled(enabled bool) {
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	groupStore.enabled = enabled
}

func LoadTaskGroups() {
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	groupStore.groups = make(map[string]rpc.DownloadGroup)
	data, err := os.ReadFile(groupStore.filePathLocked())
	if err != nil {
		return
	}
	var groups map[string]rpc.DownloadGroup
	if err := json.Unmarshal(data, &groups); err != nil {
		log.Printf("[monitor] failed to load download groups: %v", err)
		return
	}
	for gid, group := range groups {
		if gid == "" || group.ID == "" {
			continue
		}
		groupStore.groups[gid] = group
	}
}

func RegisterTaskGroup(gid string, group rpc.DownloadGroup) {
	if gid == "" || group.ID == "" {
		return
	}
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	groupStore.groups[gid] = group
	groupStore.saveLocked()
}

func GetStoredTaskGroup(gid string) *rpc.DownloadGroup {
	groupStore.mu.RLock()
	defer groupStore.mu.RUnlock()
	group, ok := groupStore.groups[gid]
	if !ok {
		return nil
	}
	return copyDownloadGroup(&group)
}

func RemoveTaskGroup(gid string) {
	if gid == "" {
		return
	}
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	if _, ok := groupStore.groups[gid]; !ok {
		return
	}
	delete(groupStore.groups, gid)
	groupStore.saveLocked()
}

func RemoveTaskGroups(gids []string) {
	if len(gids) == 0 {
		return
	}
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	changed := false
	for _, gid := range gids {
		if _, ok := groupStore.groups[gid]; ok {
			delete(groupStore.groups, gid)
			changed = true
		}
	}
	if changed {
		groupStore.saveLocked()
	}
}

func ClearTaskGroups() {
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	if len(groupStore.groups) == 0 {
		return
	}
	groupStore.groups = make(map[string]rpc.DownloadGroup)
	groupStore.saveLocked()
}

func HydrateTaskGroups(tasks []rpc.Task) {
	if len(tasks) == 0 {
		return
	}
	groupStore.mu.RLock()
	defer groupStore.mu.RUnlock()
	for i := range tasks {
		if tasks[i].DownloadGroup != nil {
			continue
		}
		if group, ok := groupStore.groups[tasks[i].GID]; ok {
			tasks[i].DownloadGroup = copyDownloadGroup(&group)
		}
	}
}

func (s *taskGroupStore) filePathLocked() string {
	if s.path != "" {
		return s.path
	}
	return defaultTaskGroupStorePath()
}

func (s *taskGroupStore) saveLocked() {
	if !s.enabled {
		return
	}
	path := s.filePathLocked()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[monitor] failed to create download group store dir: %v", err)
		return
	}
	if len(s.groups) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[monitor] failed to remove download group store: %v", err)
		}
		return
	}
	data, err := json.MarshalIndent(s.groups, "", "  ")
	if err != nil {
		log.Printf("[monitor] failed to marshal download groups: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("[monitor] failed to save download groups: %v", err)
	}
}
