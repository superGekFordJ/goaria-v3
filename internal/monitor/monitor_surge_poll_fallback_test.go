package monitor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

// mockSurgeListReader is a test-only surgeListReader that returns
// configurable active/waiting/stopped task lists.
type mockSurgeListReader struct {
	mu       sync.Mutex
	active   []rpc.Task
	waiting  []rpc.Task
	stopped  []rpc.Task
	activeFn func() ([]rpc.Task, error)
	err      error
}

func (m *mockSurgeListReader) TellActive() ([]rpc.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeFn != nil {
		return m.activeFn()
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.active, nil
}

func (m *mockSurgeListReader) TellWaiting(offset, num int) ([]rpc.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return m.waiting, nil
}

func (m *mockSurgeListReader) TellStopped(offset, num int) ([]rpc.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return m.stopped, nil
}

func (m *mockSurgeListReader) setLists(active, waiting, stopped []rpc.Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = active
	m.waiting = waiting
	m.stopped = stopped
}

// newReconcileTestMonitor builds a Monitor wired with a mockSurgeListReader,
// pusher, tracker, and hub for reconciliation tests. The caller must defer
// cache cleanup.
func newReconcileTestMonitor(t *testing.T) (*Monitor, *mockSurgeListReader, *Pusher, *TaskTracker) {
	t.Helper()
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	reader := &mockSurgeListReader{}
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{
		hub:               hub,
		pusher:            pusher,
		tracker:           tracker,
		engine:            hybrid,
		telemetry:         NewTelemetryCache(),
		surgePollReader:   reader,
		surgePollInterval: 10 * time.Second,
	}
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	t.Cleanup(func() {
		State.SetWindowExists(prevWindow)
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgStopped = nil
	})
	return m, reader, pusher, tracker
}

func resetCacheSg() {
	Cache.sgActive = nil
	Cache.sgWaiting = nil
	Cache.sgStopped = nil
}

// historyCount returns the current number of history entries.
func historyCount() int {
	entries := history.GetAll()
	return len(entries)
}

// resetHistoryForTest clears global history and disables file saves for the
// duration of a test so history-count assertions are independent of test order.
func resetHistoryForTest(t *testing.T) {
	t.Helper()
	prevSave := history.SaveEnabled
	history.DisableSaveForTest()
	history.Clear()
	t.Cleanup(func() {
		history.Clear()
		history.SetSaveEnabled(prevSave)
	})
}

// TestReconcileSurgeCache_MissedComplete verifies that a task the engine
// reports as complete but Cache still has active is moved to stopped,
// handleTaskComplete runs (writing history), and a complete delta is pushed
// to the frontend (matching the handleSurgeEvent event path).
func TestReconcileSurgeCache_MissedComplete(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	// Provide file metadata so handleTaskComplete can write history.
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()
	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "complete" {
				t.Errorf("Status = %s, want complete", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in stopped after reconcile")
	}

	if historyCount() != before+1 {
		t.Errorf("history count = %d, want %d (handleTaskComplete should write history)", historyCount(), before+1)
	}

	if !tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}

	pusher.mu.Lock()
	foundComplete := false
	for _, d := range pusher.pending {
		if d.Type == "complete" && d.GID == "sg_task1" {
			foundComplete = true
		}
	}
	pusher.mu.Unlock()
	if !foundComplete {
		t.Error("expected complete delta from poll (reconcile now emits deltas matching event path)")
	}
}

// TestReconcileSurgeCache_MissedComplete_AlreadyProcessed verifies that when
// the event path already processed a complete, the poll does not call
// handleTaskComplete again (history count unchanged).
func TestReconcileSurgeCache_MissedComplete_AlreadyProcessed(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "complete",
		TotalLength: "1000",
	}, "stopped")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	tracker.processedComplete["sg_task1"] = true

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()
	m.reconcileSurgeCache()

	if historyCount() != before {
		t.Errorf("history count = %d, want %d (should not double-process)", historyCount(), before)
	}
}

