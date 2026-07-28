package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

func setupDomainGateProbeReady(ct *ConvergenceTicker, gid string, bestEff int64, peakWorkers int) {
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.bestEff = bestEff
	s.peakWorkers = peakWorkers
	s.prevCompleted = 10 * 1024 * 1024
	s.lastStep = 0
	s.probeBaseline = 0
	s.probeBaselineWorkers = 0
	s.probeMomentum = false
	s.probeCooldown = 0
	s.kneeFrozen = false
	s.probeUpBaseline = 0
	s.probeUpBaselineWorkers = 0
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()
}

func accumulatePending(approved map[string]int, ps pendingScale) {
	if ps.delta <= 0 {
		return
	}
	approved[approvedScopeKey(ps.scope, ps.envKey)] += ps.delta
	if ps.domain != "" {
		approved[approvedDomainKey(ps.scope, ps.domain, ps.envKey)] += ps.delta
		approved[approvedNMaxKey(ps.scope, ps.domain)] += ps.delta
	}
}

// TestConvergence_ProbeUp_BlockedByDomainNMaxMultiTask is the core SPEC-244
// regression: two same-domain peers each below per-task nMax must not both
// Probe-Up when domain sum + 1 would exceed nMax.
func TestConvergence_ProbeUp_BlockedByDomainNMaxMultiTask(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	domain := "example.com"
	key := limitKey("wan", domain)
	gid1 := "sg_domain_nmax_a"
	gid2 := "sg_domain_nmax_b"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, CompletedLength: 60 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, CompletedLength: 60 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeWorkers(6, 2*1024*1024),
			gid2: makeWorkers(6, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 12) // 6+6=12; either +1 breaches

	for _, gid := range []string{gid1, gid2} {
		setupDomainGateProbeReady(ct, gid, 1_310_720, 6)
	}

	dStats := map[string]*domainStats{
		key: {activeWorkers: 12, tasksInDomain: 2},
	}
	approved := make(map[string]int)

	ps1, ok1 := ct.processTask(tracker.tasks[0], false, approved, dStats, nil)
	if ok1 && ps1.delta > 0 {
		t.Fatalf("expected first peer blocked by domain N_max (12+1>12), got ok=%v delta=%d", ok1, ps1.delta)
	}

	// Control fixture: domainWorkers=11 leaves one slot; second peer then blocked by approvedDelta.
	setupDomainGateProbeReady(ct, gid1, 1_310_720, 6)
	setupDomainGateProbeReady(ct, gid2, 1_310_720, 6)
	dStats[key].activeWorkers = 11
	approved = make(map[string]int)

	psOK, okOK := ct.processTask(tracker.tasks[0], false, approved, dStats, nil)
	if !okOK || psOK.delta != 1 {
		t.Fatalf("expected Probe-Up when domainWorkers=11 < nMax=12, got ok=%v delta=%d", okOK, psOK.delta)
	}
	accumulatePending(approved, psOK)

	ps2, ok2 := ct.processTask(tracker.tasks[1], false, approved, dStats, nil)
	if ok2 && ps2.delta > 0 {
		t.Fatalf("expected second peer blocked by domain approvedDelta, got delta=%d", ps2.delta)
	}
}

// TestConvergence_ProbeUp_DomainBudgetExhaustedScopeHealthy denies Probe-Up
// when domain peak is exhausted even if scope headroom remains.
func TestConvergence_ProbeUp_DomainBudgetExhaustedScopeHealthy(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Scope peak huge; domain peak tight. Seed domain first then overwrite
	// global via a second domain so GetGlobalPeak can exceed domain.
	speedstats.AddRecordV2(5*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")
	speedstats.AddRecordV2(50*1024*1024, 1, 10*1024*1024, false, 50, "other.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	gid := "sg_domain_bw_gate"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 60 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{gid: makeWorkers(4, 2*1024*1024)}}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(limitKey("wan", "example.com"))
	setupDomainGateProbeReady(ct, gid, 1_310_720, 4)

	// Domain occupancy near peak (5MB); vThreadAvg ≈ 5MB → no room for +1.
	domainMacro := map[string]int64{
		approvedDomainKey("wan", "example.com", "testenv"): 5 * 1024 * 1024,
	}
	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, domainMacro)
	if ok && ps.delta > 0 {
		t.Fatalf("expected Probe-Up denied by domain budget, got delta=%d", ps.delta)
	}
}

// TestConvergence_ProbeUp_ScopeExhaustedDomainHealthy still denies (no widen).
func TestConvergence_ProbeUp_ScopeExhaustedDomainHealthy(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// One record → both GetGlobalPeak and GetDomainPeak see 10MB.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	// Scope fully occupied; domain occupancy 0 so domain dim would allow.
	activeBandwidthProvider = func(scope, envKey string) int64 { return 10 * 1024 * 1024 }

	gid := "sg_scope_bw_gate"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 60 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{gid: makeWorkers(4, 2*1024*1024)}}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(limitKey("wan", "example.com"))
	setupDomainGateProbeReady(ct, gid, 1_310_720, 4)

	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	if ok && ps.delta > 0 {
		t.Fatalf("expected Probe-Up denied by exhausted scope, got delta=%d", ps.delta)
	}
}

