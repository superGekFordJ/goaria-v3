package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

// ---------------------------------------------------------------------------
// N_max clamp + conservative unlock tests
// ---------------------------------------------------------------------------

// TestConvergenceNMaxClamp_KneeCrossedRebound verifies that when N_max is set
// and currentWorkers >= nMax, the knee-crossed rebound is clamped to 0 and the
// system enters FloorHit without scaling up.
func TestConvergenceNMaxClamp_KneeCrossedRebound(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_rebound"
	domain := "example.com"
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

	ct.limits.Clear(domain)
	ct.limits.SetNMax(domain, 7)

	// Set up knee-crossed conditions: lastStep > 0, linear zone drop.
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

	// 7 workers (== nMax), rawBps drops significantly → knee crossed.
	// dropRatio > 0.5 → rebound would be ceil(2/2)=1, but clamped to 0 (headroom=0).
	telemetry.data[gid] = makeWorkers(7, 2*1024*1024)
	tracker.tasks[0].CompletedLength = 110 * 1024 * 1024 // +10MB/5s = 10MB/s (big drop from 32MB/s)
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
		tracker2 := &mockTracker{
			tasks: []TrackedTaskInfo{
				{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
			},
		}
		telemetry2 := &mockTelemetry{
			data: map[string][]types.WorkerSnapshot{gid2: makeWorkers(7, 2*1024*1024)},
		}
		ct2 := NewConvergenceTicker(he, tracker2, telemetry2, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)
		ct2.limits.Clear(domain)
		ct2.limits.SetNMax(domain, 8) // headroom = 8 - 7 = 1

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
// candidates already at or above N_max.
func TestConvergenceNMaxClamp_BandwidthRelease(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	domain := "example.com"
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

	ct.limits.Clear(domain)
	ct.limits.SetNMax(domain, 6) // candidate has 6 workers == nMax

	// Simulate a completed task to trigger bandwidthRelease.
	completedGid := "sg_bw_completed"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: domain, Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{candidateGid: {Domain: domain, Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
	)
	for _, r := range releases {
		if r.gid == candidateGid {
			t.Fatal("expected bandwidthRelease to skip candidate at N_max")
		}
	}

	t.Run("candidate_below_nmax", func(t *testing.T) {
		ct.limits.Clear(domain)
		ct.limits.SetNMax(domain, 7) // candidate has 6 < 7 → should be elected

		releases2 := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{candidateGid: {Domain: domain, Scope: "wan", EnvKey: "testenv"}},
			map[string]bool{},
			nil,
		)
		found := false
		for _, r := range releases2 {
			if r.gid == candidateGid {
				found = true
			}
		}
		if !found {
			t.Fatal("expected bandwidthRelease to elect candidate below N_max")
		}
	})
}

// TestConvergenceNMaxUnlock_RetryCountSumZero verifies that N_max is cleared
// after lockUnlockConfirmTicks consecutive ticks with retryCountSum == 0 and
// currentWorkers >= nMax.
func TestConvergenceNMaxUnlock_RetryCountSumZero(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_unlock"
	domain := "example.com"
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

	ct.limits.Clear(domain)
	ct.limits.SetNMax(domain, 3)

	// Tick 1: retryCountSum == 0, currentWorkers == 3 >= nMax == 3 → zeroRetryCount=1
	ct.tick()
	if _, hasLimit := ct.limits.GetNMax(domain); !hasLimit {
		t.Fatal("N_max should not be cleared after only 1 tick")
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.zeroRetryCount != 1 {
		t.Fatalf("expected zeroRetryCount=1 after tick 1, got %d", s.zeroRetryCount)
	}

	// Tick 2: retryCountSum == 0 again → zeroRetryCount=2 >= lockUnlockConfirmTicks → unlock
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	if _, hasLimit := ct.limits.GetNMax(domain); hasLimit {
		t.Fatal("expected N_max to be cleared after 2 consecutive zero-retry ticks")
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.zeroRetryCount != 0 {
		t.Fatalf("expected zeroRetryCount=0 after unlock, got %d", s.zeroRetryCount)
	}
}

// TestConvergenceNMaxUnlock_PartialRetryResetsCounter verifies that a partial
// recovery (0 < retryCountSum < threshold) resets the unlock counter.
func TestConvergenceNMaxUnlock_PartialRetryResetsCounter(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_partial"
	domain := "example.com"
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

	ct.limits.Clear(domain)
	ct.limits.SetNMax(domain, 3)

	// Tick 1: retryCountSum=0 → zeroRetryCount=1
	ct.tick()
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.zeroRetryCount != 1 {
		t.Fatalf("expected zeroRetryCount=1 after tick 1, got %d", s.zeroRetryCount)
	}

	// Tick 2: retryCountSum=1 (partial) → reset
	telemetry.data[gid] = []types.WorkerSnapshot{
		{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 1},
		{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
		{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
	}
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.zeroRetryCount != 0 {
		t.Fatalf("expected zeroRetryCount=0 after partial retry, got %d", s.zeroRetryCount)
	}
	if _, hasLimit := ct.limits.GetNMax(domain); !hasLimit {
		t.Fatal("N_max should still be locked after partial retry")
	}

	// Tick 3: retryCountSum=0 → zeroRetryCount=1
	telemetry.data[gid] = []types.WorkerSnapshot{
		{WorkerID: 0, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
		{WorkerID: 1, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
		{WorkerID: 2, EMASpeed: 2 * 1024 * 1024, RetryCount: 0},
	}
	tracker.tasks[0].CompletedLength = 70 * 1024 * 1024
	ct.tick()
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.zeroRetryCount != 1 {
		t.Fatalf("expected zeroRetryCount=1 after tick 3, got %d", s.zeroRetryCount)
	}

	// Tick 4: retryCountSum=0 → zeroRetryCount=2 → unlock
	tracker.tasks[0].CompletedLength = 80 * 1024 * 1024
	ct.tick()
	if _, hasLimit := ct.limits.GetNMax(domain); hasLimit {
		t.Fatal("expected N_max to be cleared after tick 4 (2 consecutive zero-retry ticks)")
	}
}

// TestConvergenceNMaxUnlock_WorkersBelowNMaxNoUnlock verifies that N_max is NOT
// cleared when currentWorkers < nMax, even with retryCountSum == 0.
func TestConvergenceNMaxUnlock_WorkersBelowNMaxNoUnlock(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_below"
	domain := "example.com"
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

	ct.limits.Clear(domain)
	ct.limits.SetNMax(domain, 5) // nMax=5, currentWorkers=3 < 5

	for i := 0; i < 3; i++ {
		tracker.tasks[0].CompletedLength = int64(50+i*10) * 1024 * 1024
		ct.tick()
	}

	if _, hasLimit := ct.limits.GetNMax(domain); !hasLimit {
		t.Fatal("N_max should NOT be cleared when currentWorkers < nMax")
	}
}

// TestConvergenceNMaxUnlock_ActiveSetChangeResetsCounter verifies that an
// active-set change resets zeroRetryCount to 0.
func TestConvergenceNMaxUnlock_ActiveSetChangeResetsCounter(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_nmax_activeset"
	domain := "example.com"
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

	ct.limits.Clear(domain)
	ct.limits.SetNMax(domain, 3)

	// Tick 1: zeroRetryCount=1
	ct.tick()
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.zeroRetryCount != 1 {
		t.Fatalf("expected zeroRetryCount=1 after tick 1, got %d", s.zeroRetryCount)
	}

	// Add a second task → active set changes → windowInvalidated → zeroRetryCount reset.
	// The reset happens at the start of the tick, then C1 fuse increments it again
	// (retryCountSum=0, currentWorkers >= nMax). So after this tick, zeroRetryCount=1
	// (not 2, which it would have been without the reset). This means 2 more ticks
	// are needed to unlock instead of 1.
	gid2 := "sg_nmax_activeset_2"
	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, IsKeepAlive: true, CompletedLength: 20 * 1024 * 1024,
	})
	telemetry.data[gid2] = makeWorkers(3, 2*1024*1024)

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	// Without reset, zeroRetryCount would be 2 (carried over from tick 1 + this tick).
	// With reset, it's 1 (reset to 0, then incremented once this tick).
	if s.zeroRetryCount != 1 {
		t.Fatalf("expected zeroRetryCount=1 after active-set change (reset then re-incremented), got %d", s.zeroRetryCount)
	}
	if _, hasLimit := ct.limits.GetNMax(domain); !hasLimit {
		t.Fatal("N_max should still be locked after active-set change (needs 1 more tick)")
	}

	// One more tick → zeroRetryCount=2 → unlock
	tracker.tasks[0].CompletedLength = 70 * 1024 * 1024
	ct.tick()
	if _, hasLimit := ct.limits.GetNMax(domain); hasLimit {
		t.Fatal("expected N_max to be cleared after 2 consecutive zero-retry ticks post-reset")
	}
}