// TestReconcileSurgeCache_MissedComplete_EmitsDeltaWithPayload verifies that
// the complete delta emitted by reconcile carries the progress payload
// (completedLength/totalLength) when TotalLength is set.
func TestReconcileSurgeCache_MissedComplete_EmitsDeltaWithPayload(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	pusher.mu.Lock()
	var delta *events.TaskDelta
	for i := range pusher.pending {
		if pusher.pending[i].Type == "complete" && pusher.pending[i].GID == "sg_task1" {
			d := pusher.pending[i]
			delta = &d
			break
		}
	}
	pusher.mu.Unlock()
	if delta == nil {
		t.Fatal("expected complete delta with payload")
	}
	payload, ok := delta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", delta.Payload)
	}
	if payload["completedLength"] != "1000" {
		t.Errorf("completedLength = %v, want 1000", payload["completedLength"])
	}
	if payload["totalLength"] != "1000" {
		t.Errorf("totalLength = %v, want 1000", payload["totalLength"])
	}
}

// TestReconcileSurgeCache_MissedComplete_EmptyTotalLength verifies that when
// TotalLength is empty, the complete delta is still emitted but without a
// payload (matches handleSurgeEvent error/no-total path).
func TestReconcileSurgeCache_MissedComplete_EmptyTotalLength(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:    "sg_task1",
		Status: "active",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete"}})

	m.reconcileSurgeCache()

	pusher.mu.Lock()
	foundDelta := false
	for _, d := range pusher.pending {
		if d.Type == "complete" && d.GID == "sg_task1" {
			foundDelta = true
			if d.Payload != nil {
				t.Errorf("expected nil payload for empty TotalLength, got %v", d.Payload)
			}
		}
	}
	pusher.mu.Unlock()
	if !foundDelta {
		t.Fatal("expected complete delta even with empty TotalLength")
	}
}

// TestReconcileSurgeCache_MissedComplete_HeadlessNoDelta verifies that in
// headless mode (no window), reconcile does not push a delta to the pusher,
// but still calls NotifyInternal (triggers forceTick for tray update).
func TestReconcileSurgeCache_MissedComplete_HeadlessNoDelta(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	// Override to headless after newReconcileTestMonitor set it true.
	prevWindow := State.HasWindow()
	State.SetWindowExists(false)
	t.Cleanup(func() { State.SetWindowExists(prevWindow) })

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Register a delta listener to verify NotifyInternal fires.
	var internalFired atomic.Bool
	m.hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		if delta.Type == "complete" && delta.GID == "sg_task1" {
			internalFired.Store(true)
		}
	})

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	pusher.mu.Lock()
	for _, d := range pusher.pending {
		if d.Type == "complete" && d.GID == "sg_task1" {
			t.Error("did not expect complete delta in headless mode")
		}
	}
	pusher.mu.Unlock()

	if !internalFired.Load() {
		t.Error("expected NotifyInternal to fire even in headless mode")
	}
}

// TestReconcileSurgeCache_MissedComplete_DuplicateDeltaDedup verifies that
// when the event path already queued a complete delta, reconcile's delta is
// deduped by the pusher (same GID, same type → replacement, single entry).
func TestReconcileSurgeCache_MissedComplete_DuplicateDeltaDedup(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Simulate the event path already queuing a complete delta.
	pusher.Queue(events.TaskDelta{
		Type: "complete", GID: "sg_task1",
		Payload: map[string]string{
			"completedLength": "1000",
			"downloadSpeed":   "0",
			"totalLength":     "1000",
		},
	})

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	pusher.mu.Lock()
	count := 0
	for _, d := range pusher.pending {
		if d.GID == "sg_task1" && d.Type == "complete" {
			count++
		}
	}
	pusher.mu.Unlock()
	if count != 1 {
		t.Errorf("expected exactly 1 complete delta for sg_task1 (dedup), got %d", count)
	}
}

