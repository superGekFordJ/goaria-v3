package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge"
	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/tasks"
	"goaria-v3/internal/tray"
	"goaria-v3/internal/update"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// version is injected at build time via -ldflags "-X main.version=..."
var version = "dev"

// 命令行参数
var (
	flagHidden = flag.Bool("hidden", false, "Start in headless mode (tray only, no window)")
)

var resumeScopeClassifier = speedstats.NewScopeClassifier()
//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	flag.Parse()

	// Initialize config, history, speedstats, and embedded extractor packs.
	config.Load()

	// Clean up old binary from previous update
	if exe, err := os.Executable(); err == nil {
		oldExe := exe + ".old"
		if _, err := os.Stat(oldExe); err == nil {
			_ = os.Remove(oldExe)
			log.Println("[Update] Cleaned up old binary:", oldExe)
		}
	}

	history.Load()
	speedstats.Load()
	monitor.LoadTaskGroups()

	// Initialize Surge DB & Session Self-Healing
	surge.Initialize(filepath.Dir(config.GetConfigPath()))

	// Create the App service for bindings and fail closed on invalid required embedded packs
	// before starting aria2, so extractor startup failure cannot leave the daemon running.
	appService := NewApp()
	configureEmbeddedExtractorDispatcher(appService)

	rpc.Init(config.Current.RPCPort, config.Current.RPCSecret)
	if err := process.StartAria2(config.Current); err != nil {
		log.Fatalf("failed to start bundled aria2: %v", err)
	}

	// Get the frontend/dist subdirectory
	frontendFS, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}

	// Create the application
	app := application.New(application.Options{
		Name:        "GoAria",
		Description: "High-performance download manager powered by Aria2",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(appService),
		},
		Assets: application.AssetOptions{
			Handler:    http.FileServer(http.FS(frontendFS)),
			Middleware: appService.hostAuthCallbackMiddleware,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "singleinstance-goaria-cf3e88a7f3c5",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				appService.ShowWindow()
			},
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		RawMessageHandler: appHostAuthRawMessageHandler,
	})

	// Initialize Event Hub (after app is created)
	eventHub := events.NewHub(app)

	// Start Aria2 WebSocket listener (after Aria2 is ready)
	go func() {
		if err := rpc.WaitForReady(5 * time.Second); err == nil {
			rpc.InitNotifier(eventHub, config.Current.RPCPort, config.Current.RPCSecret)
		}
	}()

	// Store app and event hub references for window creation
	appService.SetApp(app)
	appService.SetEventHub(eventHub)
	appService.updater = update.NewUpdater(eventHub)

	// Initialize extension WebSocket server (downloads go through tasks.Service)
	if config.Current.ExtensionEnabled {
		extStore := extension.NewSecretStore()
		extServer := extension.NewServer(eventHub, appService.taskService(), extStore)
		appService.SetExtensionServer(extServer)
		go func() {
			if err := extServer.Start(config.Current.ExtensionWSPort); err != nil {
				log.Printf("[Extension] WebSocket server failed to start: %v", err)
			}
		}()
	}

	// Create system tray (always created, even in headless mode)
	systray := app.SystemTray.New()
	appService.SetSystemTray(systray)
	systray.SetIcon(tray.GetIconForState(tray.StateIdle))
	systray.SetTooltip("GoAria - Download Manager")

	// Start backend monitor loop
	mon := monitor.New(app, eventHub, systray, appService.downloadEngine)
	monitor.State.SetMonitor(mon) // 注册到全局状态，供 RemoveTask 调用
	mon.Start()

	smartthread.SetActiveBandwidthProvider(monitor.ActiveBandwidthByScope)

	if hybrid, ok := appService.downloadEngine.(*rpc.HybridEngine); ok {
		if surgeEng, ok := hybrid.SurgeEngineRef(); ok {
			surgeEng.SetResumeParamsHook(func(cfg *types.DownloadConfig) {
				if !config.Current.SmartThreadMode {
					return
				}

				gid := "sg_" + cfg.ID
				tracker := monitor.State.GetTracker()
				if tracker == nil {
					return
				}
				scope, domain, prevEnvKey, ok := tracker.GetScopeAndEnv(gid)

				remaining := cfg.TotalSize
				if cfg.State != nil {
					downloaded := cfg.State.Downloaded.Load()
					if cfg.TotalSize > 0 && downloaded < cfg.TotalSize {
						remaining = cfg.TotalSize - downloaded
					}
				}
				if remaining <= 0 {
					return
				}

				// Re-probe TTFB and remote IP on resume; 1s timeout trades
				// accuracy for latency vs AddUri's 3s. Skipped for custom
				// headers (extracted/protected) to mirror the AddUri path.
				var resumeTTFB int64
				var remoteIP string
				if len(cfg.Headers) == 0 {
					probe := rpc.HeadProbe(cfg.URL, 1*time.Second)
					resumeTTFB = probe.TTFBMs
					remoteIP = probe.RemoteIP
				}

				// Preserve existing scope to keep it consistent with PeakEnvKey;
				// only classify on first-seen (restart recovery / external RPC).
				if !ok {
					if remoteIP != "" {
						scope, domain = resumeScopeClassifier.ClassifyByURLAndIP(cfg.URL, remoteIP)
					} else {
						scope, domain = resumeScopeClassifier.ClassifyByURL(cfg.URL)
					}
				}

				envKey := monitor.ComputeEnvKeyForDownload(cfg.URL, remoteIP)
				// On probe skip/failure, keep the prior envKey instead of
				// degrading to the proxy fallback — avoids cross-env pollution.
				if remoteIP == "" && ok && prevEnvKey != "" {
					envKey = prevEnvKey
				}

				maxConn, _ := strconv.Atoi(config.Current.MaxConnections)
				if maxConn <= 0 {
					maxConn = 8
				}

				params := smartthread.Calculate(smartthread.CalcParams{
					FileSize:          remaining,
					MaxConnections:    maxConn,
					Scope:             scope,
					Domain:            domain,
					EnvKey:            envKey,
					ReservedBandwidth: monitor.ActiveBandwidthByScope(scope, envKey),
				})
				if params.Split > 0 {
					cfg.Runtime.Workers = params.Split
				}
				if params.MinSize > 0 {
					cfg.Runtime.MinChunkSize = params.MinSize
				}
				tracker.SetScopeAndEnv(gid, scope, resumeTTFB, domain, envKey)
			})
		}
	}

	// Update shutdown handler to stop monitor
	app.OnShutdown(func() {
		mon.Stop()
		rpc.StopNotifier()
		rpc.ForceSaveSession()

		// Best-effort: stop extension WebSocket server
		if appService.extensionServer != nil {
			appService.extensionServer.Stop()
		}

		// Gracefully clean up Surge engine background workers
		if closer, ok := appService.downloadEngine.(interface{ Close() }); ok {
			closer.Close()
		}

		time.Sleep(500 * time.Millisecond)
		process.StopAria2()
	})

	// Conditionally create window based on --hidden flag
	if !*flagHidden {
		appService.CreateWindow()
	}

	// Handle tray click - toggle window
	systray.OnClick(func() {
		// 使用 goroutine 异步调用，避免阻塞 UI 主线程。
		// 关键原因：Wails 的 Window.NewWithOptions 创建窗口时需要主线程的消息循环来处理初始化。
		// 如果在此处同步调用，会占住主线程，导致窗口创建请求无法被处理（死锁），从而出现创建失败或 about:blank。
		go appService.ToggleWindow()
	})

	// Create tray menu (shown on right-click)
	trayMenu := app.NewMenu()
	trayMenu.Add("显示 GoAria").OnClick(func(ctx *application.Context) {
		appService.ShowWindow()
	})
	trayMenu.AddSeparator()
	trayMenu.Add("退出").OnClick(func(ctx *application.Context) {
		app.Quit()
	})
	systray.SetMenu(trayMenu)

	// Run the application
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func configureEmbeddedExtractorDispatcher(appService *App) {
	if err := configureEmbeddedExtractorDispatcherWithDeps(appService, defaultEmbeddedExtractorConfigDeps()); err != nil {
		log.Fatalf("failed to configure embedded extractor runtime: %v", extractor.RedactSensitive(err.Error()))
	}
}

