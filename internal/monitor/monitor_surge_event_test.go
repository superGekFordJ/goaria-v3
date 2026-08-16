package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/testutil"
	surgeEvents "goaria-v3/internal/surge/types"
)

func TestHandleSurgeEvent_ProgressMsg_QueuesProgressDelta(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventProgress,
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

	payload, ok := delta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", delta.Payload)
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

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventBatchProgress, BatchEvents: []surgeEvents.DownloadEvent{
		{DownloadID: "a", Downloaded: 100, Total: 200, Speed: 10.0},
		{DownloadID: "b", Downloaded: 300, Total: 600, Speed: 20.0},
	}})

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
	Cache.sgActive = []rpc.Task{{GID: "sg_test-pause", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventPaused,
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
	Cache.sgMu.RLock()
	for _, task := range Cache.sgActive {
		if task.GID == "sg_test-pause" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected task removed from active list")
		}
	}
	foundInWaiting := false
	for _, task := range Cache.sgWaiting {
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
	Cache.sgMu.RUnlock()
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
	Cache.sgWaiting = []rpc.Task{{GID: "sg_test-resume", Status: "paused"}}
	defer func() { Cache.sgWaiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventResumed,
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
	Cache.sgMu.RLock()
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_test-resume" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected task removed from waiting list")
		}
	}
	foundInActive := false
	for _, task := range Cache.sgActive {
		if task.GID == "sg_test-resume" {
			if task.Status != "active" {
				t.Errorf("expected status 'active', got %q", task.Status)
			}
			foundInActive = true
			break
		}
	}
	Cache.sgMu.RUnlock()
	if !foundInActive {
		t.Error("expected task in active list after resume")
	}
}

func TestHandleSurgeEvent_ResumeEvent_FromStopped_EmitsStoppedFrom(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_test-resume-stopped", Status: "error",
		ErrorCode: "9", ErrorMessage: "fail",
		Files: []rpc.File{{Path: "/dl/a.bin"}}, Dir: "/dl",
	}}
	Cache.sgActive = nil
	Cache.sgWaiting = nil
	defer func() {
		Cache.sgStopped = nil
		Cache.sgActive = nil
		Cache.sgWaiting = nil
	}()

	var gotMove *events.TaskMove
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		cp := move
		gotMove = &cp
	})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventResumed,
		DownloadID: "test-resume-stopped",
	})

	if len(Cache.GetStopped()) != 0 {
		t.Fatalf("expected stopped empty, got %#v", Cache.GetStopped())
	}
	found := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_test-resume-stopped" {
			found = true
			if task.Status != "active" {
				t.Errorf("Status = %q, want active", task.Status)
			}
			if task.ErrorCode != "" || task.ErrorMessage != "" {
				t.Errorf("expected cleared errors, got code=%q msg=%q", task.ErrorCode, task.ErrorMessage)
			}
		}
	}
	if !found {
		t.Fatal("expected task in active after resume from stopped")
	}
	if gotMove == nil {
		t.Fatal("expected task:move emission")
	}
	if gotMove.From != "stopped" || gotMove.To != "active" {
		t.Errorf("move From/To = %q/%q, want stopped/active", gotMove.From, gotMove.To)
	}
}

func TestHandleSurgeEvent_ResumeEvent_UnknownGID_NoMove(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = nil
	Cache.sgWaiting = nil
	Cache.sgStopped = nil

	var moveCount int
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		if move.GID == "sg_test-resume-missing" {
			moveCount++
		}
	})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventResumed,
		DownloadID: "test-resume-missing",
	})

	if moveCount != 0 {
		t.Fatalf("expected no task:move for unknown GID, got %d", moveCount)
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

// mockSurgeActiveEngine wraps Aria2Engine but reports IsSurgeActive()=true,
// simulating production where SurgeEngine always has a non-nil service.

func TestHandleSurgeEvent_ProgressMsg_NoWindow_DoesNotPush(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(false)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventProgress,
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

// TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback verifies that when a task
// completes without reaching the stable sample threshold (PeakSpeed==0),
// DownloadCompleteMsg.AvgSpeed is used as a fallback.
func TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	const gid = "sg_avg-fallback"

	// 1. Create tracked task via DownloadStartedMsg (no progress events → PeakSpeed stays 0)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: "avg-fallback",
		Total:      100000000, // >50MB
		URL:        "https://avg-fallback.example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv(gid, "wan", 50, "avg-fallback.example.com", "envA")

	before := speedstatsRecordCount()

	// 2. Complete without any ProgressMsg — PeakSpeed should be 0
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "avg-fallback",
		Total:      100000000,
		AvgSpeed:   5000000, // 5MB/s average
	})

	// 3. Verify the tracker's internal PeakSpeed is still 0 (fallback only on copy)
	tracked := tracker.tasks[gid]
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
	if !tracker.processedComplete[gid] {
		t.Error("Expected processedComplete to be set")
	}

	if after := speedstatsRecordCount(); after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d (before=%d, after=%d)", after-before, before, after)
	}
	rec := findRecordByDomain("avg-fallback.example.com")
	if rec == nil {
		t.Fatal("expected speedstats record")
	}
	if rec.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d, want 8 (EventStarted Workers)", rec.ThreadCount)
	}
	if rec.PeakSpeed != 5000000 {
		t.Errorf("PeakSpeed = %d, want 5000000 (AvgSpeed substitute)", rec.PeakSpeed)
	}
}

// TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback_RefreshesPeakEnvKeyToCurrent
// verifies AvgSpeed substitute-peak refreshes PeakEnvKey to Current on the
// complete copy only (resume changed Current; seed PeakEnvKey must not stick).
func TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback_RefreshesPeakEnvKeyToCurrent(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	const (
		downloadID = "avg-env-refresh"
		gid        = "sg_" + downloadID
		avgSpeed   = int64(7_000_000)
		total      = int64(100_000_000)
	)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: downloadID,
		Total:      total,
		URL:        "https://avg-env-refresh.example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv(gid, "wan", 50, "avg-env-refresh.example.com", "envA")

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected tracked task")
	}
	if tracked.PeakEnvKey != "envA" {
		t.Fatalf("PeakEnvKey seed = %q, want envA", tracked.PeakEnvKey)
	}
	if tracked.PeakSpeed != 0 {
		t.Fatalf("PeakSpeed before complete = %d, want 0", tracked.PeakSpeed)
	}
	// Resume refreshes Current only; PeakEnvKey seed stays until accept.
	tracked.CurrentEnvKey = "envB"

	before := speedstatsRecordCount()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: downloadID,
		Total:      total,
		AvgSpeed:   float64(avgSpeed),
	})

	if tracked.PeakSpeed != 0 {
		t.Errorf("tracker PeakSpeed = %d, want 0 (fallback only on copy)", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("tracker PeakEnvKey = %q, want envA (copy-only refresh)", tracked.PeakEnvKey)
	}

	if after := speedstatsRecordCount(); after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d (before=%d, after=%d)", after-before, before, after)
	}
	rec := findRecordByDomain("avg-env-refresh.example.com")
	if rec == nil {
		t.Fatal("expected speedstats record")
	}
	if rec.EnvKey != "envB" {
		t.Errorf("EnvKey = %q, want envB (AvgSpeed attributed to Current)", rec.EnvKey)
	}
	if rec.PeakSpeed != avgSpeed {
		t.Errorf("PeakSpeed = %d, want %d (AvgSpeed substitute)", rec.PeakSpeed, avgSpeed)
	}
	if rec.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d, want 8 (EventStarted Workers)", rec.ThreadCount)
	}
}

// TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback_EmptyCurrentDoesNotInventOrWipePeakEnvKey
// verifies empty CurrentEnvKey keeps the seed PeakEnvKey (no invent, no wipe).
func TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback_EmptyCurrentDoesNotInventOrWipePeakEnvKey(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	const (
		downloadID = "avg-env-empty-current"
		gid        = "sg_" + downloadID
		avgSpeed   = int64(6_000_000)
		total      = int64(100_000_000)
	)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: downloadID,
		Total:      total,
		URL:        "https://avg-empty-current.example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv(gid, "wan", 50, "avg-empty-current.example.com", "envA")

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected tracked task")
	}
	tracked.CurrentEnvKey = ""

	before := speedstatsRecordCount()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: downloadID,
		Total:      total,
		AvgSpeed:   float64(avgSpeed),
	})

	if tracked.PeakSpeed != 0 {
		t.Errorf("tracker PeakSpeed = %d, want 0", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("tracker PeakEnvKey = %q, want envA", tracked.PeakEnvKey)
	}

	if after := speedstatsRecordCount(); after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d", after-before)
	}
	rec := findRecordByDomain("avg-empty-current.example.com")
	if rec == nil {
		t.Fatal("expected speedstats record")
	}
	if rec.EnvKey != "envA" {
		t.Errorf("EnvKey = %q, want envA (empty Current must not invent or wipe)", rec.EnvKey)
	}
	if rec.PeakSpeed != avgSpeed {
		t.Errorf("PeakSpeed = %d, want %d", rec.PeakSpeed, avgSpeed)
	}
	if rec.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d, want 8 (EventStarted Workers)", rec.ThreadCount)
	}
}

