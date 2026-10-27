package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	"goaria-v3/internal/surge/types"
)

// A saturated ordinary observer must not starve lifecycle persistence: the
// reliable feed still delivers EventComplete to the worker, which promotes the
// working file and records a durable completion.
func TestLifecyclePersistsCompletionWithUnreadObserver(t *testing.T) {
	root := testutil.SetupStateDB(t)
	bus := NewEventBus()
	defer bus.Shutdown()
	saturated, stopSaturated := bus.Subscribe()
	defer stopSaturated()
	_ = saturated // deliberately unread: it overflows and is disconnected
	core, stopCore := bus.SubscribeLifecycle()
	defer stopCore()
	settings := config.DefaultSettings()
	settings.General.DownloadCompleteNotification.Value = false
	manager := NewLifecycleManager(nil, bus, settings)
	done := make(chan struct{})
	go func() { defer close(done); manager.StartEventWorker(core) }()

	path := filepath.Join(root, "complete.bin")
	content := []byte("verified bytes") // 14 bytes
	if err := os.WriteFile(path+types.IncompleteSuffix, content, 0o600); err != nil {
		t.Fatal(err)
	}
	// Fork enqueues persist the master row synchronously; seed the equivalent
	// record so completion can resolve destPath and promote the working file.
	const dlID = "persisted-id"
	const url = "https://example.com/file"
	testutil.SeedMasterList(t, types.DownloadRecord{
		ID:         dlID,
		URL:        url,
		URLHash:    store.URLHash(url),
		DestPath:   path,
		Filename:   "complete.bin",
		Status:     "downloading",
		TotalSize:  int64(len(content)),
		Downloaded: int64(len(content)),
	})

	// Overflow the unread observer; lifecycle events must still flow.
	for range eventQueueCapacity * 2 {
		if err := bus.Publish(types.DownloadEvent{Type: types.EventSystem, Message: "fill slow observer"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := bus.Publish(types.DownloadEvent{
		Type:       types.EventComplete,
		DownloadID: dlID,
		Filename:   "complete.bin",
		Total:      int64(len(content)),
		Elapsed:    time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	bus.Shutdown()
	<-done

	record, err := store.GetDownload(dlID)
	if err != nil || record == nil || record.Status != "completed" {
		t.Fatalf("completion not durable: %+v, %v", record, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(content) {
		t.Fatalf("file not finalized: %q, %v", data, err)
	}
}
