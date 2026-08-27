package smartthread

import (
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// TestConvergence_BandwidthRelease_SkipsCeilingHit verifies that bandwidthRelease
// suppresses ScaleUp for a task in phaseCeilingHit (with kneeFrozen=false), covering
// the phaseCeilingHit branch of the suppression check.
func TestConvergence_BandwidthRelease_SkipsCeilingHit(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_ceiling_bwrelease"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	// phaseCeilingHit with kneeFrozen=false isolates the ceiling-hit suppression.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseCeilingHit
	s.kneeFrozen = false
	s.ceilingMemory = 10 * 1024 * 1024
	s.frozenCooldown = ceilingHitCooldownCycles
	ct.mu.Unlock()

	// Simulate a completed task to trigger bandwidthRelease.
	completedGid := "sg_completed_ceiling"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{gid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		nil,
		nil,
	)
	for _, r := range releases {
		if r.gid == gid {
			t.Fatal("expected bandwidthRelease to skip phaseCeilingHit task")
		}
	}
}

func TestConvergence_BandwidthRelease_SkipsBlackout(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_bwrelease"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.blackout = true
	s.kneeFrozen = false
	s.phase = phaseStable
	ct.mu.Unlock()

	completedGid := "sg_completed_blackout"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{gid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		nil,
		nil,
	)
	for _, r := range releases {
		if r.gid == gid {
			t.Fatal("expected bandwidthRelease to skip blackout task")
		}
	}
}

// TestConvergence_BandwidthRelease_NonKeepAliveTaskBenefits verifies that
// after removing the IsKeepAlive hard gate, a non-keep-alive task in the same
// scope as a completed task can receive a bandwidth-release ScaleUp.
func TestConvergence_BandwidthRelease_NonKeepAliveTaskBenefits(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	beneficiaryGid := "sg_beneficiary_nonkeepalive"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_completed_nonkeepalive"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	approvedDelta := make(map[string]int)
	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		approvedDelta,
		nil,
		nil,
	)
	found := false
	for _, r := range releases {
		if r.gid == beneficiaryGid {
			found = true
		}
	}
	if !found {
		t.Fatal("expected non-keep-alive task to receive bandwidth-release ScaleUp")
	}
	if approvedDelta[approvedScopeKey("wan", "testenv")] != 1 {
		t.Fatalf("expected approvedDelta[scope]=1, got %d", approvedDelta[approvedScopeKey("wan", "testenv")])
	}
	if approvedDelta[approvedDomainKey("wan", "example.com", "testenv")] != 1 {
		t.Fatalf("expected approvedDelta[domain]=1, got %d", approvedDelta[approvedDomainKey("wan", "example.com", "testenv")])
	}
	if approvedDelta[approvedNMaxKey("wan", "example.com")] != 1 {
		t.Fatalf("expected approvedDelta[nmax]=1, got %d", approvedDelta[approvedNMaxKey("wan", "example.com")])
	}
}

