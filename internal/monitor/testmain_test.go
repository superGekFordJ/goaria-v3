package monitor

import (
	"os"
	"testing"

	"goaria-v3/internal/config"
)

func TestMain(m *testing.M) {
	config.SetTestConfig(&config.AppConfig{MaxConcurrentDownloads: "3"})
	code := m.Run()
	os.Exit(code)
}
