package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

// ---------------------------------------------------------------------------
// N_max clamp + conservative unlock tests (domain-level aggregation)
// ---------------------------------------------------------------------------

func lk(scope, domain string) string { return scope + "|" + domain }

// TestConvergenceNMaxClamp_KneeCrossedRebound verifies that when N_max is set
// and currentWorkers >= nMax, the knee-crossed rebound is clamped to 0 and the
// system enters FloorHit without scaling up.
func TestConvergenceNMaxClamp_KneeCrossedRebound(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_rebound"
	domain := "example.com"
	key := lk("wan", domain)
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(7, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 7)

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.lastStep = 2
	s.probeBaseline = 32 * 1024 * 1024
	s.probeBaselineWorkers = 9
	s.probeMomentum = true
	s.probeCooldown = 0
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	telemetry.data[gid] = makeWorkers(7, 2*1024*1024)
	tracker.tasks[0].CompletedLength = 110 * 1024 * 1024
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok := ct.processTask(tracker.tasks[0], false, nil)
	if ok && ps.delta > 0 {
		t.Fatalf("expected no scale-up (rebound clamped to 0), got delta=%d", ps.delta)
	}

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.kneeFrozen {
		t.Error("expected kneeFrozen=true after knee-crossed (even with rebound=0)")
	}
	if s.phase != phaseFloorHit {
		t.Errorf("expected phase=phaseFloorHit, got %d", s.phase)
	}

	t.Run("partial_clamp", func(t *testing.T) {
		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)

		gid2 := "sg_nmax_rebound_partial"
		key2 := lk("wan", domain)
		tracker2 := &mockTracker{
			tasks: []TrackedTaskInfo{
				{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
			},
		}
		telemetry2 := &mockTelemetry{
			data: map[string][]types.WorkerSnapshot{gid2: makeWorkers(7, 2*1024*1024)},
		}
		ct2 := NewConvergenceTicker(he, tracker2, telemetry2, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)
		ct2.limits.Clear(key2)
		ct2.limits.SetNMax(key2, 8) // headroom = 8 - 7 = 1

		ct2.mu.Lock()
		s2 := ct2.getOrCreateState(gid2)
		s2.phase = phaseStable
		s2.lastStep = 4                     // rebound = ceil(4/2) = 2
		s2.probeBaseline = 32 * 1024 * 1024 // 32MB/s
		s2.probeBaselineWorkers = 11        // 11 workers before probe-down
		s2.probeMomentum = true
		s2.probeCooldown = 0
		s2.prevCompleted = 100 * 1024 * 1024
		setPrevSampleAgoState(s2, 5*time.Second)
		ct2.mu.Unlock()

		telemetry2.data[gid2] = makeWorkers(7, 2*1024*1024)
		tracker2.tasks[0].CompletedLength = 110 * 1024 * 1024
		ct2.mu.Lock()
		setPrevSampleAgoState(ct2.states[gid2], 5*time.Second)
		ct2.mu.Unlock()

		ps2, ok2 := ct2.processTask(tracker2.tasks[0], false, nil)
		if !ok2 || ps2.delta != 1 {
			t.Fatalf("expected rebound clamped to 1 (headroom=1), got ok=%v delta=%d", ok2, ps2.delta)
		}
	})
}