// TestReconcileSurgeCache_MissedPause_EmitsDelta verifies that the waiting
// branch emits a pause delta and an EmitTaskMove (From: active, To: waiting).
func TestReconcileSurgeCache_MissedPause_EmitsDelta(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "active"}, "active")

	var moveFired atomic.Bool
	var moveFrom, moveTo string
	m.hub.SubscribeTaskMove(func(move events.TaskMove) {
		if move.GID == "sg_task1" {
			moveFired.Store(true)
			moveFrom = move.From
			moveTo = move.To
		}
	})

	reader.setLists(nil, []rpc.Task{{GID: "task1", Status: "paused"}}, nil)

	m.reconcileSurgeCache()

	pusher.mu.Lock()
	foundPause := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_task1" {
			foundPause = true
		}
	}
	pusher.mu.Unlock()
	if !foundPause {
		t.Error("expected pause delta from poll")
	}

	if !moveFired.Load() {
		t.Fatal("expected EmitTaskMove for missed pause")
	}
	if moveFrom != "active" {
		t.Errorf("EmitTaskMove From = %s, want active", moveFrom)
	}
	if moveTo != "waiting" {
		t.Errorf("EmitTaskMove To = %s, want waiting", moveTo)
	}
}

// TestReconcileSurgeCache_MissedResume_EmitsDelta verifies that the active
// branch emits a resume delta and an EmitTaskMove (From: waiting, To: active).
func TestReconcileSurgeCache_MissedResume_EmitsDelta(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "paused"}, "waiting")

	var moveFired atomic.Bool
	var moveFrom, moveTo string
	m.hub.SubscribeTaskMove(func(move events.TaskMove) {
		if move.GID == "sg_task1" {
			moveFired.Store(true)
			moveFrom = move.From
			moveTo = move.To
		}
	})

	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading"}}, nil, nil)

	m.reconcileSurgeCache()

	pusher.mu.Lock()
	foundResume := false
	for _, d := range pusher.pending {
		if d.Type == "resume" && d.GID == "sg_task1" {
			foundResume = true
		}
	}
	pusher.mu.Unlock()
	if !foundResume {
		t.Error("expected resume delta from poll")
	}

	if !moveFired.Load() {
		t.Fatal("expected EmitTaskMove for missed resume")
	}
	if moveFrom != "waiting" {
		t.Errorf("EmitTaskMove From = %s, want waiting", moveFrom)
	}
	if moveTo != "active" {
		t.Errorf("EmitTaskMove To = %s, want active", moveTo)
	}
}

// TestReconcileSurgeCache_MissedPause verifies that a task the engine reports
// as paused but Cache has active is moved to waiting.
func TestReconcileSurgeCache_MissedPause(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "active"}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 0, "", 0, "active")

	reader.setLists(nil, []rpc.Task{{GID: "task1", Status: "paused"}}, nil)

	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "paused" {
				t.Errorf("Status = %s, want paused", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in waiting after reconcile")
	}
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in active after reconcile")
		}
	}

	if tt := tracker.tasks["sg_task1"]; tt != nil {
		if tt.Status != "paused" {
			t.Errorf("tracker status = %s, want paused", tt.Status)
		}
	} else {
		t.Fatal("expected tracker entry for sg_task1")
	}
	for _, at := range tracker.GetActiveTrackedTasks() {
		if at.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in active tracked tasks after missed pause")
		}
	}
}

// TestReconcileSurgeCache_MissedResume verifies that a task the engine reports
// as active but Cache has waiting is moved to active.
func TestReconcileSurgeCache_MissedResume(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "paused"}, "waiting")
	tracker.EnsureTrackedFromEvent("sg_task1", 0, "", 0, "paused")

	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading"}}, nil, nil)

	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "active" {
				t.Errorf("Status = %s, want active", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in active after reconcile")
	}

	if tt := tracker.tasks["sg_task1"]; tt != nil {
		if tt.Status != "active" {
			t.Errorf("tracker status = %s, want active", tt.Status)
		}
	} else {
		t.Fatal("expected tracker entry for sg_task1")
	}
	foundActiveTracked := false
	for _, at := range tracker.GetActiveTrackedTasks() {
		if at.GID == "sg_task1" {
			foundActiveTracked = true
		}
	}
	if !foundActiveTracked {
		t.Fatal("expected sg_task1 in active tracked tasks after missed resume")
	}
}