// TestConvergence_ApprovedDelta_PreventsSameTickOversell verifies that the
// tick-local approvedDelta accumulator prevents a second same-scope task from
// passing the V_available check after the first task already consumed the
// headroom in the same tick.
func TestConvergence_ApprovedDelta_PreventsSameTickOversell(t *testing.T) {
	gid1 := "sg_oversell_1"
	gid2 := "sg_oversell_2"

	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// globalPeak = 10 MB/s, vThreadAvg = 10 MB/s (1 thread).
	// activeBw = 0 → first +1: effectiveBw=0, headroom=10MB >= 10MB → pass.
	// After approvedDelta[approvedScopeKey]=1 → second +1: effectiveBw=10MB, headroom=0 < 10MB → blocked.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeWorkers(8, 2*1024*1024),
			gid2: makeWorkers(8, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 256)
	ct.limits.Clear(limitKey("wan", "example.com"))

	// Both tasks in Probe-Up-ready state.
	for _, gid := range []string{gid1, gid2} {
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseStable
		s.bestEff = 1_310_720
		s.peakWorkers = 8
		s.prevCompleted = 10 * 1024 * 1024
		setPrevSampleAgoState(s, 5*time.Second)
		ct.mu.Unlock()
	}

	approvedDelta := make(map[string]int)

	// First task: rawBps = 10 MB/s, 8 workers → newEff = 1.25 MB/s >= bestEff*0.95.
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	telemetry.data[gid1] = makeWorkers(8, 2*1024*1024)
	ct.mu.Lock()
	if s, ok := ct.states[gid1]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	ps1, ok1 := ct.processTask(tracker.tasks[0], false, approvedDelta, nil, nil)
	if !ok1 || ps1.delta != 1 {
		t.Fatalf("expected first task probe-up +1, got ok=%v delta=%d", ok1, ps1.delta)
	}
	if ps1.delta > 0 {
		approvedDelta[approvedScopeKey(ps1.scope, ps1.envKey)] += ps1.delta
		if ps1.domain != "" {
			approvedDelta[approvedDomainKey(ps1.scope, ps1.domain, ps1.envKey)] += ps1.delta
			approvedDelta[approvedNMaxKey(ps1.scope, ps1.domain)] += ps1.delta
		}
	}

	// Second task: same scope, V_available headroom now consumed by first.
	tracker.tasks[1].CompletedLength = 60 * 1024 * 1024
	telemetry.data[gid2] = makeWorkers(8, 2*1024*1024)
	ct.mu.Lock()
	if s, ok := ct.states[gid2]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	ps2, ok2 := ct.processTask(tracker.tasks[1], false, approvedDelta, nil, nil)
	if ok2 && ps2.delta > 0 {
		t.Fatalf("expected second task blocked by approvedDelta, got delta=%d", ps2.delta)
	}
	ct.mu.Lock()
	s2 := ct.states[gid2]
	ct.mu.Unlock()
	if s2.phase == phaseProbingUp {
		t.Fatal("expected second task phase != phaseProbingUp (blocked by accumulator)")
	}
}

// TestConvergence_BandwidthRelease_BlockedByVAvailable verifies that
// bandwidthRelease suppresses ScaleUp when V_available is insufficient.
func TestConvergence_BandwidthRelease_BlockedByVAvailable(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// globalPeak = 10 MB/s, vThreadAvg = 10 MB/s.
	// activeBw = 10 MB/s → headroom = 0 < 10 MB/s → blocked.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 10 * 1024 * 1024 }

	beneficiaryGid := "sg_bwrelease_vavail"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_completed_vavail"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		nil,
		nil,
	)
	for _, r := range releases {
		if r.gid == beneficiaryGid {
			t.Fatal("expected bandwidthRelease to skip task when V_available insufficient")
		}
	}
}

func TestConvergence_BandwidthRelease_DomainScopeMatching(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gidA := "sg_domain_a"
	gidB := "sg_domain_b"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gidA, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "a.com", CompletedLength: 100 * 1024 * 1024},
			{GID: gidB, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "b.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gidA: makeWorkers(4, 2*1024*1024),
			gidB: makeWorkers(4, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	for _, gid := range []string{gidA, gidB} {
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseStable
		s.kneeFrozen = false
		s.blackout = false
		ct.mu.Unlock()
	}

	completedGid := "sg_completed_domain_a"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "a.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{gidA: {Domain: "a.com", Scope: "wan", EnvKey: "testenv"}, gidB: {Domain: "b.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		nil,
		nil,
	)
	if len(releases) != 1 {
		t.Fatalf("expected 1 release (same-domain only), got %d: %+v", len(releases), releases)
	}
	if releases[0].gid != gidA {
		t.Errorf("expected release for gidA (same domain), got %s", releases[0].gid)
	}
}

