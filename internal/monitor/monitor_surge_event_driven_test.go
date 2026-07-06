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
// moves the task from active to waiting in the cache (event-driven path)
// and emits a task:move event with From=active, To=waiting.
func TestEventDriven_PauseMovesTaskToWaiting(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeIntentions: make(map[string]string),
	}

	var recordedMove *events.TaskMove
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		rm := move
		recordedMove = &rm
	})

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

	if recordedMove == nil {
		t.Fatal("expected task:move event emitted for pause")
	}
	if recordedMove.GID != "sg_evt-pause-1" {
		t.Errorf("move GID = %s, want sg_evt-pause-1", recordedMove.GID)
	}
	if recordedMove.From != "active" {
		t.Errorf("move From = %s, want active", recordedMove.From)
	}
	if recordedMove.To != "waiting" {
		t.Errorf("move To = %s, want waiting", recordedMove.To)
	}
}

// TestEventDriven_ResumeMovesTaskToActive verifies that DownloadResumedMsg
// moves the task from waiting to active in the cache (event-driven path)
// and emits a task:move event with From=waiting, To=active.
func TestEventDriven_ResumeMovesTaskToActive(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:    hub,
		pusher: pusher,
	}

	var recordedMove *events.TaskMove
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		rm := move
		recordedMove = &rm
	})

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

	if recordedMove == nil {
		t.Fatal("expected task:move event emitted for resume")
	}
	if recordedMove.GID != "sg_evt-resume-1" {
		t.Errorf("move GID = %s, want sg_evt-resume-1", recordedMove.GID)
	}
	if recordedMove.From != "waiting" {
		t.Errorf("move From = %s, want waiting", recordedMove.From)
	}
	if recordedMove.To != "active" {
		t.Errorf("move To = %s, want active", recordedMove.To)
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

// TestEventDriven_PrefetchMetadata_EnrichesSgSliceEntry verifies that after
// handleSurgeEvent processes a DownloadStartedMsg, the sg slice entry is
// enriched with full metadata (Files/Title/Dir) from the TellStatus result,
// not just the minimal GID/Status/TotalLength fields. This ensures that
// GetTasks/GetFullSnapshot return enriched sg tasks without per-call EnrichTasks.
func TestEventDriven_PrefetchMetadata_EnrichesSgSliceEntry(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockTellStatusEngine{
		task: rpc.Task{
			Title:       "file.zip",
			Dir:         "/downloads",
			Files:       []rpc.File{{Path: "/downloads/file.zip", Uris: []rpc.Uri{{Uri: "https://example.com/file.zip"}}}},
			Status:      "active",
			TotalLength: "1000",
		},
	}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
		Cache.engine = nil
		Cache.metadata = make(map[string]*TaskMetadata)
	}()

	Cache.engine = hybrid

	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "enrich-1",
		Total:      1000,
		URL:        "https://example.com/file.zip",
		Workers:    4,
	})

	// Verify the sg slice entry itself is enriched (not just the add delta copy)
	Cache.sgMu.RLock()
	var found *rpc.Task
	for i := range Cache.sgActive {
		if Cache.sgActive[i].GID == "sg_enrich-1" {
			t := Cache.sgActive[i]
			found = &t
			break
		}
	}
	Cache.sgMu.RUnlock()
	if found == nil {
		t.Fatal("expected sg_enrich-1 in sgActive after DownloadStartedMsg")
	}
	if found.Title != "file.zip" {
		t.Errorf("Title = %q, want file.zip", found.Title)
	}
	if found.Dir != "/downloads" {
		t.Errorf("Dir = %q, want /downloads", found.Dir)
	}
	if len(found.Files) == 0 || found.Files[0].Path != "/downloads/file.zip" {
		t.Errorf("Files not enriched, got %v", found.Files)
	}

	// Verify GetActive returns the enriched task without extra EnrichTasks
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_enrich-1" {
			if task.Title != "file.zip" {
				t.Errorf("GetActive Title = %q, want file.zip", task.Title)
			}
			if task.Dir != "/downloads" {
				t.Errorf("GetActive Dir = %q, want /downloads", task.Dir)
			}
			if len(task.Files) == 0 || task.Files[0].Path != "/downloads/file.zip" {
				t.Errorf("GetActive Files not enriched, got %v", task.Files)
			}
		}
	}
}

