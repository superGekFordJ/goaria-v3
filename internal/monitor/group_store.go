package monitor

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

var groupStore = newTaskGroupStore()

const (
	groupStoreSaveDebounce = 100 * time.Millisecond
	groupStoreSaveMaxBurst = time.Second
)

func init() {
	history.SetGroupCleanupHooks(RemoveTaskGroup, RemoveTaskGroups, ClearTaskGroups)
}

// taskGroupStore orders writers as writeMu then mu; mutations take only mu and
// never touch disk or wait on a writer.
type taskGroupStore struct {
	mu      sync.RWMutex
	path    string
	groups  map[string]rpc.DownloadGroup
	enabled bool
	stopped bool

	writeMu      sync.Mutex
	saveChan     chan struct{}
	stopChan     chan struct{}
	doneChan     chan struct{}
	saverOnce    sync.Once
	saverStarted bool

	saveTriggerCount atomic.Int64
	lastSavedTrigger atomic.Int64
	saveWriteCount   atomic.Int64

	// Coalescing windows; fixed before the worker can start, read lock-free.
	saveDebounce time.Duration
	saveMaxBurst time.Duration
}

func newTaskGroupStore() *taskGroupStore {
	s := &taskGroupStore{
		groups:       make(map[string]rpc.DownloadGroup),
		enabled:      true,
		saveDebounce: groupStoreSaveDebounce,
		saveMaxBurst: groupStoreSaveMaxBurst,
	}
	s.initSaver()
	return s
}

// initSaver rebuilds saver channels/Once/counters for a new epoch; reset
// callers hold s.mu after joining any previous worker.
func (s *taskGroupStore) initSaver() {
	s.saveChan = make(chan struct{}, 1)
	s.stopChan = make(chan struct{})
	s.doneChan = make(chan struct{})
	s.saverOnce = sync.Once{}
	s.saverStarted = false
	s.stopped = false
	s.saveTriggerCount.Store(0)
	s.lastSavedTrigger.Store(0)
	s.saveWriteCount.Store(0)
}

func defaultTaskGroupStorePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".goaria", "download_groups.json")
}

func SetTaskGroupStorePath(path string) {
	s := groupStore
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
	s.groups = make(map[string]rpc.DownloadGroup)
	s.discardPendingSaveLocked()
}

func ResetTaskGroupStoreForTest(path string, enabled bool) {
	// Namer jobs mutate the store; join them before rebuilding saver state.
	ResetDownloadGroupNamerForTest()
	groupStore.resetForTest(path, enabled)
}

// resetForTest joins the saver, drops pending fixture state without writing
// it, and rebuilds saver state; empty path forces persistence off.
func (s *taskGroupStore) resetForTest(path string, enabled bool) {
	s.stopSaver()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
	s.groups = make(map[string]rpc.DownloadGroup)
	s.enabled = enabled && path != ""
	s.initSaver()
}

func SetTaskGroupStoreEnabled(enabled bool) {
	s := groupStore
	// In-flight writes finish first; pending triggers stay handled on re-enable.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = enabled
	if !enabled {
		s.discardPendingSaveLocked()
	}
}

func LoadTaskGroups() {
	groupStore.loadFromDisk()
}

// loadFromDisk replaces groups from disk under writeMu; pending triggers are
// discarded so queued signals cannot rewrite the replaced map.
func (s *taskGroupStore) loadFromDisk() {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups = make(map[string]rpc.DownloadGroup)
	s.discardPendingSaveLocked()
	path := s.path
	if path == "" {
		path = defaultTaskGroupStorePath()
	}
	data, err := os.ReadFile(path)
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
		s.groups[gid] = group
	}
}

// FlushGroupStoreForTest persists pending state; disabled/clean stores no-op.
func FlushGroupStoreForTest() error {
	return groupStore.performSave()
}

// StopTaskGroupStore stops the saver and flushes the last pending snapshot.
func StopTaskGroupStore() error {
	groupStore.stopSaver()
	return groupStore.performSave()
}

// stopSaver marks the store stopped and joins the saver without holding
// s.mu or writeMu; safe to call repeatedly.
func (s *taskGroupStore) stopSaver() {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.stopChan)
		if !s.saverStarted {
			// No worker can run this epoch; close done so join is uniform.
			close(s.doneChan)
		}
	}
	done := s.doneChan
	s.mu.Unlock()
	<-done
}

// discardPendingSaveLocked marks queued triggers handled and drops a stale
// buffered signal so old snapshots cannot write a changed path.
func (s *taskGroupStore) discardPendingSaveLocked() {
	s.lastSavedTrigger.Store(s.saveTriggerCount.Load())
	select {
	case <-s.saveChan:
	default:
	}
}

