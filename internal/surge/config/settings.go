package config

import (
	"path/filepath"
	"strconv"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/surge/engine/types"
)

type Setting struct {
	Key          string
	Label        string
	Description  string
	NeedsRestart bool
	Type         string
	Value        any
	DefaultValue any
	ValidateFunc func(val any) error
}

type Settings struct {
	General         GeneralSettings
	Network         NetworkSettings
	Performance     PerformanceSettings
	Categories      CategorySettings
	Extension       ExtensionSettings
	CategoriesList  []*SettingsCategory
	StartupWarnings []string
}

type GeneralSettings struct {
	DefaultDownloadDir           *Setting
	WarnOnDuplicate              *Setting
	DownloadCompleteNotification *Setting
	AllowRemoteOpenActions       *Setting
	AutoResume                   *Setting
	AutoStart                    *Setting
	SkipUpdateCheck              *Setting
	ClipboardMonitor             *Setting
	Theme                        *Setting
	ThemePath                    *Setting
	LogRetentionCount            *Setting
	LiveSpeedGraph               *Setting
}

type NetworkSettings struct {
	MaxConnectionsPerDownload *Setting
	MaxConcurrentDownloads    *Setting
	MaxConcurrentProbes       *Setting
	UserAgent                 *Setting
	ProxyURL                  *Setting
	CustomDNS                 *Setting
	SequentialDownload        *Setting
	MinChunkSize              *Setting
	WorkerBufferSize          *Setting
	DialHedgeCount            *Setting
}

type PerformanceSettings struct {
	MaxTaskRetries        *Setting
	SlowWorkerThreshold   *Setting
	SlowWorkerGracePeriod *Setting
	StallTimeout          *Setting
	SpeedEmaAlpha         *Setting
}

type CategorySettings struct {
	CategoryEnabled *Setting
	Categories      []Category
}

type ExtensionSettings struct {
	ExtensionPrompt     *Setting
	ChromeExtensionURL  *Setting
	FirefoxExtensionURL *Setting
	AuthToken           *Setting
	InstructionsURL     *Setting
}

type SettingsCategory struct {
	Name     string
	Settings []*Setting
}

var activeSettings *Settings

func LoadSettings() (*Settings, error) {
	if activeSettings == nil {
		activeSettings = DefaultSettings()
	}
	return activeSettings, nil
}

func DefaultSettings() *Settings {
	s := &Settings{
		General: GeneralSettings{
			DefaultDownloadDir:           &Setting{Key: "default_download_dir", Type: "string"},
			WarnOnDuplicate:              &Setting{Key: "warn_on_duplicate", Type: "bool", DefaultValue: true},
			DownloadCompleteNotification: &Setting{Key: "download_complete_notification", Type: "bool", DefaultValue: true},
			AllowRemoteOpenActions:       &Setting{Key: "allow_remote_open_actions", Type: "bool", DefaultValue: false, Value: false},
			AutoResume:                   &Setting{Key: "auto_resume", Type: "bool", DefaultValue: false, Value: false},
			AutoStart:                    &Setting{Key: "auto_start", Type: "bool", DefaultValue: false, Value: false},
			SkipUpdateCheck:              &Setting{Key: "skip_update_check", Type: "bool", DefaultValue: false, Value: false},
			ClipboardMonitor:             &Setting{Key: "clipboard_monitor", Type: "bool", DefaultValue: true, Value: true},
			Theme:                        &Setting{Key: "theme", Type: "int", DefaultValue: 0, Value: 0},
			ThemePath:                    &Setting{Key: "theme_path", Type: "string", DefaultValue: "", Value: ""},
			LogRetentionCount:            &Setting{Key: "log_retention_count", Type: "int", DefaultValue: 5, Value: 5},
			LiveSpeedGraph:               &Setting{Key: "live_speed_graph", Type: "bool", DefaultValue: false, Value: false},
		},
		Network: NetworkSettings{
			MaxConnectionsPerDownload: &Setting{Key: "max_connections_per_host", Type: "int", DefaultValue: 32},
			MaxConcurrentDownloads:    &Setting{Key: "max_concurrent_downloads", Type: "int", DefaultValue: 3},
			MaxConcurrentProbes:       &Setting{Key: "max_concurrent_probes", Type: "int", DefaultValue: 3},
			UserAgent:                 &Setting{Key: "user_agent", Type: "string"},
			ProxyURL:                  &Setting{Key: "proxy_url", Type: "string", DefaultValue: "", Value: ""},
			CustomDNS:                 &Setting{Key: "custom_dns", Type: "string", DefaultValue: "", Value: ""},
			SequentialDownload:        &Setting{Key: "sequential_download", Type: "bool", DefaultValue: false, Value: false},
			MinChunkSize:              &Setting{Key: "min_chunk_size", Type: "int64", DefaultValue: int64(2 * 1024 * 1024), Value: int64(2 * 1024 * 1024)},
			WorkerBufferSize:          &Setting{Key: "worker_buffer_size", Type: "int", DefaultValue: 512 * 1024, Value: 512 * 1024},
			DialHedgeCount:            &Setting{Key: "dial_hedge_count", Type: "int", DefaultValue: 4, Value: 4},
		},
		Performance: PerformanceSettings{
			MaxTaskRetries:        &Setting{Key: "max_task_retries", Type: "int", DefaultValue: 3},
			SlowWorkerThreshold:   &Setting{Key: "slow_worker_threshold", Type: "float64", DefaultValue: 0.3},
			SlowWorkerGracePeriod: &Setting{Key: "slow_worker_grace_period", Type: "duration", DefaultValue: 5 * time.Second},
			StallTimeout:          &Setting{Key: "stall_timeout", Type: "duration", DefaultValue: 3 * time.Second},
			SpeedEmaAlpha:         &Setting{Key: "speed_ema_alpha", Type: "float64", DefaultValue: 0.3},
		},
		Categories: CategorySettings{
			CategoryEnabled: &Setting{Key: "category_enabled", Type: "bool", DefaultValue: false, Value: false},
			Categories:      DefaultCategories(),
		},
	}
	return s
}

