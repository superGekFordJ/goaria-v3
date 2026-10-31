package tasks

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"

	"goaria-v3/internal/config"
)

var (
	lookupHostCallsMu sync.Mutex
	lookupHostCalls   []string
)

func lookupHostSaw(host string) bool {
	lookupHostCallsMu.Lock()
	defer lookupHostCallsMu.Unlock()
	return slices.Contains(lookupHostCalls, host)
}

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
	// No test may reach the OS resolver: an empty answer is exactly what
	// unresolved (.invalid/NXDOMAIN) fixture hosts already produced — callers
	// fall back to the proxy envKey and wan scope. Set once before m.Run so
	// parallel tests only ever read the var.
	lookupHost = func(_ context.Context, host string) ([]string, error) {
		lookupHostCallsMu.Lock()
		lookupHostCalls = append(lookupHostCalls, host)
		lookupHostCallsMu.Unlock()
		return nil, nil
	}
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
