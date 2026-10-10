package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	surgeEvents "goaria-v3/internal/surge/types"
)

// newEventTestMonitor builds a Monitor wired for handleSurgeEvent fact tests.
func newEventTestMonitor(t *testing.T) (*Monitor, *TaskTracker) {
	t.Helper()
	m, _, _, tracker := newReconcileTestMonitor(t)
	return m, tracker
}

func startedEvent(downloaded int64, isResume bool) surgeEvents.DownloadEvent {
	return surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: "x",
		Total:      1000,
		URL:        "https://example.com/f.bin",
		Workers:    4,
		State:      &surgeEvents.DownloadRecord{Downloaded: downloaded, IsResume: isResume},
	}
}

func progressEvent(downloaded int64) surgeEvents.DownloadEvent {
	return surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventProgress,
		DownloadID: "x",
		Total:      1000,
		Downloaded: downloaded,
	}
}

// TestHandleSurgeEvent_StartedEstablishesActivation verifies a real started
// event stamps ActivatedAt and a reliable baseline — the genuine activation
// fact the Target-floor lease depends on.
func TestHandleSurgeEvent_StartedEstablishesActivation(t *testing.T) {
	m, tracker := newEventTestMonitor(t)

	m.handleSurgeEvent(startedEvent(500, true))

	tt := tracker.tasks["sg_x"]
	if tt == nil || tt.ActivatedAt.IsZero() {
		t.Fatal("EventStarted must stamp ActivatedAt")
	}
	if !tt.progressBaselineKnown || tt.progressBaseline != 500 {
		t.Errorf("baseline = known:%v v:%d, want true/500", tt.progressBaselineKnown, tt.progressBaseline)
	}
	if tt.Status != "active" {
		t.Errorf("status = %q, want active", tt.Status)
	}
	// A genuine start is not a compat claim.
	if tt.compatClaim != smartthread.CompatNone {
		t.Errorf("compat = %v, want none", tt.compatClaim)
	}
}

// TestHandleSurgeEvent_DuplicateStartedDoesNotReset verifies repeated started
// observations keep the original activation instant/baseline.
func TestHandleSurgeEvent_DuplicateStartedDoesNotReset(t *testing.T) {
	m, tracker := newEventTestMonitor(t)

	m.handleSurgeEvent(startedEvent(500, false))
	first := tracker.tasks["sg_x"].ActivatedAt

	m.handleSurgeEvent(startedEvent(900, false))
	tt := tracker.tasks["sg_x"]
	if !tt.ActivatedAt.Equal(first) {
		t.Error("duplicate started reset ActivatedAt")
	}
	if tt.progressBaseline != 500 {
		t.Errorf("duplicate started reset baseline to %d", tt.progressBaseline)
	}
}

// TestHandleSurgeEvent_ProgressStampsFirstGrowthOnce verifies progress beyond
// the activation baseline stamps FirstProgressAt once; regressions and equal
// values do not.
func TestHandleSurgeEvent_ProgressStampsFirstGrowthOnce(t *testing.T) {
	m, tracker := newEventTestMonitor(t)

	m.handleSurgeEvent(startedEvent(500, false))

	// Equal to baseline → no growth.
	m.handleSurgeEvent(progressEvent(500))
	if !tracker.tasks["sg_x"].FirstProgressAt.IsZero() {
		t.Fatal("progress == baseline must not stamp FirstProgressAt")
	}

	// Beyond baseline → stamps once.
	m.handleSurgeEvent(progressEvent(800))
	stamp := tracker.tasks["sg_x"].FirstProgressAt
	if stamp.IsZero() {
		t.Fatal("progress > baseline must stamp FirstProgressAt")
	}

	// Later progress does not move the stamp.
	m.handleSurgeEvent(progressEvent(950))
	if !tracker.tasks["sg_x"].FirstProgressAt.Equal(stamp) {
		t.Error("subsequent progress moved FirstProgressAt")
	}

	// A regression never rewinds the stamp or baseline.
	m.handleSurgeEvent(progressEvent(100))
	if !tracker.tasks["sg_x"].FirstProgressAt.Equal(stamp) {
		t.Error("regressed progress moved FirstProgressAt")
	}
}

