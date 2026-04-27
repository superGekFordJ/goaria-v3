package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
)

// --- Task Management ---

// RecordTaskSpeed 已废弃 - 后端 TaskTracker 自动采集
// 保留空实现以兼容现有前端
func (a *App) RecordTaskSpeed(gid string, speed int64, cl int64) {
	// 业务逻辑已迁移到 monitor.TaskTracker
	// 此方法保留以兼容前端，但不执行任何操作
}

// BatchAddResult holds the result of a batch add operation
type BatchAddResult struct {
	Succeeded  []string          `json:"succeeded"`
	Duplicates []string          `json:"duplicates"`
	Errors     map[string]string `json:"errors"`
}

func containsTaskSourceURL(tasks []rpc.Task, normalizedURL string) bool {
	for _, task := range tasks {
		for _, file := range task.Files {
			for _, uri := range file.Uris {
				if strings.TrimSpace(uri.Uri) == normalizedURL {
					return true
				}
			}
		}
	}
	return false
}

func collectTaskSourceURLs(existingURLs map[string]bool, tasks []rpc.Task) {
	for _, task := range tasks {
		for _, file := range task.Files {
			for _, uri := range file.Uris {
				existingURLs[strings.TrimSpace(uri.Uri)] = true
			}
		}
	}
}

// AddUri adds a new download task
// Returns "success" on success, "duplicate" if task already exists, or error message
func (a *App) AddUri(url string) string {
	normalizedUrl := strings.TrimSpace(url)
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 1000)
	stopped, _ := rpc.TellStopped(0, 1000)

	if containsTaskSourceURL(active, normalizedUrl) || containsTaskSourceURL(waiting, normalizedUrl) || containsTaskSourceURL(stopped, normalizedUrl) {
		return "duplicate"
	}

	if history.ContainsSource(normalizedUrl) {
		return "duplicate"
	}

	if err := a.addSingleTask(normalizedUrl); err != nil {
		return err.Error()
	}
	return "success"
}

// BatchAddUri adds multiple download URLs in one batch.
// Performs O(1) Set-based deduplication with only 3 RPC calls total.
func (a *App) BatchAddUri(urls []string) BatchAddResult {
	result := BatchAddResult{
		Succeeded:  []string{},
		Duplicates: []string{},
		Errors:     make(map[string]string),
	}

	if len(urls) > 100 {
		urls = urls[:100]
	}

	// 3 RPC calls total for deduplication (not 3N)
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 1000)
	stopped, _ := rpc.TellStopped(0, 1000)

	// Build existing URL set for O(1) lookup
	existingUrls := make(map[string]bool)
	collectTaskSourceURLs(existingUrls, active)
	collectTaskSourceURLs(existingUrls, waiting)
	collectTaskSourceURLs(existingUrls, stopped)

	// History dedup for only the capped, normalized batch candidates.
	normalizedSources := make([]string, 0, len(urls))
	for _, rawUrl := range urls {
		normalized := strings.TrimSpace(rawUrl)
		if normalized == "" {
			continue
		}
		normalizedSources = append(normalizedSources, normalized)
	}
	historyDuplicates := history.ContainsSources(normalizedSources)

	// Batch-internal dedup
	seen := make(map[string]bool)

	for _, rawUrl := range urls {
		normalized := strings.TrimSpace(rawUrl)
		if normalized == "" {
			continue
		}

		// Batch-internal dedup
		if seen[normalized] {
			result.Duplicates = append(result.Duplicates, normalized)
			continue
		}
		seen[normalized] = true

		// Existing task/history dedup
		if existingUrls[normalized] || historyDuplicates[normalized] {
			result.Duplicates = append(result.Duplicates, normalized)
			continue
		}

		if err := a.addSingleTask(normalized); err != nil {
			result.Errors[normalized] = err.Error()
		} else {
			result.Succeeded = append(result.Succeeded, normalized)
		}
	}

	return result
}

