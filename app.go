package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/tray"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// App struct for service bindings
type App struct {
	app       *application.App
	window    *application.WebviewWindow
	systray   *application.SystemTray
	eventHub  *events.Hub
	trayState tray.TrayState

	windowMu       sync.Mutex // 保护窗口操作
	lastToggleTime time.Time  // 上次切换窗口时间，用于全局防抖
	isToggling     bool       // 防止重入标志
}

// NewApp creates a new App instance
func NewApp() *App {
	return &App{
		trayState: tray.StateIdle,
	}
}

// SetApp stores the application instance reference
func (a *App) SetApp(app *application.App) {
	a.app = app
}

// SetEventHub stores the event hub reference
func (a *App) SetEventHub(hub *events.Hub) {
	a.eventHub = hub
}

// SetWindow stores the main window reference (for backwards compatibility)
func (a *App) SetWindow(w *application.WebviewWindow) {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	a.window = w
	if w != nil {
		monitor.State.SetWindowExists(true)
	}
}

// CreateWindow creates or recreates the window
func (a *App) CreateWindow() {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()

	if a.window != nil {
		// 窗口已存在，直接显示
		a.window.Show()
		a.window.Focus()
		return
	}

	if a.app == nil {
		return
	}

	// 获取窗口配置
	backgroundType, backgroundColour, backdropType, macBackdrop := a.getWindowConfig()

	log.Printf(
		"[App] Creating window: transparency=%s backgroundType=%v",
		config.Current.WindowTransparency,
		backgroundType,
	)

	// 创建新窗口
	a.window = a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "GoAria",
		URL:              "/", // 重要：确保窗口导航到前端 URL
		Width:            1024,
		Height:           680,
		MinWidth:         800,
		MinHeight:        500,
		Frameless:        true,
		BackgroundType:   backgroundType,
		BackgroundColour: backgroundColour,
		Hidden:           false,
		Mac: application.MacWindow{
			Backdrop: macBackdrop,
		},
		Windows: application.WindowsWindow{
			DisableFramelessWindowDecorations: false,
			BackdropType:                      backdropType,
		},
	})

	// 强制导航到根路径，解决部分场景下重建窗口显示 about:blank 的问题
	if a.window != nil {
		a.window.SetURL("/")
	}

	// 更新状态
	monitor.State.SetWindowExists(true)

	// 通知前端窗口已创建
	if a.eventHub != nil {
		a.eventHub.EmitWindowCreated()

		// 延迟发送焦点事件，确保前端已初始化完成
		// 用于托盘恢复时触发剪贴板检测
		go func() {
			time.Sleep(300 * time.Millisecond)
			a.eventHub.EmitWindowFocus()
		}()
	}

	log.Println("[App] Window created")
}

// DestroyWindow destroys the window (true headless mode)
func (a *App) DestroyWindow() {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()

	if a.window == nil {
		return
	}

	a.window.Close()
	a.window = nil

	// 更新状态
	monitor.State.SetWindowExists(false)

	log.Println("[App] Window destroyed - entering headless mode")
}

// ToggleWindow toggles window visibility (or create/destroy)
func (a *App) ToggleWindow() {
	a.windowMu.Lock()

	// 1. 重入保护：如果正在处理上一次切换（创建/销毁中），直接忽略
	// 这解决了 click -> async -> blocking operation 期间的任何后续点击
	if a.isToggling {
		log.Println("[App] ToggleWindow: ignoring click (operation in progress)")
		a.windowMu.Unlock()
		return
	}

	// 2. 时间防抖：操作完成后的一小段时间内也忽略，防止快速连击
	if time.Since(a.lastToggleTime) < 500*time.Millisecond {
		log.Println("[App] ToggleWindow: ignoring rapid click (debounced)")
		a.windowMu.Unlock()
		return
	}

	// 设置处理中标志
	a.isToggling = true

	// 获取当前状态决定下一步操作
	hasWindow := a.window != nil
	isVisible := hasWindow && a.window.IsVisible()

	a.windowMu.Unlock()

	// 确保操作完成后清除标志并更新时间
	defer func() {
		a.windowMu.Lock()
		a.isToggling = false
		a.lastToggleTime = time.Now()
		a.windowMu.Unlock()
	}()

	if !hasWindow {
		// 无窗口，创建新窗口
		a.CreateWindow()
	} else if isVisible {
		// 窗口可见，销毁窗口（真无头模式）
		a.DestroyWindow()
	} else {
		// 窗口存在但隐藏，显示窗口
		a.windowMu.Lock()
		if a.window != nil {
			a.window.Show()
			a.window.Focus()
		}
		a.windowMu.Unlock()
	}
}

