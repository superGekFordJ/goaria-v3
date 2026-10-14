package monitor

import (
	"errors"
	"sync/atomic"
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
// activation lease. An ordinary paused task holds no occupancy — a params
// write on the paused row no longer mints one either; only an explicit
// admission reservation claims while waiting.
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

	// A params write on the paused row is not an occupancy claim either.
	tracker.SetTargetBandwidth("sg_x", 4_000_000)
	for _, got := range tracker.GetOccupancyTrackedTasks() {
		if got.GID == "sg_x" {
			t.Fatal("paused params write must not occupy")
		}
	}
}

// TestHandleSurgeEvent_StartedOnTerminalWithResumeIntention verifies the
// remove-then-re-add cycle: a started event on a terminal-marked task reopens
// it (resume-intended) and establishes a fresh activation.
func TestHandleSurgeEvent_StartedOnTerminalWithResumeIntention(t *testing.T) {
	m, tracker := newEventTestMonitor(t)
	resetHistoryForTest(t)

	m.handleSurgeEvent(startedEvent(0, false))
	// Give the task resolvable metadata so the terminal finish can proceed.
	Cache.SetMetadataForTest("sg_x", &TaskMetadata{GID: "sg_x", Files: []string{"/d/f.bin"}, Dir: "/d"})
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

	m.BeginPauseResumeRequest("sg_x", PauseResumeIntentionResume)
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
	Cache.SetMetadataForTest("sg_x", &TaskMetadata{GID: "sg_x", Files: []string{"/d/f.bin"}, Dir: "/d"})
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

	reader.setLiveStatus("rep", rpc.SurgeLiveStatus{Known: true, Status: "downloading", CompletedKnown: true, CompletedLength: 300})
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

	reader.setLiveStatus("mm", rpc.SurgeLiveStatus{Known: true, Status: "downloading", CompletedKnown: true, CompletedLength: 700})
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

	Cache.PatchTaskProgress("sg_w", "400", "0", "1000")
	tracker.UpdateProgressFromEvent("sg_w", 1000, 400)
	reader.setLiveStatus("w", rpc.SurgeLiveStatus{Known: true, Status: "queued"})
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
	// An ordinary waiting row holds no claim: it must drop out of the
	// occupancy set. Only explicit admission reservations keep a waiting
	// row occupancy-visible.
	for _, got := range tracker.GetOccupancyTrackedTasks() {
		if got.GID == "sg_w" {
			t.Error("ordinary surge waiting row must not occupy")
		}
	}
	// With an explicit pending reservation the same row re-enters the set.
	if _, created := tracker.ReserveAdmission("sg_w", 7,
		smartthread.ThreadParams{Split: 4, MinSize: 1 << 20, TargetBandwidth: 4_000_000},
		time.Now(), tt.CompletedLength, true); !created {
		t.Fatal("reservation on waiting row refused")
	}
	found := false
	for _, got := range tracker.GetOccupancyTrackedTasks() {
		if got.GID == "sg_w" {
			found = true
		}
	}
	if !found {
		t.Error("admitted-not-started reservation must remain occupancy-visible")
	}
}

// ==================== Admission fact kicks ====================
//
// Every authoritative monitor fact that can free or create a dispatch
// opportunity must reach the admission controller through KickAdmission —
// before any hub/window early-return, outside locks, nil-safe, and never
// fired from RPC-failure or progress hot paths.

func TestAdmissionFacts_AcceptedChangeKicksBeforeHubGate(t *testing.T) {
	m, _ := newEventTestMonitor(t)
	var kicks atomic.Int64
	m.SetAdmissionKick(func() { kicks.Add(1) })
	t.Cleanup(func() { m.SetAdmissionKick(nil) })

	m.publishAcceptedTaskChange(acceptedTaskChange{
		task: rpc.Task{GID: "sg_k"}, from: "active", to: "paused", kind: "pause",
	})
	if kicks.Load() != 1 {
		t.Fatalf("accepted change fired %d kicks, want 1", kicks.Load())
	}
	// Headless/no-hub path: the kick precedes the hub early-return.
	m.hub = nil
	m.publishAcceptedTaskChange(acceptedTaskChange{
		task: rpc.Task{GID: "sg_k2"}, from: "waiting", to: "active", kind: "start",
	})
	if kicks.Load() != 2 {
		t.Fatalf("nil-hub change fired %d kicks, want 2", kicks.Load())
	}
	// Empty changes are not facts.
	m.publishAcceptedTaskChange(acceptedTaskChange{})
	if kicks.Load() != 2 {
		t.Fatalf("empty change fired a kick: %d", kicks.Load())
	}
}