// TestReconcileSurgeCache_MissedRemove verifies that a task in Cache but not
// in the engine is removed and a remove delta is pushed.
func TestReconcileSurgeCache_MissedRemove(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "complete"}, "stopped")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com", 4, "active")

	reader.setLists(nil, nil, nil)

	m.reconcileSurgeCache()

	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in stopped after reconcile")
		}
	}

	pusher.mu.Lock()
	deltaFound := false
	for _, d := range pusher.pending {
		if d.Type == "remove" && d.GID == "sg_task1" {
			deltaFound = true
		}
	}
	pusher.mu.Unlock()
	if !deltaFound {
		t.Fatal("expected remove delta pushed for sg_task1")
	}

	if tracker.tasks["sg_task1"] != nil {
		t.Error("expected tracker entry removed for sg_task1")
	}
}

// TestReconcileSurgeCache_MissedAdd verifies that a task in the engine but not
// in Cache is added to the correct list. No add delta is pushed.
func TestReconcileSurgeCache_MissedAdd(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading", TotalLength: "5000"}}, nil, nil)

	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in active after reconcile")
	}

	pusher.mu.Lock()
	for _, d := range pusher.pending {
		if d.Type == "add" && d.GID == "sg_task1" {
			t.Error("did not expect add delta from poll")
		}
	}
	pusher.mu.Unlock()
}

// TestReconcileSurgeCache_MissedAddWithComplete verifies that a task the engine
// reports as complete but is absent from Cache (both add and complete events
// dropped, or startup) is added to stopped AND has handleTaskComplete run so
// history/speed stats are not permanently lost.
func TestReconcileSurgeCache_MissedAddWithComplete(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()
	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "complete" {
				t.Errorf("Status = %s, want complete", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in stopped after missed-add-with-complete")
	}

	if got := historyCount(); got != before+1 {
		t.Errorf("history count = %d, want %d (handleTaskComplete should run for missed complete)", got, before+1)
	}
	if !tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}
}

// TestReconcileSurgeCache_RapidStateChange verifies that when a task cycles
// active→waiting→active→complete between polls, the poll reconciles to the
// final state (stopped) and records history exactly once.
func TestReconcileSurgeCache_RapidStateChange(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Simulate intermediate states the poll never observed; only the final
	// engine state matters for reconciliation.
	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()
	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "complete" {
				t.Errorf("Status = %s, want complete", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in stopped after rapid state change")
	}
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in active after reconcile")
		}
	}

	if got := historyCount(); got != before+1 {
		t.Errorf("history count = %d, want %d (final complete processed once)", got, before+1)
	}
	if !tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}
}

// TestReconcileSurgeCache_NoDiscrepancy verifies that when Cache and engine
// agree, no corrections or deltas occur.
func TestReconcileSurgeCache_NoDiscrepancy(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "active"}, "active")
	Cache.AddSgTask(rpc.Task{GID: "sg_task2", Status: "paused"}, "waiting")
	Cache.AddSgTask(rpc.Task{GID: "sg_task3", Status: "complete"}, "stopped")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "", 4, "active")

	reader.setLists(
		[]rpc.Task{{GID: "task1", Status: "downloading"}},
		[]rpc.Task{{GID: "task2", Status: "paused"}},
		[]rpc.Task{{GID: "task3", Status: "complete"}},
	)

	before := historyCount()
	m.reconcileSurgeCache()

	if historyCount() != before {
		t.Errorf("history count changed: %d != %d", historyCount(), before)
	}

	pusher.mu.Lock()
	if len(pusher.pending) > 0 {
		t.Errorf("expected no deltas, got %d", len(pusher.pending))
	}
	pusher.mu.Unlock()
}