type embeddedExtractorConfigDeps struct {
	hasEmbeddedReleasePacks             func() bool
	embeddedReleaseRequired             func() bool
	loadHostPolicyResolver              func() (extractor.HostPolicyResolver, error)
	loadAuthRuntimeBundle               func() (*extractor.PrivateAuthRuntimeBundle, error)
	defaultAuthProfileStorePath         func() (string, error)
	newFileAuthProfileStore             func(string) (extractor.AuthProfileStore, error)
	newAuthWebViewDriver                func(*App) extractor.AuthWebViewDriver
	newEmbeddedReleaseAddTaskDispatcher func(extractor.EmbeddedReleaseDispatcherConfig) (tasks.ExtractorAddTaskDispatcher, error)
}

func defaultEmbeddedExtractorConfigDeps() embeddedExtractorConfigDeps {
	return embeddedExtractorConfigDeps{
		hasEmbeddedReleasePacks:     extractor.HasEmbeddedReleasePacks,
		embeddedReleaseRequired:     extractor.EmbeddedReleaseRequired,
		loadHostPolicyResolver:      extractor.LoadPrivatePolicyBundleResolverFromRuntimeSources,
		loadAuthRuntimeBundle:       extractor.LoadPrivateAuthRuntimeBundleFromRuntimeSources,
		defaultAuthProfileStorePath: extractor.DefaultAuthProfileStorePath,
		newFileAuthProfileStore: func(path string) (extractor.AuthProfileStore, error) {
			return extractor.NewFileAuthProfileStore(path)
		},
		newAuthWebViewDriver: func(appService *App) extractor.AuthWebViewDriver {
			return newAppHostAuthDriver(appService)
		},
		newEmbeddedReleaseAddTaskDispatcher: func(config extractor.EmbeddedReleaseDispatcherConfig) (tasks.ExtractorAddTaskDispatcher, error) {
			return extractor.NewEmbeddedReleaseAddTaskDispatcher(config)
		},
	}
}

