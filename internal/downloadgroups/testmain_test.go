package downloadgroups

import (
	"log"
	"os"
	"testing"

	"goaria-v3/internal/monitor"
)

func TestMain(m *testing.M) {
	// Silent in-memory store for the whole package; tests opt back in with a
	// real temp path via monitor.ResetTaskGroupStoreForTest.
	monitor.ResetTaskGroupStoreForTest("", false)
	code := m.Run()
	// Join leftover naming jobs before stopping the store so no producer can
	// trigger a save after the saver is gone.
	monitor.StopDownloadGroupNamer()
	if err := monitor.StopTaskGroupStore(); err != nil {
		log.Printf("[downloadgroups] failed to stop download group store: %v", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
