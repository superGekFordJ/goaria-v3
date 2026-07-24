// lint:ignore-leak-check
package orchestrator

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	goariaconfig "goaria-v3/internal/config"
	"goaria-v3/internal/surge/transport"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goariaconfig.SetTestConfig(&goariaconfig.AppConfig{
		DownloadDir:            os.TempDir(),
		MaxConnections:         "32",
		MaxConcurrentDownloads: "3",
		UserAgent:              "Mozilla/5.0 (test)",
	})
	code := m.Run()
	stopPendingGC()
	http.DefaultClient.CloseIdleConnections()
	transport.DefaultNetworkPool.CloseAll()
	if code == 0 {
		if err := goleak.Find(
			goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
			goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
			goleak.IgnoreTopFunction("net.(*Resolver).lookupIP.func1"),
			goleak.IgnoreTopFunction("net.(*Resolver).lookupIP.func2"),
		); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: Errors on successful test run: %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(code)
}