// TestPreserveLoop_OnlyArStopped verifies that the shouldFetchStoppedUntil
// fast-retry preserve loop skips sg_ tasks, preserving only ar_ stopped tasks
// that are not yet in the tick's stopped list. This exercises the preserve
// loop body (which existing tests do not, since they never set
// shouldFetchStoppedUntil).
func TestPreserveLoop_OnlyArStopped(t *testing.T) {
	engine := &mockStoppedEngine{
		stopped: []rpc.Task{
			{GID: "ar_tick-1", Status: "complete", TotalLength: "1000"},
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

	// Pre-seed a sg_ stopped task and an ar_ stopped task in cache that
	// the tick's engine result does NOT include. The preserve loop should
	// keep the ar_ task but skip the sg_ task (sg_ maintained by event path).
	Cache.AddSgTask(rpc.Task{GID: "sg_preserve-1", Status: "complete", TotalLength: "500"}, "stopped")
	Cache.arMu.Lock()
	Cache.arStopped = []rpc.Task{{GID: "ar_preserve-1", Status: "complete", TotalLength: "800"}}
	Cache.arMu.Unlock()

	// Pre-process the ar_tick-1 in tracker so it's not treated as newly completed
	tracker.Update(nil, nil, []rpc.Task{{GID: "ar_tick-1", Status: "complete"}})

	// Set shouldFetchStopped + shouldFetchStoppedUntil so the preserve loop runs
	m.mu.Lock()
	m.shouldFetchStopped = true
	m.shouldFetchStoppedUntil = time.Now().Add(5 * time.Second)
	m.mu.Unlock()

	m.tick()

	// ar_preserve-1 should be preserved (kept by the preserve loop)
	foundArPreserve := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "ar_preserve-1" {
			foundArPreserve = true
		}
	}
	if !foundArPreserve {
		t.Fatal("expected ar_preserve-1 preserved in stopped after tick")
	}

	// sg_preserve-1 should still be in cache (event path maintains it,
	// tick does not touch sg_ slices via UpdateFromAria2)
	foundSgPreserve := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_preserve-1" {
			foundSgPreserve = true
		}
	}
	if !foundSgPreserve {
		t.Fatal("expected sg_preserve-1 still in stopped (event-maintained, not cleared by tick)")
	}

	// ar_tick-1 from the engine should be in cache
	foundArTick := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "ar_tick-1" {
			foundArTick = true
		}
	}
	if !foundArTick {
		t.Fatal("expected ar_tick-1 in stopped after tick")
	}
}

// TestSurgeRemove_DirectCacheDelete_NoTombstone verifies that removing a sg_
// task via DownloadRemovedMsg deletes it directly from the cache slices without
// adding a tombstone entry to deletedGids. sg_ tasks use direct cache deletion
// (Cache.RemoveTask), not the ar_ tombstone mechanism (deletedGids).
func TestSurgeRemove_DirectCacheDelete_NoTombstone(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{
		hub:         hub,
		pusher:      pusher,
		tracker:     tracker,
		engine:      hybrid,
		deletedGids: make(map[string]time.Time),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	defer func() {
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	}()

	// Seed a sg_ task in cache
	Cache.AddSgTask(rpc.Task{GID: "sg_tombstone-1", Status: "active"}, "active")

	m.handleSurgeEvent(surgeEvents.DownloadRemovedMsg{
		DownloadID: "tombstone-1",
	})

	// Verify task is gone from all cache slices
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_tombstone-1" {
			t.Fatal("expected sg_tombstone-1 NOT in active after remove")
		}
	}
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_tombstone-1" {
			t.Fatal("expected sg_tombstone-1 NOT in waiting after remove")
		}
	}
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_tombstone-1" {
			t.Fatal("expected sg_tombstone-1 NOT in stopped after remove")
		}
	}

	// Verify NO tombstone was set for the sg_ GID
	m.mu.Lock()
	_, hasTombstone := m.deletedGids["sg_tombstone-1"]
	m.mu.Unlock()
	if hasTombstone {
		t.Fatal("expected sg_tombstone-1 NOT in deletedGids (sg_ uses direct cache delete, not tombstone)")
	}
}