// TestHandleSurgeEvent_PausedClearsActivation verifies a pause event ends the
// activation lease. An ordinary paused task holds no occupancy; the claim
// only returns when Resume rewrites Target on the paused row (the retained
// paused-Resume hold).
func TestHandleSurgeEvent_PausedClearsActivation(t *testing.T) {
	m, tracker := newEventTestMonitor(t)

	m.handleSurgeEvent(startedEvent(0, false))
	if tracker.tasks["sg_x"].ActivatedAt.IsZero() {
		t.Fatal("precondition: must be activated")
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventPaused,
		DownloadID: "x",
	})

	tt := tracker.tasks["sg_x"]
	if !tt.ActivatedAt.IsZero() {
		t.Error("pause must clear ActivatedAt")
	}
	if tt.Status != "paused" {
		t.Errorf("status = %q, want paused", tt.Status)
	}
	// Ordinary paused: not occupancy-visible.
	for _, got := range tracker.GetOccupancyTrackedTasks() {
		if got.GID == "sg_x" {
			t.Fatal("ordinary paused task must not occupy")
		}
	}

	// Resume hook writes Target while paused → hold becomes the claim.
	tracker.SetTargetBandwidth("sg_x", 4_000_000)
	if !tracker.tasks["sg_x"].resumeOccupancyHold {
		t.Fatal("paused Target write must set resumeOccupancyHold")
	}
	found := false
	for _, got := range tracker.GetOccupancyTrackedTasks() {
		if got.GID == "sg_x" {
			found = true
		}
	}
	if !found {
		t.Error("paused+resume-hold must remain occupancy-visible")
	}
}

// TestHandleSurgeEvent_StartedOnTerminalWithResumeIntention verifies the
// remove-then-re-add cycle: a started event on a terminal-marked task reopens
// it (resume-intended) and establishes a fresh activation.
func TestHandleSurgeEvent_StartedOnTerminalWithResumeIntention(t *testing.T) {
	m, tracker := newEventTestMonitor(t)
	resetHistoryForTest(t)

	m.handleSurgeEvent(startedEvent(0, false))
	// Give the task resolvable metadata so handleTaskComplete can proceed.
	Cache.metadata["sg_x"] = &TaskMetadata{GID: "sg_x", Files: []string{"/d/f.bin"}, Dir: "/d"}
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "x",
		Total:      1000,
		Elapsed:    time.Second,
		AvgSpeed:   1000,
	})
	if tt := tracker.tasks["sg_x"]; tt.Status == "active" {
		t.Fatal("precondition: task should be terminal")
	}
	if !Cache.IsInStopped("sg_x") {
		t.Fatal("precondition: task should be in cache stopped")
	}

	m.BumpPauseResumeIntention("sg_x", PauseResumeIntentionResume)
	m.handleSurgeEvent(startedEvent(0, true))

	tt := tracker.tasks["sg_x"]
	if tt == nil || tt.Status != "active" {
		t.Fatalf("reopen must restore live active tracking, got %#v", tt)
	}
	if tt.ActivatedAt.IsZero() {
		t.Error("reopened started must stamp fresh ActivatedAt")
	}
	// The legitimate resume-intended path moved the stopped row to live.
	if Cache.IsInStopped("sg_x") {
		t.Error("resume-intended started must move the row out of stopped")
	}
}

// TestHandleSurgeEvent_StartedOnTerminalWithoutIntention verifies a spurious
// started event on a stopped task does NOT reopen the tracker row — no
// intention, no terminal-clear, no activation.
func TestHandleSurgeEvent_StartedOnTerminalWithoutIntention(t *testing.T) {
	m, tracker := newEventTestMonitor(t)
	resetHistoryForTest(t)

	m.handleSurgeEvent(startedEvent(0, false))
	Cache.metadata["sg_x"] = &TaskMetadata{GID: "sg_x", Files: []string{"/d/f.bin"}, Dir: "/d"}
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "x",
		Total:      1000,
		Elapsed:    time.Second,
		AvgSpeed:   1000,
	})

	m.handleSurgeEvent(startedEvent(0, false))

	tt := tracker.tasks["sg_x"]
	if tt != nil && tt.Status == "active" {
		t.Error("started on terminal task without resume intention must not reopen tracker status")
	}
	if tt != nil && !tt.ActivatedAt.IsZero() {
		t.Error("no activation may be stamped on a still-terminal row")
	}
	// The stopped cache membership and its full metadata must survive — the
	// sparse add must not run.
	if !Cache.IsInStopped("sg_x") {
		t.Error("non-intended started must not evict the stopped row")
	}
	if task := findTaskInCache("sg_x"); task == nil || task.Status != "complete" {
		t.Errorf("stopped row clobbered: %#v", task)
	}
}

