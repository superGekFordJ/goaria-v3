package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

// mockTracker implements TrackerProvider for testing
type mockTracker struct {
	tasks []TrackedTaskInfo
}

func (m *mockTracker) GetActiveTrackedTasks() []TrackedTaskInfo {
	return m.tasks
}

func (m *mockTracker) GetScope(gid string) (scope, domain string, ok bool) {
	for _, t := range m.tasks {
		if t.GID == gid {
			return t.Scope, t.Domain, true
		}
	}
	return "", "", false
}

// mockTelemetry implements TelemetryProvider for testing
type mockTelemetry struct {
	data map[string][]types.WorkerSnapshot
}

func (m *mockTelemetry) Get(gid string) []types.WorkerSnapshot {
	return m.data[gid]
}

func TestConvergenceTicker_ScaleDownOnLowThroughput(t *testing.T) {
	speedstats.ResetRecordsForTest()
	// Seed speedstats so GetRecentPeakByScope returns a valid vThreadAvg
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_test123"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 100 * 1024},
				{WorkerID: 1, EMASpeed: 100 * 1024},
				{WorkerID: 2, EMASpeed: 100 * 1024},
				{WorkerID: 3, EMASpeed: 100 * 1024},
			},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	for i := 0; i < scaleDownStableCycles; i++ {
		ct.tick()
	}

	ct.mu.Lock()
	s, exists := ct.states[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist")
	}
	if s.scaleDownCycles != 0 {
		t.Errorf("expected scaleDownCycles=0 after triggering, got %d", s.scaleDownCycles)
	}
	if s.releaseCycles != 1 {
		t.Errorf("expected releaseCycles=1 after one scale-down, got %d", s.releaseCycles)
	}
}

func TestConvergenceTicker_NoTelemetryNoOp(t *testing.T) {
	gid := "sg_notelemetry"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com"},
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com"},
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
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_remove"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com"},
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
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist after tick")
	}
	ct.mu.Lock()
	_, prevExists := ct.prevActiveGids[gid]
	ct.mu.Unlock()
	if !prevExists {
		t.Fatal("expected prevActiveGids to contain gid after tick")
	}

	ct.RemoveTask(gid)

	ct.mu.Lock()
	_, exists = ct.states[gid]
	_, prevExists = ct.prevActiveGids[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected convergence state to be removed")
	}
	if prevExists {
		t.Error("expected prevActiveGids to be cleaned by RemoveTask")
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
}

func TestConvergenceState_LaggedFiltering(t *testing.T) {
	s := &convergenceState{}

	for i := 0; i < scaleDownStableCycles-1; i++ {
		s.scaleDownCycles++
	}
	if s.scaleDownCycles != scaleDownStableCycles-1 {
		t.Fatalf("expected %d cycles, got %d", scaleDownStableCycles-1, s.scaleDownCycles)
	}

	s.scaleDownCycles++
	if s.scaleDownCycles != scaleDownStableCycles {
		t.Fatalf("expected %d cycles, got %d", scaleDownStableCycles, s.scaleDownCycles)
	}
}

