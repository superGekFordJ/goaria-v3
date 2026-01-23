package main

import (
	"embed"
	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/tray"
	"io/fs"
	"log"
	"net/http"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	// Initialize config, history, speedstats, and Aria2
	config.Load()
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
	})

	// Initialize Event Hub (after app is created)
	eventHub := events.NewHub(app)

	// Start Aria2 WebSocket listener (after Aria2 is ready)
	go func() {
		if err := rpc.WaitForReady(5 * time.Second); err == nil {
			rpc.InitNotifier(eventHub, config.Current.RPCPort, config.Current.RPCSecret)
		}
	}()

	// Set shutdown handler
	app.OnShutdown(func() {
		// Save session and stop Aria2 on shutdown
		rpc.StopNotifier()
		rpc.ForceSaveSession()
		time.Sleep(500 * time.Millisecond)
		process.StopAria2()
	})

	// Determine window background/backdrop configuration
	backgroundType := application.BackgroundTypeSolid
	backgroundColour := application.NewRGBA(12, 12, 15, 255)
	backdropType := application.Auto
	macBackdrop := application.MacBackdropNormal

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
	default:
		backgroundType = application.BackgroundTypeSolid
		backgroundColour = application.NewRGBA(12, 12, 15, 255)
		backdropType = application.Auto
		macBackdrop = application.MacBackdropNormal
	}

	// Linux currently only supports solid background
	if runtime.GOOS == "linux" {
		backgroundType = application.BackgroundTypeSolid
		backgroundColour = application.NewRGBA(12, 12, 15, 255)
		backdropType = application.Auto
		macBackdrop = application.MacBackdropNormal
	}

	log.Printf(
		"window_transparency=%s backgroundType=%v windowsBackdropType=%v",
		config.Current.WindowTransparency,
		backgroundType,
		backdropType,
	)

	// Create the main window using Window manager
	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "GoAria",
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

	// Store window reference for the app service
	appService.SetWindow(mainWindow)

	// Create system tray using SystemTray manager
	systray := app.SystemTray.New()

	// Store systray reference in app service for dynamic icon updates
	appService.SetSystemTray(systray)

	// Set initial tray icon (idle state) and tooltip
	systray.SetIcon(tray.GetIconForState(tray.StateIdle))
	systray.SetTooltip("GoAria - Download Manager")

	// Handle left-click on tray icon - toggle window visibility
	systray.OnClick(func() {
		if mainWindow.IsVisible() {
			mainWindow.Hide()
		} else {
			mainWindow.Show()
			mainWindow.Focus()
		}
	})

	// Create tray menu (shown on right-click)
	trayMenu := app.NewMenu()

	trayMenu.Add("显示 GoAria").OnClick(func(ctx *application.Context) {
		mainWindow.Show()
		mainWindow.Focus()
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
