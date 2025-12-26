package main

import (
	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tray"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// App struct for service bindings
type App struct {
	window    *application.WebviewWindow
	systray   *application.SystemTray
	trayState tray.TrayState
}

// NewApp creates a new App instance
func NewApp() *App {
	return &App{
		trayState: tray.StateIdle,
	}
}

// SetWindow stores the main window reference
func (a *App) SetWindow(w *application.WebviewWindow) {
	a.window = w
}

// SetSystemTray stores the system tray reference
func (a *App) SetSystemTray(st *application.SystemTray) {
	a.systray = st
}

// UpdateTrayState updates the tray icon based on download state
func (a *App) UpdateTrayState(hasActive, hasPaused, hasError bool) {
	var newState tray.TrayState
	if hasError {
		newState = tray.StateError
	} else if hasActive {
		newState = tray.StateActive
	} else if hasPaused {
		newState = tray.StatePaused
	} else {
		newState = tray.StateIdle
	}

	// Only update if state changed
	if a.systray != nil && newState != a.trayState {
		a.trayState = newState
		a.systray.SetIcon(tray.GetIconForState(newState))
	}
}

// --- Task Management ---

// AddUri adds a new download task
// Returns "success" on success, "duplicate" if task already exists, or error message
func (a *App) AddUri(url string) string {
	// Check for duplicate URL in existing tasks
	normalizedUrl := strings.TrimSpace(url)
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 1000)
	stopped, _ := rpc.TellStopped(0, 1000)
	allTasks := append(active, append(waiting, stopped...)...)

	for _, t := range allTasks {
		for _, f := range t.Files {
			for _, u := range f.Uris {
				if strings.TrimSpace(u.Uri) == normalizedUrl {
					// Found duplicate - return special marker
					return "duplicate"
				}
			}
		}
	}

	// Also check history for completed tasks that may have been cleared from aria2
	for _, h := range history.GetAll() {
		if h.Source == normalizedUrl {
			return "duplicate"
		}
	}

	err := rpc.AddUri(url, config.Current.DownloadDir)
	if err != nil {
		return err.Error()
	}
	return "success"
}

// GetTasks returns all tasks grouped by status
func (a *App) GetTasks() map[string][]rpc.Task {
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 50)
	var stopped []rpc.Task
	if config.Current.ShowHistory {
		stopped, _ = rpc.TellStopped(0, 50)

		// Track completed tasks for history persistence
		for _, t := range stopped {
			if t.Status == "complete" && len(t.Files) > 0 && t.Files[0].Path != "" {
				// Extract source URL from first file's URIs
				var sourceUrl string
				if len(t.Files[0].Uris) > 0 {
					sourceUrl = t.Files[0].Uris[0].Uri
				}
				history.Add(history.HistoryEntry{
					GID:             t.GID,
					Title:           filepath.Base(t.Files[0].Path),
					Dir:             t.Dir,
					Path:            t.Files[0].Path,
					TotalLength:     t.TotalLength,
					CompletedLength: t.CompletedLength,
					Source:          sourceUrl,
				})
			}
		}

		// Merge history entries with stopped tasks (dedup by GID)
		gidSet := make(map[string]bool)
		for _, t := range stopped {
			gidSet[t.GID] = true
		}

		for _, h := range history.GetAll() {
			if !gidSet[h.GID] {
				// Convert history entry to Task for UI display
				stopped = append(stopped, rpc.Task{
					GID:             h.GID,
					Status:          "complete",
					TotalLength:     h.TotalLength,
					CompletedLength: h.CompletedLength,
					Dir:             h.Dir,
					Files:           []rpc.File{{Path: h.Path}},
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

// --- System Operations ---

// OpenFolder opens the folder containing the downloaded file
func (a *App) OpenFolder(task rpc.Task) {
	// Prefer file path
	target := task.Dir
	if len(task.Files) > 0 && task.Files[0].Path != "" {
		target = task.Files[0].Path
	}

	// Clean path: handle slashes and absolute paths
	cleanPath := filepath.Clean(filepath.FromSlash(target))
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(config.Current.DownloadDir, cleanPath)
	}

	if runtime.GOOS == "windows" {
		// Check if it's a file or directory
		fi, err := os.Stat(cleanPath)
		if err == nil && !fi.IsDir() {
			// If it's a file, use /select to locate it precisely
			exec.Command("explorer.exe", "/select,", cleanPath).Run()
		} else {
			// If it's a directory or file doesn't exist, open directory
			exec.Command("explorer.exe", filepath.Dir(cleanPath)).Run()
		}
	} else if runtime.GOOS == "darwin" {
		exec.Command("open", "-R", cleanPath).Run()
	} else {
		exec.Command("xdg-open", filepath.Dir(cleanPath)).Run()
	}
}

// --- Configuration ---

// GetConfig returns the current configuration
func (a *App) GetConfig() *config.AppConfig {
	return config.Current
}

// SaveConfig saves the configuration and restarts Aria2 if needed
func (a *App) SaveConfig(newCfg config.AppConfig) string {
	*config.Current = newCfg
	if err := config.Save(); err != nil {
		return err.Error()
	}
	if err := process.RestartAria2(config.Current); err != nil {
		return err.Error()
	}
	rpc.Init(config.Current.RPCPort, config.Current.RPCSecret)
	_ = rpc.WaitForReady(4 * time.Second)
	_ = rpc.ChangeGlobalOption(map[string]string{
		"max-concurrent-downloads":  config.Current.MaxConcurrentDownloads,
		"max-connection-per-server": config.Current.MaxConnections,
		"user-agent":                config.Current.UserAgent,
	})
	return "success"
}

// SelectDirectory opens a directory picker dialog
func (a *App) SelectDirectory() string {
	// Use the global app instance for dialog
	app := application.Get()
	if app == nil {
		return ""
	}

	result, err := app.Dialog.OpenFile().
		SetTitle("选择下载目录").
		SetDirectory(config.Current.DownloadDir).
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()

	if err != nil || result == "" {
		return ""
	}
	return result
}