// addSingleTask handles the SmartThread + AddUri logic for a single normalized URL.
func (a *App) addSingleTask(normalizedUrl string) error {
	if config.Current.SmartThreadMode {
		fileSize := rpc.HeadContentLength(normalizedUrl, 3*time.Second)

		maxConn, _ := strconv.Atoi(config.Current.MaxConnections)
		if maxConn <= 0 {
			maxConn = 16
		}

		params := smartthread.Calculate(fileSize, maxConn, normalizedUrl)
		gid, err := rpc.AddUriWithOptions(normalizedUrl, config.Current.DownloadDir, params.Split, params.MinSize)
		if err != nil {
			return err
		}

		if gid != "" && params.Split > 0 {
			if tracker := monitor.State.GetTracker(); tracker != nil {
				tracker.SetThreadInfo(gid, params.Split, params.IsExploration)
			}
		}
		return nil
	}

	return rpc.AddUri(normalizedUrl, config.Current.DownloadDir)
}

// GetActiveTasks returns only active and waiting tasks (high-frequency channel)
// This endpoint is optimized for frequent polling (every 1000ms)
// 优化：从后端 Cache 读取，避免重复调用 Aria2 RPC
func (a *App) GetActiveTasks() map[string][]rpc.Task {
	return map[string][]rpc.Task{
		"active":  monitor.Cache.GetActive(),
		"waiting": monitor.Cache.GetWaiting(),
	}
}

func (a *App) GetActiveProgress() []rpc.TaskProgress {
	progress, err := rpc.TellActiveProgress()
	if err != nil {
		return []rpc.TaskProgress{}
	}
	return progress
}

// GetStoppedTasks returns stopped tasks with history (low-frequency channel)
// Called on-demand when user switches to "Completed" tab or every 30s in background
// 业务逻辑（速度统计、历史写入）已迁移到 Monitor
// 优化：从后端 Cache 读取，避免重复调用 Aria2 RPC
func (a *App) GetStoppedTasks() []rpc.Task {
	if !config.Current.ShowHistory {
		return []rpc.Task{}
	}

	return stoppedTasksWithHistory(monitor.Cache.GetStopped())
}

func stoppedTasksWithHistory(stopped []rpc.Task) []rpc.Task {
	existingGIDs := make(map[string]struct{}, len(stopped))
	for i := range stopped {
		existingGIDs[stopped[i].GID] = struct{}{}
		if entry, ok := history.Get(stopped[i].GID); ok {
			backfillStoppedTaskFromHistory(&stopped[i], entry)
		}
	}

	for _, entry := range history.GetMissingByGID(existingGIDs) {
		stopped = append(stopped, historyEntryToStoppedTask(entry))
	}
	return stopped
}

func backfillStoppedTaskFromHistory(task *rpc.Task, entry history.HistoryEntry) {
	if entry.Path != "" && (len(task.Files) == 0 || task.Files[0].Path == "") {
		var uris []rpc.Uri
		if len(task.Files) > 0 && len(task.Files[0].Uris) > 0 {
			uris = task.Files[0].Uris
		} else {
			uris = historySourceURIs(entry.Source)
		}
		task.Files = []rpc.File{{Path: entry.Path, Uris: uris}}
	}
	if len(task.Files) > 0 && len(task.Files[0].Uris) == 0 && entry.Source != "" {
		task.Files[0].Uris = []rpc.Uri{{Uri: entry.Source}}
	}

	if task.TotalLength == "0" && isNonZeroLength(entry.TotalLength) {
		task.TotalLength = entry.TotalLength
	}
	if task.CompletedLength == "0" && isNonZeroLength(entry.CompletedLength) {
		task.CompletedLength = entry.CompletedLength
	}
}

func historyEntryToStoppedTask(entry history.HistoryEntry) rpc.Task {
	return rpc.Task{
		GID:             entry.GID,
		Status:          "complete",
		TotalLength:     entry.TotalLength,
		CompletedLength: entry.CompletedLength,
		Dir:             entry.Dir,
		Files:           []rpc.File{{Path: entry.Path, Uris: historySourceURIs(entry.Source)}},
	}
}

