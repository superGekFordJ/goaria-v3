package monitor

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/download"
	surgeEvents "goaria-v3/internal/surge/engine/events"
	"goaria-v3/internal/surge/engine/types"
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
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{},
		prevWaitingGids:  map[string]bool{},
		engine:           hybrid,
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
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"ar_123": true},
		prevWaitingGids:  map[string]bool{},
		engine:           hybrid,
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
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{},
		prevWaitingGids:  map[string]bool{"ar_456": true},
		engine:           hybrid,
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
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"ar_123": true},
		prevWaitingGids:  map[string]bool{},
		engine:           hybrid,
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

// mockSafeEngine returns errors from TellStatus without panicking,
// for tests that trigger handleTaskComplete but don't need real RPC.
type mockSafeEngine struct {
	mockSurgeActiveEngine
}

func (e *mockSafeEngine) TellStatus(gid string, keys []string) (rpc.Task, error) {
	return rpc.Task{}, fmt.Errorf("mock: no engine")
}

func TestCurrentTickInterval_SurgeActiveWithAria2Tasks_Active_UsesWindow(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"ar_123": true},
		prevWaitingGids:  map[string]bool{},
		engine:           engine,
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
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{},
		prevWaitingGids:  map[string]bool{"ar_456": true},
		engine:           engine,
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
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"sg_001": true},
		prevWaitingGids:  map[string]bool{},
		engine:           engine,
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
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"sg_001": true},
		prevWaitingGids:  map[string]bool{},
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
		windowInterval:          1 * time.Second,
		headlessInterval:        5 * time.Second,
		prevActiveGids:          map[string]bool{"sg_001": true},
		prevWaitingGids:         map[string]bool{},
		engine:                  engine,
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
		windowInterval:          1 * time.Second,
		headlessInterval:        5 * time.Second,
		prevActiveGids:          map[string]bool{"sg_001": true},
		prevWaitingGids:         map[string]bool{},
		engine:                  engine,
		shouldFetchStoppedUntil: time.Now().Add(-1 * time.Second), // expired
	}

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Errorf("expected 5s after shouldFetchStoppedUntil expired, got %v", d)
	}
}

func TestCurrentTickInterval_ShouldFetchStoppedUntil_LifecycleTransition(t *testing.T) {
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	engine := &mockSurgeActiveEngine{}

	m := &Monitor{
		windowInterval:   1 * time.Second,
		headlessInterval: 5 * time.Second,
		prevActiveGids:   map[string]bool{"sg_001": true},
		prevWaitingGids:  map[string]bool{},
		engine:           engine,
		mu:               sync.Mutex{},
	}

	// Phase 1: Before any complete event — 5s headless
	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Fatalf("phase 1: expected 5s (no shouldFetchStoppedUntil), got %v", d)
	}

	// Phase 2: Simulate complete event setting shouldFetchStoppedUntil
	m.mu.Lock()
	m.shouldFetchStoppedUntil = time.Now().Add(15 * time.Second)
	m.mu.Unlock()

	if d := m.currentTickInterval(); d != 1*time.Second {
		t.Fatalf("phase 2: expected 1s (shouldFetchStoppedUntil active), got %v", d)
	}

	// Phase 3: Simulate window expiry
	m.mu.Lock()
	m.shouldFetchStoppedUntil = time.Now().Add(-1 * time.Millisecond)
	m.mu.Unlock()

	if d := m.currentTickInterval(); d != 5*time.Second {
		t.Fatalf("phase 3: expected 5s (shouldFetchStoppedUntil expired), got %v", d)
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

// TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback verifies that when a task
// completes without reaching the stable sample threshold (PeakSpeed==0),
// DownloadCompleteMsg.AvgSpeed is used as a fallback.
func TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// 1. Create tracked task via DownloadStartedMsg (no progress events → PeakSpeed stays 0)
	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "avg-fallback",
		Total:      100000000, // >50MB
		URL:        "https://example.com/large.zip",
		Workers:    8,
	})

	// 2. Complete without any ProgressMsg — PeakSpeed should be 0
	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "avg-fallback",
		Total:      100000000,
		AvgSpeed:   5000000, // 5MB/s average
	})

	// 3. Verify the tracker's internal PeakSpeed is still 0 (fallback only on copy)
	tracked := tracker.tasks["sg_avg-fallback"]
	if tracked == nil {
		t.Fatal("Expected tracked task to exist")
	}
	if tracked.PeakSpeed != 0 {
		t.Errorf("Internal PeakSpeed = %d, want 0 (fallback only on copy)", tracked.PeakSpeed)
	}
	if tracked.TotalLength != 100000000 {
		t.Errorf("TotalLength = %d, want 100000000 (from DownloadCompleteMsg.Total)", tracked.TotalLength)
	}

	// 4. Verify processedComplete was set
	if !tracker.processedComplete["sg_avg-fallback"] {
		t.Error("Expected processedComplete to be set")
	}
}

