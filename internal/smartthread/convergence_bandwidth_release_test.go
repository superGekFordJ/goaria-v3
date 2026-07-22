package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
	if approvedDelta["wantestenv"] != 1 {
		t.Fatalf("expected approvedDelta[wantestenv]=1, got %d", approvedDelta["wantestenv"])
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
	// After approvedDelta["wan"]=1 → second +1: effectiveBw=10MB, headroom=0 < 10MB → blocked.
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear("wan|example.com")

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
	ps1, ok1 := ct.processTask(tracker.tasks[0], false, approvedDelta)
	if !ok1 || ps1.delta != 1 {
		t.Fatalf("expected first task probe-up +1, got ok=%v delta=%d", ok1, ps1.delta)
	}
	if ps1.delta > 0 {
		approvedDelta[ps1.scope+ps1.envKey] += ps1.delta
	}

	// Second task: same scope, V_available headroom now consumed by first.
	tracker.tasks[1].CompletedLength = 60 * 1024 * 1024
	telemetry.data[gid2] = makeWorkers(8, 2*1024*1024)
	ct.mu.Lock()
	if s, ok := ct.states[gid2]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	ps2, ok2 := ct.processTask(tracker.tasks[1], false, approvedDelta)
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
	)
	if len(releases) != 1 {
		t.Fatalf("expected 1 release (same-domain only), got %d: %+v", len(releases), releases)
	}
	if releases[0].gid != gidA {
		t.Errorf("expected release for gidA (same domain), got %s", releases[0].gid)
	}
}

func TestConvergence_BandwidthRelease_EmptyDomainFallbackToScopeOnly(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gidA := "sg_empty_domain"
	gidB := "sg_known_domain"

	// Scenario 1: both candidates present, gidA has fewer workers so it is
	// elected deterministically (lowest currentWorkers wins). gidB has a
	// non-empty domain but must still be a valid candidate under the
	// empty-domain fallback.
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
		ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
		)
		if len(releases) != 1 {
			t.Fatalf("expected 1 release, got %d: %+v", len(releases), releases)
		}
		if releases[0].gid != gidA {
			t.Errorf("expected gidA elected (lowest workers), got %s", releases[0].gid)
		}
	})

	// Scenario 2: only gidB (non-empty domain) present. If the empty-domain
	// fallback correctly ignores domain, gidB is elected — proving it is a
	// valid candidate when the disappeared task's domain is empty.
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
		ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
		)
		if len(releases) != 1 {
			t.Fatalf("expected 1 release for gidB, got %d: %+v", len(releases), releases)
		}
		if releases[0].gid != gidB {
			t.Errorf("expected gidB elected (non-empty domain still valid under empty-domain fallback), got %s", releases[0].gid)
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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

	releases1 := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil)
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

	releases2 := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil)
	if len(releases2) != 1 {
		t.Fatalf("expected 1 release on second call, got %d", len(releases2))
	}
	secondElected := releases2[0].gid

	if firstElected == secondElected {
		t.Errorf("expected fair rotation to elect different beneficiaries, got %s twice", firstElected)
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)

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
	)
	for _, r := range releases {
		if r.gid == beneficiaryGid {
			t.Fatal("expected no ScaleUp when disappearedSpeed=0 and V_available insufficient")
		}
	}
}
