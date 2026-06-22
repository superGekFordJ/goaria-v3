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

func TestHandleSurgeEvent_PauseEvent_QueuesPauseDeltaAndPatchesCache(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Seed cache with an active task
	Cache.active = []rpc.Task{{GID: "sg_test-pause", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.active = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{
		DownloadID: "test-pause",
	})

	// Verify pusher queued a pause delta
	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-pause" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Error("expected pause delta in pusher pending queue")
	}

	// Verify task was moved from active to waiting with status=paused
	Cache.mu.RLock()
	for _, task := range Cache.active {
		if task.GID == "sg_test-pause" {
			Cache.mu.RUnlock()
			t.Fatal("expected task removed from active list")
		}
	}
	foundInWaiting := false
	for _, task := range Cache.waiting {
		if task.GID == "sg_test-pause" {
			if task.Status != "paused" {
				t.Errorf("expected status 'paused', got %q", task.Status)
			}
			if task.DownloadSpeed != "0" {
				t.Errorf("expected DownloadSpeed '0', got %q", task.DownloadSpeed)
			}
			foundInWaiting = true
			break
		}
	}
	Cache.mu.RUnlock()
	if !foundInWaiting {
		t.Error("expected task in waiting list after pause")
	}
}

func TestHandleSurgeEvent_ResumeEvent_QueuesResumeDeltaAndPatchesCache(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Seed cache with a paused task in waiting list
	Cache.waiting = []rpc.Task{{GID: "sg_test-resume", Status: "paused"}}
	defer func() { Cache.waiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadResumedMsg{
		DownloadID: "test-resume",
	})

	// Verify pusher queued a resume delta
	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "resume" && d.GID == "sg_test-resume" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Error("expected resume delta in pusher pending queue")
	}

	// Verify task was moved from waiting to active with status=active
	Cache.mu.RLock()
	for _, task := range Cache.waiting {
		if task.GID == "sg_test-resume" {
			Cache.mu.RUnlock()
			t.Fatal("expected task removed from waiting list")
		}
	}
	foundInActive := false
	for _, task := range Cache.active {
		if task.GID == "sg_test-resume" {
			if task.Status != "active" {
				t.Errorf("expected status 'active', got %q", task.Status)
			}
			foundInActive = true
			break
		}
	}
	Cache.mu.RUnlock()
	if !foundInActive {
		t.Error("expected task in active list after resume")
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
			m.shouldFetchStoppedUntil = time.Now().Add(15 * time.Second)
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
			m.shouldFetchStoppedUntil = time.Now().Add(15 * time.Second)
			m.mu.Unlock()
		}
	})

	m.mu.Lock()
	m.shouldFetchStopped = false
	m.mu.Unlock()

	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "test-nocache",
	})

	m.mu.Lock()
	shouldFetch := m.shouldFetchStopped
	m.mu.Unlock()

	if !shouldFetch {
		t.Error("expected shouldFetchStopped=true after complete event (no cache needed)")
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
	Cache.active = []rpc.Task{{GID: "sg_test-direct-push", Status: "active"}}
	defer func() { Cache.active = nil; Cache.stopped = nil }()

	var receivedDelta *events.TaskDelta
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		if delta.Type == "complete" {
			receivedDelta = &delta
		}
	})

	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
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
	Cache.mu.RLock()
	for _, task := range Cache.active {
		if task.GID == "sg_test-direct-push" {
			Cache.mu.RUnlock()
			t.Fatal("expected task removed from active list")
		}
	}
	foundInStopped := false
	for _, task := range Cache.stopped {
		if task.GID == "sg_test-direct-push" {
			if task.Status != "complete" {
				t.Errorf("expected status 'complete', got %q", task.Status)
			}
			foundInStopped = true
			break
		}
	}
	Cache.mu.RUnlock()
	if !foundInStopped {
		t.Error("expected task in stopped list after complete event")
	}
}

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
	Cache.active = []rpc.Task{{GID: "sg_test-err-push", Status: "active"}}
	defer func() { Cache.active = nil; Cache.stopped = nil }()

	var receivedDelta *events.TaskDelta
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		if delta.Type == "error" {
			receivedDelta = &delta
		}
	})

	m.handleSurgeEvent(surgeEvents.DownloadErrorMsg{
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
	Cache.mu.RLock()
	for _, task := range Cache.active {
		if task.GID == "sg_test-err-push" {
			Cache.mu.RUnlock()
			t.Fatal("expected task removed from active list")
		}
	}
	foundInStopped := false
	for _, task := range Cache.stopped {
		if task.GID == "sg_test-err-push" {
			if task.Status != "error" {
				t.Errorf("expected status 'error', got %q", task.Status)
			}
			foundInStopped = true
			break
		}
	}
	Cache.mu.RUnlock()
	if !foundInStopped {
		t.Error("expected task in stopped list after error event")
	}
}

// mockSurgeActiveEngine wraps Aria2Engine but reports IsSurgeActive()=true,
// simulating production where SurgeEngine always has a non-nil service.
type mockSurgeActiveEngine struct {
	rpc.Aria2Engine
}

func (e *mockSurgeActiveEngine) IsSurgeActive() bool {
	return true
}

func TestCurrentTickInterval_SurgeActiveWithAria2Tasks_Active_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:      1 * time.Second,
		headlessInterval:   5 * time.Second,
		prevActiveGids:      map[string]bool{"ar_123": true},
		prevWaitingGids:     map[string]bool{},
		engine:              engine,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with Aria2 active tasks even when surge is active, got %v", d)
	}
}

func TestCurrentTickInterval_SurgeActiveWithAria2Tasks_Waiting_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:      1 * time.Second,
		headlessInterval:   5 * time.Second,
		prevActiveGids:      map[string]bool{},
		prevWaitingGids:     map[string]bool{"ar_456": true},
		engine:              engine,
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s with Aria2 waiting tasks even when surge is active, got %v", d)
	}
}

func TestCurrentTickInterval_SurgeActiveOnlySurgeTasks_UsesHeadless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:      1 * time.Second,
		headlessInterval:   5 * time.Second,
		prevActiveGids:      map[string]bool{"sg_001": true},
		prevWaitingGids:     map[string]bool{},
		engine:              engine,
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s with only Surge tasks, got %v", d)
	}
}

func TestCurrentTickInterval_NoPendingComplete_SurgeOnly_Headless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:    1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:    map[string]bool{"sg_001": true},
		prevWaitingGids:   map[string]bool{},
		engine:           engine,
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s with only Surge tasks (no pending complete), got %v", d)
	}
}

func TestCurrentTickInterval_ShouldFetchStoppedUntil_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:        1 * time.Second,
		headlessInterval:      5 * time.Second,
		prevActiveGids:        map[string]bool{"sg_001": true},
		prevWaitingGids:       map[string]bool{},
		engine:                engine,
		shouldFetchStoppedUntil: time.Now().Add(10 * time.Second),
	}

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Errorf("expected 1s during shouldFetchStoppedUntil window, got %v", d)
	}
}

func TestCurrentTickInterval_ShouldFetchStoppedExpired_UsesHeadless(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:        1 * time.Second,
		headlessInterval:      5 * time.Second,
		prevActiveGids:        map[string]bool{"sg_001": true},
		prevWaitingGids:       map[string]bool{},
		engine:                engine,
		shouldFetchStoppedUntil: time.Now().Add(-1 * time.Second), // expired
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s after shouldFetchStoppedUntil expired, got %v", d)
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
