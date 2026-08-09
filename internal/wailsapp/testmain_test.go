package wailsapp

import (
	"os"
	"testing"

	"goaria-v3/internal/config"
)

func TestMain(m *testing.M) {
	config.SetTestConfig(&config.AppConfig{
		DownloadDir:            os.TempDir(),
		MaxConnections:         "32",
		MaxConcurrentDownloads: "3",
		UserAgent:              "Mozilla/5.0 (test)",
	})
	code := m.Run()
	os.Exit(code)
}
