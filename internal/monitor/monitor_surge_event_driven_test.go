package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/engine/events"
)

// TestEventDriven_AddQueuedMsg_InsertsCacheWaiting verifies that
// DownloadQueuedMsg inserts a task into Cache.sgWaiting and pushes an "add" delta.
func TestEventDriven_AddQueuedMsg_InsertsCacheWaiting(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	m.handleSurgeEvent(surgeEvents.DownloadQueuedMsg{
		DownloadID: "evt-queued-1",
		URL:        "https://example.com/file.zip",
		Workers:    4,
	})

	// Verify task is in Cache.sgWaiting
	Cache.sgMu.RLock()
	found := false
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_evt-queued-1" {
			found = true
			if task.Status != "waiting" {
				t.Errorf("Status = %s, want waiting", task.Status)
			}
		}
	}
	Cache.sgMu.RUnlock()
	if !found {
		t.Fatal("expected sg_evt-queued-1 in Cache.sgWaiting after DownloadQueuedMsg")
	}

	// Verify add delta was pushed
	pusher.mu.Lock()
	deltaFound := false
	for _, d := range pusher.pending {
		if d.Type == "add" && d.GID == "sg_evt-queued-1" {
			deltaFound = true
		}
	}
	pusher.mu.Unlock()
	if !deltaFound {
		t.Fatal("expected add delta pushed for sg_evt-queued-1")
	}
}

// TestEventDriven_AddStartedMsg_InsertsCacheActive verifies that
// DownloadStartedMsg inserts a task into Cache.sgActive and pushes an "add" delta.
func TestEventDriven_AddStartedMsg_InsertsCacheActive(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "evt-started-1",
		Total:      50000000,
		URL:        "https://example.com/large.zip",
		Workers:    8,
	})

	// Verify task is in Cache.sgActive
	Cache.sgMu.RLock()
	found := false
	for _, task := range Cache.sgActive {
		if task.GID == "sg_evt-started-1" {
			found = true
			if task.Status != "active" {
				t.Errorf("Status = %s, want active", task.Status)
			}
		}
	}
	Cache.sgMu.RUnlock()
	if !found {
		t.Fatal("expected sg_evt-started-1 in Cache.sgActive after DownloadStartedMsg")
	}

	// Verify add delta was pushed
	pusher.mu.Lock()
	deltaFound := false
	for _, d := range pusher.pending {
		if d.Type == "add" && d.GID == "sg_evt-started-1" {
			deltaFound = true
		}
	}
	pusher.mu.Unlock()
	if !deltaFound {
		t.Fatal("expected add delta pushed for sg_evt-started-1")
	}
}

// TestEventDriven_RemoveMsg_DeletesCacheAndPushesRemoveDelta verifies that
// DownloadRemovedMsg removes the task from Cache and pushes a "remove" delta.
func TestEventDriven_RemoveMsg_DeletesCacheAndPushesRemoveDelta(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	// Seed a task in cache
	Cache.AddSgTask(rpc.Task{
		GID:    "sg_evt-remove-1",
		Status: "active",
	}, "active")

	// Also seed tracker and telemetry
	tracker.EnsureTrackedFromEvent("sg_evt-remove-1", 1000, "https://example.com", 4)
	if m.telemetry == nil {
		m.telemetry = NewTelemetryCache()
	}

	m.handleSurgeEvent(surgeEvents.DownloadRemovedMsg{
		DownloadID: "evt-remove-1",
	})

	// Verify task is NOT in any cache slice
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_evt-remove-1" {
			t.Fatal("expected sg_evt-remove-1 NOT in active after remove")
		}
	}
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_evt-remove-1" {
			t.Fatal("expected sg_evt-remove-1 NOT in waiting after remove")
		}
	}
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_evt-remove-1" {
			t.Fatal("expected sg_evt-remove-1 NOT in stopped after remove")
		}
	}

	// Verify remove delta was pushed
	pusher.mu.Lock()
	deltaFound := false
	for _, d := range pusher.pending {
		if d.Type == "remove" && d.GID == "sg_evt-remove-1" {
			deltaFound = true
		}
	}
	pusher.mu.Unlock()
	if !deltaFound {
		t.Fatal("expected remove delta pushed for sg_evt-remove-1")
	}

	// Verify tracker cleaned up
	if tracker.tasks["sg_evt-remove-1"] != nil {
		t.Error("expected tracker entry removed for sg_evt-remove-1")
	}
}

