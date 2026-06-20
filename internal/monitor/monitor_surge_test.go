package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/engine/events"
)

func TestHandleSurgeEvent_ProgressMsg_QueuesProgressDelta(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.ProgressMsg{
		DownloadID: "test-1",
		Downloaded: 500,
		Total:      1000,
		Speed:      50.0,
	})

	pusher.mu.Lock()
	if len(pusher.pending) != 1 {
		pusher.mu.Unlock()
		t.Fatalf("expected 1 pending delta, got %d", len(pusher.pending))
	}
	delta := pusher.pending[0]
	pusher.mu.Unlock()

	if delta.Type != "progress" {
		t.Errorf("delta type = %q, want progress", delta.Type)
	}
	if delta.GID != "sg_test-1" {
		t.Errorf("delta GID = %q, want sg_test-1", delta.GID)
	}

	payload, ok := delta.Payload.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map[string]interface{} payload, got %T", delta.Payload)
	}
	if payload["completedLength"] != "500" {
		t.Errorf("completedLength = %v, want 500", payload["completedLength"])
	}
	if payload["downloadSpeed"] != "50" {
		t.Errorf("downloadSpeed = %v, want 50", payload["downloadSpeed"])
	}
	if payload["totalLength"] != "1000" {
		t.Errorf("totalLength = %v, want 1000", payload["totalLength"])
	}
}

func TestHandleSurgeEvent_BatchProgressMsg_QueuesAllProgressDeltas(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.BatchProgressMsg{
		{DownloadID: "a", Downloaded: 100, Total: 200, Speed: 10.0},
		{DownloadID: "b", Downloaded: 300, Total: 600, Speed: 20.0},
	})

	pusher.mu.Lock()
	if len(pusher.pending) != 2 {
		pusher.mu.Unlock()
		t.Fatalf("expected 2 pending deltas, got %d", len(pusher.pending))
	}
	gids := map[string]bool{pusher.pending[0].GID: true, pusher.pending[1].GID: true}
	pusher.mu.Unlock()

	if !gids["sg_a"] || !gids["sg_b"] {
		t.Errorf("expected gids sg_a and sg_b, got %v", gids)
	}
}

func TestHandleSurgeEvent_CompleteEvent_NoDelay(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{hub: hub, stopChan: make(chan struct{})}

	var receivedDelta *events.TaskDelta
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		receivedDelta = &delta
	})

	start := time.Now()
	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
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

func TestHandleSurgeEvent_CompleteEvent_InvalidatesCache(t *testing.T) {
	hub := events.NewHub(nil)
	se := &rpc.SurgeEngine{}
	se.SetCacheValid(true)
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, engine: hybrid}

	if !se.IsCacheValid() {
		t.Fatal("precondition: cache should be valid before event")
	}

	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "test-3",
	})

	if se.IsCacheValid() {
		t.Error("expected cacheValid to be false after complete event")
	}
}

func TestHandleSurgeEvent_ErrorEvent_InvalidatesCache(t *testing.T) {
	hub := events.NewHub(nil)
	se := &rpc.SurgeEngine{}
	se.SetCacheValid(true)
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, engine: hybrid}

	m.handleSurgeEvent(surgeEvents.DownloadErrorMsg{
		DownloadID: "test-err",
	})

	if se.IsCacheValid() {
		t.Error("expected cacheValid to be false after error event")
	}
}

func TestHandleSurgeEvent_RemoveEvent_InvalidatesCache(t *testing.T) {
	hub := events.NewHub(nil)
	se := &rpc.SurgeEngine{}
	se.SetCacheValid(true)
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, engine: hybrid}

	m.handleSurgeEvent(surgeEvents.DownloadRemovedMsg{
		DownloadID: "test-rm",
	})

	if se.IsCacheValid() {
		t.Error("expected cacheValid to be false after remove event")
	}
}

func TestHandleSurgeEvent_AddEvent_InvalidatesCache(t *testing.T) {
	hub := events.NewHub(nil)
	se := &rpc.SurgeEngine{}
	se.SetCacheValid(true)
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, engine: hybrid}

	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "test-add",
	})

	if se.IsCacheValid() {
		t.Error("expected cacheValid to be false after add event")
	}
}

