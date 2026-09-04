package wailsapp

import (
	"sync"
	"time"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/events"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tasks"
	"goaria-v3/internal/tray"
	"goaria-v3/internal/update"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// App struct for service bindings
//
// Methods are split across files by domain:
//   - app.go:    struct definition, constructor, setters
//   - window.go: window lifecycle, tray, snapshot
//   - tasks.go:  task CRUD, batch operations, metadata
//   - system.go: OpenFolder, config, self-update, directory picker
type App struct {
	app       *application.App
	window    *application.WebviewWindow
	systray   *application.SystemTray
	eventHub  *events.Hub
	updater   *update.Updater
	trayState tray.TrayState
	version   string

	extractorRuntime  extractorRuntimeProvider
	authMu            sync.RWMutex             //nolint:unused,nolintlint
	authProfileStore  hostAuthProfileStore     //nolint:unused,nolintlint
	hostAuthRuntime   hostAuthRuntime          //nolint:unused,nolintlint
	authWebViewDriver hostAuthDriver           //nolint:unused,nolintlint
	hostAuthCallbacks hostAuthCallbackRegistry //nolint:unused,nolintlint

	windowMu       sync.Mutex // 保护窗口操作
	lastToggleTime time.Time  // 上次切换窗口时间，用于全局防抖
	isToggling     bool       // 防止重入标志

	// Windows tray reclaim: debounced GC after successful DestroyWindow
	reclaimMu    sync.Mutex
	reclaimTimer *time.Timer

	downloadEngine  rpc.DownloadEngine
	extensionServer *extension.Server

	pendingMu               sync.Mutex         //nolint:unused,nolintlint
	pendingExtensionLinkage *extension.Linkage //nolint:unused,nolintlint

	configSaveMu sync.Mutex
	configDeps   configSaveDeps
}

type Options struct {
	DownloadEngine rpc.DownloadEngine
	Version        string
}

type downloadGroupMultiResultEngine interface {
	PauseMultiResults(gids []string) ([]rpc.MultiCallItemResult, error)
	ResumeMultiResults(gids []string) ([]rpc.MultiCallItemResult, error)
}

func wrapMultiResults(fn func(gids []string) error) func(gids []string) ([]rpc.MultiCallItemResult, error) {
	return func(gids []string) ([]rpc.MultiCallItemResult, error) {
		results := make([]rpc.MultiCallItemResult, 0, len(gids))
		if len(gids) == 0 {
			return results, nil
		}
		if err := fn(gids); err != nil {
			for _, gid := range gids {
				results = append(results, rpc.MultiCallItemResult{GID: gid, OK: false, Error: err.Error()})
			}
			return results, nil
		}
		for _, gid := range gids {
			results = append(results, rpc.MultiCallItemResult{GID: gid, OK: true})
		}
		return results, nil
	}
}

// NewApp creates a new App instance
func NewApp(options Options) *App {
	downloadgroups.OpenFolderLauncher = func(dir string) error {
		return openFolderLauncher(openFolderLaunchTarget{OpenDir: dir})
	}
	if engine, ok := options.DownloadEngine.(downloadGroupMultiResultEngine); ok {
		downloadgroups.PauseMultiResults = engine.PauseMultiResults
		downloadgroups.ResumeMultiResults = engine.ResumeMultiResults
	} else if options.DownloadEngine != nil {
		downloadgroups.PauseMultiResults = wrapMultiResults(options.DownloadEngine.PauseMulti)
		downloadgroups.ResumeMultiResults = wrapMultiResults(options.DownloadEngine.ResumeMulti)
	} else {
		downloadgroups.PauseMultiResults = rpc.PauseMultiResults
		downloadgroups.ResumeMultiResults = rpc.UnpauseMultiResults
	}

	version := options.Version
	if version == "" {
		version = "dev"
	}

	app := &App{
		trayState:      tray.StateIdle,
		version:        version,
		downloadEngine: options.DownloadEngine,
	}
	app.configDeps = defaultConfigSaveDeps(app)
	return app
}

func ConfigureUpdater(appService *App, eventHub *events.Hub) {
	if appService == nil {
		return
	}
	appService.updater = update.NewUpdater(eventHub)
}

func NewExtensionServer(eventHub *events.Hub, appService *App, secret string) *extension.Server {
	extStore := extension.NewSecretStore()
	extStore.SetSecret(secret)
	s := extension.NewServer(eventHub, appService.taskService(), extStore)
	s.SetHostVersion(appService.GetAppVersion())
	return s
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

// SetExtensionServer stores the extension WebSocket server reference
func (a *App) SetExtensionServer(s *extension.Server) {
	a.extensionServer = s
}

func (a *App) setExtractorRuntime(provider extractorRuntimeProvider) {
	a.extractorRuntime = provider
}

func (a *App) setExtractorAdapter(adapter tasks.ExtractorAdapter) { //nolint:unused,nolintlint
	if adapter == nil {
		a.extractorRuntime = nil
		return
	}
	a.extractorRuntime = simpleAdapterProvider{adapter: adapter}
}

func (a *App) extractorAdapterForTest() tasks.ExtractorAdapter { //nolint:unused,nolintlint
	if a.extractorRuntime == nil {
		return nil
	}
	return a.extractorRuntime.currentTasksAdapter()
}

func (a *App) setPendingExtensionLinkage(l extension.Linkage) { //nolint:unused,nolintlint
	cp := l
	a.pendingMu.Lock()
	a.pendingExtensionLinkage = &cp
	a.pendingMu.Unlock()
}
