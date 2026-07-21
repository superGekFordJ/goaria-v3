package extension

import (
	"os"
	"testing"

	"goaria-v3/internal/config"
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "goaria-ext-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("USERPROFILE", tmp)
	_ = os.Setenv("HOME", tmp)
	config.SetTestConfig(&config.AppConfig{})
	code := m.Run()
	os.Exit(code)
}
