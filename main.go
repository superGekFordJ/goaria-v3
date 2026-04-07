package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
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

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	flag.Parse()

	// Initialize config, history, speedstats, and Aria2
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
	rpc.Init(config.Current.RPCPort, config.Current.RPCSecret)
	_ = process.StartAria2(config.Current)

	// Create the App service for bindings
	appService := NewApp()

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
			Handler: http.FileServer(http.FS(frontendFS)),
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

	// Create system tray (always created, even in headless mode)
	systray := app.SystemTray.New()
	appService.SetSystemTray(systray)
	systray.SetIcon(tray.GetIconForState(tray.StateIdle))
	systray.SetTooltip("GoAria - Download Manager")

	// Start backend monitor loop
	mon := monitor.New(app, eventHub, systray)
	monitor.State.SetMonitor(mon) // 注册到全局状态，供 RemoveTask 调用
	mon.Start()

	// Update shutdown handler to stop monitor
	app.OnShutdown(func() {
		mon.Stop()
		rpc.StopNotifier()
		rpc.ForceSaveSession()
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