// TestReconcileSurgeCache_NilSurgeEng verifies that reconcileSurgeCache
// returns immediately without panic when no Surge reader is available.
func TestReconcileSurgeCache_NilSurgeEng(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:               hub,
		pusher:            NewPusher(hub),
		tracker:           NewTaskTracker(),
		engine:            &mockSafeEngine{},
		surgePollInterval: 10 * time.Second,
	}

	m.reconcileSurgeCache()
}

// TestReconcileSurgeCache_TellActiveError verifies that a TellActive error
// causes the poll to skip that round without panic.
func TestReconcileSurgeCache_TellActiveError(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	reader.mu.Lock()
	reader.err = context.DeadlineExceeded
	reader.mu.Unlock()

	m.reconcileSurgeCache()
}

// TestReconcileSurgeCache_ConcurrentWithEvent verifies that concurrent
// reconciliation and event handling for the same GID does not double-process
// completion (processedComplete dedup) and is race-free.
func TestReconcileSurgeCache_ConcurrentWithEvent(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		m.reconcileSurgeCache()
	}()

	go func() {
		defer wg.Done()
		m.handleSurgeEvent(surgeEvents.DownloadEvent{
			Type:       surgeEvents.EventComplete,
			DownloadID: "task1",
			Total:      1000,
		})
	}()

	wg.Wait()

	if !tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}

	// Dedup must prevent double handleTaskComplete: exactly one history write.
	if got := historyCount(); got != before+1 {
		t.Errorf("history count = %d, want %d (complete should be processed exactly once)", got, before+1)
	}
}

// TestSurgePollLoop_StartupWindow verifies that surgePollLoop's first
// reconciliation populates empty sg_ slices from the engine list.
func TestSurgePollLoop_StartupWindow(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	reader.setLists(
		[]rpc.Task{{GID: "task1", Status: "downloading"}},
		[]rpc.Task{{GID: "task2", Status: "paused"}},
		[]rpc.Task{{GID: "task3", Status: "complete"}},
	)

	m.surgePollStopChan = make(chan struct{})
	m.surgePollInterval = 50 * time.Millisecond

	done := make(chan struct{})
	go func() {
		m.surgePollLoop()
		close(done)
	}()

	// Wait for the immediate first reconcile to populate cache.
	requireCondition(t, func() bool {
		return taskInCache("sg_task1", "active") &&
			taskInCache("sg_task2", "waiting") &&
			taskInCache("sg_task3", "stopped")
	}, 2*time.Second, "startup window did not populate sg_ slices")

	close(m.surgePollStopChan)
	<-done
}

// TestSurgePollLoop_PollInterval verifies that surgePollLoop runs
// reconciliation at the configured interval.
func TestSurgePollLoop_PollInterval(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	m.surgePollStopChan = make(chan struct{})
	m.surgePollInterval = 50 * time.Millisecond

	var pollCount int64

	originalReader := m.surgePollReader
	wrapped := &countingReader{surgeListReader: originalReader, count: &pollCount}
	m.surgePollReader = wrapped

	reader.setLists(nil, nil, nil)

	done := make(chan struct{})
	go func() {
		m.surgePollLoop()
		close(done)
	}()

	requireCondition(t, func() bool {
		return atomic.LoadInt64(&pollCount) >= 3
	}, 2*time.Second, "expected at least 3 poll ticks")

	close(m.surgePollStopChan)
	<-done
}

