package tasks

import (
	"os"
	"path/filepath"
	"strings"
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

func resolveRemovalTargetsBatch(gids []string) map[string]removalTarget {
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

	tasks, err := rpc.TellStatusMulti(fallbackGIDs)
	if err != nil {
		return targets
	}

	for _, task := range tasks {
		if task == nil {
			continue
		}
		target, ok := removalTargetFromTask(*task)
		if !ok {
			continue
		}
		targets[task.GID] = target
	}

	return targets
}

func resolveRemovalTarget(gid string) removalTarget {
	return resolveRemovalTargetsBatch([]string{gid})[strings.TrimSpace(gid)]
}

func removeTaskWithTarget(gid string, target removalTarget, deleteFile bool) {
	rpc.Remove(gid)
	history.Remove(gid)
	cleanupRemovedTask(gid, target, deleteFile)
}

func cleanupRemovedTask(gid string, target removalTarget, deleteFile bool) {
	if tracker := monitor.State.GetTracker(); tracker != nil {
		tracker.RemoveTask(gid)
	}

	if mon := monitor.State.GetMonitor(); mon != nil {
		mon.InvalidateTask(gid)
	} else {
		monitor.Cache.InvalidateMetadata(gid)
	}

	if target.path == "" {
		return
	}

	go func(p string, dir string) {
		time.Sleep(1 * time.Second)

		cleanP := filepath.Clean(filepath.FromSlash(p))
		absPath := cleanP
		if !filepath.IsAbs(cleanP) {
			baseDir := dir
			if baseDir == "" {
				baseDir = config.Current.DownloadDir
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
	}(target.path, target.dir)
}

func BatchRemove(gids []string, deleteFiles bool) {
	uniqueGIDs := normalizeRemovalGIDs(gids)
	if len(uniqueGIDs) == 0 {
		return
	}

	targets := resolveRemovalTargetsBatch(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		rpc.Remove(gid)
	}
	history.RemoveMany(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		cleanupRemovedTask(gid, targets[gid], deleteFiles)
	}
}

func RemoveTask(gid string, deleteFile bool) {
	gid = strings.TrimSpace(gid)
	if gid == "" {
		return
	}

	target := resolveRemovalTarget(gid)
	removeTaskWithTarget(gid, target, deleteFile)
}
