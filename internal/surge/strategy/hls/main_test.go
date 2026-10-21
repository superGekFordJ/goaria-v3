// lint:ignore-leak-check
package hls_test

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"goaria-v3/internal/surge/transport"

	"go.uber.org/goleak"
)

// TestMain closes every connection the tests may have left in the default
// client and the shared network pool, then asserts no goroutine leaked.
func TestMain(m *testing.M) {
	code := m.Run()
	http.DefaultClient.CloseIdleConnections()
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
	transport.DefaultNetworkPool.CloseAll()
	if code != 0 {
		os.Exit(code)
	}
	if err := goleak.Find(); err != nil {
		fmt.Fprintf(os.Stderr, "goleak: Errors on successful test run: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
