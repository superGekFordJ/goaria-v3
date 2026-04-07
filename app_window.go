package main

import (
	"log"
	"runtime"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tray"

	"github.com/wailsapp/wails/v3/pkg/application"
)

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

	switch {
	case !hasWindow:
		// 无窗口，创建新窗口
		a.CreateWindow()
	case isVisible:
		// 窗口可见，销毁窗口（真无头模式）
		a.DestroyWindow()
	default:
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

// --- Tray ---

// UpdateTrayState updates the tray icon based on download state
func (a *App) UpdateTrayState(hasActive, hasPaused, hasError bool) {
	var newState tray.TrayState
	switch {
	case hasError:
		newState = tray.StateError
	case hasActive:
		newState = tray.StateActive
	case hasPaused:
		newState = tray.StatePaused
	default:
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
	hasActive, hasPaused, hasError, _, _ := monitor.State.GetTrayState()
	snapshot.TrayState.HasActive = hasActive
	snapshot.TrayState.HasPaused = hasPaused
	snapshot.TrayState.HasError = hasError

	return snapshot
}