// TestHandleSurgeEvent_FirstByteMsg_SetsTTFB verifies that FirstByteMsg
// writes TTFB into the tracker without clearing scope/domain/envKey.
func TestHandleSurgeEvent_FirstByteMsg_SetsTTFB(t *testing.T) {
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

	// Create the task via DownloadStartedMsg (TTFBMs=0 initially).
	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "fb-1",
		Total:      50000000,
		URL:        "https://example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv("sg_fb-1", "wan", 0, "example.com", "envA")

	// Send FirstByteMsg with TTFB=80.
	m.handleSurgeEvent(surgeEvents.FirstByteMsg{
		DownloadID: "fb-1",
		TTFBMs:     80,
	})

	tracked := tracker.tasks["sg_fb-1"]
	if tracked == nil {
		t.Fatal("expected tracked task sg_fb-1 to exist")
	}
	if tracked.TTFBMs != 80 {
		t.Errorf("TTFBMs = %d, want 80", tracked.TTFBMs)
	}
	if tracked.Scope != "wan" || tracked.Domain != "example.com" || tracked.CurrentEnvKey != "envA" {
		t.Errorf("scope/domain/envKey cleared: scope=%q domain=%q envKey=%q", tracked.Scope, tracked.Domain, tracked.CurrentEnvKey)
	}
}

// TestHandleSurgeEvent_FirstByteMsg_NoDeltaPush verifies that FirstByteMsg
// does not push any task:delta to the frontend.
func TestHandleSurgeEvent_FirstByteMsg_NoDeltaPush(t *testing.T) {
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

	tracker.EnsureTrackedFromEvent("sg_fb-2", 1000, "https://example.com", 4)

	m.handleSurgeEvent(surgeEvents.FirstByteMsg{
		DownloadID: "fb-2",
		TTFBMs:     50,
	})

	pusher.mu.Lock()
	for _, d := range pusher.pending {
		if d.GID == "sg_fb-2" {
			pusher.mu.Unlock()
			t.Fatalf("expected no delta push for sg_fb-2, got type=%q", d.Type)
		}
	}
	pusher.mu.Unlock()
}

// TestHandleSurgeEvent_FirstByteMsg_BeforeStarted_SilentSkip verifies that
// a FirstByteMsg arriving before DownloadStartedMsg is silently skipped
// (task does not exist yet), and the subsequent DownloadStartedMsg creates
// the task with TTFBMs=0.
func TestHandleSurgeEvent_FirstByteMsg_BeforeStarted_SilentSkip(t *testing.T) {
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

	// FirstByteMsg before any DownloadStartedMsg — task does not exist.
	m.handleSurgeEvent(surgeEvents.FirstByteMsg{
		DownloadID: "fb-3",
		TTFBMs:     90,
	})

	if tracker.tasks["sg_fb-3"] != nil {
		t.Fatal("expected no tracker entry before DownloadStartedMsg")
	}

	// Now DownloadStartedMsg creates the task with TTFBMs=0.
	m.handleSurgeEvent(surgeEvents.DownloadStartedMsg{
		DownloadID: "fb-3",
		Total:      1000,
		URL:        "https://example.com/file.zip",
		Workers:    4,
	})

	tracked := tracker.tasks["sg_fb-3"]
	if tracked == nil {
		t.Fatal("expected tracked task sg_fb-3 to exist after DownloadStartedMsg")
	}
	if tracked.TTFBMs != 0 {
		t.Errorf("TTFBMs = %d, want 0 (FirstByteMsg before task creation should be skipped)", tracked.TTFBMs)
	}
}
