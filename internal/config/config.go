package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

type AppConfig struct {
	RPCPort                         string `json:"rpc_port"`
	RPCSecret                       string `json:"rpc_secret"`
	DownloadDir                     string `json:"download_dir"`
	MaxConnections                  string `json:"max_connections"`
	MaxConcurrentDownloads          string `json:"max_concurrent_downloads"`
	UserAgent                       string `json:"user_agent"`
	ShowHistory                     bool   `json:"show_history"`
	WindowTransparency              string `json:"window_transparency"`                 // "none", "acrylic", "mica", "tabbed"
	SmartThreadMode                 bool   `json:"smart_thread_mode"`                   // 智能线程模式开关
	MinThreadLife                   int    `json:"min_thread_life"`                     // T_min: 线程最小生存时间(秒), 默认 5
	CloseToTray                     bool   `json:"close_to_tray"`                       // 关闭窗口时最小化到托盘（true）还是退出应用（false）
	ConvergenceInterval             int    `json:"convergence_interval"`                // 收敛tick间隔(秒), 0=默认5秒
	ExtensionEnabled                bool   `json:"extension_enabled"`                   // 浏览器扩展集成开关
	ExtensionWSPort                 int    `json:"extension_ws_port"`                   // WebSocket 端口, 0=自动探测
	ExtensionSecret                 string `json:"extension_secret"`                    // 浏览器扩展认证密钥, 持久化到 config.json
	EnablePhysicalMacAwareBandwidth bool   `json:"enable_physical_mac_aware_bandwidth"` // 物理网卡感知带宽天花板开关（默认 false）
}

var (
	Current *AppConfig
	mu      sync.RWMutex
)

func GetConfigPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".goaria")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "config.json")
}

func Load() {
	mu.Lock()
	defer mu.Unlock()
	Current = &AppConfig{
		RPCPort:                "16800",
		RPCSecret:              "",
		DownloadDir:            getDefaultDownloadDir(),
		MaxConnections:         "8",
		MaxConcurrentDownloads: "3",
		UserAgent:              "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
		ShowHistory:            true,
		WindowTransparency:     "none",
		SmartThreadMode:        true,
		MinThreadLife:          5,
		ExtensionEnabled:       true,
		ExtensionWSPort:        16801,
	}
	data, err := os.ReadFile(GetConfigPath())
	if err == nil {
		_ = json.Unmarshal(data, Current)
	}

	if Current.ExtensionSecret == "" {
		Current.ExtensionSecret = generateSecretHex()
		if err := saveLocked(); err != nil {
			log.Printf("[Config] failed to persist extension secret: %v", err)
		}
	}
}

func Save() error {
	mu.Lock()
	defer mu.Unlock()
	return saveLocked()
}

func saveLocked() error {
	data, err := json.MarshalIndent(Current, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(GetConfigPath(), data, 0o600)
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