// TestConvergenceNMaxClamp_BandwidthRelease verifies that bandwidthRelease skips
// candidates when domain total workers + 1 would exceed N_max.
func TestConvergenceNMaxClamp_BandwidthRelease(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	domain := "example.com"
	key := lk("wan", domain)
	candidateGid := "sg_bw_candidate"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: candidateGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{candidateGid: makeWorkers(6, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 6) // domain total = 6, +1 = 7 > 6 → skip

	completedGid := "sg_bw_completed"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: domain, Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	dStats := map[string]*domainStats{
		key: {activeWorkers: 6, tasksInDomain: 1},
	}

	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{candidateGid: {Domain: domain, Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		dStats,
	)
	for _, r := range releases {
		if r.gid == candidateGid {
			t.Fatal("expected bandwidthRelease to skip candidate at domain N_max")
		}
	}

	t.Run("candidate_below_nmax", func(t *testing.T) {
		ct.limits.Clear(key)
		ct.limits.SetNMax(key, 7) // domain total = 6, +1 = 7 <= 7 → elect

		releases2 := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{candidateGid: {Domain: domain, Scope: "wan", EnvKey: "testenv"}},
			map[string]bool{},
			nil,
			dStats,
		)
		found := false
		for _, r := range releases2 {
			if r.gid == candidateGid {
				found = true
			}
		}
		if !found {
			t.Fatal("expected bandwidthRelease to elect candidate below domain N_max")
		}
	})
}

// TestConvergenceNMaxUnlock_RetryCountSumZero verifies that N_max is cleared
// after lockUnlockConfirmTicks consecutive ticks with retryCountSum == 0.
func TestConvergenceNMaxUnlock_RetryCountSumZero(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_unlock"
	domain := "example.com"
	key := lk("wan", domain)
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
			},
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 3)

	// Tick 1: retryCountSum == 0 → domainUnlockTicks=1
	ct.tick()
	if _, hasLimit := ct.limits.GetNMax(key); !hasLimit {
		t.Fatal("N_max should not be cleared after only 1 tick")
	}
	if ct.domainUnlockTicks[key] != 1 {
		t.Fatalf("expected domainUnlockTicks=1 after tick 1, got %d", ct.domainUnlockTicks[key])
	}

	// Tick 2: retryCountSum == 0 again → domainUnlockTicks=2 >= lockUnlockConfirmTicks → unlock
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	if _, hasLimit := ct.limits.GetNMax(key); hasLimit {
		t.Fatal("expected N_max to be cleared after 2 consecutive zero-retry ticks")
	}
	if ct.domainUnlockTicks[key] != 0 {
		t.Fatalf("expected domainUnlockTicks=0 after unlock, got %d", ct.domainUnlockTicks[key])
	}
}

// TestConvergenceNMaxUnlock_PartialRetryResetsCounter verifies that a partial
// recovery (0 < retryCountSum < threshold) resets the unlock counter.
func TestConvergenceNMaxUnlock_PartialRetryResetsCounter(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_partial"
	domain := "example.com"
	key := lk("wan", domain)
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
			},
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 3)

	// Tick 1: retryCountSum=0 → domainUnlockTicks=1
	ct.tick()
	if ct.domainUnlockTicks[key] != 1 {
		t.Fatalf("expected domainUnlockTicks=1 after tick 1, got %d", ct.domainUnlockTicks[key])
	}

	// Tick 2: retryCountSum=1 (partial) → reset
	telemetry.data[gid] = []types.WorkerSnapshot{
		{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 1},
		{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
		{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
	}
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()
	if ct.domainUnlockTicks[key] != 0 {
		t.Fatalf("expected domainUnlockTicks=0 after partial retry, got %d", ct.domainUnlockTicks[key])
	}
	if _, hasLimit := ct.limits.GetNMax(key); !hasLimit {
		t.Fatal("N_max should still be locked after partial retry")
	}

	// Tick 3: retryCountSum=0 → domainUnlockTicks=1
	telemetry.data[gid] = []types.WorkerSnapshot{
		{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
		{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
		{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
	}
	tracker.tasks[0].CompletedLength = 70 * 1024 * 1024
	ct.tick()
	if ct.domainUnlockTicks[key] != 1 {
		t.Fatalf("expected domainUnlockTicks=1 after tick 3, got %d", ct.domainUnlockTicks[key])
	}

	// Tick 4: retryCountSum=0 → domainUnlockTicks=2 → unlock
	tracker.tasks[0].CompletedLength = 80 * 1024 * 1024
	ct.tick()
	if _, hasLimit := ct.limits.GetNMax(key); hasLimit {
		t.Fatal("expected N_max to be cleared after tick 4 (2 consecutive zero-retry ticks)")
	}
}

// TestConvergenceNMaxUnlock_WorkersBelowNMaxStillUnlocks verifies that N_max IS
// cleared when currentWorkers < nMax, as long as retryCountSum == 0 for
// lockUnlockConfirmTicks consecutive ticks. The domain-level unlock no longer
// requires currentWorkers >= nMax.
func TestConvergenceNMaxUnlock_WorkersBelowNMaxStillUnlocks(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_below"
	domain := "example.com"
	key := lk("wan", domain)
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
			},
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 5) // nMax=5, currentWorkers=3 < 5

	for i := 0; i < 2; i++ {
		tracker.tasks[0].CompletedLength = int64(50+i*10) * 1024 * 1024
		ct.tick()
	}

	if _, hasLimit := ct.limits.GetNMax(key); hasLimit {
		t.Fatal("N_max should be cleared after 2 consecutive zero-retry ticks even when currentWorkers < nMax")
	}
}

// TestConvergenceNMaxUnlock_ActiveSetChangeResetsCounter verifies that an
// active-set change resets domainUnlockTicks to 0.
func TestConvergenceNMaxUnlock_ActiveSetChangeResetsCounter(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_activeset"
	domain := "example.com"
	key := lk("wan", domain)
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
			},
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 3)

	// Tick 1: domainUnlockTicks=1
	ct.tick()
	if ct.domainUnlockTicks[key] != 1 {
		t.Fatalf("expected domainUnlockTicks=1 after tick 1, got %d", ct.domainUnlockTicks[key])
	}

	// Add a second task → active set changes → windowInvalidated → domainUnlockTicks reset.
	gid2 := "sg_nmax_activeset_2"
	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 20 * 1024 * 1024,
	})
	telemetry.data[gid2] = makeWorkers(3, 2*1024*1024)

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	// After reset + re-increment, domainUnlockTicks should be 1 (not 2).
	if ct.domainUnlockTicks[key] != 1 {
		t.Fatalf("expected domainUnlockTicks=1 after active-set change (reset then re-incremented), got %d", ct.domainUnlockTicks[key])
	}
	if _, hasLimit := ct.limits.GetNMax(key); !hasLimit {
		t.Fatal("N_max should still be locked after active-set change (needs 1 more tick)")
	}

	// One more tick → domainUnlockTicks=2 → unlock
	tracker.tasks[0].CompletedLength = 70 * 1024 * 1024
	ct.tick()
	if _, hasLimit := ct.limits.GetNMax(key); hasLimit {
		t.Fatal("expected N_max to be cleared after 2 consecutive zero-retry ticks post-reset")
	}
}