func TestConvergenceTicker_SelfCleanupStaleStates(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_stale_test"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
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

func TestConvergenceTicker_ScaleUpLaggedFiltering(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_scaleup_test"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
		},
	}
	// High EMASpeed → throughputRatio >= throughputStableRatio
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {{WorkerID: 0, EMASpeed: 2 * 1024 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Tick scaleUpStableCycles-1 times → should NOT trigger ScaleUp
	for i := 0; i < scaleUpStableCycles-1; i++ {
		ct.tick()
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected convergence state to exist")
	}
	if s.scaleUpCycles != scaleUpStableCycles-1 {
		t.Errorf("expected scaleUpCycles=%d before trigger, got %d", scaleUpStableCycles-1, s.scaleUpCycles)
	}

	// One more tick → should trigger ScaleUp and reset counter
	ct.tick()
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.scaleUpCycles != 0 {
		t.Errorf("expected scaleUpCycles=0 after trigger, got %d", s.scaleUpCycles)
	}
}

func TestConvergenceTicker_BandwidthBorrowing(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	completedGid := "sg_completed"
	keepAliveGid := "sg_keepalive"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: completedGid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false},
			{GID: keepAliveGid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
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
		{GID: keepAliveGid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
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
	// releaseCycles should be reset to 0 after triggering bandwidth borrow
	if s.releaseCycles != 0 {
		t.Errorf("expected releaseCycles=0 after bandwidth borrow trigger, got %d", s.releaseCycles)
	}
}

func TestConvergenceTicker_NoDoubleScaleUp(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	completedGid := "sg_completed_n1"
	keepAliveGid := "sg_keepalive_n1"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: completedGid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false},
			{GID: keepAliveGid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
		},
	}
	// High EMASpeed for keepAlive → throughputRatio >= throughputStableRatio → ScaleUp after 3 cycles
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			completedGid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
			keepAliveGid: {{WorkerID: 0, EMASpeed: 2 * 1024 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Run scaleUpStableCycles-1 ticks with both tasks active.
	// keepAliveGid's scaleUpCycles reaches scaleUpStableCycles-1.
	for i := 0; i < scaleUpStableCycles-1; i++ {
		ct.tick()
	}

	ct.mu.Lock()
	s := ct.states[keepAliveGid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected convergence state for keep-alive task")
	}
	if s.scaleUpCycles != scaleUpStableCycles-1 {
		t.Fatalf("expected scaleUpCycles=%d before trigger, got %d", scaleUpStableCycles-1, s.scaleUpCycles)
	}

	// Pre-set releaseCycles to 1 so we can detect if bandwidth borrowing touches it.
	ct.mu.Lock()
	s.releaseCycles = 1
	ct.mu.Unlock()

	// Remove completedGid — simulate it finishing.
	tracker.tasks = []TrackedTaskInfo{
		{GID: keepAliveGid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
	}
	telemetry.data = map[string][]types.WorkerSnapshot{
		keepAliveGid: {{WorkerID: 0, EMASpeed: 2 * 1024 * 1024}},
	}

	// Triggering tick: processTask generates ScaleUp for keepAliveGid.
	// Bandwidth borrowing detects completedGid disappeared but must skip keepAliveGid
	// because it's already in pendingGids.
	ct.tick()

	ct.mu.Lock()
	s = ct.states[keepAliveGid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected convergence state to persist")
	}
	// processTask triggered ScaleUp → scaleUpCycles reset to 0
	if s.scaleUpCycles != 0 {
		t.Errorf("expected scaleUpCycles=0 after processTask trigger, got %d", s.scaleUpCycles)
	}
	// Bandwidth borrowing was skipped → releaseCycles stays at 1 (not incremented/reset)
	if s.releaseCycles != 1 {
		t.Errorf("expected releaseCycles=1 (bandwidth borrowing skipped), got %d — double ScaleUp not prevented", s.releaseCycles)
	}
}

func TestConvergenceTicker_ServerLimitFuse(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_fuse_test"
	domain := "example.com"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: domain, IsKeepAlive: true},
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
	ct.limits.Clear(domain)

	ct.tick()

	// N_max should be locked to currentWorkers (3)
	nMax, ok := ct.limits.GetNMax(domain)
	if !ok {
		t.Fatal("expected N_max to be set after conn error threshold exceeded")
	}
	if nMax != 3 {
		t.Errorf("expected N_max=3, got %d", nMax)
	}
}

func TestConvergence_DomainIsolation_NoPollution(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// 3 slow.com records: 1MB/s, 1 thread → V_thread = 1MB/s each
	// 1 fast.com record: 67MB/s, 4 threads → V_thread = 16.75MB/s
	// Scope median (polluted): [1MB, 1MB, 1MB, 16.75MB] → median = 1MB/s
	// Domain median for fast.com: 16.75MB/s (not polluted)
	for i := 0; i < 3; i++ {
		speedstats.AddRecordV2(1*1024*1024, 1, 200*1024*1024, false, 100, "slow.com", "wan")
	}
	speedstats.AddRecordV2(67*1024*1024, 4, 200*1024*1024, false, 100, "fast.com", "wan")

	gid := "sg_domain_iso"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "fast.com", IsKeepAlive: true},
		},
	}
	// 4 workers, each 5MB/s → aggregateSpeed = 20MB/s
	// With domain isolation: V_thread_avg = 16.75MB/s
	// expectedThroughput = 16.75MB/s * 4 = 67MB/s
	// throughputRatio = 20MB / 67MB = 0.298 < 0.5 → ScaleDown path (correct)
	// Without isolation (scope median = 1MB/s): ratio = 20MB / 4MB = 5.0 → ScaleUp path (wrong)
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 5 * 1024 * 1024},
				{WorkerID: 1, EMASpeed: 5 * 1024 * 1024},
				{WorkerID: 2, EMASpeed: 5 * 1024 * 1024},
				{WorkerID: 3, EMASpeed: 5 * 1024 * 1024},
			},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	for i := 0; i < scaleDownStableCycles; i++ {
		ct.tick()
	}

	ct.mu.Lock()
	s, exists := ct.states[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist")
	}
	if s.scaleDownCycles != 0 {
		t.Errorf("expected scaleDownCycles=0 after ScaleDown trigger, got %d (domain-isolated ratio should be <0.5, not inflated by slow.com pollution)", s.scaleDownCycles)
	}
	if s.releaseCycles != 1 {
		t.Errorf("expected releaseCycles=1 after ScaleDown trigger, got %d", s.releaseCycles)
	}
}

