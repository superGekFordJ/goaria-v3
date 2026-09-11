package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

func TestHandleSurgeEvent_ErrorEvent_PushesDeltaToFrontend(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		forceTickChan: make(chan struct{}, 1),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Seed cache with an active task
	Cache.sgActive = []rpc.Task{{GID: "sg_test-err-push", Status: "active"}}
	defer func() { Cache.sgActive = nil; Cache.sgStopped = nil }()

	var receivedDelta *events.TaskDelta
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		if delta.Type == "error" {
			receivedDelta = &delta
		}
	})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventError,
		DownloadID: "test-err-push",
	})

	pusher.FlushNow()

	if receivedDelta == nil {
		t.Fatal("expected to receive error delta via direct push")
	}
	if receivedDelta.GID != "sg_test-err-push" {
		t.Errorf("expected GID sg_test-err-push, got %s", receivedDelta.GID)
	}

	// Verify task was moved from active to stopped with error status
	Cache.sgMu.RLock()
	for _, task := range Cache.sgActive {
		if task.GID == "sg_test-err-push" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected task removed from active list")
		}
	}
	foundInStopped := false
	for _, task := range Cache.sgStopped {
		if task.GID == "sg_test-err-push" {
			if task.Status != "error" {
				t.Errorf("expected status 'error', got %q", task.Status)
			}
			foundInStopped = true
			break
		}
	}
	Cache.sgMu.RUnlock()
	if !foundInStopped {
		t.Error("expected task in stopped list after error event")
	}
}

func TestHandleSurgeEvent_ErrorEvent_DiskSpacePersistsErrorAndDelta(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	surgeEng := &rpc.SurgeEngine{}
	surgeEng.UpsertMasterCacheEntry(surgeEvents.DownloadRecord{
		ID:       "disk-err",
		URL:      "http://example.com/big.bin",
		Filename: "big.bin",
		Status:   "downloading",
	})
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		forceTickChan: make(chan struct{}, 1),
		surgeEng:      surgeEng,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{GID: "sg_disk-err", Status: "active"}}
	defer func() { Cache.sgActive = nil; Cache.sgStopped = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventError,
		DownloadID: "disk-err",
		Err:        surgeEvents.ErrInsufficientDiskSpace,
	})

	entry, ok := surgeEng.GetMasterCacheEntry("disk-err")
	if !ok {
		t.Fatal("expected masterCache entry")
	}
	if entry.Error != surgeEvents.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("masterCache Error = %q, want sentinel", entry.Error)
	}

	pusher.mu.Lock()
	defer pusher.mu.Unlock()
	var errDelta *events.TaskDelta
	for i := range pusher.pending {
		if pusher.pending[i].Type == "error" && pusher.pending[i].GID == "sg_disk-err" {
			errDelta = &pusher.pending[i]
			break
		}
	}
	if errDelta == nil {
		t.Fatal("expected error delta in pusher queue")
	}
	payload, ok := errDelta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]string", errDelta.Payload)
	}
	if payload["errorCode"] != "9" {
		t.Fatalf("errorCode = %q, want 9", payload["errorCode"])
	}
	if payload["errorMessage"] != surgeEvents.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("errorMessage = %q, want sentinel", payload["errorMessage"])
	}

	Cache.sgMu.RLock()
	defer Cache.sgMu.RUnlock()
	var stopped *rpc.Task
	for i := range Cache.sgStopped {
		if Cache.sgStopped[i].GID == "sg_disk-err" {
			stopped = &Cache.sgStopped[i]
			break
		}
	}
	if stopped == nil {
		t.Fatal("expected stopped cache task")
	}
	if stopped.ErrorCode != "9" {
		t.Fatalf("stopped ErrorCode = %q, want 9", stopped.ErrorCode)
	}
	if stopped.ErrorMessage != surgeEvents.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("stopped ErrorMessage = %q, want sentinel", stopped.ErrorMessage)
	}
}

func TestHandleSurgeEvent_ErrorResumeError_AllowsSecondTerminal(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		tracker:               tracker,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_reerr", Status: "error",
		ErrorCode: "1", ErrorMessage: "fail",
		Files: []rpc.File{{Path: "/tmp/reerr.bin"}},
	}}
	Cache.metadata["sg_reerr"] = &TaskMetadata{
		GID: "sg_reerr", Files: []string{"/tmp/reerr.bin"}, Dir: "/tmp",
	}
	defer func() {
		Cache.sgStopped = nil
		Cache.sgActive = nil
		delete(Cache.metadata, "sg_reerr")
	}()

	tracker.EnsureTrackedFromEvent("sg_reerr", 1000, "https://example.com/reerr.bin", 0, "error")
	if completed := tracker.MarkCompleteFromEvent("sg_reerr", "error"); completed == nil {
		t.Fatal("expected first terminal mark")
	}
	history.Add(history.HistoryEntry{GID: "sg_reerr", Path: "/tmp/reerr.bin", Status: "error"})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventResumed, DownloadID: "reerr"})

	if tracker.processedComplete["sg_reerr"] {
		t.Fatal("expected processedComplete cleared after resume reopen")
	}
	if _, ok := history.Get("sg_reerr"); ok {
		t.Fatal("expected history retired on resume")
	}

	second := tracker.MarkCompleteFromEvent("sg_reerr", "error")
	if second == nil {
		t.Fatal("expected second MarkCompleteFromEvent after reopen")
	}
	m.handleTaskComplete(second)
	entry, ok := history.Get("sg_reerr")
	if !ok || entry.Status != "error" {
		t.Fatalf("expected second history error entry, got ok=%v entry=%#v", ok, entry)
	}
}