func SaveSettings(s *Settings) error {
	activeSettings = s
	return nil
}

func Resolve[T any](s *Setting) T {
	var zero T
	if s == nil {
		return zero
	}

	// Map dynamically to GoAria's global config.Current
	switch s.Key {
	case "default_download_dir":
		if config.Current != nil {
			return any(config.Current.DownloadDir).(T)
		}
	case "user_agent":
		if config.Current != nil {
			return any(config.Current.UserAgent).(T)
		}
	case "max_connections_per_host":
		if config.Current != nil {
			if val, err := strconv.Atoi(config.Current.MaxConnections); err == nil {
				return any(val).(T)
			}
		}
	case "max_concurrent_downloads":
		if config.Current != nil {
			if val, err := strconv.Atoi(config.Current.MaxConcurrentDownloads); err == nil {
				return any(val).(T)
			}
		}
	}

	// Fallback to defaults
	anyVal := s.Value
	if anyVal == nil {
		anyVal = s.DefaultValue
	}
	if anyVal == nil {
		return zero
	}
	if val, ok := anyVal.(T); ok {
		return val
	}

	// Basic runtime conversions
	switch any(zero).(type) {
	case bool:
		if b, ok := anyVal.(bool); ok {
			return any(b).(T)
		}
	case int:
		if i, ok := anyVal.(int); ok {
			return any(i).(T)
		}
		if f, ok := anyVal.(float64); ok {
			return any(int(f)).(T)
		}
	case int64:
		if i, ok := anyVal.(int64); ok {
			return any(i).(T)
		}
		if i, ok := anyVal.(int); ok {
			return any(int64(i)).(T)
		}
		if f, ok := anyVal.(float64); ok {
			return any(int64(f)).(T)
		}
	case float64:
		if f, ok := anyVal.(float64); ok {
			return any(f).(T)
		}
		if i, ok := anyVal.(int); ok {
			return any(float64(i)).(T)
		}
	case string:
		if str, ok := anyVal.(string); ok {
			return any(str).(T)
		}
	case time.Duration:
		if d, ok := anyVal.(time.Duration); ok {
			return any(d).(T)
		}
		if i, ok := anyVal.(int64); ok {
			return any(time.Duration(i)).(T)
		}
	}

	return zero
}

func (s *Settings) ToRuntimeConfig() *types.RuntimeConfig {
	return &types.RuntimeConfig{
		MaxConnectionsPerDownload: Resolve[int](s.Network.MaxConnectionsPerDownload),
		UserAgent:                 Resolve[string](s.Network.UserAgent),
		ProxyURL:                  Resolve[string](s.Network.ProxyURL),
		CustomDNS:                 Resolve[string](s.Network.CustomDNS),
		SequentialDownload:        Resolve[bool](s.Network.SequentialDownload),
		MinChunkSize:              Resolve[int64](s.Network.MinChunkSize),
		WorkerBufferSize:          Resolve[int](s.Network.WorkerBufferSize),
		DialHedgeCount:            Resolve[int](s.Network.DialHedgeCount),
		MaxTaskRetries:            Resolve[int](s.Performance.MaxTaskRetries),
		SlowWorkerThreshold:       Resolve[float64](s.Performance.SlowWorkerThreshold),
		SlowWorkerGracePeriod:     Resolve[time.Duration](s.Performance.SlowWorkerGracePeriod),
		StallTimeout:              Resolve[time.Duration](s.Performance.StallTimeout),
		SpeedEmaAlpha:             Resolve[float64](s.Performance.SpeedEmaAlpha),
	}
}

func GetSurgeDir() string {
	return filepath.Join(filepath.Dir(config.GetConfigPath()), "surge")
}

func GetStateDir() string {
	return filepath.Join(filepath.Dir(config.GetConfigPath()), "surge")
}

func GetDownloadsDir() string {
	if config.Current != nil {
		return config.Current.DownloadDir
	}
	return ""
}

func GetRuntimeDir() string {
	return filepath.Join(filepath.Dir(config.GetConfigPath()), "surge", "runtime")
}

func GetLogsDir() string {
	return filepath.Join(filepath.Dir(config.GetConfigPath()), "surge", "logs")
}

func GetThemesDir() string {
	return filepath.Join(filepath.Dir(config.GetConfigPath()), "surge", "themes")
}

func EnsureDirs() error {
	return nil
}