func RegisterTaskGroup(gid string, group rpc.DownloadGroup) {
	gid = strings.TrimSpace(gid)
	group.ID = strings.TrimSpace(group.ID)
	if gid == "" || group.ID == "" {
		return
	}
	groupStore.mu.Lock()
	groupStore.groups[gid] = group
	groupStore.triggerSaveLocked()
	groupStore.mu.Unlock()
	Cache.markPendingStart(gid)
	queueDownloadGroupNameRefresh(group.ID)
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

func ListStoredTaskGroups() map[string]rpc.DownloadGroup {
	groupStore.mu.RLock()
	defer groupStore.mu.RUnlock()
	groups := make(map[string]rpc.DownloadGroup, len(groupStore.groups))
	maps.Copy(groups, groupStore.groups)
	return groups
}

func UpdateStoredTaskGroupName(groupKey, name, status string) int {
	groupKey = strings.TrimSpace(groupKey)
	name = strings.TrimSpace(name)
	status = strings.TrimSpace(status)
	if groupKey == "" || name == "" || !rpc.IsDownloadGroupNameStatus(status) {
		return 0
	}

	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()

	changed := 0
	for gid, group := range groupStore.groups {
		if group.ID != groupKey {
			continue
		}
		if group.Name == name && group.NameStatus == status {
			continue
		}
		group.Name = name
		group.NameStatus = status
		groupStore.groups[gid] = group
		changed++
	}
	if changed > 0 {
		groupStore.triggerSaveLocked()
	}
	return changed
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
	groupStore.triggerSaveLocked()
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
		groupStore.triggerSaveLocked()
	}
}

func ClearTaskGroups() {
	groupStore.mu.Lock()
	defer groupStore.mu.Unlock()
	if len(groupStore.groups) == 0 {
		return
	}
	groupStore.groups = make(map[string]rpc.DownloadGroup)
	groupStore.triggerSaveLocked()
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

// triggerSaveLocked records a mutation and wakes the saver; caller holds s.mu
// and it never touches disk, writeMu, timers, or a join.
func (s *taskGroupStore) triggerSaveLocked() {
	s.saveTriggerCount.Add(1)
	if !s.enabled {
		s.lastSavedTrigger.Store(s.saveTriggerCount.Load())
		return
	}
	if s.stopped {
		return
	}
	s.saverOnce.Do(func() {
		s.saverStarted = true
		go s.saveLoop(s.saveChan, s.stopChan, s.doneChan)
	})
	select {
	case s.saveChan <- struct{}{}:
	default:
	}
}

// drainTimer stops t and drains a pending tick.
func drainTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

// saveLoop coalesces signals under an idle debounce plus a non-extending
// max-burst timer; channel args pin the worker to its own epoch.
func (s *taskGroupStore) saveLoop(saveChan, stopChan, doneChan chan struct{}) {
	defer close(doneChan)
	for {
		select {
		case <-saveChan:
		case <-stopChan:
			return
		}

		idleTimer := time.NewTimer(s.saveDebounce)
		maxTimer := time.NewTimer(s.saveMaxBurst)

	coalesce:
		for {
			select {
			case <-saveChan:
				drainTimer(idleTimer)
				idleTimer.Reset(s.saveDebounce)
			case <-idleTimer.C:
				break coalesce
			case <-maxTimer.C:
				break coalesce
			case <-stopChan:
				break coalesce
			}
		}
		drainTimer(idleTimer)
		drainTimer(maxTimer)

		// Drop the redundant buffered signal before snapshotting.
		select {
		case <-saveChan:
		default:
		}
		select {
		case <-stopChan:
			return
		default:
		}

		if s.saveTriggerCount.Load() <= s.lastSavedTrigger.Load() {
			continue
		}
		if err := s.performSave(); err != nil {
			log.Printf("[monitor] background download group save failed: %v", err)
		}
	}
}

// performSave serializes snapshot → write/remove under writeMu so a queued
// write cannot overwrite a newer Flush; callers must not hold s.mu.
func (s *taskGroupStore) performSave() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.RLock()
	if !s.enabled {
		s.mu.RUnlock()
		return nil
	}
	triggerSnap := s.saveTriggerCount.Load()
	if triggerSnap <= s.lastSavedTrigger.Load() {
		s.mu.RUnlock()
		return nil
	}
	path := s.path
	snapshot := make(map[string]rpc.DownloadGroup, len(s.groups))
	maps.Copy(snapshot, s.groups)
	s.mu.RUnlock()
	if path == "" {
		path = defaultTaskGroupStorePath()
	}

	if len(snapshot) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove download group store %s: %w", path, err)
		}
	} else {
		data, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal download groups: %w", err)
		}
		if err := writeDownloadGroupStoreFile(path, data); err != nil {
			return fmt.Errorf("write download group store %s: %w", path, err)
		}
	}

	s.saveWriteCount.Add(1)
	s.lastSavedTrigger.Store(triggerSnap)
	return nil
}

// writeDownloadGroupStoreFile writes via temp + rename so a failed write
// never truncates the live store.
func writeDownloadGroupStoreFile(targetPath string, data []byte) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".download-groups-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	_ = tmp.Chmod(0o644)
	if err := tmp.Sync(); err != nil {
		return err
	}
	closed = true
	if err := tmp.Close(); err != nil {
		return err
	}

	backoffs := []time.Duration{0, 5 * time.Millisecond, 10 * time.Millisecond, 15 * time.Millisecond}
	var renameErr error
	for _, sleep := range backoffs {
		if sleep > 0 {
			time.Sleep(sleep)
		}
		if renameErr = os.Rename(tmpName, targetPath); renameErr == nil {
			return nil
		}
	}
	return renameErr
}
