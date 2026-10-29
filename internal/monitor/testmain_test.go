package monitor

import (
	"log"
	"net"
	"net/url"
	"os"
	"testing"

	"goaria-v3/internal/config"
)

func TestMain(m *testing.M) {
	config.SetTestConfig(&config.AppConfig{MaxConcurrentDownloads: "3"})
	// Silent in-memory store for the whole package; tests opt back in with a
	// real temp path via ResetTaskGroupStoreForTest.
	ResetTaskGroupStoreForTest("", false)

	// Terminal-fallback classification must never reach OS DNS in tests: IP
	// literals still classify directly (no lookup anyway), and hostnames
	// answer as a fixed public TEST-NET address — scope lands on wan
	// deterministically while the domain still comes from the URL.
	scopeClassifyByURL = func(rawURL string) (string, string) {
		if parsed, err := url.Parse(rawURL); err == nil && parsed != nil {
			if net.ParseIP(parsed.Hostname()) != nil {
				return scopeClassifier.ClassifyByURL(rawURL)
			}
		}
		return scopeClassifier.ClassifyByURLAndIP(rawURL, "203.0.113.1")
	}

	code := m.Run()
	// Join leftover naming jobs before stopping the store so no producer can
	// trigger a save after the saver is gone.
	StopDownloadGroupNamer()
	if err := StopTaskGroupStore(); err != nil {
		log.Printf("[monitor] failed to stop download group store: %v", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
