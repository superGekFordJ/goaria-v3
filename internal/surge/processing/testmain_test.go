package processing

import (
	"os"
	"testing"

	goariaconfig "goaria-v3/internal/config"
)

func TestMain(m *testing.M) {
	goariaconfig.SetTestConfig(&goariaconfig.AppConfig{
		DownloadDir:            os.TempDir(),
		MaxConnections:         "32",
		MaxConcurrentDownloads: "3",
		UserAgent:              "Mozilla/5.0 (test)",
	})
	code := m.Run()
	os.Exit(code)
}
