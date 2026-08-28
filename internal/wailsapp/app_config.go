package wailsapp

import (
	"errors"
	"log"
	"strconv"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/process"
	"goaria-v3/internal/rpc"
)

const aria2PortReleaseWait = time.Second

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
	afterDaemonStop func()
	rpcInit         func(port, secret string)
	waitForReady    func(time.Duration) error
	initNotifier    func(hub *events.Hub, port, secret string)
	stopNotifier    func()
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
		afterDaemonStop: func() {
			time.Sleep(aria2PortReleaseWait)
		},
		rpcInit:      rpc.Init,
		waitForReady: rpc.WaitForReady,
		initNotifier: rpc.InitNotifier,
		stopNotifier: rpc.StopNotifier,
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

func failedSaveResult(code string, cfg config.AppConfig, message string) SaveConfigResult {
	return SaveConfigResult{
		Success:   false,
		Config:    cfg,
		ErrorCode: code,
		Message:   message,
	}
}

func successSaveResult(cfg config.AppConfig, ariaRestarted bool) SaveConfigResult {
	return SaveConfigResult{
		Success:        true,
		Config:         cfg,
		Aria2Restarted: ariaRestarted,
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
		return failedSaveResult(errCodeNotLoaded, config.AppConfig{}, msgConfigNotLoaded)
	}
	old := *oldPtr
	candidate := config.ValidateAndSanitize(request)
	candidate.ExtensionSecret = old.ExtensionSecret
	if candidate == old {
		return successSaveResult(liveSnapshot(deps, old), false)
	}

	if candidate.DownloadDir != old.DownloadDir {
		if err := deps.validateDir(candidate.DownloadDir); err != nil {
			log.Printf("[Config] download dir preflight failed: %v", err)
			return failedSaveResult(errCodeDownloadDirUnavailable, liveSnapshot(deps, old), msgDownloadDirUnavailable)
		}
	}

	if wsPort, listening := deps.extensionStatus(); listening && wsPort != 0 {
		if candidate.RPCPort == strconv.Itoa(wsPort) {
			return failedSaveResult(errCodeRPCExtensionPort, liveSnapshot(deps, old), msgRPCExtensionConflict)
		}
	}

	update, err := deps.updateChecked(func(current *config.AppConfig) {
		managedSecret := current.ExtensionSecret
		*current = candidate
		current.ExtensionSecret = managedSecret
	})
	if err != nil {
		return failedSaveResult(errCodePersistFailed, liveSnapshot(deps, update.Current), msgPersistFailed)
	}

	committed := update.Current
	if ariaProjection(old) == ariaProjection(committed) {
		return successSaveResult(liveSnapshot(deps, committed), false)
	}
	if err := a.activateAria(deps, committed); err != nil {
		return a.rollbackConfigAndAria(deps, old, committed, err)
	}
	return successSaveResult(liveSnapshot(deps, committed), true)
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
		if daemonWasReplaced(activateErr) && deps.stopNotifier != nil {
			deps.stopNotifier()
		}
		snap := liveSnapshot(deps, candidate)
		if snap == (config.AppConfig{}) && rollback.Current != (config.AppConfig{}) {
			snap = rollback.Current
		}
		return failedSaveResult(errCodeConfigRollbackFailed, snap, msgConfigRollbackFailed)
	}

	restored := rollback.Current
	if daemonWasReplaced(activateErr) {
		if err := deps.restartAria2(&restored); err != nil {
			var re *process.Aria2RestartError
			if errors.As(err, &re) && re != nil && !re.Stopped && deps.stopAria2 != nil {
				deps.stopAria2()
				if deps.afterDaemonStop != nil {
					deps.afterDaemonStop()
				}
			}
			if deps.rpcInit != nil {
				deps.rpcInit(restored.RPCPort, restored.RPCSecret)
			}
			return failedSaveResult(errCodeAriaRollbackFailed, liveSnapshot(deps, restored), msgAriaRollbackFailed)
		}
		if err := a.bindRPC(deps, restored); err != nil {
			return failedSaveResult(errCodeAriaRollbackFailed, liveSnapshot(deps, restored), msgAriaRollbackFailed)
		}
		return failedSaveResult(code, liveSnapshot(deps, restored), msg)
	}

	if deps.rpcInit != nil {
		deps.rpcInit(restored.RPCPort, restored.RPCSecret)
	}
	return failedSaveResult(code, liveSnapshot(deps, restored), msg)
}