// TestHandleSurgeEvent_CompleteMsg_TotalEnrichesTrackedTask verifies that
// DownloadCompleteMsg.Total fills TotalLength when it was 0 from DownloadQueuedMsg.
func TestHandleSurgeEvent_CompleteMsg_TotalEnrichesTrackedTask(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// 1. Queue with Total=0 (DownloadQueuedMsg typically has no size)
	m.handleSurgeEvent(surgeEvents.DownloadQueuedMsg{
		DownloadID: "total-enrich",
		URL:        "https://example.com/queued.zip",
		Workers:    4,
	})

	tracked := tracker.tasks["sg_total-enrich"]
	if tracked == nil {
		t.Fatal("Expected tracked task after DownloadQueuedMsg")
	}
	if tracked.TotalLength != 0 {
		t.Errorf("TotalLength before complete = %d, want 0", tracked.TotalLength)
	}

	// 2. Complete with Total — should enrich TotalLength via EnsureTrackedFromEvent
	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "total-enrich",
		Total:      200000000, // 200MB
		AvgSpeed:   10000000,  // 10MB/s
	})

	tracked = tracker.tasks["sg_total-enrich"]
	if tracked.TotalLength != 200000000 {
		t.Errorf("TotalLength after complete = %d, want 200000000", tracked.TotalLength)
	}
}

// ==================== handleTaskComplete fallback chain + rate limit tests====================

// speedstatsRecordCount returns the number of in-memory speedstats records.
func speedstatsRecordCount() int {
	return len(speedstats.GetAllRecords())
}

// findRecordByDomain returns the most recent speedstats record matching the given domain.
func findRecordByDomain(domain string) *speedstats.SpeedRecord {
	records := speedstats.GetAllRecords()
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Domain == domain {
			return &records[i]
		}
	}
	return nil
}

// TestHandleTaskComplete_PeakThreadCountFallback verifies that handleTaskComplete
// uses PeakThreadCount (from convergence) as the primary source for speedstats ThreadCount.
func TestHandleTaskComplete_PeakThreadCountFallback(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_peak-tc", 200000000, "https://example.com/large.zip", 32)

	// Simulate convergence recording a peak at 22 workers
	tracker.RecordPeakEfficiency("sg_peak-tc", 50*1024*1024, 22)

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_peak-tc"]
	task.Status = "complete"
	task.PeakSpeed = 50 * 1024 * 1024
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\large.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d (before=%d, after=%d)", after-before, before, after)
	}

	rec := findRecordByDomain("example.com")
	if rec == nil {
		t.Fatal("expected to find speedstats record for example.com")
	}
	if rec.ThreadCount != 22 {
		t.Errorf("ThreadCount = %d, want 22 (from PeakThreadCount)", rec.ThreadCount)
	}
	if rec.PeakSpeed != 50*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d", rec.PeakSpeed, 50*1024*1024)
	}
}

// TestHandleTaskComplete_ThreadCountFallback verifies that when PeakThreadCount is 0,
// handleTaskComplete falls back to ThreadCount.
func TestHandleTaskComplete_ThreadCountFallback(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_tc-fallback", 200000000, "https://example.com/medium.zip", 16)

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_tc-fallback"]
	task.Status = "complete"
	task.PeakSpeed = 30 * 1024 * 1024
	task.ThreadCount = 16
	task.PeakThreadCount = 0 // Not set by convergence
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\medium.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d", after-before)
	}

	rec := findRecordByDomain("example.com")
	if rec == nil {
		t.Fatal("expected to find speedstats record")
	}
	if rec.ThreadCount != 16 {
		t.Errorf("ThreadCount = %d, want 16 (from ThreadCount fallback)", rec.ThreadCount)
	}
}

// TestHandleTaskComplete_ConfigFallback verifies that when both PeakThreadCount and
// ThreadCount are 0, handleTaskComplete falls back to config.MaxConnections (default 8).
func TestHandleTaskComplete_ConfigFallback(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Ensure config is set with a known MaxConnections
	origConfig := config.Current
	config.Current = &config.AppConfig{MaxConnections: "12"}
	defer func() { config.Current = origConfig }()

	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_cfg-fallback", 200000000, "https://example.com/config.zip", 0)

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_cfg-fallback"]
	task.Status = "complete"
	task.PeakSpeed = 20 * 1024 * 1024
	task.ThreadCount = 0
	task.PeakThreadCount = 0
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\config.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d", after-before)
	}

	rec := findRecordByDomain("example.com")
	if rec == nil {
		t.Fatal("expected to find speedstats record")
	}
	if rec.ThreadCount != 12 {
		t.Errorf("ThreadCount = %d, want 12 (from config.MaxConnections)", rec.ThreadCount)
	}
}

// TestHandleTaskComplete_RateLimitSkip verifies that rate-limited tasks skip
// AddRecordV2 to avoid polluting speedstats with throttled throughput.
func TestHandleTaskComplete_RateLimitSkip(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Create a WorkerPool with a rate-limited download entry
	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{
		"ratelimited": {
			URL:          "https://example.com/limited.zip",
			ID:           "ratelimited",
			RateLimitBps: 1_000_000, // 1MB/s rate limit
			RateLimitSet: true,
			State:        types.NewProgressState("ratelimited", 200000000),
		},
	})
	surge := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_ratelimited", 200000000, "https://example.com/limited.zip", 8)

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_ratelimited"]
	task.Status = "complete"
	task.PeakSpeed = 1 * 1024 * 1024 // 1MB/s (rate-limited)
	task.ThreadCount = 8
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\limited.zip"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before {
		t.Errorf("expected no new speedstats record when rate-limited, got %d new records", after-before)
	}
}

