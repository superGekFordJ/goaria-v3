// lint:ignore-leak-check
package scheduler

import (
	"os"
	"testing"

	goariaconfig "goaria-v3/internal/config"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goariaconfig.SetTestConfig(&goariaconfig.AppConfig{
		DownloadDir:            os.TempDir(),
		MaxConnections:         "32",
		MaxConcurrentDownloads: "3",
		UserAgent:              "Mozilla/5.0 (test)",
	})
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("sync.runtime_notifyListWait"),
		goleak.IgnoreTopFunction("goaria-v3/internal/surge/scheduler.safeSendProgress"),
	)
}