// TestConvergence_BandwidthRelease_EmptyDomainFallbackToScopeOnly keeps its
// historical name. SPEC-243 reversed empty-Domain semantics: unknown ownership
// skips the disappearance entirely (no cross-domain wildcard release).
func TestConvergence_BandwidthRelease_EmptyDomainFallbackToScopeOnly(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gidA := "sg_empty_domain"
	gidB := "sg_known_domain"

	t.Run("both_present_lowest_workers_elected", func(t *testing.T) {
		tracker := &mockTracker{
			tasks: []TrackedTaskInfo{
				{GID: gidA, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "", CompletedLength: 100 * 1024 * 1024},
				{GID: gidB, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "b.com", CompletedLength: 100 * 1024 * 1024},
			},
		}
		telemetry := &mockTelemetry{
			data: map[string][]types.WorkerSnapshot{
				gidA: makeWorkers(2, 2*1024*1024),
				gidB: makeWorkers(4, 2*1024*1024),
			},
		}
		aria2 := &rpc.Aria2Engine{}
		surge := rpc.NewSurgeEngineForTesting(nil)
		he := rpc.NewHybridEngine(aria2, surge)
		ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

		for _, gid := range []string{gidA, gidB} {
			ct.mu.Lock()
			s := ct.getOrCreateState(gid)
			s.phase = phaseStable
			s.kneeFrozen = false
			s.blackout = false
			ct.mu.Unlock()
		}

		completedGid := "sg_completed_empty"
		ct.mu.Lock()
		ct.prevActiveGids = map[string]gidInfo{
			completedGid: {Domain: "", Scope: "wan", EnvKey: "testenv"},
		}
		ct.mu.Unlock()

		releases := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{gidA: {Domain: "", Scope: "wan", EnvKey: "testenv"}, gidB: {Domain: "b.com", Scope: "wan", EnvKey: "testenv"}},
			map[string]bool{},
			nil,
			nil,
			nil,
		)
		if len(releases) != 0 {
			t.Fatalf("SPEC-243: empty-Domain disappearance must skip release, got %d: %+v", len(releases), releases)
		}
	})

	t.Run("only_nonempty_domain_candidate_elected", func(t *testing.T) {
		tracker := &mockTracker{
			tasks: []TrackedTaskInfo{
				{GID: gidB, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "b.com", CompletedLength: 100 * 1024 * 1024},
			},
		}
		telemetry := &mockTelemetry{
			data: map[string][]types.WorkerSnapshot{
				gidB: makeWorkers(4, 2*1024*1024),
			},
		}
		aria2 := &rpc.Aria2Engine{}
		surge := rpc.NewSurgeEngineForTesting(nil)
		he := rpc.NewHybridEngine(aria2, surge)
		ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

		ct.mu.Lock()
		s := ct.getOrCreateState(gidB)
		s.phase = phaseStable
		s.kneeFrozen = false
		s.blackout = false
		ct.mu.Unlock()

		completedGid := "sg_completed_empty"
		ct.mu.Lock()
		ct.prevActiveGids = map[string]gidInfo{
			completedGid: {Domain: "", Scope: "wan", EnvKey: "testenv"},
		}
		ct.mu.Unlock()

		releases := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{gidB: {Domain: "b.com", Scope: "wan", EnvKey: "testenv"}},
			map[string]bool{},
			nil,
			nil,
			nil,
		)
		if len(releases) != 0 {
			t.Fatalf("SPEC-243: empty-Domain disappearance must skip release, got %d: %+v", len(releases), releases)
		}
	})
}

func TestConvergence_BandwidthRelease_SingleBeneficiaryElection(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_elect_1"
	gid2 := "sg_elect_2"
	gid3 := "sg_elect_3"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
			{GID: gid3, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeWorkers(4, 2*1024*1024),
			gid2: makeWorkers(2, 2*1024*1024),
			gid3: makeWorkers(2, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	for _, gid := range []string{gid1, gid2, gid3} {
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseStable
		s.kneeFrozen = false
		s.blackout = false
		ct.mu.Unlock()
	}

	completedGid := "sg_completed_elect"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{gid1: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}, gid2: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}, gid3: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		nil,
		nil,
	)
	if len(releases) != 1 {
		t.Fatalf("expected exactly 1 release (single beneficiary), got %d: %+v", len(releases), releases)
	}
	elected := releases[0].gid
	if elected != gid2 && elected != gid3 {
		t.Errorf("expected beneficiary to be gid2 or gid3 (lowest workers), got %s", elected)
	}
	ct.mu.Lock()
	rc := ct.rotationCounter
	ct.mu.Unlock()
	if rc != 1 {
		t.Errorf("expected rotationCounter=1 after one election, got %d", rc)
	}
}

