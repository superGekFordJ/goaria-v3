package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/testutil"
	surgeEvents "goaria-v3/internal/surge/types"
)

func TestHandleSurgeEvent_CompleteEvent_NoDelay(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{hub: hub, stopChan: make(chan struct{})}

	var receivedDelta *events.TaskDelta
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		receivedDelta = &delta
	})

	start := time.Now()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "test-2",
	})
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("dispatch took %v, expected immediate (<100ms)", elapsed)
	}

	if receivedDelta == nil {
		t.Fatal("expected non-nil TaskDelta received synchronously")
	}
	if receivedDelta.Type != "complete" {
		t.Errorf("delta type = %q, want complete", receivedDelta.Type)
	}
	if receivedDelta.GID != "sg_test-2" {
		t.Errorf("delta GID = %q, want sg_test-2", receivedDelta.GID)
	}
}

func TestHandleSurgeEvent_CompleteEvent_QueuesToFrontend(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		forceTickChan: make(chan struct{}, 1),
	}

	// Register the same internal handler that NewMonitor sets up
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		switch delta.Type {
		case "remove", "complete", "error":
			m.mu.Lock()
			m.shouldFetchStopped = true
			m.shouldFetchStoppedUntil = time.Now().Add(1500 * time.Millisecond)
			m.mu.Unlock()
		}
	})

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "test-push",
	})

	m.mu.Lock()
	shouldFetch := m.shouldFetchStopped
	until := m.shouldFetchStoppedUntil
	m.mu.Unlock()

	if !shouldFetch {
		t.Error("expected shouldFetchStopped to be true after complete event")
	}
	if !time.Now().Before(until) {
		t.Error("expected shouldFetchStoppedUntil to be in the future")
	}
}

func TestHandleSurgeEvent_CompleteEvent_NoCacheNeeded_StoppedVisibleNextTick(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:           hub,
		stopChan:      make(chan struct{}),
		forceTickChan: make(chan struct{}, 1),
	}

	// Register the same internal handler that NewMonitor sets up
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		switch delta.Type {
		case "remove", "complete", "error":
			m.mu.Lock()
			m.shouldFetchStopped = true
			m.shouldFetchStoppedUntil = time.Now().Add(1500 * time.Millisecond)
			m.mu.Unlock()
		}
	})

	m.mu.Lock()
	m.shouldFetchStopped = false
	m.mu.Unlock()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "test-nocache",
	})

	m.mu.Lock()
	shouldFetch := m.shouldFetchStopped
	m.mu.Unlock()

	if !shouldFetch {
		t.Error("expected shouldFetchStopped=true after complete event (no cache needed)")
	}
}

// TestHandleSurgeEvent_CompleteEvent_UpdatesMasterCache verifies that
// handleSurgeEvent upserts the completed entry into the SurgeEngine
// masterCache on the complete event, so TellStoppedLite reads it from
// cache (statistically ahead of the lifecycle worker's file persistence).

// TestHandleSurgeEvent_CompleteEvent_UpdatesMasterCache verifies that
// handleSurgeEvent upserts the completed entry into the SurgeEngine
// masterCache on the complete event, so TellStoppedLite reads it from
// cache (statistically ahead of the lifecycle worker's file persistence).
func TestHandleSurgeEvent_CompleteEvent_UpdatesMasterCache(t *testing.T) {
	testutil.SetupStateDB(t)
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)

	se := rpc.NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
	se.SetMasterCacheForTesting([]surgeEvents.DownloadRecord{
		{ID: "dl-cache-timing", URL: "http://x/a", DestPath: "/out/a", Status: "downloading", TotalSize: 1000, Mirrors: []string{"http://m1"}, Workers: 4},
	})

	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		surgeEng:      se,
		stopChan:      make(chan struct{}),
		forceTickChan: make(chan struct{}, 1),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "dl-cache-timing",
		Total:      1000,
		Filename:   "a.bin",
	})

	got, ok := se.GetMasterCacheEntry("dl-cache-timing")
	if !ok {
		t.Fatal("expected completed entry in masterCache after complete event")
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	// Merge mode must preserve URL/DestPath/Mirrors/Workers from prior entry.
	if got.URL != "http://x/a" {
		t.Errorf("URL = %q, want preserved http://x/a", got.URL)
	}
	if got.DestPath != "/out/a" {
		t.Errorf("DestPath = %q, want preserved /out/a", got.DestPath)
	}
	if len(got.Mirrors) != 1 || got.Mirrors[0] != "http://m1" {
		t.Errorf("Mirrors = %v, want preserved [http://m1]", got.Mirrors)
	}
	if got.Workers != 4 {
		t.Errorf("Workers = %d, want preserved 4", got.Workers)
	}

	// TellStoppedLite should return the completed task from cache.
	tasks, err := se.TellStoppedLite(0, 100)
	if err != nil {
		t.Fatalf("TellStoppedLite: %v", err)
	}
	found := false
	for _, task := range tasks {
		if task.GID == "dl-cache-timing" {
			found = true
			if task.Status != "complete" {
				t.Errorf("task status = %q, want complete", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected completed task in TellStoppedLite output from cache")
	}
}

func TestHandleSurgeEvent_CompleteEvent_PushesDeltaToFrontend(t *testing.T) {
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
	Cache.sgActive = []rpc.Task{{GID: "sg_test-direct-push", Status: "active"}}
	defer func() { Cache.sgActive = nil; Cache.sgStopped = nil }()

	var receivedDelta *events.TaskDelta
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		if delta.Type == "complete" {
			receivedDelta = &delta
		}
	})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "test-direct-push",
	})

	pusher.FlushNow()

	if receivedDelta == nil {
		t.Fatal("expected to receive complete delta via direct push")
	}
	if receivedDelta.GID != "sg_test-direct-push" {
		t.Errorf("expected GID sg_test-direct-push, got %s", receivedDelta.GID)
	}

	// Verify task was moved from active to stopped
	Cache.sgMu.RLock()
	for _, task := range Cache.sgActive {
		if task.GID == "sg_test-direct-push" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected task removed from active list")
		}
	}
	foundInStopped := false
	for _, task := range Cache.sgStopped {
		if task.GID == "sg_test-direct-push" {
			if task.Status != "complete" {
				t.Errorf("expected status 'complete', got %q", task.Status)
			}
			foundInStopped = true
			break
		}
	}
	Cache.sgMu.RUnlock()
	if !foundInStopped {
		t.Error("expected task in stopped list after complete event")
	}
}
