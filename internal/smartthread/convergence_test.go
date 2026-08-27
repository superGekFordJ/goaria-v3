package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// mockTracker implements TrackerProvider for testing
type mockTracker struct {
	tasks []TrackedTaskInfo
}

func (m *mockTracker) GetActiveTrackedTasks() []TrackedTaskInfo {
	return m.tasks
}

func (m *mockTracker) GetScopeAndEnv(gid string) (scope, domain, envKey string, ok bool) {
	for _, t := range m.tasks {
		if t.GID == gid {
			return t.Scope, t.Domain, t.EnvKey, true
		}
	}
	return "", "", "", false
}

// mockTelemetry implements TelemetryProvider for testing
type mockTelemetry struct {
	data map[string][]types.WorkerSnapshot
}

func (m *mockTelemetry) Get(gid string) []types.WorkerSnapshot {
	return m.data[gid]
}

func TestNewConvergenceTicker_DefaultMaxConnections(t *testing.T) {
	ct := NewConvergenceTicker(nil, &mockTracker{}, &mockTelemetry{}, nil, nil, 0, 0)
	if ct.maxConnections != 16 {
		t.Fatalf("default maxConnections=%d, want 16", ct.maxConnections)
	}
	capped := NewConvergenceTicker(nil, &mockTracker{}, &mockTelemetry{}, nil, nil, 0, 999)
	if capped.maxConnections != 256 {
		t.Fatalf("capped maxConnections=%d, want 256", capped.maxConnections)
	}
}

func TestConvergenceTicker_NoTelemetryNoOp(t *testing.T) {
	gid := "sg_notelemetry"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com"},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	_, exists := ct.states[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected no convergence state when telemetry is nil")
	}
}

func TestConvergenceTicker_NonSurgeGidSkipped(t *testing.T) {
	gid := "ar_aria2task"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com"},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	_, exists := ct.states[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected no convergence state for non-sg GID")
	}
}

func TestConvergenceTicker_RemoveTask(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	gid := "sg_remove"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com"},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 100 * 1024},
				{WorkerID: 1, EMASpeed: 100 * 1024},
			},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	_, exists := ct.states[gid]
	_, prevExists := ct.prevActiveGids[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist after tick")
	}
	if !prevExists {
		t.Fatal("expected prevActiveGids to contain gid after tick")
	}

	// Seed speeds so we can assert prune-by-activeGids after RemoveTask.
	ct.mu.Lock()
	ct.prevActiveSpeeds[gid] = 2 * 1024 * 1024
	ct.mu.Unlock()

	ct.RemoveTask(gid)

	ct.mu.Lock()
	_, exists = ct.states[gid]
	_, prevExists = ct.prevActiveGids[gid]
	_, speedExists := ct.prevActiveSpeeds[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected convergence state to be removed")
	}
	// SPEC-243: RemoveTask preserves prevActiveGids / prevActiveSpeeds so the
	// next tick can observe disappearance + windowInvalidated on complete/delete.
	if !prevExists {
		t.Error("expected prevActiveGids retained after RemoveTask until tick replace")
	}
	if !speedExists {
		t.Error("expected prevActiveSpeeds retained after RemoveTask until tick prune")
	}
	if bps, ready := ct.LastRawBps(gid); bps != 0 || ready {
		t.Errorf("LastRawBps after RemoveTask: got (%d,%v), want (0,false)", bps, ready)
	}

	// Empty active set → tick replaces prevActiveGids and prunes speeds by activeGids.
	tracker.tasks = nil
	delete(telemetry.data, gid)
	ct.tick()

	ct.mu.Lock()
	_, prevExists = ct.prevActiveGids[gid]
	_, speedExists = ct.prevActiveSpeeds[gid]
	ct.mu.Unlock()
	if prevExists {
		t.Error("expected prevActiveGids cleared after tick replace")
	}
	if speedExists {
		t.Error("expected prevActiveSpeeds pruned by activeGids after tick")
	}
}

func TestConvergenceTicker_StartStop(t *testing.T) {
	tracker := &mockTracker{}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{}}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)

	ct.Start()
	time.Sleep(10 * time.Millisecond)
	ct.Stop()

	select {
	case <-ct.stopChan:
	default:
		t.Error("stopChan not closed after Stop()")
	}
}

func TestConvergenceTicker_SelfCleanupStaleStates(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	gid := "sg_stale_test"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// First tick: creates a state entry for gid
	ct.tick()
	ct.mu.Lock()
	_, exists := ct.states[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist after first tick")
	}

	// Remove task from active list — simulate task disappearing from engine
	tracker.tasks = nil
	telemetry.data = nil

	// Second tick: self-cleanup should remove the stale state
	ct.tick()
	ct.mu.Lock()
	_, exists = ct.states[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected stale convergence state to be cleaned up by self-cleanup")
	}
}