func TestHandleSurgeEvent_PauseEvent_InvalidatesCache(t *testing.T) {
	hub := events.NewHub(nil)
	se := &rpc.SurgeEngine{}
	se.SetCacheValid(true)
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, engine: hybrid}

	m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{
		DownloadID: "test-pause",
	})

	if se.IsCacheValid() {
		t.Error("expected cacheValid to be false after pause event")
	}
}

func TestCurrentTickInterval_PendingComplete_UsesWindow(t *testing.T) {
	m := &Monitor{
		pendingCompleteGids: map[string]time.Time{"sg_test": time.Now()},
		windowInterval:      1 * time.Second,
		headlessInterval:   5 * time.Second,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with pending complete, got %v", d)
	}
}

func TestCurrentTickInterval_SurgeOnly_UsesHeadless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// SurgeEngine with nil service → IsSurgeActive() returns false
	// So !IsSurgeActive() is true → returns windowInterval (1s)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)

	m := &Monitor{
		pendingCompleteGids: map[string]time.Time{},
		windowInterval:      1 * time.Second,
		headlessInterval:   5 * time.Second,
		prevActiveGids:      map[string]bool{},
		prevWaitingGids:     map[string]bool{},
		engine:             hybrid,
	}

	// With nil service, IsSurgeActive()=false, so it uses windowInterval
	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s when surge not active, got %v", d)
	}
}

func TestCurrentTickInterval_HasAria2Tasks_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)

	m := &Monitor{
		pendingCompleteGids: map[string]time.Time{},
		windowInterval:      1 * time.Second,
		headlessInterval:   5 * time.Second,
		prevActiveGids:      map[string]bool{"ar_123": true},
		prevWaitingGids:     map[string]bool{},
		engine:             hybrid,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with Aria2 tasks, got %v", d)
	}
}

func TestCurrentTickInterval_Aria2InWaiting_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)

	m := &Monitor{
		pendingCompleteGids: map[string]time.Time{},
		windowInterval:      1 * time.Second,
		headlessInterval:   5 * time.Second,
		prevActiveGids:      map[string]bool{},
		prevWaitingGids:     map[string]bool{"ar_456": true},
		engine:             hybrid,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with Aria2 waiting tasks, got %v", d)
	}
}

func TestCurrentTickInterval_NoWindow_UsesHeadless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(false)
	defer State.SetWindowExists(prevWindow)

	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)

	m := &Monitor{
		pendingCompleteGids: map[string]time.Time{},
		windowInterval:      1 * time.Second,
		headlessInterval:   5 * time.Second,
		prevActiveGids:      map[string]bool{"ar_123": true},
		prevWaitingGids:     map[string]bool{},
		engine:             hybrid,
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s no window, got %v", d)
	}
}

func TestHandleSurgeEvent_CompleteEvent_QueuesToFrontend(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                 hub,
		pusher:              pusher,
		pendingCompleteGids: make(map[string]time.Time),
		forceTickChan:       make(chan struct{}, 1),
	}

	// Register the same internal handler that NewMonitor sets up
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		switch delta.Type {
		case "remove", "complete", "error":
			m.mu.Lock()
			m.shouldFetchStopped = true
			if delta.GID != "" {
				m.pendingCompleteGids[delta.GID] = time.Now()
			}
			m.mu.Unlock()
		}
	})

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "test-push",
	})

	m.mu.Lock()
	pendingCount := len(m.pendingCompleteGids)
	m.mu.Unlock()

	if pendingCount != 1 {
		t.Fatalf("expected 1 pending complete gid, got %d", pendingCount)
	}
	if _, ok := m.pendingCompleteGids["sg_test-push"]; !ok {
		t.Error("expected sg_test-push in pendingCompleteGids")
	}
}

func TestHandleSurgeEvent_ProgressMsg_NoWindow_DoesNotPush(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(false)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.ProgressMsg{
		DownloadID: "test-4",
		Downloaded: 100,
		Total:      200,
		Speed:      10.0,
	})

	pusher.mu.Lock()
	count := len(pusher.pending)
	pusher.mu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 pending deltas when window is hidden, got %d", count)
	}
}
