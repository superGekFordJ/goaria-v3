package main

import (
	"embed"
	"flag"
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
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/tasks"
	"goaria-v3/internal/tray"
	"goaria-v3/internal/wailsapp"

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
	monitor.LoadTaskGroups()
	speedstats.Load()

	// Initialize Surge DB & Session Self-Healing
	surge.Initialize(filepath.Dir(config.GetConfigPath()))

	// Create the App service for bindings and fail closed on invalid required embedded packs
	// before starting aria2, so extractor startup failure cannot leave the daemon running.
	hybrid := rpc.NewHybridEngine(
		&rpc.Aria2Engine{},
		rpc.NewSurgeEngine(),
	)
	appService := wailsapp.NewApp(wailsapp.Options{
		DownloadEngine: hybrid,
		Version:        version,
	})
	if err := wailsapp.ConfigureEmbeddedExtractorDispatcher(appService); err != nil {
		log.Fatalf("failed to configure embedded extractor runtime: %v", err)
	}

	rpc.Init(config.Get().RPCPort, config.Get().RPCSecret)
	if err := process.StartAria2(config.Get()); err != nil {
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
			Middleware: wailsapp.HostAuthCallbackMiddleware(appService),
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "singleinstance-goaria-cf3e88a7f3c5",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				appService.ShowWindow()
			},
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
			AdditionalBrowserArgs:         []string{"--enable-smooth-scrolling"},
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		// RawMessageHandler is the only collector→host transport that survives
		// Mixed Content and CSP restrictions when the auth WebView navigates to
		// a third-party login page. The handler ignores anything that does not
		// match the "goaria-auth-diag:" prefix, so it is safe to register
		// globally even though the main app window also dispatches Wails IPC.
		RawMessageHandler: wailsapp.HostAuthRawMessageHandler,
	})

	// Initialize Event Hub (after app is created)
	eventHub := events.NewHub(app)

	// Start Aria2 WebSocket listener (after Aria2 is ready)
	go func() {
		if err := rpc.WaitForReady(5 * time.Second); err == nil {
			rpc.InitNotifier(eventHub, config.Get().RPCPort, config.Get().RPCSecret)
		}
	}()

	// Store app and event hub references for window creation
	appService.SetApp(app)
	appService.SetEventHub(eventHub)
	wailsapp.ConfigureUpdater(appService, eventHub)

	// Initialize extension WebSocket server (downloads go through tasks.Service)
	var extServer *extension.Server
	if config.Get().ExtensionEnabled {
		extServer = wailsapp.NewExtensionServer(eventHub, appService, config.Get().ExtensionSecret)
		appService.SetExtensionServer(extServer)
		wailsapp.ConfigureExtensionLinkage(appService, extServer)
		go func() {
			if err := extServer.Start(config.Get().ExtensionWSPort); err != nil {
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
	mon := monitor.New(app, eventHub, systray, hybrid)
	monitor.State.SetMonitor(mon) // 注册到全局状态，供 RemoveTask 调用
	mon.Start()

	smartthread.SetActiveBandwidthProvider(monitor.MacroBandwidthByScope)

	if surgeEng, ok := hybrid.SurgeEngineRef(); ok {
		surgeEng.SetResumeParamsHook(func(cfg *types.DownloadRecord) {
			if !config.Get().SmartThreadMode {
				return
			}

			gid := "sg_" + cfg.ID
			tracker := monitor.State.GetTracker()
			if tracker == nil {
				return
			}
			scope, domain, prevEnvKey, ok := tracker.GetScopeAndEnv(gid)

			remaining := cfg.TotalSize
			downloaded := cfg.Downloaded
			if cp := progress.CfgProgress(cfg); cp != nil {
				d, _, _, _, _, _ := cp.GetProgress()
				downloaded = d
			}
			if cfg.TotalSize > 0 && downloaded < cfg.TotalSize {
				remaining = cfg.TotalSize - downloaded
			}
			if remaining <= 0 {
				return
			}

			// Re-probe TTFB and remote IP on resume; 1s timeout trades
			// accuracy for latency vs AddUri's 3s. Skipped for custom
			// headers, skip-origin, and payload-first (presigned URLs).
			var resumeTTFB int64
			var remoteIP string
			if !tasks.ShouldSkipResumeHeadProbe(cfg) {
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

			maxConn, _ := strconv.Atoi(config.Get().MaxConnections)
			if maxConn <= 0 {
				maxConn = 8
			}

			occupancyLedger := smartthread.NewBandwidthLedger(tasks.BuildOccupancyTaskInfos())
			params := smartthread.Calculate(smartthread.CalcParams{
				FileSize:                remaining,
				MaxConnections:          maxConn,
				Scope:                   scope,
				Domain:                  domain,
				EnvKey:                  envKey,
				ReservedBandwidth:       monitor.MacroBandwidthByScope(scope, envKey),
				ReservedDomainBandwidth: occupancyLedger.ReservedByDomain(scope, domain),
				// Ledger/ActiveMACsFunc/ComputeEnvKeyFunc left nil:
				// Resume path degrades to logical-only ceiling (no batch ledger).
			})
			params = smartthread.ClampToServerLimit(params, remaining, scope, domain,
				tasks.ExistingDomainWorkersFromTelemetry(scope, domain),
				smartthread.GetDefaultServerLimits())
			if cfg.Runtime == nil {
				cfg.Runtime = &types.RuntimeConfig{}
			}
			if params.Split > 0 {
				cfg.Runtime.Workers = params.Split
			}
			if params.MinSize > 0 {
				cfg.Runtime.MinChunkSize = params.MinSize
			}
			tracker.SetScopeAndEnv(gid, scope, resumeTTFB, domain, envKey)
			if params.Split > 0 || params.TargetBandwidth > 0 {
				tracker.SetTargetBandwidth(gid, params.TargetBandwidth)
			}
		})
		surgeEng.SetTightenOnPickupHook(func(cfg *types.DownloadRecord) {
			tasks.ApplyPickupTighten(cfg)
		})
	}

	// Update shutdown handler to stop monitor
	app.OnShutdown(func() {
		mon.Stop()
		rpc.StopNotifier()
		rpc.ForceSaveSession()

		// Best-effort: stop extension WebSocket server
		if extServer != nil {
			extServer.Stop()
		}

		// Gracefully clean up Surge engine background workers
		hybrid.Close()

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