func TestConvergenceTicker_BandwidthBorrowing(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	completedGid := "sg_completed"
	keepAliveGid := "sg_keepalive"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: completedGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false},
			{GID: keepAliveGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			completedGid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
			keepAliveGid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// First tick: both tasks active, prevActiveGids populated
	ct.tick()

	// Remove completed task — simulate it finishing
	tracker.tasks = []TrackedTaskInfo{
		{GID: keepAliveGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true},
	}
	telemetry.data = map[string][]types.WorkerSnapshot{
		keepAliveGid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
	}

	// Second tick: completedGid disappeared → bandwidth borrowing should trigger
	ct.tick()

	ct.mu.Lock()
	s := ct.states[keepAliveGid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected convergence state for keep-alive task")
	}
}

func TestConvergenceTicker_ServerLimitFuse(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	gid := "sg_fuse_test"
	domain := "example.com"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true},
		},
	}
	// RetryCount sum = 3 >= connErrorThreshold → should trigger SetNMax
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 1},
				{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 1},
				{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 1},
			},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Clear any pre-existing limit for this domain
	key := limitKey("wan", domain)
	ct.limits.Clear(key)

	ct.tick()

	// N_max should be locked to currentWorkers (3)
	nMax, ok := ct.limits.GetNMax(key)
	if !ok {
		t.Fatal("expected N_max to be set after conn error threshold exceeded")
	}
	if nMax != 3 {
		t.Errorf("expected N_max=3, got %d", nMax)
	}
}

func TestConvergenceTicker_PrevActiveGidsCarriesDomain(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "a.com", "wan", "testenv")
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "b.com", "wan", "testenv")

	gid1 := "sg_domain_a"
	gid2 := "sg_domain_b"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "a.com", IsKeepAlive: true},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "b.com", IsKeepAlive: true},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: {{WorkerID: 0, EMASpeed: 100 * 1024}},
			gid2: {{WorkerID: 0, EMASpeed: 100 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	info1 := ct.prevActiveGids[gid1]
	info2 := ct.prevActiveGids[gid2]
	ct.mu.Unlock()

	if info1.Domain != "a.com" || info1.Scope != "wan" {
		t.Errorf("prevActiveGids[%s] = %+v, want {Domain:a.com Scope:wan}", gid1, info1)
	}
	if info2.Domain != "b.com" || info2.Scope != "wan" {
		t.Errorf("prevActiveGids[%s] = %+v, want {Domain:b.com Scope:wan}", gid2, info2)
	}

	// sameActiveSet compares by key set only — same keys with different values must return true.
	a := map[string]gidInfo{"g1": {Domain: "x.com", Scope: "wan"}, "g2": {Domain: "y.com", Scope: "lan"}}
	b := map[string]gidInfo{"g1": {Domain: "z.com", Scope: "lan"}, "g2": {Domain: "w.com", Scope: "wan"}}
	if !sameActiveSet(a, b) {
		t.Error("sameActiveSet should return true for identical key sets regardless of values")
	}
	// Different key sets must return false.
	c := map[string]gidInfo{"g1": {Domain: "x.com", Scope: "wan"}, "g3": {Domain: "y.com", Scope: "lan"}}
	if sameActiveSet(a, c) {
		t.Error("sameActiveSet should return false for different key sets")
	}
}

// TestConvergenceTicker_PendingGidsPreventsBandwidthReleaseScaleUp verifies that the
// pendingGids mechanism prevents bandwidthRelease from issuing a ScaleUp on a task
// that processTask already scaled this tick.
//
// In the current architecture, when a task disappears (triggering bandwidthRelease),
// window-invalidation suppresses all processTask scale decisions, so pendingGids is
// naturally empty during bandwidthRelease. This test exercises the pendingGids skip
// check directly via the extracted bandwidthRelease method to cover the defensive guard.
func TestConvergenceTicker_PendingGidsPreventsBandwidthReleaseScaleUp(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	completedGid := "sg_completed_pending"
	keepAliveGid := "sg_keepalive_pending"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: keepAliveGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			keepAliveGid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Simulate that the previous tick had both tasks active.
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		keepAliveGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	// Pre-create state so we can inspect after the skip.
	ct.getOrCreateState(keepAliveGid)
	ct.mu.Unlock()

	activeGids := map[string]gidInfo{
		keepAliveGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}

	// Scenario 1: pendingGids contains keepAliveGid (processTask already scaled it).
	// bandwidthRelease must skip it — no ScaleUp issued.
	pendingGids := map[string]bool{keepAliveGid: true}
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, pendingGids, nil, nil, nil)
	if len(releases) != 0 {
		t.Errorf("expected 0 releases when pendingGids contains keepAliveGid, got %d: %+v", len(releases), releases)
	}
	ct.mu.Lock()
	s := ct.states[keepAliveGid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state for keepAliveGid")
	}

	// Scenario 2 (control): pendingGids is empty — bandwidthRelease should issue ScaleUp.
	releases = ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil)
	if len(releases) != 1 {
		t.Fatalf("expected 1 release without pendingGids, got %d: %+v", len(releases), releases)
	}
	if releases[0].gid != keepAliveGid || releases[0].delta != 1 {
		t.Errorf("expected release{gid=%s delta=1}, got %+v", keepAliveGid, releases[0])
	}
}