func configureEmbeddedExtractorDispatcherWithDeps(appService *App, deps embeddedExtractorConfigDeps) error {
	if appService == nil {
		return nil
	}
	deps = normalizeEmbeddedExtractorConfigDeps(deps)

	hasPacks := deps.hasEmbeddedReleasePacks()
	required := deps.embeddedReleaseRequired()
	authBundle, err := deps.loadAuthRuntimeBundle()
	if err != nil {
		return sanitizedEmbeddedExtractorConfigError("load auth runtime bundle", err)
	}
	hasAuthRuntime := authBundle != nil && authBundle.PackCount() > 0
	if !hasPacks && !required && !hasAuthRuntime {
		return nil
	}

	var hostPolicyResolver extractor.HostPolicyResolver
	if hasPacks || required {
		hostPolicyResolver, err = deps.loadHostPolicyResolver()
		if err != nil {
			return sanitizedEmbeddedExtractorConfigError("load host policy resolver", err)
		}
	}

	var store extractor.AuthProfileStore
	if hasPacks || hasAuthRuntime {
		storePath, err := deps.defaultAuthProfileStorePath()
		if err != nil {
			return sanitizedEmbeddedExtractorConfigError("locate auth profile store", err)
		}
		store, err = deps.newFileAuthProfileStore(storePath)
		if err != nil {
			return sanitizedEmbeddedExtractorConfigError("load auth profile store", err)
		}
		if store == nil {
			return sanitizedEmbeddedExtractorConfigError("load auth profile store", errors.New("auth profile store is nil"))
		}
	}

	var authResolver extractor.AuthProfileResolver
	var hostRuntime *extractor.HostAuthRuntime
	var driver extractor.AuthWebViewDriver
	if hasAuthRuntime {
		driver = deps.newAuthWebViewDriver(appService)
		if driver == nil {
			return sanitizedEmbeddedExtractorConfigError("create auth webview driver", errors.New("auth webview driver is nil"))
		}
		coordinator := extractor.NewWebViewAuthCoordinator(store, driver)
		hostRuntime = extractor.NewHostAuthRuntime(extractor.HostAuthRuntimeConfig{
			Bundle:             authBundle,
			Store:              store,
			Coordinator:        coordinator,
			HostPolicyResolver: hostPolicyResolver,
		})
		authResolver = hostRuntime
	} else if store != nil {
		authResolver = store
	}
	appService.setHostAuthState(store, hostRuntime, driver)

	dispatcher, err := deps.newEmbeddedReleaseAddTaskDispatcher(extractor.EmbeddedReleaseDispatcherConfig{
		AuthResolver:       authResolver,
		HostPolicyResolver: hostPolicyResolver,
		AuthRuntimeBundle:  authBundle,
	})
	if err != nil {
		return sanitizedEmbeddedExtractorConfigError("create embedded extractor dispatcher", err)
	}
	if dispatcher != nil {
		appService.setExtractorDispatcher(dispatcher)
	}

	return nil
}

func normalizeEmbeddedExtractorConfigDeps(deps embeddedExtractorConfigDeps) embeddedExtractorConfigDeps {
	defaults := defaultEmbeddedExtractorConfigDeps()
	if deps.hasEmbeddedReleasePacks == nil {
		deps.hasEmbeddedReleasePacks = defaults.hasEmbeddedReleasePacks
	}
	if deps.embeddedReleaseRequired == nil {
		deps.embeddedReleaseRequired = defaults.embeddedReleaseRequired
	}
	if deps.loadHostPolicyResolver == nil {
		deps.loadHostPolicyResolver = defaults.loadHostPolicyResolver
	}
	if deps.loadAuthRuntimeBundle == nil {
		deps.loadAuthRuntimeBundle = defaults.loadAuthRuntimeBundle
	}
	if deps.defaultAuthProfileStorePath == nil {
		deps.defaultAuthProfileStorePath = defaults.defaultAuthProfileStorePath
	}
	if deps.newFileAuthProfileStore == nil {
		deps.newFileAuthProfileStore = defaults.newFileAuthProfileStore
	}
	if deps.newAuthWebViewDriver == nil {
		deps.newAuthWebViewDriver = defaults.newAuthWebViewDriver
	}
	if deps.newEmbeddedReleaseAddTaskDispatcher == nil {
		deps.newEmbeddedReleaseAddTaskDispatcher = defaults.newEmbeddedReleaseAddTaskDispatcher
	}

	return deps
}

func sanitizedEmbeddedExtractorConfigError(action string, err error) error {
	return fmt.Errorf("%s failed", action)
}
