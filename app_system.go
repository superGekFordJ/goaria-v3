package main

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/update"

	"github.com/wailsapp/wails/v3/pkg/application"
)

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

// --- Self-Update ---

// GetAppVersion returns the current application version
func (a *App) GetAppVersion() string {
	return version
}

// CheckForUpdate checks GitHub Releases for a newer version
func (a *App) CheckForUpdate() update.UpdateResult {
	if a.eventHub != nil {
		a.eventHub.EmitUpdateStatus(update.StatusChecking, nil)
	}

	result, err := update.Check(version)
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
