package tasks

import (
	"os"
	"testing"

	"goaria-v3/internal/config"
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "goaria-tasks-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("USERPROFILE", tmp)
	_ = os.Setenv("HOME", tmp)
	config.SetTestConfig(&config.AppConfig{
		DownloadDir:     os.TempDir(),
		SmartThreadMode: false,
	})
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