// TestStartSurgeEventBridge_DisconnectReconnect verifies that when
// StreamEvents returns an error, surgeStreamConnected is set false, and
// after a retry interval, a successful reconnection sets it true.
func TestStartSurgeEventBridge_DisconnectReconnect(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:               hub,
		pusher:            NewPusher(hub),
		tracker:           NewTaskTracker(),
		engine:            &reconnectMockEngine{},
		surgePollInterval: 50 * time.Millisecond,
		stopChan:          make(chan struct{}),
	}
	resetCacheSg()
	t.Cleanup(func() { resetCacheSg() })

	mockEng := m.engine.(*reconnectMockEngine)
	mockEng.failCount = 1 // first call fails, second succeeds
	mockEng.streamChan = make(chan any, 1)
	mockEng.streamChan <- surgeEvents.DownloadEvent{Type: surgeEvents.EventProgress, DownloadID: "x"}

	done := make(chan struct{})
	go func() {
		m.surgeEventBridgeLoop()
		close(done)
	}()

	requireCondition(t, func() bool {
		return m.surgeStreamConnected.Load()
	}, 2*time.Second, "expected surgeStreamConnected=true after reconnect")

	close(m.stopChan)
	<-done
}

// TestStartSurgeEventBridge_ChannelClose verifies that when the event stream
// channel closes, surgeStreamConnected is set false and the loop retries.
func TestStartSurgeEventBridge_ChannelClose(t *testing.T) {
	hub := events.NewHub(nil)
	mockEng := &reconnectMockEngine{
		streamChan: make(chan any),
	}
	m := &Monitor{
		hub:               hub,
		pusher:            NewPusher(hub),
		tracker:           NewTaskTracker(),
		engine:            mockEng,
		surgePollInterval: 50 * time.Millisecond,
		stopChan:          make(chan struct{}),
	}
	resetCacheSg()
	t.Cleanup(func() { resetCacheSg() })

	done := make(chan struct{})
	go func() {
		m.surgeEventBridgeLoop()
		close(done)
	}()

	requireCondition(t, func() bool {
		return m.surgeStreamConnected.Load()
	}, 1*time.Second, "expected connected after first subscribe")

	close(mockEng.streamChan)

	requireCondition(t, func() bool {
		return !m.surgeStreamConnected.Load()
	}, 1*time.Second, "expected disconnected after channel close")

	close(m.stopChan)
	<-done
}

// TestStartSurgeEventBridge_Aria2Only verifies that startSurgeEventBridge
// does not enter a reconnect loop when surgeEng is nil (Aria2-only mode).
func TestStartSurgeEventBridge_Aria2Only(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:               hub,
		pusher:            NewPusher(hub),
		tracker:           NewTaskTracker(),
		engine:            &mockSafeEngine{},
		surgePollInterval: 50 * time.Millisecond,
		stopChan:          make(chan struct{}),
	}

	// surgeEng is nil (Aria2-only mode) — startSurgeEventBridge must return
	// immediately without launching a reconnect loop.
	m.startSurgeEventBridge()

	if m.surgeStreamConnected.Load() {
		t.Error("expected surgeStreamConnected=false in Aria2-only mode")
	}

	close(m.stopChan)
}

// TestReconcileSurgeCache_MissedComplete_ClearsPauseResumeIntention verifies
// that the reconcile stopped branch clears the pauseResumeIntentions entry for
// the completed GID, matching handleSurgeEvent's terminal cleanup.
func TestReconcileSurgeCache_MissedComplete_ClearsPauseResumeIntention(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	m.pauseResumeIntentions = map[string]string{"sg_task1": "pause"}

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	m.pauseResumeVersionMu.RLock()
	_, exists := m.pauseResumeIntentions["sg_task1"]
	m.pauseResumeVersionMu.RUnlock()
	if exists {
		t.Error("expected pauseResumeIntentions[sg_task1] cleared after reconcile stopped")
	}
}

// TestReconcileSurgeCache_MissedRemove_ClearsPauseResumeIntention verifies
// that the reconcile remove path clears the pauseResumeIntentions entry,
// matching handleSurgeEvent's terminal cleanup.
func TestReconcileSurgeCache_MissedRemove_ClearsPauseResumeIntention(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	m.pauseResumeIntentions = map[string]string{"sg_task1": "pause"}

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "complete"}, "stopped")

	reader.setLists(nil, nil, nil)

	m.reconcileSurgeCache()

	m.pauseResumeVersionMu.RLock()
	_, exists := m.pauseResumeIntentions["sg_task1"]
	m.pauseResumeVersionMu.RUnlock()
	if exists {
		t.Error("expected pauseResumeIntentions[sg_task1] cleared after reconcile remove")
	}
}