// ShowWindow shows the window (creates if not exists)
func (a *App) ShowWindow() {
	a.windowMu.Lock()
	hasWindow := a.window != nil
	a.windowMu.Unlock()

	if !hasWindow {
		a.CreateWindow()
	} else {
		a.windowMu.Lock()
		a.window.Show()
		a.window.Focus()
		a.windowMu.Unlock()
	}
}

// MinimizeToTray minimizes to tray (destroys window for true headless mode)
// Frontend calls this method to implement true headless mode
func (a *App) MinimizeToTray() {
	a.DestroyWindow()
}

// HasWindow returns whether window exists (for frontend query)
func (a *App) HasWindow() bool {
	a.windowMu.Lock()
	defer a.windowMu.Unlock()
	return a.window != nil
}

// getWindowConfig returns window configuration (reuses original logic)
func (a *App) getWindowConfig() (
	backgroundType application.BackgroundType,
	backgroundColour application.RGBA,
	backdropType application.BackdropType,
	macBackdrop application.MacBackdrop,
) {
	backgroundType = application.BackgroundTypeSolid
	backgroundColour = application.NewRGBA(12, 12, 15, 255)
	backdropType = application.Auto
	macBackdrop = application.MacBackdropNormal

	switch config.Current.WindowTransparency {
	case "acrylic":
		backgroundType = application.BackgroundTypeTranslucent
		backgroundColour = application.NewRGBA(0, 0, 0, 0)
		backdropType = application.Acrylic
		macBackdrop = application.MacBackdropTranslucent
	case "mica":
		backgroundType = application.BackgroundTypeTranslucent
		backgroundColour = application.NewRGBA(0, 0, 0, 0)
		backdropType = application.Mica
		macBackdrop = application.MacBackdropTranslucent
	case "tabbed":
		backgroundType = application.BackgroundTypeTranslucent
		backgroundColour = application.NewRGBA(0, 0, 0, 0)
		backdropType = application.Tabbed
		macBackdrop = application.MacBackdropTranslucent
	}

	if runtime.GOOS == "linux" {
		backgroundType = application.BackgroundTypeSolid
		backgroundColour = application.NewRGBA(12, 12, 15, 255)
		backdropType = application.Auto
		macBackdrop = application.MacBackdropNormal
	}

	return
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

// --- Snapshot Sync ---

// FullSnapshot 全量快照结构
type FullSnapshot struct {
	Tasks struct {
		Active  []rpc.Task `json:"active"`
		Waiting []rpc.Task `json:"waiting"`
		Stopped []rpc.Task `json:"stopped"`
	} `json:"tasks"`
	TrayState struct {
		HasActive bool `json:"hasActive"`
		HasPaused bool `json:"hasPaused"`
		HasError  bool `json:"hasError"`
	} `json:"trayState"`
}

// GetFullSnapshot returns complete state snapshot (for window rebuild sync)
func (a *App) GetFullSnapshot() FullSnapshot {
	snapshot := FullSnapshot{}

	// 获取任务列表
	active, _ := rpc.TellActive()
	waiting, _ := rpc.TellWaiting(0, 50)

	snapshot.Tasks.Active = active
	snapshot.Tasks.Waiting = waiting

	// 仅在显示历史时获取 stopped
	if config.Current.ShowHistory {
		stopped, _ := rpc.TellStopped(0, 50)
		snapshot.Tasks.Stopped = stopped
	}

	// 获取托盘状态
	hasActive, hasPaused, hasError, _ := monitor.State.GetTrayState()
	snapshot.TrayState.HasActive = hasActive
	snapshot.TrayState.HasPaused = hasPaused
	snapshot.TrayState.HasError = hasError

	return snapshot
}

// --- Task Management ---

// RecordTaskSpeed 已废弃 - 后端 TaskTracker 自动采集
// 保留空实现以兼容现有前端
func (a *App) RecordTaskSpeed(gid string, speed int64, cl int64) {
	// 业务逻辑已迁移到 monitor.TaskTracker
	// 此方法保留以兼容前端，但不执行任何操作
}

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

	if config.Current.SmartThreadMode {
		// 智能模式：根据历史数据动态计算线程参数
		fileSize := rpc.HeadContentLength(url, 3*time.Second)

		maxConn, _ := strconv.Atoi(config.Current.MaxConnections)
		if maxConn <= 0 {
			maxConn = 16
		}

		params := smartthread.Calculate(fileSize, maxConn, normalizedUrl)
		gid, err := rpc.AddUriWithOptions(normalizedUrl, config.Current.DownloadDir, params.Split, params.MinSize)
		if err != nil {
			return err.Error()
		}

		if gid != "" && params.Split > 0 {
			// 通过 Monitor Tracker 注册线程信息
			if tracker := monitor.State.GetTracker(); tracker != nil {
				tracker.SetThreadInfo(gid, params.Split, params.IsExploration)
			}
		}
		return "success"
	}

	// 非智能模式，走原逻辑
	err := rpc.AddUri(url, config.Current.DownloadDir)
	if err != nil {
		return err.Error()
	}
	return "success"
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

	// 合并历史记录（仅用于 UI 展示）
	gidSet := make(map[string]bool)
	for _, t := range stopped {
		gidSet[t.GID] = true
	}

	for _, h := range history.GetAll() {
		if !gidSet[h.GID] {
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

		// 合并历史记录（仅用于 UI 展示）
		gidSet := make(map[string]bool)
		for _, t := range stopped {
			gidSet[t.GID] = true
		}

		for _, h := range history.GetAll() {
			if !gidSet[h.GID] {
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
// Strategy:
// 1) Resolve target path (file preferred, fallback to task.Dir) and normalize slashes.
// 2) Anchor relative paths to configured download dir and prevent escaping it.
// 3) If target is missing, walk up to nearest existing parent; fallback to download dir, then home.
// 4) Block path traversal: relative inputs are clamped to the download dir boundary.
// 5) Open with platform-specific explorer, selecting the file when possible.
func (a *App) OpenFolder(task rpc.Task) {
	// Prefer file path
	target := task.Dir
	if len(task.Files) > 0 && task.Files[0].Path != "" {
		target = task.Files[0].Path
	}

	resolveExistingDir := func(dir string) string {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return ""
		}
		dir = filepath.Clean(dir)
		for {
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		return ""
	}

	trimmed := strings.TrimSpace(target)
	cleanTarget := ""
	if trimmed != "" {
		cleanTarget = filepath.Clean(filepath.FromSlash(trimmed))
	}

	baseDir := strings.TrimSpace(config.Current.DownloadDir)
	if baseDir != "" {
		baseDir = filepath.Clean(filepath.FromSlash(baseDir))
	}

	absBase := ""
	if baseDir != "" {
		if b, err := filepath.Abs(baseDir); err == nil {
			absBase = b
		} else {
			absBase = baseDir
		}
	}

	absPath := ""
	if cleanTarget != "" {
		if filepath.IsAbs(cleanTarget) {
			if a, err := filepath.Abs(cleanTarget); err == nil {
				absPath = a
			} else {
				absPath = cleanTarget
			}
		} else if absBase != "" {
			joined := filepath.Clean(filepath.Join(absBase, cleanTarget))
			if a, err := filepath.Abs(joined); err == nil {
				absPath = a
			} else {
				absPath = joined
			}
		}
	}
	if absPath == "" {
		absPath = absBase
	}

	if cleanTarget != "" && !filepath.IsAbs(cleanTarget) && absBase != "" && absPath != "" {
		rel, err := filepath.Rel(absBase, absPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			absPath = absBase
		}
	}

	selectFile := ""
	openDir := ""
	if absPath != "" {
		if fi, err := os.Stat(absPath); err == nil {
			if fi.IsDir() {
				openDir = absPath
			} else {
				selectFile = absPath
				openDir = filepath.Dir(absPath)
			}
		} else {
			openDir = resolveExistingDir(filepath.Dir(absPath))
		}
	}
	if openDir == "" {
		openDir = resolveExistingDir(absBase)
	}
	if openDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			openDir = resolveExistingDir(home)
		}
	}

	if runtime.GOOS == "windows" {
		if selectFile != "" {
			_ = exec.Command("explorer.exe", "/select,", selectFile).Run()
			return
		}
		if openDir != "" {
			_ = exec.Command("explorer.exe", openDir).Run()
		}
		return
	}

	if runtime.GOOS == "darwin" {
		if selectFile != "" {
			_ = exec.Command("open", "-R", selectFile).Run()
			return
		}
		if openDir != "" {
			_ = exec.Command("open", openDir).Run()
		}
		return
	}

	if openDir != "" {
		_ = exec.Command("xdg-open", openDir).Run()
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
	resolveExistingDir := func(dir string) string {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return ""
		}
		dir = filepath.Clean(dir)
		for {
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		return ""
	}

	// Use the global app instance for dialog
	app := application.Get()
	if app == nil {
		return ""
	}

	startDir := resolveExistingDir(config.Current.DownloadDir)
	if startDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			startDir = resolveExistingDir(home)
		}
	}

	dlg := app.Dialog.OpenFile().
		SetTitle("选择下载目录").
		CanChooseDirectories(true).
		CanChooseFiles(false)
	if startDir != "" {
		dlg = dlg.SetDirectory(startDir)
	}

	result, err := dlg.PromptForSingleSelection()

	if err != nil || result == "" {
		return ""
	}
	return result
}
