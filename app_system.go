package main

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/extension"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/update"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type openFolderLaunchTarget struct {
	OpenDir    string
	SelectFile string
}

type openFolderCommandSpec struct {
	Name string
	Args []string
	Wait bool
}

var openFolderLauncher = launchOpenFolderTarget

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
	if launchTarget, ok := resolveOpenFolderLaunchTarget(target, config.Current.DownloadDir, true); ok {
		_ = openFolderLauncher(launchTarget)
	}
}

func resolveOpenFolderLaunchTarget(target string, downloadDir string, allowFallback bool) (openFolderLaunchTarget, bool) {
	target = strings.TrimSpace(target)
	downloadDir = strings.TrimSpace(downloadDir)
	if target == "" && downloadDir == "" && !allowFallback {
		return openFolderLaunchTarget{}, false
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

	baseDir := strings.TrimSpace(downloadDir)
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
	if openDir == "" {
		return openFolderLaunchTarget{}, false
	}

	return openFolderLaunchTarget{OpenDir: openDir, SelectFile: selectFile}, true
}

func launchOpenFolderTarget(target openFolderLaunchTarget) error {
	if target.OpenDir == "" && target.SelectFile == "" {
		return errors.New("folder unavailable")
	}
	spec, ok := openFolderCommandSpecForGOOS(runtime.GOOS, target)
	if !ok {
		return errors.New("folder unavailable")
	}
	cmd := exec.Command(spec.Name, spec.Args...)
	if spec.Wait {
		return cmd.Run()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func openFolderCommandSpecForGOOS(goos string, target openFolderLaunchTarget) (openFolderCommandSpec, bool) {
	if goos == "windows" {
		if target.SelectFile != "" {
			return openFolderCommandSpec{Name: "explorer.exe", Args: []string{"/select,", target.SelectFile}, Wait: false}, true
		}
		if target.OpenDir != "" {
			return openFolderCommandSpec{Name: "explorer.exe", Args: []string{target.OpenDir}, Wait: false}, true
		}
		return openFolderCommandSpec{}, false
	}

	if goos == "darwin" {
		if target.SelectFile != "" {
			return openFolderCommandSpec{Name: "open", Args: []string{"-R", target.SelectFile}, Wait: true}, true
		}
		if target.OpenDir != "" {
			return openFolderCommandSpec{Name: "open", Args: []string{target.OpenDir}, Wait: true}, true
		}
		return openFolderCommandSpec{}, false
	}

	if target.OpenDir != "" {
		return openFolderCommandSpec{Name: "xdg-open", Args: []string{target.OpenDir}, Wait: true}, true
	}
	return openFolderCommandSpec{}, false
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
	_ = a.downloadEngine.ChangeGlobalOption(map[string]string{
		"max-concurrent-downloads":  config.Current.MaxConcurrentDownloads,
		"max-connection-per-server": config.Current.MaxConnections,
		"user-agent":                config.Current.UserAgent,
	})
	return "success"
}

// --- Self-Update ---

// GetAppVersion returns the current application version
func (a *App) GetAppVersion() string {
	return version
}

// CheckForUpdate checks GitHub Releases for a newer version
func (a *App) CheckForUpdate(includePreRelease bool) update.UpdateResult {
	if a.eventHub != nil {
		a.eventHub.EmitUpdateStatus(update.StatusChecking, nil)
	}

	result, err := update.Check(version, includePreRelease)
	if err != nil {
		errResult := update.UpdateResult{
			Current: version,
			Error:   err.Error(),
		}
		if a.eventHub != nil {
			a.eventHub.EmitUpdateStatus(update.StatusError, err.Error())
		}
		return errResult
	}

	if result.Available {
		if a.eventHub != nil {
			a.eventHub.EmitUpdateStatus(update.StatusAvailable, result)
		}
	} else {
		if a.eventHub != nil {
			a.eventHub.EmitUpdateStatus(update.StatusIdle, nil)
		}
	}

	return *result
}

// ApplyUpdate starts downloading and applying the update in the background
func (a *App) ApplyUpdate(assetURL string, assetSize int64) string {
	if a.updater == nil {
		return "updater not initialized"
	}

	info := &update.ReleaseInfo{
		AssetURL:  assetURL,
		AssetSize: assetSize,
	}

	go func() {
		if err := a.updater.Apply(info); err != nil {
			log.Printf("[Update] Apply failed: %v", err)
		}
	}()

	return "started"
}

// RestartApp restarts the application to apply the update
func (a *App) RestartApp() {
	if a.updater != nil {
		if err := a.updater.Restart(); err != nil {
			log.Printf("[Update] Restart failed: %v", err)
		}
	}
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

// --- Browser Extension ---

// openURLInDefaultBrowser opens a URL in the system's default browser.
func openURLInDefaultBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "windows":
		name = "cmd"
		args = []string{"/c", "start", "", url}
	case "darwin":
		name = "open"
		args = []string{url}
	default:
		name = "xdg-open"
		args = []string{url}
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

// GetExtensionStatus returns the extension server status for the frontend.
func (a *App) GetExtensionStatus() extension.ExtensionStatus {
	if a.extensionServer == nil {
		return extension.ExtensionStatus{Status: "disconnected"}
	}
	return a.extensionServer.GetStatus()
}

// PairExtension starts the pairing flow and opens the pairing page in the default browser.
func (a *App) PairExtension() (string, error) {
	if a.extensionServer == nil {
		return "", errors.New("extension server not initialized")
	}
	ps := extension.NewPairingService(a.extensionServer.GetStore(), a.eventHub)
	a.extensionServer.SetPairingService(ps)

	url, err := ps.Start()
	if err != nil {
		return "", err
	}
	_ = openURLInDefaultBrowser(url)
	return url, nil
}

// UnpairExtension clears the secret and emits the unpaired event.
func (a *App) UnpairExtension() error {
	if a.extensionServer == nil {
		return errors.New("extension server not initialized")
	}
	a.extensionServer.NotifyUnpaired()
	return nil
}
