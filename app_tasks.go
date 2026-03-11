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

// AddUri adds a new download task
// Returns "success" on success, "duplicate" if task already exists, or error message
func (a *App) AddUri(url string) string {
	normalizedUrl := strings.TrimSpace(url)
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 1000)
	stopped, _ := rpc.TellStopped(0, 1000)
	allTasks := append(active, append(waiting, stopped...)...)

	for _, t := range allTasks {
		for _, f := range t.Files {
			for _, u := range f.Uris {
				if strings.TrimSpace(u.Uri) == normalizedUrl {
					return "duplicate"
				}
			}
		}
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
	for _, t := range append(active, append(waiting, stopped...)...) {
		for _, f := range t.Files {
			for _, u := range f.Uris {
				existingUrls[strings.TrimSpace(u.Uri)] = true
			}
		}
	}

	// History dedup
	for _, h := range history.GetAll() {
		existingUrls[h.Source] = true
	}

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
		if existingUrls[normalized] {
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

	stopped := monitor.Cache.GetStopped()

	// 用历史记录补全缺失的文件信息
	// 场景：Lite RPC 返回的任务没有文件信息，但历史记录有
	gidSet := make(map[string]bool)
	for i := range stopped {
		gidSet[stopped[i].GID] = true
		// 如果缓存任务缺少文件或大小信息(小文件竞态)，尝试从历史记录补全
		if h, ok := history.Get(stopped[i].GID); ok {
			if (len(stopped[i].Files) == 0 || stopped[i].Files[0].Path == "") && h.Path != "" {
				stopped[i].Files = []rpc.File{{Path: h.Path, Uris: []rpc.Uri{{Uri: h.Source}}}}
			} else if len(stopped[i].Files) > 0 && len(stopped[i].Files[0].Uris) == 0 && h.Source != "" {
				stopped[i].Files[0].Uris = []rpc.Uri{{Uri: h.Source}}
			}

			if stopped[i].TotalLength == "0" && h.TotalLength != "0" {
				stopped[i].TotalLength = h.TotalLength
			}
			if stopped[i].CompletedLength == "0" && h.CompletedLength != "0" {
				stopped[i].CompletedLength = h.CompletedLength
			}
		}
	}

	// 添加仅存在于历史记录中的任务（Aria2 重启后丢失的）
	for _, h := range history.GetAll() {
		if !gidSet[h.GID] {
			stopped = append(stopped, rpc.Task{
				GID:             h.GID,
				Status:          "complete",
				TotalLength:     h.TotalLength,
				CompletedLength: h.CompletedLength,
				Dir:             h.Dir,
				Files:           []rpc.File{{Path: h.Path, Uris: []rpc.Uri{{Uri: h.Source}}}},
			})
		}
	}

	return stopped
}

// GetTasks returns all tasks grouped by status
// 业务逻辑（历史写入）已迁移到 Monitor
// 优化：从后端 Cache 读取，避免重复调用 Aria2 RPC
func (a *App) GetTasks() map[string][]rpc.Task {
	active := monitor.Cache.GetActive()
	waiting := monitor.Cache.GetWaiting()
	var stopped []rpc.Task
	if config.Current.ShowHistory {
		stopped = monitor.Cache.GetStopped()

		// 用历史记录补全缺失的文件信息和大小(小文件竞态)
		gidSet := make(map[string]bool)
		for i := range stopped {
			gidSet[stopped[i].GID] = true
			if h, ok := history.Get(stopped[i].GID); ok {
				if (len(stopped[i].Files) == 0 || stopped[i].Files[0].Path == "") && h.Path != "" {
					stopped[i].Files = []rpc.File{{Path: h.Path, Uris: []rpc.Uri{{Uri: h.Source}}}}
				} else if len(stopped[i].Files) > 0 && len(stopped[i].Files[0].Uris) == 0 && h.Source != "" {
					stopped[i].Files[0].Uris = []rpc.Uri{{Uri: h.Source}}
				}

				if stopped[i].TotalLength == "0" && h.TotalLength != "0" {
					stopped[i].TotalLength = h.TotalLength
				}
				if stopped[i].CompletedLength == "0" && h.CompletedLength != "0" {
					stopped[i].CompletedLength = h.CompletedLength
				}
			}
		}

		// 添加仅存在于历史记录中的任务
		for _, h := range history.GetAll() {
			if !gidSet[h.GID] {
				stopped = append(stopped, rpc.Task{
					GID:             h.GID,
					Status:          "complete",
					TotalLength:     h.TotalLength,
					CompletedLength: h.CompletedLength,
					Dir:             h.Dir,
					Files:           []rpc.File{{Path: h.Path, Uris: []rpc.Uri{{Uri: h.Source}}}},
				})
			}
		}
	}
	return map[string][]rpc.Task{"active": active, "waiting": waiting, "stopped": stopped}
}

// GetTaskMetadata fetches detailed metadata for tasks with missing file paths
func (a *App) GetTaskMetadata(gids []string) map[string]rpc.Task {
	result := make(map[string]rpc.Task)
	for _, gid := range gids {
		task, err := rpc.TellStatus(gid)
		if err == nil && task != nil {
			result[gid] = *task
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
	for _, gid := range gids {
		rpc.Pause(gid)
	}
}

// BatchResume resumes multiple paused tasks
func (a *App) BatchResume(gids []string) {
	for _, gid := range gids {
		rpc.Unpause(gid)
	}
}

// BatchRemove removes multiple tasks
func (a *App) BatchRemove(gids []string, deleteFiles bool) {
	for _, gid := range gids {
		a.RemoveTask(gid, deleteFiles)
	}
}

// RemoveTask removes a task and optionally deletes the file
func (a *App) RemoveTask(gid string, deleteFile bool) {
	var targetPath string
	var targetDir string

	// 1. Find the file path
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 1000)
	stopped, _ := rpc.TellStopped(0, 1000)
	all := append(active, append(waiting, stopped...)...)
	for _, t := range all {
		if t.GID == gid && len(t.Files) > 0 && t.Files[0].Path != "" {
			targetPath = t.Files[0].Path
			targetDir = t.Dir
			break
		}
	}

	// Fallback: some tasks may not include file metadata in TellActive/TellWaiting
	if targetPath == "" {
		if t, err := rpc.TellStatus(gid); err == nil && t != nil && len(t.Files) > 0 && t.Files[0].Path != "" {
			targetPath = t.Files[0].Path
			targetDir = t.Dir
		}
	}

	// Fallback: tasks restored from history may not exist in Aria2 lists after restart
	if targetPath == "" {
		for _, h := range history.GetAll() {
			if h.GID == gid && h.Path != "" {
				targetPath = h.Path
				targetDir = h.Dir
				break
			}
		}
	}

	// 2. Remove from Aria2 memory and result list
	rpc.Remove(gid)

	// 3. Remove from history
	history.Remove(gid)

	// 4. Clean up from Tracker
	if tracker := monitor.State.GetTracker(); tracker != nil {
		tracker.RemoveTask(gid)
	}

	// 5. Invalidate cache and emit remove event
	// 这确保 lastStopped 缓存和元数据缓存被清理，防止幽灵任务
	if mon := monitor.State.GetMonitor(); mon != nil {
		mon.InvalidateTask(gid)
	}

	// 4. Physical cleanup
	if targetPath != "" {
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
		}(targetPath, targetDir)
	}
}