// TestReconcileSurgeCache_SameListActiveRepairsMissedStarted verifies the
// poll path repairs facts for a task already active in the same cache list —
// the missed-EventStarted recovery that previously only fixed status, not
// activation.
func TestReconcileSurgeCache_SameListActiveRepairsMissedStarted(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{GID: "sg_rep", Status: "active", CompletedLength: "300", TotalLength: "1000"}, "active")
	tracker.EnsureTrackedFromEvent("sg_rep", 1000, "https://e/f", 4, "active")
	// Simulate a missed started event: status is active but no activation fact.
	if !tracker.tasks["sg_rep"].ActivatedAt.IsZero() {
		t.Fatal("precondition: no activation yet")
	}

	reader.setLists([]rpc.Task{{GID: "rep", Status: "active", CompletedLength: "300", TotalLength: "1000"}}, nil, nil)
	m.reconcileSurgeCache()

	tt := tracker.tasks["sg_rep"]
	if tt.ActivatedAt.IsZero() {
		t.Error("same-list active reconcile must repair missed activation")
	}
	if tt.progressBaseline != 300 {
		t.Errorf("baseline = %d, want 300 from polled completedLength", tt.progressBaseline)
	}
}

// TestReconcileSurgeCache_MismatchActiveRepairsFacts verifies the cross-list
// mismatch path: engine reports active while the cache row still sits in
// waiting — the move repairs status AND stamps activation with the polled
// completed length as baseline.
func TestReconcileSurgeCache_MismatchActiveRepairsFacts(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{GID: "sg_mm", Status: "paused", CompletedLength: "700", TotalLength: "1000"}, "waiting")
	tracker.EnsureTrackedFromEvent("sg_mm", 1000, "https://e/f", 4, "paused")

	reader.setLists([]rpc.Task{{GID: "mm", Status: "active", CompletedLength: "700", TotalLength: "1000"}}, nil, nil)
	m.reconcileSurgeCache()

	tt := tracker.tasks["sg_mm"]
	if tt == nil || tt.ActivatedAt.IsZero() {
		t.Fatal("mismatch active reconcile must stamp activation")
	}
	if tt.progressBaseline != 700 {
		t.Errorf("baseline = %d, want 700 from polled completedLength", tt.progressBaseline)
	}
	if tt.Status != "active" {
		t.Errorf("status = %q, want active", tt.Status)
	}
}

// TestReconcileSurgeCache_WaitingClearsActivation verifies a task that moved
// to the waiting list loses its activation facts while the retained Surge
// waiting claim still holds the Target floor.
func TestReconcileSurgeCache_WaitingClearsActivation(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{GID: "sg_w", Status: "active", TotalLength: "1000"}, "active")
	tracker.EnsureTrackedFromEvent("sg_w", 1000, "https://e/f", 4, "active")
	tracker.SetTargetBandwidth("sg_w", 4_000_000)
	tracker.ObserveActivation("sg_w", 0, true)

	reader.setLists(nil, []rpc.Task{{GID: "w", Status: "waiting", CompletedLength: "400", TotalLength: "1000"}}, nil)
	m.reconcileSurgeCache()

	tt := tracker.tasks["sg_w"]
	if !tt.ActivatedAt.IsZero() {
		t.Error("waiting reconcile must clear activation")
	}
	if tt.Status != "waiting" {
		t.Errorf("status = %q, want waiting", tt.Status)
	}
	// The polled completed value seeds the next run's baseline source —
	// checkpoint only, never growth.
	if tt.CompletedLength != 400 {
		t.Errorf("CompletedLength = %d, want 400 checkpointed from poll", tt.CompletedLength)
	}
	if !tt.FirstProgressAt.IsZero() {
		t.Error("waiting checkpoint must not stamp FirstProgressAt")
	}
	// The retained waiting claim keeps the task occupancy-visible.
	found := false
	for _, got := range tracker.GetOccupancyTrackedTasks() {
		if got.GID == "sg_w" {
			found = true
		}
	}
	if !found {
		t.Error("surge waiting claim must remain occupancy-visible")
	}
}