// TestHandleSurgeEvent_CompleteMsg_NoAvgSpeedRefreshWhenPeakSpeedAlreadySet verifies
// that an existing peak-time accept is not overwritten by AvgSpeed / Current.
func TestHandleSurgeEvent_CompleteMsg_NoAvgSpeedRefreshWhenPeakSpeedAlreadySet(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	const (
		downloadID = "avg-env-no-refresh"
		gid        = "sg_" + downloadID
		peakSpeed  = int64(40_000_000)
		avgSpeed   = int64(5_000_000)
		total      = int64(200_000_000)
	)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: downloadID,
		Total:      total,
		URL:        "https://avg-no-refresh.example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv(gid, "wan", 50, "avg-no-refresh.example.com", "envA")
	tracker.RecordPeakEfficiency(gid, peakSpeed, 16)

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected tracked task")
	}
	if tracked.PeakSpeed != peakSpeed {
		t.Fatalf("PeakSpeed after RecordPeak = %d, want %d", tracked.PeakSpeed, peakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Fatalf("PeakEnvKey after RecordPeak = %q, want envA", tracked.PeakEnvKey)
	}
	tracked.CurrentEnvKey = "envB"

	before := speedstatsRecordCount()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: downloadID,
		Total:      total,
		AvgSpeed:   float64(avgSpeed),
	})

	if tracked.PeakSpeed != peakSpeed {
		t.Errorf("tracker PeakSpeed = %d, want %d (AvgSpeed must not overwrite)", tracked.PeakSpeed, peakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("tracker PeakEnvKey = %q, want envA", tracked.PeakEnvKey)
	}

	if after := speedstatsRecordCount(); after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d", after-before)
	}
	rec := findRecordByDomain("avg-no-refresh.example.com")
	if rec == nil {
		t.Fatal("expected speedstats record")
	}
	if rec.EnvKey != "envA" {
		t.Errorf("EnvKey = %q, want envA (no refresh when PeakSpeed already set)", rec.EnvKey)
	}
	if rec.PeakSpeed != peakSpeed {
		t.Errorf("PeakSpeed = %d, want %d", rec.PeakSpeed, peakSpeed)
	}
}

// TestHandleSurgeEvent_CompleteMsg_TotalEnrichesTrackedTask verifies that
// DownloadCompleteMsg.Total fills TotalLength when it was 0 from DownloadQueuedMsg.

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
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventQueued,
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
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
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

func TestHandleSurgeEvent_DiscardsStalePauseAfterResume(t *testing.T) {
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

	Cache.sgActive = []rpc.Task{{GID: "sg_test-1", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.BumpPauseResumeIntention("sg_test-1", PauseResumeIntentionPause)
	m.BumpPauseResumeIntention("sg_test-1", PauseResumeIntentionResume)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-1"})

	pusher.mu.Lock()
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-1" {
			pusher.mu.Unlock()
			t.Fatal("expected no pause delta for stale pause event")
		}
	}
	pusher.mu.Unlock()

	Cache.sgMu.RLock()
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_test-1" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected task NOT moved to waiting for stale pause")
		}
	}
	Cache.sgMu.RUnlock()
}

func TestHandleSurgeEvent_AcceptsPauseWhenLastIntentionIsPause(t *testing.T) {
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

	Cache.sgActive = []rpc.Task{{GID: "sg_test-2", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.BumpPauseResumeIntention("sg_test-2", PauseResumeIntentionPause)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-2"})

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

	Cache.sgMu.RLock()
	foundInWaiting := false
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_test-2" {
			foundInWaiting = true
			break
		}
	}
	Cache.sgMu.RUnlock()
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
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{GID: "sg_test-3", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-3"})

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

func TestHandleSurgeEvent_DiscardsPauseAgainstStoppedWithoutIntention(t *testing.T) {
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

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_term-1", Status: "error",
		ErrorCode: "1", ErrorMessage: "fail",
	}}
	defer func() { Cache.sgStopped = nil; Cache.sgWaiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "term-1"})

	if !Cache.IsInStopped("sg_term-1") {
		t.Fatal("expected task to remain in stopped after late pause")
	}
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_term-1" {
			t.Fatal("expected late pause not to revive stopped task into waiting")
		}
	}
}

