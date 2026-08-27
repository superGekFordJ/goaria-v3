package wailsapp

import (
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
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
	if launchTarget, ok := resolveOpenFolderLaunchTarget(target, config.Get().DownloadDir, true); ok {
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

const (
	errCodePersistFailed           = "config_persist_failed"
	errCodeNotLoaded               = "config_not_loaded"
	errCodeDownloadDirUnavailable  = "download_dir_unavailable"
	errCodeRPCExtensionPort        = "rpc_extension_port_conflict"
	errCodeAriaRestartRolledBack   = "aria2_restart_failed_rolled_back"
	errCodeAriaReadinessRolledBack = "aria2_readiness_failed_rolled_back"
	errCodeConfigRollbackFailed    = "config_rollback_failed"
	errCodeAriaRollbackFailed      = "aria2_rollback_failed"

	msgPersistFailed          = "Failed to save configuration."
	msgConfigNotLoaded        = "Configuration is not loaded."
	msgDownloadDirUnavailable = "Download directory is unavailable."
	msgRPCExtensionConflict   = "RPC port conflicts with the extension listener."
	msgAriaRestartRolledBack  = "The download engine failed to restart. Previous settings were restored."
	msgAriaReadyRolledBack    = "The download engine was not ready. Previous settings were restored."
	msgConfigRollbackFailed   = "Failed to restore the previous configuration."
	msgAriaRollbackFailed     = "Failed to restore the previous download engine."
)

// SaveConfigResult is the structured outcome of a configuration save.
type SaveConfigResult struct {
	Success            bool             `json:"success"`
	Config             config.AppConfig `json:"config"`
	Aria2Restarted     bool             `json:"aria2_restarted"`
	RequiresAppRestart bool             `json:"requires_app_restart"`
	ErrorCode          string           `json:"error_code,omitempty"`
	Message            string           `json:"message,omitempty"`
}

type aria2LaunchProjection struct {
	RPCPort                string
	RPCSecret              string
	DownloadDir            string
	MaxConcurrentDownloads string
	UserAgent              string
	MaxConnections         int
}

type configSaveDeps struct {
	get             func() *config.AppConfig
	updateChecked   func(func(*config.AppConfig)) (config.UpdateResult, error)
	validateDir     func(string) error
	restartAria2    func(*config.AppConfig) error
	stopAria2       func()
	rpcInit         func(port, secret string)
	waitForReady    func(time.Duration) error
	initNotifier    func(hub *events.Hub, port, secret string)
	extensionStatus func() (wsPort int, listening bool)
}

type ariaActivateError struct {
	kind string
	err  error
}

func (e ariaActivateError) Error() string {
	if e.err != nil {
		return e.kind + ": " + e.err.Error()
	}
	return e.kind
}

func (e ariaActivateError) Unwrap() error {
	return e.err
}

func defaultConfigSaveDeps(a *App) configSaveDeps {
	return configSaveDeps{
		get:           config.Get,
		updateChecked: config.UpdateChecked,
		validateDir:   process.ValidateDownloadDir,
		restartAria2:  process.RestartAria2,
		stopAria2:     process.StopAria2,
		rpcInit:       rpc.Init,
		waitForReady:  rpc.WaitForReady,
		initNotifier:  rpc.InitNotifier,
		extensionStatus: func() (int, bool) {
			return a.liveExtensionStatus()
		},
	}
}

func (a *App) liveExtensionStatus() (wsPort int, listening bool) {
	if a == nil || a.extensionServer == nil {
		return 0, false
	}
	st := a.extensionServer.GetStatus()
	switch st.Status {
	case "listening", "paired":
		return st.WSPort, true
	default:
		return st.WSPort, false
	}
}

func ariaProjection(cfg config.AppConfig) aria2LaunchProjection {
	sanitized := config.ValidateAndSanitize(cfg)
	return aria2LaunchProjection{
		RPCPort:                sanitized.RPCPort,
		RPCSecret:              sanitized.RPCSecret,
		DownloadDir:            sanitized.DownloadDir,
		MaxConcurrentDownloads: sanitized.MaxConcurrentDownloads,
		UserAgent:              sanitized.UserAgent,
		MaxConnections:         process.EffectiveAria2MaxConnections(sanitized.MaxConnections),
	}
}

func requiresAppRestart(oldCfg, newCfg config.AppConfig) bool {
	return oldCfg.WindowTransparency != newCfg.WindowTransparency ||
		oldCfg.MaxConnections != newCfg.MaxConnections ||
		oldCfg.ConvergenceInterval != newCfg.ConvergenceInterval ||
		oldCfg.MaxConcurrentDownloads != newCfg.MaxConcurrentDownloads ||
		oldCfg.ExtensionEnabled != newCfg.ExtensionEnabled ||
		oldCfg.ExtensionWSPort != newCfg.ExtensionWSPort
}

func liveSnapshot(deps configSaveDeps, fallback config.AppConfig) config.AppConfig {
	if ptr := deps.get(); ptr != nil {
		return *ptr
	}
	return fallback
}

func daemonWasReplaced(activateErr error) bool {
	var ae ariaActivateError
	if !errors.As(activateErr, &ae) || ae.kind != "restart" {
		return true
	}
	var re *process.Aria2RestartError
	if errors.As(ae.err, &re) && re != nil && !re.Stopped {
		return false
	}
	return true
}

func (a *App) bindRPC(deps configSaveDeps, cfg config.AppConfig) error {
	if deps.rpcInit != nil {
		deps.rpcInit(cfg.RPCPort, cfg.RPCSecret)
	}
	if deps.waitForReady != nil {
		if err := deps.waitForReady(4 * time.Second); err != nil {
			return err
		}
	}
	if a.eventHub != nil && deps.initNotifier != nil {
		deps.initNotifier(a.eventHub, cfg.RPCPort, cfg.RPCSecret)
	}
	return nil
}

func failedSaveResult(code string, cfg config.AppConfig, message string, old config.AppConfig) SaveConfigResult {
	return SaveConfigResult{
		Success:            false,
		Config:             cfg,
		ErrorCode:          code,
		Message:            message,
		RequiresAppRestart: requiresAppRestart(old, cfg),
	}
}

func successSaveResult(cfg config.AppConfig, ariaRestarted, appRestart bool) SaveConfigResult {
	return SaveConfigResult{
		Success:            true,
		Config:             cfg,
		Aria2Restarted:     ariaRestarted,
		RequiresAppRestart: appRestart,
	}
}

// GetConfig returns the current configuration
func (a *App) GetConfig() *config.AppConfig {
	cur := config.Get()
	if cur == nil {
		return nil
	}
	cp := *cur
	return &cp
}

func (a *App) saveDeps() configSaveDeps {
	if a.configDeps.get != nil {
		return a.configDeps
	}
	return defaultConfigSaveDeps(a)
}

// SaveConfig persists a canonical snapshot and restarts Aria2 only when the
// effective launch projection changes.
func (a *App) SaveConfig(request config.AppConfig) SaveConfigResult {
	a.configSaveMu.Lock()
	defer a.configSaveMu.Unlock()

	deps := a.saveDeps()
	oldPtr := deps.get()
	if oldPtr == nil {
		return failedSaveResult(errCodeNotLoaded, config.AppConfig{}, msgConfigNotLoaded, config.AppConfig{})
	}
	old := *oldPtr
	candidate := config.ValidateAndSanitize(request)
	candidate.ExtensionSecret = old.ExtensionSecret
	if candidate == old {
		return successSaveResult(liveSnapshot(deps, old), false, false)
	}

	if candidate.DownloadDir != old.DownloadDir {
		if err := deps.validateDir(candidate.DownloadDir); err != nil {
			log.Printf("[Config] download dir preflight failed: %v", err)
			return failedSaveResult(errCodeDownloadDirUnavailable, liveSnapshot(deps, old), msgDownloadDirUnavailable, old)
		}
	}

	if wsPort, listening := deps.extensionStatus(); listening && wsPort != 0 {
		if candidate.RPCPort == strconv.Itoa(wsPort) {
			return failedSaveResult(errCodeRPCExtensionPort, liveSnapshot(deps, old), msgRPCExtensionConflict, old)
		}
	}

	update, err := deps.updateChecked(func(current *config.AppConfig) {
		managedSecret := current.ExtensionSecret
		*current = candidate
		current.ExtensionSecret = managedSecret
	})
	if err != nil {
		return failedSaveResult(errCodePersistFailed, liveSnapshot(deps, update.Current), msgPersistFailed, old)
	}

	committed := update.Current
	needApp := requiresAppRestart(old, committed)
	if ariaProjection(old) == ariaProjection(committed) {
		return successSaveResult(liveSnapshot(deps, committed), false, needApp)
	}
	if err := a.activateAria(deps, committed); err != nil {
		return a.rollbackConfigAndAria(deps, old, committed, err)
	}
	return successSaveResult(liveSnapshot(deps, committed), true, needApp)
}

func (a *App) activateAria(deps configSaveDeps, cfg config.AppConfig) error {
	if err := deps.restartAria2(&cfg); err != nil {
		return ariaActivateError{kind: "restart", err: err}
	}
	if err := a.bindRPC(deps, cfg); err != nil {
		return ariaActivateError{kind: "ready", err: err}
	}
	return nil
}

func (a *App) rollbackConfigAndAria(deps configSaveDeps, old, candidate config.AppConfig, activateErr error) SaveConfigResult {
	code := errCodeAriaRestartRolledBack
	msg := msgAriaRestartRolledBack
	var ae ariaActivateError
	if errors.As(activateErr, &ae) && ae.kind == "ready" {
		code = errCodeAriaReadinessRolledBack
		msg = msgAriaReadyRolledBack
	}

	rollback, err := deps.updateChecked(func(current *config.AppConfig) {
		managedSecret := current.ExtensionSecret
		*current = old
		current.ExtensionSecret = managedSecret
	})
	if err != nil {
		snap := liveSnapshot(deps, candidate)
		if snap == (config.AppConfig{}) && rollback.Current != (config.AppConfig{}) {
			snap = rollback.Current
		}
		return failedSaveResult(errCodeConfigRollbackFailed, snap, msgConfigRollbackFailed, old)
	}

	restored := rollback.Current
	if daemonWasReplaced(activateErr) {
		if err := deps.restartAria2(&restored); err != nil {
			var re *process.Aria2RestartError
			if errors.As(err, &re) && re != nil && !re.Stopped && deps.stopAria2 != nil {
				deps.stopAria2()
			}
			if deps.rpcInit != nil {
				deps.rpcInit(restored.RPCPort, restored.RPCSecret)
			}
			return failedSaveResult(errCodeAriaRollbackFailed, liveSnapshot(deps, restored), msgAriaRollbackFailed, old)
		}
	}
	if err := a.bindRPC(deps, restored); err != nil {
		return failedSaveResult(errCodeAriaRollbackFailed, liveSnapshot(deps, restored), msgAriaRollbackFailed, old)
	}
	return failedSaveResult(code, liveSnapshot(deps, restored), msg, old)
}

// GetAria2Connected returns the current Aria2 WebSocket connection status
func (a *App) GetAria2Connected() bool {
	return rpc.IsAria2Connected()
}

// --- Self-Update ---

// GetAppVersion returns the current application version
func (a *App) GetAppVersion() string {
	return a.version
}

// CheckForUpdate checks GitHub Releases for a newer version
func (a *App) CheckForUpdate(includePreRelease bool) update.UpdateResult {
	if a.eventHub != nil {
		a.eventHub.EmitUpdateStatus(update.StatusChecking, nil)
	}

	result, err := update.Check(a.version, includePreRelease)
	if err != nil {
		errResult := update.UpdateResult{
			Current: a.version,
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
	if a.updater == nil {
		return
	}
	// Restart either os.Exit(0) or returns a real error; it never returns nil.
	err := a.updater.Restart()
	log.Printf("[Update] Restart failed: %v", err)
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

	startDir := resolveExistingDir(config.Get().DownloadDir)
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

// GetExtensionStatus returns the extension server status for the frontend.
func (a *App) GetExtensionStatus() extension.ExtensionStatus {
	if a.extensionServer == nil {
		return extension.ExtensionStatus{Status: "disconnected"}
	}
	return a.extensionServer.GetStatus()
}

// PairExtension starts the pairing flow and returns the pairing URL.
// The frontend opens the URL in the default browser on explicit user action via Wails Browser.OpenURL.
func (a *App) PairExtension() (string, error) {
	if a.extensionServer == nil {
		return "", errors.New("extension server not initialized")
	}
	return a.extensionServer.StartPairing()
}

// RegeneratePairing stops the current pairing server and starts a fresh one.
func (a *App) RegeneratePairing() (string, error) {
	if a.extensionServer == nil {
		return "", errors.New("extension server not initialized")
	}
	return a.extensionServer.RegeneratePairing()
}

// UnpairExtension rotates the secret and emits the unpaired event.
func (a *App) UnpairExtension() error {
	if a.extensionServer == nil {
		return errors.New("extension server not initialized")
	}
	a.extensionServer.NotifyUnpaired()
	return nil
}