func TestConvergence_BandwidthRelease_FairRotation(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_rotate_1"
	gid2 := "sg_rotate_2"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeWorkers(2, 2*1024*1024),
			gid2: makeWorkers(2, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	for _, gid := range []string{gid1, gid2} {
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseStable
		s.kneeFrozen = false
		s.blackout = false
		ct.mu.Unlock()
	}

	activeGids := map[string]gidInfo{
		gid1: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		gid2: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}

	completedGid1 := "sg_completed_rotate_1"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid1: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.rotationCounter = 0
	ct.mu.Unlock()

	releases1 := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil)
	if len(releases1) != 1 {
		t.Fatalf("expected 1 release on first call, got %d", len(releases1))
	}
	firstElected := releases1[0].gid

	completedGid2 := "sg_completed_rotate_2"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid2: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	releases2 := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil)
	if len(releases2) != 1 {
		t.Fatalf("expected 1 release on second call, got %d", len(releases2))
	}
	secondElected := releases2[0].gid

	if firstElected == secondElected {
		t.Errorf("expected fair rotation to elect different beneficiaries, got %s twice", firstElected)
	}
}

func TestConvergence_BandwidthRelease_MacroProviderNoDeadlock(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	speedstats.AddRecordV2(100*1024*1024, 1, 100*1024*1024, false, 50, "example.com", "wan", "testenv")

	beneficiaryGid := "sg_release_macro_ben"
	completedGid := "sg_release_macro_done"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256,
	)
	defer ct.Stop()

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	var providerCalls atomic.Int32
	activeBandwidthProvider = func(scope, envKey string) int64 {
		providerCalls.Add(1)
		// Concurrent RemoveTask while bandwidthRelease is unlocked for the
		// provider — must not race a live range over prevActiveGids.
		ct.RemoveTask(completedGid)
		bps, ready := ct.LastRawBps(beneficiaryGid)
		if ready {
			return bps
		}
		return 0
	}
	ct.InjectMacroOccupancyForTest(beneficiaryGid, 0, true)

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.prevActiveSpeeds = map[string]int64{completedGid: 10 * 1024 * 1024}
	ct.mu.Unlock()

	done := make(chan struct{})
	var releases []pendingScale
	go func() {
		defer close(done)
		releases = ct.bandwidthRelease(
			[]TrackedTaskInfo{tracker.tasks[0]},
			map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
			map[string]bool{},
			nil,
			nil,
			nil,
		)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: bandwidthRelease held c.mu while Macro provider re-entered LastRawBps")
	}
	if len(releases) != 1 || releases[0].gid != beneficiaryGid {
		t.Fatalf("expected release to %s, got %+v", beneficiaryGid, releases)
	}
	if providerCalls.Load() < 1 {
		t.Fatal("expected activeBandwidthProvider to be invoked (GetGlobalPeak path)")
	}
}

// TestConvergence_BandwidthRelease_SnapshotSurvivesConcurrentRemove verifies
// that RemoveTask during unlock-before-provider does not fatal on concurrent
// map iteration (snapshot of disappearances is taken under lock).
func TestConvergence_BandwidthRelease_SnapshotSurvivesConcurrentRemove(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	speedstats.AddRecordV2(100*1024*1024, 1, 100*1024*1024, false, 50, "example.com", "wan", "testenv")

	beneficiaryGid := "sg_release_snap_ben"
	done1 := "sg_release_snap_d1"
	done2 := "sg_release_snap_d2"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256,
	)
	defer ct.Stop()

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	var removes atomic.Int32
	activeBandwidthProvider = func(scope, envKey string) int64 {
		// Delete both disappearance keys while unlocked — live range would fatal.
		ct.RemoveTask(done1)
		ct.RemoveTask(done2)
		removes.Add(1)
		return 0
	}

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.prevActiveGids = map[string]gidInfo{
		done1: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		done2: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.prevActiveSpeeds = map[string]int64{
		done1: 10 * 1024 * 1024,
		done2: 10 * 1024 * 1024,
	}
	ct.mu.Unlock()

	done := make(chan struct{})
	var releases []pendingScale
	go func() {
		defer close(done)
		releases = ct.bandwidthRelease(
			[]TrackedTaskInfo{tracker.tasks[0]},
			map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
			map[string]bool{},
			nil,
			nil,
			nil,
		)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out / hung during snapshot+RemoveTask race")
	}
	if removes.Load() < 1 {
		t.Fatal("expected provider to run (and RemoveTask under unlock)")
	}
	// First disappearance elects beneficiary; second sees pendingGids skip → 1 release.
	if len(releases) != 1 || releases[0].gid != beneficiaryGid {
		t.Fatalf("expected 1 release to %s, got %+v", beneficiaryGid, releases)
	}
}