// TestEventDriven_PauseMovesTaskToWaiting verifies that DownloadPausedMsg
// moves the task from active to waiting in the cache (event-driven path).
func TestEventDriven_PauseMovesTaskToWaiting(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	Cache.sgActive = []rpc.Task{{GID: "sg_evt-pause-1", Status: "active", DownloadSpeed: "100"}}

	m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{
		DownloadID: "evt-pause-1",
	})

	// Verify task moved to waiting
	Cache.sgMu.RLock()
	foundWaiting := false
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_evt-pause-1" {
			foundWaiting = true
			if task.Status != "paused" {
				t.Errorf("Status = %s, want paused", task.Status)
			}
		}
	}
	stillActive := false
	for _, task := range Cache.sgActive {
		if task.GID == "sg_evt-pause-1" {
			stillActive = true
		}
	}
	Cache.sgMu.RUnlock()
	if !foundWaiting {
		t.Fatal("expected sg_evt-pause-1 in waiting after pause")
	}
	if stillActive {
		t.Fatal("expected sg_evt-pause-1 NOT in active after pause")
	}
}

// TestEventDriven_ResumeMovesTaskToActive verifies that DownloadResumedMsg
// moves the task from waiting to active in the cache (event-driven path).
func TestEventDriven_ResumeMovesTaskToActive(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:    hub,
		pusher: pusher,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	Cache.sgWaiting = []rpc.Task{{GID: "sg_evt-resume-1", Status: "paused", DownloadSpeed: "0"}}

	m.handleSurgeEvent(surgeEvents.DownloadResumedMsg{
		DownloadID: "evt-resume-1",
	})

	// Verify task moved to active
	Cache.sgMu.RLock()
	foundActive := false
	for _, task := range Cache.sgActive {
		if task.GID == "sg_evt-resume-1" {
			foundActive = true
			if task.Status != "active" {
				t.Errorf("Status = %s, want active", task.Status)
			}
		}
	}
	stillWaiting := false
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_evt-resume-1" {
			stillWaiting = true
		}
	}
	Cache.sgMu.RUnlock()
	if !foundActive {
		t.Fatal("expected sg_evt-resume-1 in active after resume")
	}
	if stillWaiting {
		t.Fatal("expected sg_evt-resume-1 NOT in waiting after resume")
	}
}

// TestEventDriven_CompleteMovesToStopped verifies that DownloadCompleteMsg
// moves the task to Cache.sgStopped.
func TestEventDriven_CompleteMovesToStopped(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	// Seed task in active
	Cache.AddSgTask(rpc.Task{
		GID:    "sg_evt-complete-1",
		Status: "active",
	}, "active")

	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "evt-complete-1",
		Total:      1000,
		AvgSpeed:   500,
	})

	// Verify task moved to stopped
	Cache.sgMu.RLock()
	found := false
	for _, task := range Cache.sgStopped {
		if task.GID == "sg_evt-complete-1" {
			found = true
		}
	}
	Cache.sgMu.RUnlock()
	if !found {
		t.Fatal("expected sg_evt-complete-1 in Cache.sgStopped after complete")
	}

	// Verify NOT in active
	Cache.sgMu.RLock()
	for _, task := range Cache.sgActive {
		if task.GID == "sg_evt-complete-1" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected sg_evt-complete-1 NOT in active after complete")
		}
	}
	Cache.sgMu.RUnlock()
}