func TestHandleSurgeEvent_DiscardsPauseAgainstStoppedEvenWithPauseIntention(t *testing.T) {
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

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_term-batch", Status: "error",
		ErrorCode: "1", ErrorMessage: "fail",
	}}
	defer func() { Cache.sgStopped = nil; Cache.sgWaiting = nil; Cache.sgActive = nil }()

	// BatchPause re-arms pause intention on terminal GIDs; must still discard.
	m.BumpPauseResumeIntention("sg_term-batch", PauseResumeIntentionPause)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "term-batch"})

	if !Cache.IsInStopped("sg_term-batch") {
		t.Fatal("expected stopped task to remain stopped despite pause intention")
	}
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_term-batch" {
			t.Fatal("expected BatchPause-armed pause not to revive stopped→waiting")
		}
	}
}

func TestHandleSurgeEvent_ResumeFromStopped_RetiresHistory(t *testing.T) {
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

	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_hist-1", Status: "error",
		ErrorCode: "1", ErrorMessage: "fail",
		Files: []rpc.File{{Path: "/tmp/hist.bin"}},
	}}
	defer func() { Cache.sgStopped = nil; Cache.sgActive = nil }()

	history.Add(history.HistoryEntry{
		GID: "sg_hist-1", Path: "/tmp/hist.bin", Status: "error",
	})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventResumed, DownloadID: "hist-1"})

	if Cache.IsInStopped("sg_hist-1") {
		t.Fatal("expected task moved out of stopped on resume")
	}
	foundActive := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_hist-1" {
			foundActive = true
		}
	}
	if !foundActive {
		t.Fatal("expected task in active after resume from stopped")
	}
	if _, ok := history.Get("sg_hist-1"); ok {
		t.Fatal("expected history entry retired after stopped→active resume")
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

func TestRetireHistoryIfResumedFromStopped_RehomesDownloadGroup(t *testing.T) {
	setupTaskGroupStoreTest(t)
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	group := testDownloadGroup("dg-retire-home")
	history.Add(history.HistoryEntry{
		GID:           "sg_grp_resume",
		Path:          "/tmp/g.bin",
		Status:        "error",
		DownloadGroup: copyDownloadGroup(&group),
	})
	history.SetGroupCleanupHooks(RemoveTaskGroup, RemoveTaskGroups, ClearTaskGroups)

	Cache.sgActive = []rpc.Task{{GID: "sg_grp_resume", Status: "active"}}
	defer func() { Cache.sgActive = nil; Cache.metadata = make(map[string]*TaskMetadata) }()

	RetireHistoryIfResumedFromStopped("sg_grp_resume", "stopped")

	if _, ok := history.Get("sg_grp_resume"); ok {
		t.Fatal("expected history removed")
	}
	stored := GetStoredTaskGroup("sg_grp_resume")
	if stored == nil || stored.ID != group.ID {
		t.Fatalf("expected download_group re-homed to group store, got %#v", stored)
	}
}

func TestTaskCache_UpdateFromAria2_RetiresHistoryOnStoppedToLive(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("ar_resume_hist", 1000, "https://example.com/ar.bin", 0, "error")
	if completed := tracker.MarkCompleteFromEvent("ar_resume_hist", "error"); completed == nil {
		t.Fatal("expected first terminal")
	}
	prevMon := State.GetMonitor()
	State.SetMonitor(&Monitor{tracker: tracker})
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.UpdateFromAria2(nil, nil, []rpc.Task{{
		GID: "ar_resume_hist", Status: "error", ErrorCode: "1",
		Files: []rpc.File{{Path: "/tmp/ar.bin"}},
	}})
	history.Add(history.HistoryEntry{
		GID: "ar_resume_hist", Path: "/tmp/ar.bin", Status: "error",
	})

	cache.UpdateFromAria2([]rpc.Task{{
		GID: "ar_resume_hist", Status: "active",
		Files: []rpc.File{{Path: "/tmp/ar.bin"}},
	}}, nil, nil)

	if _, ok := history.Get("ar_resume_hist"); ok {
		t.Fatal("expected Aria2 stopped→active to retire history")
	}
	if tracker.processedComplete["ar_resume_hist"] {
		t.Fatal("expected Aria2 stopped→live to reopen tracker processedComplete")
	}
}

func TestHandleSurgeEvent_PauseResumePauseSequence(t *testing.T) {
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

	Cache.sgActive = []rpc.Task{{GID: "sg_test-4", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.BumpPauseResumeIntention("sg_test-4", PauseResumeIntentionPause)
	m.BumpPauseResumeIntention("sg_test-4", PauseResumeIntentionResume)
	m.BumpPauseResumeIntention("sg_test-4", PauseResumeIntentionPause)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-4"})

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

	Cache.sgActive = []rpc.Task{{GID: "sg_test-5", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-5"})

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

// TestHandleSurgeEvent_InvalidatesListCacheOnPause verifies that pause/resume/
// complete/error events call SurgeEngine.InvalidateListCache, clearing the 1s
// TTL list cache so TellWaiting/TellStopped fetch fresh data on the next tick.

// TestHandleSurgeEvent_InvalidatesListCacheOnPause verifies that pause/resume/
// complete/error events call SurgeEngine.InvalidateListCache, clearing the 1s
// TTL list cache so TellWaiting/TellStopped fetch fresh data on the next tick.
func TestHandleSurgeEvent_InvalidatesListCacheOnPause(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		engine:                hybrid,
		surgeEng:              surgeEng,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	Cache.sgActive = []rpc.Task{{GID: "sg_inv-1", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil; Cache.sgStopped = nil }()

	// Populate the list cache by calling TellWaiting (which uses getDownloadList)
	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}
	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtBefore := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if cacheAtBefore.IsZero() {
		t.Fatal("expected cache to be populated before event")
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "inv-1"})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if !cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = %v, want zero (cache should be invalidated on pause)", cacheAtAfter)
	}
}

func TestHandleSurgeEvent_InvalidatesListCacheOnResume(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:      hub,
		pusher:   pusher,
		engine:   hybrid,
		surgeEng: surgeEng,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	Cache.sgWaiting = []rpc.Task{{GID: "sg_inv-2", Status: "paused", DownloadSpeed: "0"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil; Cache.sgStopped = nil }()

	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventResumed, DownloadID: "inv-2"})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if !cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = %v, want zero (cache should be invalidated on resume)", cacheAtAfter)
	}
}

func TestHandleSurgeEvent_InvalidatesListCacheOnComplete(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:      hub,
		pusher:   pusher,
		engine:   hybrid,
		surgeEng: surgeEng,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	Cache.sgActive = []rpc.Task{{GID: "sg_inv-3", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil; Cache.sgStopped = nil }()

	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventComplete, DownloadID: "inv-3", Total: 1000, AvgSpeed: 500})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if !cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = %v, want zero (cache should be invalidated on complete)", cacheAtAfter)
	}
}