func TestConvergence_NewDomain_FallbackPenalty(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// known.com: 2MB/s, 1 thread → V_thread = 2MB/s (scope median)
	speedstats.AddRecordV2(2*1024*1024, 1, 200*1024*1024, false, 100, "known.com", "wan")

	gid := "sg_newdomain"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "unknown.com", IsKeepAlive: true},
		},
	}
	// 4 workers, each 600KB/s → aggregateSpeed = 2.4MB/s
	// With 0.5x penalty: V_thread_avg = 2MB/s / 2 = 1MB/s
	// expectedThroughput = 1MB/s * 4 = 4MB/s
	// throughputRatio = 2.4MB / 4MB = 0.6 → default path (0.5 ≤ 0.6 < 0.8, no action)
	// Without penalty: V_thread_avg = 2MB/s → expectedThroughput = 8MB/s
	// ratio = 2.4MB / 8MB = 0.3 → ScaleDown path (wrong — should observe first)
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 600 * 1024},
				{WorkerID: 1, EMASpeed: 600 * 1024},
				{WorkerID: 2, EMASpeed: 600 * 1024},
				{WorkerID: 3, EMASpeed: 600 * 1024},
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
	s, exists := ct.states[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist")
	}
	if s.scaleDownCycles != 0 {
		t.Errorf("expected scaleDownCycles=0 (0.5x penalty → ratio=0.6 → default path), got %d (would be 1 without penalty → ratio=0.3 → ScaleDown)", s.scaleDownCycles)
	}
	if s.scaleUpCycles != 0 {
		t.Errorf("expected scaleUpCycles=0 (ratio=0.6 < throughputStableRatio=0.8), got %d", s.scaleUpCycles)
	}
}

func TestConvergence_CrossScopeIsolation(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Same domain example.com, two scopes:
	// wan: 2MB/s, 1 thread → V_thread = 2MB/s
	// lan: 20MB/s, 1 thread → V_thread = 20MB/s
	speedstats.AddRecordV2(2*1024*1024, 1, 200*1024*1024, false, 100, "example.com", "wan")
	speedstats.AddRecordV2(20*1024*1024, 1, 200*1024*1024, false, 100, "example.com", "lan")

	gid := "sg_crossscope"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
		},
	}
	// 4 workers, each 300KB/s → aggregateSpeed = 1.2MB/s
	// With cross-scope isolation: V_thread_avg = 2MB/s (wan only)
	// expectedThroughput = 2MB/s * 4 = 8MB/s
	// throughputRatio = 1.2MB / 8MB = 0.15 < 0.5 → ScaleDown path
	// If polluted by lan: V_thread_avg = 20MB/s → expectedThroughput = 80MB/s
	// ratio = 1.2MB / 80MB = 0.015 < 0.5 → still ScaleDown, but ratio is absurdly low
	// The key distinction: with correct wan isolation, ratio=0.15 is realistic;
	// with lan pollution, ratio=0.015 would be nonsensical
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 300 * 1024},
				{WorkerID: 1, EMASpeed: 300 * 1024},
				{WorkerID: 2, EMASpeed: 300 * 1024},
				{WorkerID: 3, EMASpeed: 300 * 1024},
			},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Clear any pre-existing N_max from shared ServerLimitStore (e.g. from ServerLimitFuse test)
	ct.limits.Clear("example.com")

	for i := 0; i < scaleDownStableCycles; i++ {
		ct.tick()
	}

	ct.mu.Lock()
	s, exists := ct.states[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist")
	}
	// Should have triggered ScaleDown (ratio < 0.5 with wan-isolated V_thread_avg=2MB/s)
	if s.scaleDownCycles != 0 {
		t.Errorf("expected scaleDownCycles=0 after ScaleDown trigger, got %d", s.scaleDownCycles)
	}
	if s.releaseCycles != 1 {
		t.Errorf("expected releaseCycles=1 after ScaleDown trigger, got %d", s.releaseCycles)
	}
}