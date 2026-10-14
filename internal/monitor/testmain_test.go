package monitor

import (
	"log"
	"os"
	"testing"

	"goaria-v3/internal/config"
)

func TestMain(m *testing.M) {
	config.SetTestConfig(&config.AppConfig{MaxConcurrentDownloads: "3"})
	// Silent in-memory store for the whole package; tests opt back in with a
	// real temp path via ResetTaskGroupStoreForTest.
	ResetTaskGroupStoreForTest("", false)
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