// TestEventDriven_QueuedThenStarted_MovesFromWaitingToActive verifies that
// a DownloadQueuedMsg followed by DownloadStartedMsg moves the task from
// waiting to active in the cache.
func TestEventDriven_QueuedThenStarted_MovesFromWaitingToActive(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	// Queue
	m.handleSurgeEvent(surgeEvents.DownloadQueuedMsg{
		DownloadID: "evt-flow-1",
		URL:        "https://example.com/file.zip",
		Workers:    4,
	})

	Cache.sgMu.RLock()
	foundWaiting := false
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_evt-flow-1" {
			foundWaiting = true
		}
	}
	Cache.sgMu.RUnlock()
	if !foundWaiting {
		t.Fatal("expected sg_evt-flow-1 in waiting after queued")
	}

	// Started
	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "evt-flow-1",
		Total:      100000,
		URL:        "https://example.com/file.zip",
		Workers:    4,
	})

	Cache.sgMu.RLock()
	foundActive := false
	for _, task := range Cache.sgActive {
		if task.GID == "sg_evt-flow-1" {
			foundActive = true
		}
	}
	stillWaiting := false
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_evt-flow-1" {
			stillWaiting = true
		}
	}
	Cache.sgMu.RUnlock()
	if !foundActive {
		t.Fatal("expected sg_evt-flow-1 in active after started")
	}
	if stillWaiting {
		t.Fatal("expected sg_evt-flow-1 NOT in waiting after started")
	}
}

// TestEventDriven_TickDoesNotReturnSgTasks verifies that tick() filters out
// sg_ tasks from the Tell*Lite results (defensive filtering).
func TestEventDriven_TickDoesNotReturnSgTasks(t *testing.T) {
	// mockStoppedEngine returns sg_ tasks from TellStoppedLite — these should
	// be filtered out by the defensive filterSurgeTasks in tick().
	engine := &mockStoppedEngine{
		stopped: []rpc.Task{
			{GID: "sg_leaked-1", Status: "complete"},
			{GID: "ar_real-1", Status: "complete"},
		},
	}
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:              hub,
		pusher:           pusher,
		tracker:          tracker,
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * 1000000000,
		windowInterval:   1 * 1000000000,
		deletedGids:      make(map[string]time.Time),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
		Cache.arActive = nil
		Cache.arWaiting = nil
		Cache.arStopped = nil
		Cache.engine = nil
	}()

	Cache.engine = engine

	// Pre-process the ar_ task in tracker so it's not treated as newly completed
	tracker.Update(nil, nil, []rpc.Task{{GID: "ar_real-1", Status: "complete"}})

	m.mu.Lock()
	m.shouldFetchStopped = true
	m.mu.Unlock()
	m.tick()

	// sg_leaked-1 should NOT be in any cache slice
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_leaked-1" {
			t.Fatal("expected sg_leaked-1 NOT in stopped after tick (defensive filter)")
		}
	}

	// ar_real-1 SHOULD be in cache
	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "ar_real-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected ar_real-1 in stopped after tick")
	}
}