func TestAdmissionFacts_InvalidateTaskKicks(t *testing.T) {
	m, tracker := newEventTestMonitor(t)
	m.deletedGids = make(map[string]time.Time)
	tracker.EnsureTrackedFromEvent("sg_iv", 100, "http://e/f", 4, "waiting")

	var kicks atomic.Int64
	m.SetAdmissionKick(func() { kicks.Add(1) })
	t.Cleanup(func() { m.SetAdmissionKick(nil) })

	m.InvalidateTask("sg_iv")
	if kicks.Load() == 0 {
		t.Fatal("InvalidateTask did not notify admission")
	}
}

func TestAdmissionFacts_SurgeTerminalEventKicks(t *testing.T) {
	m, _ := newEventTestMonitor(t)
	resetHistoryForTest(t)

	m.handleSurgeEvent(startedEvent(0, false))
	Cache.SetMetadataForTest("sg_x", &TaskMetadata{GID: "sg_x", Files: []string{"/d/f.bin"}, Dir: "/d"})

	var kicks atomic.Int64
	m.SetAdmissionKick(func() { kicks.Add(1) })
	t.Cleanup(func() { m.SetAdmissionKick(nil) })

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "x",
		Total:      1000,
		Elapsed:    time.Second,
		AvgSpeed:   1000,
	})
	if kicks.Load() == 0 {
		t.Fatal("accepted terminal event did not notify admission")
	}

	// Progress events are the 150ms hot path — they must never kick.
	before := kicks.Load()
	m.handleSurgeEvent(progressEvent(10))
	m.handleSurgeEvent(progressEvent(20))
	if kicks.Load() != before {
		t.Fatalf("progress events fired kicks: %d -> %d", before, kicks.Load())
	}
}

func TestAdmissionFacts_ReconcileRemovalKicks(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{GID: "sg_gone", Status: "waiting", TotalLength: "100"}, "waiting")
	tracker.EnsureTrackedFromEvent("sg_gone", 100, "https://e/g", 4, "waiting")
	// Deletion requires an explicit absent answer from the scheduler, not
	// merely an empty bulk read.
	reader.setLists(nil, nil, nil)
	reader.setLiveStatus("gone", rpc.SurgeLiveStatus{Absent: true})

	var kicks atomic.Int64
	m.SetAdmissionKick(func() { kicks.Add(1) })
	t.Cleanup(func() { m.SetAdmissionKick(nil) })

	m.reconcileSurgeCache()
	if kicks.Load() == 0 {
		t.Fatal("reconcile removal did not notify admission")
	}
}

func TestAdmissionFacts_TickKicksOnceAfterApply(t *testing.T) {
	engine := &mockTickEngine{}
	engine.setLists(nil, nil, nil)
	m := newTickRecoveryMonitor(t, engine)

	var kicks atomic.Int64
	m.SetAdmissionKick(func() { kicks.Add(1) })
	t.Cleanup(func() { m.SetAdmissionKick(nil) })

	m.tick()
	if got := kicks.Load(); got != 1 {
		t.Fatalf("successful tick fired %d kicks, want 1", got)
	}

	// An RPC-failed tick is not a fact and must not notify.
	engine.setActiveErr(errors.New("aria2c unreachable"))
	m.tick()
	if got := kicks.Load(); got != 1 {
		t.Fatalf("failed tick fired kicks: %d", got)
	}
}

func TestAdmissionFacts_KickNilSafe(t *testing.T) {
	m, _ := newEventTestMonitor(t)
	m.SetAdmissionKick(nil)
	m.KickAdmission() // must not panic
	// Clearing while a kick is in flight stays safe: the callback is copied
	// under kickMu and invoked outside it.
	var calls atomic.Int64
	m.SetAdmissionKick(func() { calls.Add(1) })
	m.SetAdmissionKick(nil)
	m.KickAdmission()
	if calls.Load() != 0 {
		t.Fatal("cleared callback still fired")
	}
}