func historySourceURIs(source string) []rpc.Uri {
	if source == "" {
		return []rpc.Uri{}
	}
	return []rpc.Uri{{Uri: source}}
}

func isNonZeroLength(value string) bool {
	return value != "" && value != "0"
}

// GetTasks returns all tasks grouped by status
// 业务逻辑（历史写入）已迁移到 Monitor
// 优化：从后端 Cache 读取，避免重复调用 Aria2 RPC
func (a *App) GetTasks() map[string][]rpc.Task {
	active := monitor.Cache.GetActive()
	waiting := monitor.Cache.GetWaiting()
	var stopped []rpc.Task
	if config.Current.ShowHistory {
		stopped = stoppedTasksWithHistory(monitor.Cache.GetStopped())
	}
	return map[string][]rpc.Task{"active": active, "waiting": waiting, "stopped": stopped}
}

// GetTaskMetadata fetches detailed metadata for tasks with missing file paths
func (a *App) GetTaskMetadata(gids []string) map[string]rpc.Task {
	result := make(map[string]rpc.Task)
	if len(gids) == 0 {
		return result
	}

	tasks, err := rpc.TellStatusMulti(gids)
	if err == nil {
		for _, task := range tasks {
			if task != nil {
				result[task.GID] = *task
			}
		}
	}
	return result
}

// PauseTask pauses a download task
func (a *App) PauseTask(gid string) {
	rpc.Pause(gid)
}

// ResumeTask resumes a paused task
func (a *App) ResumeTask(gid string) {
	rpc.Unpause(gid)
}

// BatchPause pauses multiple tasks
func (a *App) BatchPause(gids []string) {
	_ = rpc.PauseMulti(gids)
}

// BatchResume resumes multiple paused tasks
func (a *App) BatchResume(gids []string) {
	_ = rpc.UnpauseMulti(gids)
}

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

func (a *App) removeTaskWithTarget(gid string, target removalTarget, deleteFile bool) {
	// 1. Remove from Aria2 memory and result list
	rpc.Remove(gid)

	// 2. Remove from history
	history.Remove(gid)

	a.cleanupRemovedTask(gid, target, deleteFile)
}

func (a *App) cleanupRemovedTask(gid string, target removalTarget, deleteFile bool) {
	// 3. Clean up from Tracker
	if tracker := monitor.State.GetTracker(); tracker != nil {
		tracker.RemoveTask(gid)
	}

	// 4. Invalidate cache and emit remove event
	// 这确保 lastStopped 缓存和元数据缓存被清理，防止幽灵任务
	if mon := monitor.State.GetMonitor(); mon != nil {
		mon.InvalidateTask(gid)
	}

	// 5. Physical cleanup
	if target.path == "" {
		return
	}

	go func(p string, dir string) {
		// Give Aria2 enough time to release file handle
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

		// If user checked delete file
		if deleteFile {
			if fi, err := os.Stat(absPath); err == nil && fi.IsDir() {
				_ = os.RemoveAll(absPath)
			} else {
				_ = os.Remove(absPath)
			}
		}

		// Always remove .aria2 control file when task is removed from UI
		_ = os.Remove(absPath + ".aria2")

		// For some BT tasks, path might be a directory
		if strings.HasSuffix(absPath, ".torrent") {
			_ = os.Remove(absPath)
		}
	}(target.path, target.dir)
}

// BatchRemove removes multiple tasks
func (a *App) BatchRemove(gids []string, deleteFiles bool) {
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
		a.cleanupRemovedTask(gid, targets[gid], deleteFiles)
	}
}

// RemoveTask removes a task and optionally deletes the file
func (a *App) RemoveTask(gid string, deleteFile bool) {
	gid = strings.TrimSpace(gid)
	if gid == "" {
		return
	}

	target := resolveRemovalTarget(gid)
	a.removeTaskWithTarget(gid, target, deleteFile)
}