// TestEventDriven_DeleteRace_TaskNotResurrectedByTick verifies that when a
// Surge task is removed via DownloadRemovedMsg (Cache.RemoveTask), a subsequent
// tick does NOT resurrect it from the engine's TellStopped results.
// This is the Surge-path equivalent of the aria2c delete race test.
func TestEventDriven_DeleteRace_TaskNotResurrectedByTick(t *testing.T) {
	engine := &mockStoppedEngine{
		stopped: []rpc.Task{
			{GID: "sg_del-race-1", Status: "complete", TotalLength: "1000"},
		},
	}
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:              hub,
		pusher:           pusher,
		tracker:          tracker,
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * time.Second,
		windowInterval:   1 * time.Second,
		deletedGids:      make(map[string]time.Time),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
		Cache.arActive = nil
		Cache.arWaiting = nil
		Cache.arStopped = nil
		Cache.engine = nil
	}()

	Cache.engine = engine

	// Seed the task in cache via started event
	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "del-race-1",
		Total:      1000,
		URL:        "https://example.com/file.zip",
		Workers:    4,
	})

	// Complete the task
	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "del-race-1",
		Total:      1000,
		AvgSpeed:   500,
	})

	// Now remove it
	m.handleSurgeEvent(surgeEvents.DownloadRemovedMsg{
		DownloadID: "del-race-1",
	})

	// Verify it's gone from cache
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_del-race-1" {
			t.Fatal("expected sg_del-race-1 removed from cache after DownloadRemovedMsg")
		}
	}

	// Run a tick — the engine still reports the task in TellStopped,
	// but the defensive filter should prevent it from re-entering the cache.
	m.mu.Lock()
	m.shouldFetchStopped = true
	m.mu.Unlock()
	m.tick()

	// Task should NOT be resurrected
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_del-race-1" {
			t.Fatal("BUG: sg_del-race-1 resurrected by tick after removal (defensive filter failed)")
		}
	}
}

// TestEventDriven_CompletePersistenceRace verifies that a completed Surge task
// stays in Cache.sgStopped even when a tick fires and the engine reports
// the task as still stopped. The event path should be the sole maintainer.
func TestEventDriven_CompletePersistenceRace(t *testing.T) {
	engine := &mockStoppedEngine{
		stopped: []rpc.Task{
			{GID: "sg_persist-1", Status: "complete", TotalLength: "1000"},
		},
	}
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:              hub,
		pusher:           pusher,
		tracker:          tracker,
		engine:           engine,
		stopChan:         make(chan struct{}),
		forceTickChan:    make(chan struct{}, 1),
		headlessInterval: 5 * time.Second,
		windowInterval:   1 * time.Second,
		deletedGids:      make(map[string]time.Time),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
		Cache.arActive = nil
		Cache.arWaiting = nil
		Cache.arStopped = nil
		Cache.engine = nil
	}()

	Cache.engine = engine

	// Seed + complete via events
	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "persist-1",
		Total:      1000,
		URL:        "https://example.com/file.zip",
		Workers:    4,
	})
	m.handleSurgeEvent(surgeEvents.DownloadCompleteMsg{
		DownloadID: "persist-1",
		Total:      1000,
		AvgSpeed:   500,
	})

	// Verify task is in stopped
	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_persist-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected sg_persist-1 in stopped after complete event")
	}

	// Run a tick — the engine reports the task, but tick should filter sg_ tasks.
	// The task should remain in cache from the event path, not from tick.
	m.mu.Lock()
	m.shouldFetchStopped = true
	m.mu.Unlock()
	m.tick()

	// Task should still be in stopped (from event path, not tick)
	found = false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_persist-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected sg_persist-1 to persist in stopped after tick (event-driven path)")
	}
}