// TestReconcileSurgeCache_MissedAddWithComplete_ClearsPauseResumeIntention
// verifies that the "added missing task" stopped path also clears the
// pauseResumeIntentions entry.
func TestReconcileSurgeCache_MissedAddWithComplete_ClearsPauseResumeIntention(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	m.pauseResumeIntentions = map[string]string{"sg_task1": "pause"}

	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	m.pauseResumeVersionMu.RLock()
	_, exists := m.pauseResumeIntentions["sg_task1"]
	m.pauseResumeVersionMu.RUnlock()
	if exists {
		t.Error("expected pauseResumeIntentions[sg_task1] cleared after missed-add-with-complete")
	}
}

// TestReconcileSurgeCache_MissedComplete_UpdatesTrackerTotalLength verifies
// that the reconcile stopped branch calls EnsureTrackedFromEvent to update the
// tracker's TotalLength from the engine task before MarkCompleteFromEvent, so
// handleTaskComplete records accurate history. This matches handleSurgeEvent's
// complete case which calls EnsureTrackedFromEvent(completeTotal) first.
func TestReconcileSurgeCache_MissedComplete_UpdatesTrackerTotalLength(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	// Track the task with TotalLength=0 (simulates stale/zero tracker state).
	tracker.EnsureTrackedFromEvent("sg_task1", 0, "https://example.com/file.zip", 4, "active")
	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Engine reports the task as complete with TotalLength=5000.
	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "5000"}})

	m.reconcileSurgeCache()

	tt := tracker.tasks["sg_task1"]
	if tt == nil {
		t.Fatal("expected tracker entry for sg_task1")
	}
	if tt.TotalLength != 5000 {
		t.Errorf("tracker TotalLength = %d, want 5000 (EnsureTrackedFromEvent should update from engine)", tt.TotalLength)
	}
}

// --- helpers ---

type countingReader struct {
	surgeListReader
	count *int64
}

func (c *countingReader) TellActive() ([]rpc.Task, error) {
	atomic.AddInt64(c.count, 1)
	return c.surgeListReader.TellActive()
}

func (c *countingReader) TellWaiting(offset, num int) ([]rpc.Task, error) {
	atomic.AddInt64(c.count, 1)
	return c.surgeListReader.TellWaiting(offset, num)
}

func (c *countingReader) TellStopped(offset, num int) ([]rpc.Task, error) {
	atomic.AddInt64(c.count, 1)
	return c.surgeListReader.TellStopped(offset, num)
}

// reconnectMockEngine is a mockSafeEngine whose StreamEvents can be
// configured to fail N times then succeed, and whose channel can be closed
// to simulate disconnection.
type reconnectMockEngine struct {
	mockSafeEngine
	failCount  int
	failMu     sync.Mutex
	streamChan chan any
}

func (e *reconnectMockEngine) StreamEvents(ctx context.Context) (<-chan any, func(), error) {
	e.failMu.Lock()
	if e.failCount > 0 {
		e.failCount--
		e.failMu.Unlock()
		return nil, func() {}, context.Canceled
	}
	e.failMu.Unlock()
	cleanup := func() {}
	return e.streamChan, cleanup, nil
}

func (e *reconnectMockEngine) IsSurgeActive() bool {
	return true
}

func taskInCache(gid, list string) bool {
	switch list {
	case "active":
		for _, task := range Cache.GetActive() {
			if task.GID == gid {
				return true
			}
		}
	case "waiting":
		for _, task := range Cache.GetWaiting() {
			if task.GID == gid {
				return true
			}
		}
	case "stopped":
		for _, task := range Cache.GetStopped() {
			if task.GID == gid {
				return true
			}
		}
	}
	return false
}

func requireCondition(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
