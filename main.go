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
	"strings"
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
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/tasks"
	"goaria-v3/internal/tray"
	"goaria-v3/internal/wailsapp"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
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
	// If launched as an updater helper, perform binary swap and relaunch immediately
	// before initializing configs, background daemons, SQLite databases, or aria2.
	updater.HandleHelperMode()

	flag.Parse()

	// Initialize config, history, speedstats, and embedded extractor packs.
	config.Load()

	// Clean up old binary and update asides from previous updates
	if exe, err := os.Executable(); err == nil {
		sweepOldBinaries(exe)
		// Retry delayed sweep in background in case Windows kernel holds transient file locks during restart
		go func(p string) {
			time.Sleep(3 * time.Second)
			sweepOldBinaries(p)
		}(exe)
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

	launchCfg := *config.Get()
	launchAttempt := wailsapp.RegisterAriaStartup(appService)
	rpc.Init(launchCfg.RPCPort, launchCfg.RPCSecret)
	if err := process.StartAria2(&launchCfg); err != nil {
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

	// Start Aria2 WebSocket listener (after Aria2 is ready). The notifier is
	// only initialized if no newer activation superseded this launch meanwhile.
	go func() {
		wailsapp.CompleteAriaStartup(appService, eventHub, launchAttempt, launchCfg, rpc.WaitForReady(5*time.Second))
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

	smartthread.SetActiveBandwidthProvider(monitor.MacroOccupancyByScope)

	// The resident admission controller owns managed-pool dispatch for the
	// app lifetime. All wake edges (busy-release, monitor facts, config
	// persistence) only notify; evaluation is serialized on its goroutine.
	var admissionCtl *tasks.AdmissionController
	if surgeEng, ok := hybrid.SurgeEngineRef(); ok {
		surgeEng.SetResumeParamsHook(func(cfg *types.DownloadRecord) {
			gid := "sg_" + cfg.ID
			tracker := monitor.State.GetTracker()
			if tracker == nil {
				return
			}
			scope, domain, prevEnvKey, ok := tracker.GetScopeAndEnv(gid)

			// Re-probe TTFB and remote IP on resume in smart mode; 1s timeout
			// trades accuracy for latency vs AddUri's 3s. Skipped for custom
			// headers, skip-origin, payload-first (presigned URLs), and when
			// smart mode is off — classification still runs so a resume
			// missing basic grouping metadata gets prepared.
			var resumeTTFB int64
			var remoteIP string
			if config.Get().SmartThreadMode && !tasks.ShouldSkipResumeHeadProbe(cfg) {
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

			tracker.SetScopeAndEnv(gid, scope, resumeTTFB, domain, envKey)
		})

		admissionCtl = tasks.NewAdmissionController(surgeEng)
		config.SetSaveKick(admissionCtl.Kick)
		mon.SetAdmissionKick(admissionCtl.Kick)
		surgeEng.SetAdmissionWake(admissionCtl.Kick)
		admissionCtl.Start()
	}

	// Update shutdown handler to stop monitor
	app.OnShutdown(func() {
		// Stop admission entrypoints and join the controller goroutine first;
		// Stop returns only after the loop can no longer issue Admit calls.
		if admissionCtl != nil {
			admissionCtl.Stop()
		}
		config.SetSaveKick(nil)
		mon.SetAdmissionKick(nil)
		if surgeEng, ok := hybrid.SurgeEngineRef(); ok {
			surgeEng.SetAdmissionWake(nil)
		}

		// Drain the engine before stopping the monitor so lifecycle facts
		// keep flowing while the scheduler finishes physical claims.
		hybrid.Close()
		mon.Stop()

		rpc.StopNotifier()
		rpc.ForceSaveSession()

		// Best-effort: stop extension WebSocket server
		if extServer != nil {
			extServer.Stop()
		}

		time.Sleep(500 * time.Millisecond)

		// Flush download history store to disk. Placed after all monitor
		// runLoops, workers, and background session flushes have fully joined
		// before process termination, ensuring all terminal task events have
		// been delivered to history.Add prior to disk flush.
		if err := history.Flush(); err != nil {
			log.Printf("[Shutdown] Failed to flush history: %v", err)
		}

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

func sweepOldBinaries(exe string) {
	if exe == "" {
		return
	}

	safeRemoveFile := func(path string) {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return
		}
		if err := os.Remove(path); err == nil {
			log.Println("[Update] Cleaned up old binary aside:", path)
		}
	}

	exeDir := filepath.Dir(exe)
	exeName := filepath.Base(exe)

	// 1. Clean direct legacy .old and Wails 3 backup .bak files
	safeRemoveFile(exe + ".old")
	safeRemoveFile(exe + ".bak")

	// 2. Target specific executable names (current exe, plus cross-compatibility for goaria.exe / goaria-v3.exe)
	targetBases := []string{exeName}
	switch exeName {
	case "goaria.exe":
		targetBases = append(targetBases, "goaria-v3.exe")
	case "goaria-v3.exe":
		targetBases = append(targetBases, "goaria.exe")
	}

	for _, base := range targetBases {
		pattern := filepath.Join(exeDir, base+".old.*")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			suffix := strings.TrimPrefix(m, filepath.Join(exeDir, base+".old."))
			// Only sweep files generated by Wails 3 Helper with UnixNano numeric timestamps
			if _, err := strconv.ParseInt(suffix, 10, 64); err == nil {
				safeRemoveFile(m)
			}
		}
	}
}
