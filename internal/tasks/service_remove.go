package tasks

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

type removalTarget struct {
	path string
	dir  string
}

func removalTargetFromTask(task rpc.Task) (removalTarget, bool) {
	if len(task.Files) == 0 || task.Files[0].Path == "" {
		return removalTarget{}, false
	}
	return removalTarget{path: task.Files[0].Path, dir: task.Dir}, true
}

func removalTargetFromMetadata(meta *monitor.TaskMetadata) (removalTarget, bool) {
	if meta == nil || len(meta.Files) == 0 || meta.Files[0] == "" {
		return removalTarget{}, false
	}
	return removalTarget{path: meta.Files[0], dir: meta.Dir}, true
}

func removalTargetFromHistory(entry history.HistoryEntry) (removalTarget, bool) {
	if entry.Path == "" {
		return removalTarget{}, false
	}
	return removalTarget{path: entry.Path, dir: entry.Dir}, true
}

func normalizeRemovalGIDs(gids []string) []string {
	seen := make(map[string]struct{}, len(gids))
	unique := make([]string, 0, len(gids))
	for _, gid := range gids {
		gid = strings.TrimSpace(gid)
		if gid == "" {
			continue
		}
		if _, exists := seen[gid]; exists {
			continue
		}
		seen[gid] = struct{}{}
		unique = append(unique, gid)
	}
	return unique
}

func fillRemovalTargetsFromTasks(tasks []rpc.Task, unresolved map[string]struct{}, targets map[string]removalTarget) {
	for _, task := range tasks {
		if _, ok := unresolved[task.GID]; !ok {
			continue
		}
		target, ok := removalTargetFromTask(task)
		if !ok {
			continue
		}
		targets[task.GID] = target
		delete(unresolved, task.GID)
	}
}

func unresolvedRemovalGIDs(order []string, unresolved map[string]struct{}) []string {
	gids := make([]string, 0, len(unresolved))
	for _, gid := range order {
		if _, ok := unresolved[gid]; ok {
			gids = append(gids, gid)
		}
	}
	return gids
}

func (s *Service) resolveRemovalTargetsBatch(gids []string) map[string]removalTarget {
	uniqueGIDs := normalizeRemovalGIDs(gids)
	targets := make(map[string]removalTarget, len(uniqueGIDs))
	if len(uniqueGIDs) == 0 {
		return targets
	}

	unresolved := make(map[string]struct{}, len(uniqueGIDs))
	for _, gid := range uniqueGIDs {
		unresolved[gid] = struct{}{}
	}

	fillRemovalTargetsFromTasks(monitor.Cache.GetActive(), unresolved, targets)
	fillRemovalTargetsFromTasks(monitor.Cache.GetWaiting(), unresolved, targets)
	fillRemovalTargetsFromTasks(monitor.Cache.GetStopped(), unresolved, targets)

	for _, gid := range uniqueGIDs {
		if _, ok := unresolved[gid]; !ok {
			continue
		}
		target, ok := removalTargetFromMetadata(monitor.Cache.GetMetadata(gid))
		if !ok {
			continue
		}
		targets[gid] = target
		delete(unresolved, gid)
	}

	for _, gid := range uniqueGIDs {
		if _, ok := unresolved[gid]; !ok {
			continue
		}
		entry, ok := history.Get(gid)
		if !ok {
			continue
		}
		target, ok := removalTargetFromHistory(entry)
		if !ok {
			continue
		}
		targets[gid] = target
		delete(unresolved, gid)
	}

	fallbackGIDs := unresolvedRemovalGIDs(uniqueGIDs, unresolved)
	if len(fallbackGIDs) == 0 {
		return targets
	}

	tasks, err := s.Engine.TellStatusMulti(fallbackGIDs, nil)
	if err != nil {
		return targets
	}

	for _, task := range tasks {
		target, ok := removalTargetFromTask(task)
		if !ok {
			continue
		}
		targets[task.GID] = target
	}

	return targets
}

func (s *Service) resolveRemovalTarget(gid string) removalTarget {
	return s.resolveRemovalTargetsBatch([]string{gid})[strings.TrimSpace(gid)]
}

func (s *Service) removeTaskWithTarget(gid string, target removalTarget, deleteFile bool) {
	_ = s.Engine.Remove(gid, deleteFile)
	history.Remove(gid)
	cleanupRemovedTask(gid, target, deleteFile)
}

// FORK-PATCH: Added debounced GC trigger on task removal for memory reclamation.
// Called after file deletion and path-missing cleanup paths.
var (
	gcTimer   *time.Timer
	gcTimerMu sync.Mutex
)

func triggerDebouncedGC() {
	gcTimerMu.Lock()
	defer gcTimerMu.Unlock()

	if gcTimer != nil {
		gcTimer.Stop()
	}

	gcTimer = time.AfterFunc(5*time.Second, func() {
		runtime.GC()
		debug.FreeOSMemory()
	})
}

func cleanupRemovedTask(gid string, target removalTarget, deleteFile bool) {
	if tracker := monitor.State.GetTracker(); tracker != nil {
		tracker.RemoveTask(gid)
	}

	// 立即从缓存中移除任务，防止查询接口读取到脏数据导致 UI 重现
	monitor.Cache.RemoveTask(gid)

	if mon := monitor.State.GetMonitor(); mon != nil {
		mon.InvalidateTask(gid)
	} else {
		monitor.Cache.InvalidateMetadata(gid)
	}

	if target.path == "" {
		// Even if file path is missing, trigger memory reclamation
		triggerDebouncedGC()
		return
	}

	go func(p string, dir string) {
		time.Sleep(1 * time.Second)

		cleanP := filepath.Clean(filepath.FromSlash(p))
		absPath := cleanP
		if !filepath.IsAbs(cleanP) {
			baseDir := dir
			if baseDir == "" {
				baseDir = config.Get().DownloadDir
			}
			absPath = filepath.Clean(filepath.Join(filepath.FromSlash(baseDir), cleanP))
		}

		if deleteFile {
			if fi, err := os.Stat(absPath); err == nil && fi.IsDir() {
				_ = os.RemoveAll(absPath)
			} else {
				_ = os.Remove(absPath)
			}
		}

		_ = os.Remove(absPath + ".aria2")

		if strings.HasSuffix(absPath, ".torrent") {
			_ = os.Remove(absPath)
		}

		// Trigger memory reclamation after file deletion processes are completed
		triggerDebouncedGC()
	}(target.path, target.dir)
}

func (s *Service) BatchRemove(gids []string, deleteFiles bool) {
	uniqueGIDs := normalizeRemovalGIDs(gids)
	if len(uniqueGIDs) == 0 {
		return
	}

	targets := s.resolveRemovalTargetsBatch(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		_ = s.Engine.Remove(gid, deleteFiles)
	}
	history.RemoveMany(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		cleanupRemovedTask(gid, targets[gid], deleteFiles)
	}
}

func (s *Service) RemoveTask(gid string, deleteFile bool) {
	gid = strings.TrimSpace(gid)
	if gid == "" {
		return
	}

	target := s.resolveRemovalTarget(gid)
	s.removeTaskWithTarget(gid, target, deleteFile)
}