// --- New domain-level aggregation tests ---

// TestConvergenceNMaxFuse_MultiTaskDomainAggregation verifies that 2 same-domain
// tasks with retryCount=2 each (sum=4) trigger fuse at threshold=max(3, 2*2)=4,
// and N_max is locked at the sum of activeWorkers (not single-task count).
func TestConvergenceNMaxFuse_MultiTaskDomainAggregation(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	domain := "example.com"
	key := lk("wan", domain)
	gid1 := "sg_multi_1"
	gid2 := "sg_multi_2"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 30 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: {
				{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 2},
				{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
				{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
			},
			gid2: {
				{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 2},
				{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
			},
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.tick()

	nMax, hasLimit := ct.limits.GetNMax(key)
	if !hasLimit {
		t.Fatal("expected N_max to be locked after multi-task domain aggregation fuse")
	}
	// activeWorkers = 3 + 2 = 5
	if nMax != 5 {
		t.Fatalf("expected N_max=5 (sum of activeWorkers), got %d", nMax)
	}
}

// TestConvergenceNMaxFuse_SingleTaskThresholdSensitivity verifies that a single
// task with retryCount=3 triggers fuse at threshold=max(3, 2*1)=3.
func TestConvergenceNMaxFuse_SingleTaskThresholdSensitivity(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	domain := "example.com"
	key := lk("wan", domain)
	gid := "sg_single_fuse"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
		},
	}
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.tick()

	if _, hasLimit := ct.limits.GetNMax(key); !hasLimit {
		t.Fatal("expected N_max to be locked for single task with retryCountSum=3 >= threshold=3")
	}
}

// TestConvergenceNMaxUnlock_NoNMaxFloorRequirement verifies that unlock happens
// even when activeWorkers < nMax (no >= nMax requirement).
func TestConvergenceNMaxUnlock_NoNMaxFloorRequirement(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	domain := "example.com"
	key := lk("wan", domain)
	gid1 := "sg_floor_1"
	gid2 := "sg_floor_2"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 30 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeWorkers(3, 2*1024*1024), // all RetryCount=0
			gid2: makeWorkers(3, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 10) // nMax=10, activeWorkers=6 < 10

	// 2 ticks with retryCountSum=0 → unlock
	for i := 0; i < 2; i++ {
		tracker.tasks[0].CompletedLength = int64(50+i*10) * 1024 * 1024
		ct.tick()
	}

	if _, hasLimit := ct.limits.GetNMax(key); hasLimit {
		t.Fatal("expected N_max to be cleared even when activeWorkers < nMax (no floor requirement)")
	}
}