// TestHandleTaskComplete_RateLimitNotSet_RecordsNormally verifies that when no rate
// limit is active, AddRecordV2 proceeds normally.
func TestHandleTaskComplete_RateLimitNotSet_RecordsNormally(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Create a WorkerPool with a non-rate-limited download entry
	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{
		"unlimited": {
			URL:          "https://example.com/fast.zip",
			ID:           "unlimited",
			RateLimitBps: 0, // No rate limit
			RateLimitSet: false,
			State:        types.NewProgressState("unlimited", 200000000),
		},
	})
	surge := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_unlimited", 200000000, "https://example.com/fast.zip", 8)

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_unlimited"]
	task.Status = "complete"
	task.PeakSpeed = 50 * 1024 * 1024 // 50MB/s (not rate-limited)
	task.ThreadCount = 8
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\fast.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before+1 {
		t.Errorf("expected 1 new speedstats record when not rate-limited, got %d", after-before)
	}
}

// TestHandleTaskComplete_EmptyEnvKeySkipsRecording verifies that a task with
// PeakEnvKey="" (external RPC or wake-up path) does NOT produce a speedstats
// record — empty envKey is a dirty-data signal that would pollute env-aware buckets.
func TestHandleTaskComplete_EmptyEnvKeySkipsRecording(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_no_envkey", 200000000, "https://example.com/large.zip", 8)

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_no_envkey"]
	task.Status = "complete"
	task.PeakSpeed = 50 * 1024 * 1024
	task.ThreadCount = 8
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\large.zip"
	task.PeakEnvKey = "" // no envKey — should skip AddRecordV2

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before {
		t.Fatalf("expected 0 new speedstats records (empty envKey skipped), got %d (before=%d, after=%d)", after-before, before, after)
	}
}

func TestHandleSurgeEvent_DiscardsStalePauseAfterResume(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeVersions:   make(map[string]int64),
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.active = []rpc.Task{{GID: "sg_test-1", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.active = nil; Cache.waiting = nil }()

	m.BumpPauseResumeIntention("sg_test-1", "pause")
	m.BumpPauseResumeIntention("sg_test-1", "resume")

	m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{DownloadID: "test-1"})

	pusher.mu.Lock()
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-1" {
			pusher.mu.Unlock()
			t.Fatal("expected no pause delta for stale pause event")
		}
	}
	pusher.mu.Unlock()

	Cache.mu.RLock()
	for _, task := range Cache.waiting {
		if task.GID == "sg_test-1" {
			Cache.mu.RUnlock()
			t.Fatal("expected task NOT moved to waiting for stale pause")
		}
	}
	Cache.mu.RUnlock()
}

func TestHandleSurgeEvent_AcceptsPauseWhenLastIntentionIsPause(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeVersions:   make(map[string]int64),
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.active = []rpc.Task{{GID: "sg_test-2", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.active = nil; Cache.waiting = nil }()

	m.BumpPauseResumeIntention("sg_test-2", "pause")

	m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{DownloadID: "test-2"})

	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-2" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Fatal("expected pause delta when last intention is pause")
	}

	Cache.mu.RLock()
	foundInWaiting := false
	for _, task := range Cache.waiting {
		if task.GID == "sg_test-2" {
			foundInWaiting = true
			break
		}
	}
	Cache.mu.RUnlock()
	if !foundInWaiting {
		t.Fatal("expected task in waiting list when last intention is pause")
	}
}

func TestHandleSurgeEvent_AcceptsPauseWithNoPriorIntention(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeVersions:   make(map[string]int64),
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.active = []rpc.Task{{GID: "sg_test-3", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.active = nil; Cache.waiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{DownloadID: "test-3"})

	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-3" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Fatal("expected pause delta with no prior intention")
	}
}

func TestHandleSurgeEvent_PauseResumePauseSequence(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeVersions:   make(map[string]int64),
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.active = []rpc.Task{{GID: "sg_test-4", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.active = nil; Cache.waiting = nil }()

	m.BumpPauseResumeIntention("sg_test-4", "pause")
	m.BumpPauseResumeIntention("sg_test-4", "resume")
	m.BumpPauseResumeIntention("sg_test-4", "pause")

	m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{DownloadID: "test-4"})

	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-4" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Fatal("expected pause delta when last intention is pause in pause-resume-pause sequence")
	}
}

func TestHandleSurgeEvent_NilMonitorIntentionMaps(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.active = []rpc.Task{{GID: "sg_test-5", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.active = nil; Cache.waiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{DownloadID: "test-5"})

	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-5" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Fatal("expected pause delta with nil intention maps (accept by default)")
	}
}
