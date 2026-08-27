package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	DefaultRPCPort                = "16800"
	DefaultMaxConnections         = "16"
	DefaultMaxConcurrentDownloads = "5"
	DefaultMinThreadLife          = 5
	DefaultExtensionWSPort        = 16801
	DefaultWindowTransparency     = "none"
	DefaultUserAgent              = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36"

	MaxConnectionsUpper          = 256
	MaxConcurrentDownloadsUpper  = 32
	MinListenPort                = 1024
	MaxListenPort                = 65535
	MaxConvergenceIntervalSec    = 60
	Aria2MaxConnectionsPerServer = 16
)

var allowedExtensionWSPorts = map[int]struct{}{
	0:     {},
	16801: {},
	16802: {},
	16803: {},
}

var allowedWindowTransparency = map[string]struct{}{
	"none":    {},
	"acrylic": {},
	"mica":    {},
	"tabbed":  {},
}

// AppConfig holds all user-configurable settings. All fields must be value types
// (string/bool/int) — Update relies on shallow copy, so slice/map/pointer fields
// would share state between old and new snapshots.
type AppConfig struct {
	RPCPort                string `json:"rpc_port"`
	RPCSecret              string `json:"rpc_secret"`
	DownloadDir            string `json:"download_dir"`
	MaxConnections         string `json:"max_connections"`
	MaxConcurrentDownloads string `json:"max_concurrent_downloads"`
	UserAgent              string `json:"user_agent"`
	ShowHistory            bool   `json:"show_history"`
	WindowTransparency     string `json:"window_transparency"`  // "none", "acrylic", "mica", "tabbed"
	SmartThreadMode        bool   `json:"smart_thread_mode"`    // 智能线程模式开关
	MinThreadLife          int    `json:"min_thread_life"`      // T_min: 线程最小生存时间(秒), 默认 5
	CloseToTray            bool   `json:"close_to_tray"`        // 关闭窗口时最小化到托盘（true）还是退出应用（false）
	ConvergenceInterval    int    `json:"convergence_interval"` // 收敛tick间隔(秒), 0=默认5秒
	ExtensionEnabled       bool   `json:"extension_enabled"`    // 浏览器扩展集成开关
	ExtensionWSPort        int    `json:"extension_ws_port"`    // WebSocket 端口, 0=自动探测
	ExtensionSecret        string `json:"extension_secret"`     // 浏览器扩展认证密钥, 持久化到 config.json
}

// UpdateResult is the outcome of a checked config mutation.
type UpdateResult struct {
	Previous AppConfig
	Current  AppConfig
	Changed  bool
}

var (
	current atomic.Pointer[AppConfig]
	writeMu sync.Mutex

	errNotLoaded = errors.New("config.Update called before config.Load")

	readConfigFile     = os.ReadFile
	createConfigTemp   = os.CreateTemp
	renameConfigFile   = os.Rename
	configPathOverride string
)

// Get returns the current config snapshot. Callers must not mutate the result.
func Get() *AppConfig {
	return current.Load()
}

// DefaultConfig returns product defaults. DownloadDir uses the current user's Downloads.
func DefaultConfig() AppConfig {
	return AppConfig{
		RPCPort:                DefaultRPCPort,
		RPCSecret:              "",
		DownloadDir:            getDefaultDownloadDir(),
		MaxConnections:         DefaultMaxConnections,
		MaxConcurrentDownloads: DefaultMaxConcurrentDownloads,
		UserAgent:              DefaultUserAgent,
		ShowHistory:            true,
		WindowTransparency:     DefaultWindowTransparency,
		SmartThreadMode:        true,
		MinThreadLife:          DefaultMinThreadLife,
		CloseToTray:            false,
		ConvergenceInterval:    0,
		ExtensionEnabled:       true,
		ExtensionWSPort:        DefaultExtensionWSPort,
		ExtensionSecret:        "",
	}
}