func TestConvergence_BandwidthRelease_DelayCompensation(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// globalPeak = 10 MB/s, vThreadAvg = 10 MB/s.
	// activeBw = 10 MB/s (cache lag: disappeared task still counted).
	// disappearedSpeed = 10 MB/s → compensatedBw = 0 → headroom = 10 MB >= 10 MB → pass.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 10 * 1024 * 1024 }

	beneficiaryGid := "sg_delay_comp_beneficiary"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_delay_comp_completed"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.prevActiveSpeeds = map[string]int64{
		completedGid: 10 * 1024 * 1024,
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		nil,
		nil,
	)
	found := false
	for _, r := range releases {
		if r.gid == beneficiaryGid {
			found = true
		}
	}
	if !found {
		t.Fatal("expected delay compensation to allow ScaleUp despite cache lag")
	}
}

func TestConvergence_BandwidthRelease_NoCompensationWhenSpeedZero(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// globalPeak = 10 MB/s, vThreadAvg = 10 MB/s.
	// activeBw = 10 MB/s, disappearedSpeed = 0 → no compensation → blocked.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 10 * 1024 * 1024 }

	beneficiaryGid := "sg_no_comp_beneficiary"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_no_comp_completed"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	// prevActiveSpeeds has no entry for completedGid → disappearedSpeed = 0
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		nil,
		nil,
	)
	for _, r := range releases {
		if r.gid == beneficiaryGid {
			t.Fatal("expected no ScaleUp when disappearedSpeed=0 and V_available insufficient")
		}
	}
}

func TestConvergence_BandwidthRelease_AtMaxSkipsApprovedDelta(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_release_at_max"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{gid: makeWorkers(8, 2*1024*1024)}}
	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 8,
	)
	completed := "sg_release_at_max_done"
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	ct.prevActiveGids = map[string]gidInfo{
		completed: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		gid:       {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.prevActiveSpeeds = map[string]int64{completed: 10 * 1024 * 1024, gid: 8 * 1024 * 1024}
	ct.mu.Unlock()

	approved := make(map[string]int)
	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{gid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		approved,
		nil,
		nil,
	)
	if len(releases) != 0 {
		t.Fatalf("at max must not release, got %+v", releases)
	}
	if approved[approvedScopeKey("wan", "testenv")] != 0 {
		t.Fatalf("approvedDelta must stay 0, got %d", approved[approvedScopeKey("wan", "testenv")])
	}

	telemetry.data[gid] = makeWorkers(7, 2*1024*1024)
	releases = ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{gid: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		approved,
		nil,
		nil,
	)
	if len(releases) != 1 || releases[0].delta != 1 {
		t.Fatalf("max-1 should +1, got %+v", releases)
	}
}

func TestClampPositiveDelta_MaxConnectionsBounds(t *testing.T) {
	ct := &ConvergenceTicker{maxConnections: 1}
	if ct.clampPositiveDelta(1, 1) != 0 {
		t.Fatal("max=1 at cap")
	}
	ct.maxConnections = 8
	if ct.clampPositiveDelta(7, 1) != 1 || ct.clampPositiveDelta(8, 1) != 0 {
		t.Fatal("max=8 bounds")
	}
	ct.maxConnections = 64
	if ct.clampPositiveDelta(64, 4) != 0 || ct.clampPositiveDelta(60, 8) != 4 {
		t.Fatal("max=64 bounds")
	}
	ct.maxConnections = 256
	if ct.clampPositiveDelta(256, 1) != 0 || ct.clampPositiveDelta(250, 10) != 6 {
		t.Fatal("max=256 bounds")
	}
	if ct.clampPositiveDelta(10, -2) != -2 {
		t.Fatal("negative requested must pass through")
	}
	ct.maxConnections = 0
	if ct.clampPositiveDelta(4, 2) != 0 {
		t.Fatal("non-positive maxConnections must not apply a raise")
	}
}