// TestConvergence_ProbeUp_ColdDomainPeakAllows verifies missing GetDomainPeak
// does not lock Probe-Up when scope headroom is healthy.
func TestConvergence_ProbeUp_ColdDomainPeakAllows(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Scope peak only (different domain record) — cold for target domain.
	speedstats.AddRecordV2(50*1024*1024, 1, 10*1024*1024, false, 50, "other.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	gid := "sg_cold_domain"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "new-domain.com", CompletedLength: 60 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{gid: makeWorkers(4, 2*1024*1024)}}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(limitKey("wan", "new-domain.com"))
	setupDomainGateProbeReady(ct, gid, 1_310_720, 4)

	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	if !ok || ps.delta != 1 {
		t.Fatalf("expected Probe-Up allowed on cold domain, got ok=%v delta=%d", ok, ps.delta)
	}
}

// TestConvergence_ApprovedDelta_SameTickDomainOversell blocks a second
// same-domain Probe-Up after the first fills domain budget in the same tick.
func TestConvergence_ApprovedDelta_SameTickDomainOversell(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Domain peak = 10MB; scope peak also 10MB via same record.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	gid1 := "sg_dom_oversell_1"
	gid2 := "sg_dom_oversell_2"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 60 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 60 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeWorkers(4, 2*1024*1024),
			gid2: makeWorkers(4, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(limitKey("wan", "example.com"))
	for _, gid := range []string{gid1, gid2} {
		setupDomainGateProbeReady(ct, gid, 1_310_720, 4)
	}

	approved := make(map[string]int)
	domainMacro := map[string]int64{} // occupancy 0; first +1 fills domain budget

	ps1, ok1 := ct.processTask(tracker.tasks[0], false, approved, nil, domainMacro)
	if !ok1 || ps1.delta != 1 {
		t.Fatalf("expected first Probe-Up +1, got ok=%v delta=%d", ok1, ps1.delta)
	}
	accumulatePending(approved, ps1)

	ps2, ok2 := ct.processTask(tracker.tasks[1], false, approved, nil, domainMacro)
	if ok2 && ps2.delta > 0 {
		t.Fatalf("expected second blocked by domain approvedDelta, got delta=%d", ps2.delta)
	}
}

// TestConvergenceNMaxClamp_KneeCrossedRebound_DomainHeadroom0 verifies rebound
// uses domain-aggregated workers: peer already owns domain capacity → rebound 0
// with FloorHit unchanged.
func TestConvergenceNMaxClamp_KneeCrossedRebound_DomainHeadroom0(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	domain := "example.com"
	key := limitKey("wan", domain)
	gid := "sg_rebound_peer"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, CompletedLength: 110 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(3, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 10)

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.lastStep = 2
	s.probeBaseline = 32 * 1024 * 1024
	s.probeBaselineWorkers = 5
	s.probeMomentum = true
	s.probeCooldown = 0
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Domain already at nMax via peers; this task alone has only 3 workers.
	dStats := map[string]*domainStats{key: {activeWorkers: 10, tasksInDomain: 3}}
	ps, ok := ct.processTask(tracker.tasks[0], false, nil, dStats, nil)
	if ok && ps.delta > 0 {
		t.Fatalf("expected rebound=0 from domain headroom, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.kneeFrozen {
		t.Error("expected kneeFrozen=true")
	}
	if s.phase != phaseFloorHit {
		t.Errorf("expected phaseFloorHit, got %d", s.phase)
	}
}

// TestConvergence_BandwidthRelease_RespectsDomainApprovedDelta ensures a
// same-tick Probe-Up that filled the last N_max slot blocks release +1.
func TestConvergence_BandwidthRelease_RespectsDomainApprovedDelta(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(50*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	domain := "example.com"
	key := limitKey("wan", domain)
	beneficiary := "sg_release_ben"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiary, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, CompletedLength: 10 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiary: makeWorkers(5, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 6)

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiary)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.prevActiveGids = map[string]gidInfo{
		"sg_release_donor": {Domain: domain, Scope: "wan", EnvKey: "testenv"},
	}
	ct.mu.Unlock()

	dStats := map[string]*domainStats{key: {activeWorkers: 5}}
	approved := map[string]int{
		approvedNMaxKey("wan", domain):              1, // prior Probe-Up filled last slot (env-blind)
		approvedDomainKey("wan", domain, "testenv"): 1,
		approvedScopeKey("wan", "testenv"):          1,
	}

	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{beneficiary: {Domain: domain, Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		approved,
		dStats,
		nil,
	)
	if len(releases) != 0 {
		t.Fatalf("expected release blocked by domain approvedDelta (5+1+1>6), got %#v", releases)
	}

	// Control without approved domain delta: release allowed.
	releasesOK := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{beneficiary: {Domain: domain, Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		dStats,
		nil,
	)
	if len(releasesOK) != 1 {
		t.Fatalf("control: expected 1 release without approvedDelta, got %#v", releasesOK)
	}
}

// TestConvergence_ProbeUp_CrossEnvNMaxPendingShared proves N_max pending is
// env-blind: envA +1 fills the last slot so envB on the same scope|domain
// cannot Probe-Up even though its env-partitioned BW approvedDomainKey is 0.
func TestConvergence_ProbeUp_CrossEnvNMaxPendingShared(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	speedstats.AddRecordV2(50*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "envA")
	speedstats.AddRecordV2(50*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "envB")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	domain := "example.com"
	key := limitKey("wan", domain)
	gidA := "sg_crossenv_nmax_a"
	gidB := "sg_crossenv_nmax_b"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gidA, Status: "active", Scope: "wan", EnvKey: "envA", Domain: domain, CompletedLength: 60 * 1024 * 1024},
			{GID: gidB, Status: "active", Scope: "wan", EnvKey: "envB", Domain: domain, CompletedLength: 60 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gidA: makeWorkers(5, 2*1024*1024),
			gidB: makeWorkers(5, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(key)
	ct.limits.SetNMax(key, 11) // 5+5=10; one +1 fills last slot

	setupDomainGateProbeReady(ct, gidA, 1_310_720, 5)
	setupDomainGateProbeReady(ct, gidB, 1_310_720, 5)

	dStats := map[string]*domainStats{key: {activeWorkers: 10, tasksInDomain: 2}}
	approved := make(map[string]int)

	psA, okA := ct.processTask(tracker.tasks[0], false, approved, dStats, nil)
	if !okA || psA.delta != 1 {
		t.Fatalf("expected envA Probe-Up +1, got ok=%v delta=%d", okA, psA.delta)
	}
	accumulatePending(approved, psA)
	if approved[approvedNMaxKey("wan", domain)] != 1 {
		t.Fatalf("expected env-blind nmax pending=1, got %d", approved[approvedNMaxKey("wan", domain)])
	}

	psB, okB := ct.processTask(tracker.tasks[1], false, approved, dStats, nil)
	if okB && psB.delta > 0 {
		t.Fatalf("expected envB blocked by shared N_max pending, got delta=%d", psB.delta)
	}
}

// TestConvergence_BandwidthRelease_DomainCompDoesNotFalseAllow verifies
// disappearedSpeed compensates scope only: domainMacroBps already excludes
// the disappeared task, so domain must still deny when occupancy is at peak.
func TestConvergence_BandwidthRelease_DomainCompDoesNotFalseAllow(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	// Domain peak 10MB; scope peak also seeded high via second domain.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")
	speedstats.AddRecordV2(50*1024*1024, 1, 10*1024*1024, false, 50, "other.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	// Scope still counts disappeared 10MB; without compensation scope would deny.
	// With scope-only compensation, scope allows — domain must still deny.
	activeBandwidthProvider = func(scope, envKey string) int64 { return 10 * 1024 * 1024 }

	domain := "example.com"
	beneficiary := "sg_comp_ben"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiary, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: domain, CompletedLength: 10 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiary: makeWorkers(2, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0)
	ct.limits.Clear(limitKey("wan", domain))

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiary)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.prevActiveGids = map[string]gidInfo{
		"sg_comp_donor": {Domain: domain, Scope: "wan", EnvKey: "testenv"},
	}
	ct.prevActiveSpeeds = map[string]int64{
		"sg_comp_donor": 10 * 1024 * 1024,
	}
	ct.mu.Unlock()

	// Living peer already occupies full domain peak; disappeared is NOT in occ.
	domainMacro := map[string]int64{
		approvedDomainKey("wan", domain, "testenv"): 10 * 1024 * 1024,
	}

	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{beneficiary: {Domain: domain, Scope: "wan", EnvKey: "testenv"}},
		map[string]bool{},
		nil,
		nil,
		domainMacro,
	)
	if len(releases) != 0 {
		t.Fatalf("domain compensation must not false-allow when occ already excludes donor, got %#v", releases)
	}
}