// ValidateAndSanitize returns a canonical copy of input. It does not perform I/O,
// generate secrets, or publish to the global snapshot.
func ValidateAndSanitize(input AppConfig) AppConfig {
	out := input
	defaults := DefaultConfig()

	out.RPCPort = canonicalPort(input.RPCPort, defaults.RPCPort)
	out.RPCSecret = input.RPCSecret
	out.DownloadDir = canonicalDownloadDir(input.DownloadDir, defaults.DownloadDir)
	out.MaxConnections = canonicalBoundedInt(input.MaxConnections, 1, MaxConnectionsUpper, defaults.MaxConnections)
	out.MaxConcurrentDownloads = canonicalBoundedInt(input.MaxConcurrentDownloads, 1, MaxConcurrentDownloadsUpper, defaults.MaxConcurrentDownloads)
	if strings.TrimSpace(input.UserAgent) == "" {
		out.UserAgent = defaults.UserAgent
	} else {
		out.UserAgent = input.UserAgent
	}
	out.ShowHistory = input.ShowHistory
	out.SmartThreadMode = input.SmartThreadMode
	out.CloseToTray = input.CloseToTray
	out.ExtensionEnabled = input.ExtensionEnabled
	if _, ok := allowedWindowTransparency[strings.TrimSpace(input.WindowTransparency)]; ok {
		out.WindowTransparency = strings.TrimSpace(input.WindowTransparency)
	} else {
		out.WindowTransparency = defaults.WindowTransparency
	}
	if input.MinThreadLife < 1 {
		out.MinThreadLife = defaults.MinThreadLife
	} else {
		out.MinThreadLife = input.MinThreadLife
	}
	if input.ConvergenceInterval == 0 || (input.ConvergenceInterval >= 1 && input.ConvergenceInterval <= MaxConvergenceIntervalSec) {
		out.ConvergenceInterval = input.ConvergenceInterval
	} else {
		out.ConvergenceInterval = 0
	}
	if _, ok := allowedExtensionWSPorts[input.ExtensionWSPort]; ok {
		out.ExtensionWSPort = input.ExtensionWSPort
	} else {
		out.ExtensionWSPort = defaults.ExtensionWSPort
	}
	if out.ExtensionWSPort != 0 && strconv.Itoa(out.ExtensionWSPort) == out.RPCPort {
		out.ExtensionWSPort = 0
	}
	out.ExtensionSecret = input.ExtensionSecret
	return out
}

func canonicalPort(raw, fallback string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n < MinListenPort || n > MaxListenPort {
		return fallback
	}
	return strconv.Itoa(n)
}

func canonicalBoundedInt(raw string, minVal, maxVal int, fallback string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n < minVal || n > maxVal {
		return fallback
	}
	return strconv.Itoa(n)
}

func canonicalDownloadDir(raw, fallback string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	return filepath.Clean(trimmed)
}

// Update applies mutate to a copy of the current config, persists, then publishes.
// mutate must be a pure local mutation — never call Get/Update/Save inside it
// (re-entrancy deadlock). Persistence failures are logged and leave the previous snapshot.
func Update(mutate func(*AppConfig)) {
	if _, err := UpdateChecked(mutate); err != nil {
		log.Printf("[Config] failed to persist after update: %v", err)
	}
}

// UpdateChecked copies the current snapshot, applies mutate to the copy, sanitizes,
// persists atomically, then publishes. Canonical no-ops neither write nor swap pointers.
func UpdateChecked(mutate func(*AppConfig)) (UpdateResult, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	oldPtr := current.Load()
	if oldPtr == nil {
		return UpdateResult{}, errNotLoaded
	}
	previous := *oldPtr
	candidate := previous
	mutate(&candidate)
	candidate = ValidateAndSanitize(candidate)
	if candidate == previous {
		return UpdateResult{Previous: previous, Current: previous}, nil
	}
	if err := saveLocked(candidate); err != nil {
		return UpdateResult{Previous: previous, Current: previous}, err
	}
	committed := candidate
	current.Store(&committed)
	return UpdateResult{Previous: previous, Current: committed, Changed: true}, nil
}

// SetTestConfig replaces the global config for test setup. Production code must not call this.
func SetTestConfig(cfg *AppConfig) {
	current.Store(cfg)
}

func GetConfigPath() string {
	if configPathOverride != "" {
		return configPathOverride
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".goaria")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "config.json")
}

func Load() {
	defaults := DefaultConfig()
	cfg := defaults
	mayWrite := false
	needsWrite := false

	path := GetConfigPath()
	data, err := readConfigFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		mayWrite, needsWrite = true, true
	case err != nil:
		log.Printf("[Config] failed to read config (not overwriting): %v", err)
	default:
		decoded, decodeErr := decodeConfigObject(data, defaults)
		if decodeErr != nil {
			log.Printf("[Config] ignoring unreadable config (not overwriting): %v", decodeErr)
		} else {
			cfg = ValidateAndSanitize(decoded)
			mayWrite = true
			needsWrite = cfg != decoded
		}
	}

	if cfg.ExtensionSecret == "" {
		cfg.ExtensionSecret = generateSecretHex()
		if mayWrite {
			needsWrite = true
		}
	}

	writeMu.Lock()
	defer writeMu.Unlock()
	if mayWrite && needsWrite {
		if err := saveLocked(cfg); err != nil {
			log.Printf("[Config] failed to persist config: %v", err)
		}
	}
	published := cfg
	current.Store(&published)
}

func decodeConfigObject(data []byte, defaults AppConfig) (AppConfig, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return AppConfig{}, err
	}
	if dec.More() {
		return AppConfig{}, errors.New("trailing data after config JSON")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return AppConfig{}, errors.New("config JSON must be an object")
	}
	decoded := defaults
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return AppConfig{}, err
	}
	return decoded, nil
}

func saveLocked(cfg AppConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := GetConfigPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := createConfigTemp(dir, "config.json.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := renameConfigFile(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func getDefaultDownloadDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

func generateSecretHex() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