// TestEventDriven_RapidPauseResume verifies that rapid pause-resume-pause-resume
// sequences correctly move the task between active and waiting in the cache
// without losing the task or duplicating it.
func TestEventDriven_RapidPauseResume(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		tracker:               tracker,
		engine:                hybrid,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	// Seed task in active
	Cache.AddSgTask(rpc.Task{
		GID:    "sg_rapid-1",
		Status: "active",
	}, "active")

	// Rapid pause-resume-pause-resume cycle
	for i := 0; i < 10; i++ {
		// Pause
		m.BumpPauseResumeIntention("sg_rapid-1", PauseResumeIntentionPause)
		m.handleSurgeEvent(surgeEvents.DownloadPausedMsg{DownloadID: "rapid-1"})

		// Verify in waiting
		Cache.sgMu.RLock()
		inWaiting := false
		inActive := false
		for _, task := range Cache.sgWaiting {
			if task.GID == "sg_rapid-1" {
				inWaiting = true
			}
		}
		for _, task := range Cache.sgActive {
			if task.GID == "sg_rapid-1" {
				inActive = true
			}
		}
		Cache.sgMu.RUnlock()
		if !inWaiting {
			t.Fatalf("iteration %d: expected sg_rapid-1 in waiting after pause", i)
		}
		if inActive {
			t.Fatalf("iteration %d: expected sg_rapid-1 NOT in active after pause", i)
		}

		// Resume
		m.BumpPauseResumeIntention("sg_rapid-1", PauseResumeIntentionResume)
		m.handleSurgeEvent(surgeEvents.DownloadResumedMsg{DownloadID: "rapid-1"})

		// Verify in active
		Cache.sgMu.RLock()
		inWaiting = false
		inActive = false
		for _, task := range Cache.sgWaiting {
			if task.GID == "sg_rapid-1" {
				inWaiting = true
			}
		}
		for _, task := range Cache.sgActive {
			if task.GID == "sg_rapid-1" {
				inActive = true
			}
		}
		Cache.sgMu.RUnlock()
		if !inActive {
			t.Fatalf("iteration %d: expected sg_rapid-1 in active after resume", i)
		}
		if inWaiting {
			t.Fatalf("iteration %d: expected sg_rapid-1 NOT in waiting after resume", i)
		}
	}

	// Final check: exactly 1 task in active, 0 in waiting
	Cache.sgMu.RLock()
	activeCount := 0
	waitingCount := 0
	for _, task := range Cache.sgActive {
		if task.GID == "sg_rapid-1" {
			activeCount++
		}
	}
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_rapid-1" {
			waitingCount++
		}
	}
	Cache.sgMu.RUnlock()
	if activeCount != 1 {
		t.Errorf("expected exactly 1 sg_rapid-1 in active, got %d", activeCount)
	}
	if waitingCount != 0 {
		t.Errorf("expected 0 sg_rapid-1 in waiting, got %d", waitingCount)
	}
}

// TestEventDriven_RapidAddRemove verifies that rapid add-remove-add sequences
// correctly maintain the cache without duplication.
func TestEventDriven_RapidAddRemove(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{
		hub:     hub,
		pusher:  pusher,
		tracker: tracker,
		engine:  hybrid,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	for i := 0; i < 5; i++ {
		// Add
		m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
			DownloadID: "rapid-ar-1",
			Total:      1000,
			URL:        "https://example.com/file.zip",
			Workers:    4,
		})

		// Verify in active
		Cache.sgMu.RLock()
		found := false
		for _, task := range Cache.sgActive {
			if task.GID == "sg_rapid-ar-1" {
				found = true
			}
		}
		Cache.sgMu.RUnlock()
		if !found {
			t.Fatalf("iteration %d: expected sg_rapid-ar-1 in active after add", i)
		}

		// Remove
		m.handleSurgeEvent(surgeEvents.DownloadRemovedMsg{
			DownloadID: "rapid-ar-1",
		})

		// Verify gone
		Cache.sgMu.RLock()
		found = false
		for _, task := range Cache.sgActive {
			if task.GID == "sg_rapid-ar-1" {
				found = true
			}
		}
		Cache.sgMu.RUnlock()
		if found {
			t.Fatalf("iteration %d: expected sg_rapid-ar-1 NOT in active after remove", i)
		}
	}

	// Final: task should not be in any slice
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_rapid-ar-1" {
			t.Fatal("expected sg_rapid-ar-1 NOT in active after rapid add-remove cycle")
		}
	}
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_rapid-ar-1" {
			t.Fatal("expected sg_rapid-ar-1 NOT in waiting after rapid add-remove cycle")
		}
	}
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_rapid-ar-1" {
			t.Fatal("expected sg_rapid-ar-1 NOT in stopped after rapid add-remove cycle")
		}
	}
}
