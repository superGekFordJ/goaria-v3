package main

import (
	"sync"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/tray"
	"goaria-v3/internal/update"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// App struct for service bindings
//
// Methods are split across files by domain:
//   - app.go:        struct definition, constructor, setters
//   - app_window.go: window lifecycle, tray, snapshot
//   - app_tasks.go:  task CRUD, batch operations, metadata
//   - app_system.go: OpenFolder, config, self-update, directory picker
type App struct {
	app       *application.App
	window    *application.WebviewWindow
	systray   *application.SystemTray
	eventHub  *events.Hub
	updater   *update.Updater
	trayState tray.TrayState

	extractorDispatcher extractorAddTaskDispatcher
	authMu              sync.RWMutex
	authProfileStore    extractor.AuthProfileStore
	hostAuthRuntime     *extractor.HostAuthRuntime
	authWebViewDriver   extractor.AuthWebViewDriver

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
	} else {
		monitor.State.SetWindowExists(false)
	}
}

// SetSystemTray stores the system tray reference
func (a *App) SetSystemTray(st *application.SystemTray) {
	a.systray = st
}

func (a *App) setExtractorDispatcher(dispatcher extractorAddTaskDispatcher) {
	a.extractorDispatcher = dispatcher
}

func (a *App) setHostAuthState(store extractor.AuthProfileStore, runtime *extractor.HostAuthRuntime, driver extractor.AuthWebViewDriver) {
	if a == nil {
		return
	}
	a.authMu.Lock()
	defer a.authMu.Unlock()
	a.authProfileStore = store
	a.hostAuthRuntime = runtime
	a.authWebViewDriver = driver
}

func (a *App) authProfileStoreForTest() extractor.AuthProfileStore {
	if a == nil {
		return nil
	}
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	return a.authProfileStore
}

func (a *App) hostAuthRuntimeForTest() *extractor.HostAuthRuntime {
	return a.hostAuthRuntimeForTaskFlow()
}

func (a *App) hostAuthRuntimeForTaskFlow() *extractor.HostAuthRuntime {
	if a == nil {
		return nil
	}
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	return a.hostAuthRuntime
}

func (a *App) authWebViewDriverForTest() extractor.AuthWebViewDriver {
	if a == nil {
		return nil
	}
	a.authMu.RLock()
	defer a.authMu.RUnlock()
	return a.authWebViewDriver
}