func TestHandleSurgeEvent_InvalidatesListCacheOnError(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:      hub,
		pusher:   pusher,
		engine:   hybrid,
		surgeEng: surgeEng,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	Cache.sgActive = []rpc.Task{{GID: "sg_inv-4", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil; Cache.sgStopped = nil }()

	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventError, DownloadID: "inv-4"})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if !cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = %v, want zero (cache should be invalidated on error)", cacheAtAfter)
	}
}

// TestHandleSurgeEvent_NoInvalidationOnProgress verifies that ProgressMsg
// (which returns early before the switch deltaType) does NOT invalidate the cache.

// TestHandleSurgeEvent_NoInvalidationOnProgress verifies that ProgressMsg
// (which returns early before the switch deltaType) does NOT invalidate the cache.
func TestHandleSurgeEvent_NoInvalidationOnProgress(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:    hub,
		pusher: pusher,
		engine: hybrid,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}
	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtBefore := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventProgress,
		DownloadID: "inv-5",
		Downloaded: 100,
		Total:      1000,
		Speed:      50.0,
	})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = zero, want %v (progress should not invalidate cache)", cacheAtBefore)
	}
}

// mockStoppedEngine returns a fixed set of stopped tasks from TellStopped/
// TellStoppedLite, allowing tick-level tests to simulate a Surge engine whose
// DB still contains a completed task (the async DeleteState race window).
