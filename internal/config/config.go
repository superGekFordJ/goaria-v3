package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

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

var (
	current atomic.Pointer[AppConfig]
	writeMu sync.Mutex
)

// Get returns the current config snapshot. Callers must not mutate the result.
func Get() *AppConfig {
	return current.Load()
}

// Update applies mutate to a copy of the current config, atomically swaps it in,
// and persists to disk. mutate must be a pure local mutation — never call
// Get/Update/Save inside it (re-entrancy deadlock).
func Update(mutate func(*AppConfig)) {
	writeMu.Lock()
	defer writeMu.Unlock()
	old := current.Load()
	if old == nil {
		panic("config.Update called before config.Load")
	}
	newCfg := *old
	mutate(&newCfg)
	current.Store(&newCfg)
	if err := saveLocked(&newCfg); err != nil {
		log.Printf("[Config] failed to persist after update: %v", err)
	}
}

// SetTestConfig replaces the global config for test setup. Production code must not call this.
func SetTestConfig(cfg *AppConfig) {
	current.Store(cfg)
}

func GetConfigPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".goaria")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "config.json")
}

func Load() {
	cfg := AppConfig{
		RPCPort:                "16800",
		RPCSecret:              "",
		DownloadDir:            getDefaultDownloadDir(),
		MaxConnections:         "16",
		MaxConcurrentDownloads: "5",
		UserAgent:              "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
		ShowHistory:            true,
		WindowTransparency:     "none",
		SmartThreadMode:        true,
		MinThreadLife:          5,
		ExtensionEnabled:       true,
		ExtensionWSPort:        16801,
	}
	data, readErr := os.ReadFile(GetConfigPath())
	fileExisted := readErr == nil || !os.IsNotExist(readErr)
	if readErr == nil {
		_ = json.Unmarshal(data, &cfg)
	} else if fileExisted {
		log.Printf("[Config] failed to read config (not overwriting): %v", readErr)
	}

	writeMu.Lock()
	defer writeMu.Unlock()
	current.Store(&cfg)

	if cfg.ExtensionSecret == "" {
		cfg.ExtensionSecret = generateSecretHex()
		current.Store(&cfg)
		if readErr == nil || !fileExisted {
			if err := saveLocked(&cfg); err != nil {
				log.Printf("[Config] failed to persist extension secret: %v", err)
			}
		}
	}
}

func saveLocked(cfg *AppConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(GetConfigPath(), data, 0o644)
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
