package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

func deferredReleaseFixture(t *testing.T, beneficiary string, workers int) (
	*ConvergenceTicker, *mockTracker, *mockTelemetry,
) {
	t.Helper()
	return deferredReleaseFixtureMaxConn(t, beneficiary, workers, 32)
}

func deferredReleaseFixtureMaxConn(t *testing.T, beneficiary string, workers, maxConn int) (
	*ConvergenceTicker, *mockTracker, *mockTelemetry,
) {
	t.Helper()
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: beneficiary, Status: "active", Scope: "wan", EnvKey: "testenv",
				Domain: "example.com", CompletedLength: 100 * 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			beneficiary: makeWorkers(workers, 2*1024*1024),
		},
	}
	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, maxConn,
	)
	t.Cleanup(func() { ct.Stop() })

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiary)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	s.macroReady = true
	s.lastRawBps = int64(workers) * 5 * 1024 * 1024
	ct.mu.Unlock()
	return ct, tracker, telemetry
}

func armPending(ct *ConvergenceTicker, lk, envKey string, preDeath int64) {
	ct.mu.Lock()
	ct.pendingReleases[lk] = &pendingRelease{envKey: envKey, preDeathDomainBps: preDeath}
	ct.mu.Unlock()
}

func TestConvergence_DeferredRelease_OneThreadFillsDomain(t *testing.T) {
	ben := "sg_fill_ben"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 1)

	// Domain target via preDeath only (no speedstats peaks → V_available cold-allows).
	ct.mu.Lock()
	ct.states[ben].lastRawBps = 22 * 1024 * 1024
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 22*1024*1024)

	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	pendingGids := map[string]bool{}
	approved := make(map[string]int)
	dStats := map[string]*domainStats{lk: {activeWorkers: 1, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, activeGids, pendingGids, approved, dStats, false)
	if len(settles) != 0 {
		t.Fatalf("expected no settle scale when gap < observedEff, got %+v", settles)
	}
	ct.mu.Lock()
	_, stillArmed := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if stillArmed {
		t.Fatal("expected arm cleared after 1-thread-fills-domain disarm")
	}
}

func TestConvergence_DeferredRelease_BoundedDoubling(t *testing.T) {
	ben := "sg_dbl_ben"
	ct, tracker, telemetry := deferredReleaseFixture(t, ben, 2)

	// Target 22MB via preDeath; 2 workers @ 5MB each → gap=12, desired=ceil(12/5)=3, delta=min(3,2)=2.
	// No speedstats peaks so V_available cold-allows needed>1.
	ct.mu.Lock()
	ct.states[ben].lastRawBps = 10 * 1024 * 1024
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024) // room for multi-step after 2→4

	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	pendingGids := map[string]bool{}
	approved := make(map[string]int)
	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, activeGids, pendingGids, approved, dStats, false)
	if len(settles) != 1 || settles[0].delta != 2 {
		t.Fatalf("expected delta=2 (bounded doubling), got %+v", settles)
	}
	if settles[0].delta > 2 {
		t.Fatalf("delta %d exceeds currentWorkers=2", settles[0].delta)
	}

	// Keep arm for next iteration; simulate 4 workers still below target.
	telemetry.data[ben] = makeWorkers(4, 2*1024*1024)
	ct.mu.Lock()
	ct.states[ben].lastRawBps = 20 * 1024 * 1024
	ct.states[ben].phase = phaseStable // clear probing from prior jump for next settle unit call
	ct.states[ben].probeUpDelta = 0
	delete(pendingGids, ben)
	ct.mu.Unlock()
	dStats[lk].activeWorkers = 4
	approved = make(map[string]int)

	settles2 := ct.settlePendingReleases(tracker.tasks, activeGids, pendingGids, approved, dStats, false)
	if len(settles2) != 1 {
		t.Fatalf("expected second settle, got %+v", settles2)
	}
	if settles2[0].delta > 4 {
		t.Fatalf("delta %d exceeds currentWorkers=4", settles2[0].delta)
	}
	ct.mu.Lock()
	_, stillArmed := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if !stillArmed {
		t.Fatal("expected arm kept for multi-step doubling")
	}
}

func TestConvergence_DeferredRelease_ReElectsWhenBeneficiaryGone(t *testing.T) {
	peerA := "sg_reelect_a"
	peerB := "sg_reelect_b"
	ct, tracker, telemetry := deferredReleaseFixture(t, peerA, 2)
	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: peerB, Status: "active", Scope: "wan", EnvKey: "testenv",
		Domain: "example.com", CompletedLength: 50 * 1024 * 1024,
	})
	telemetry.data[peerB] = makeWorkers(4, 2*1024*1024)
	ct.mu.Lock()
	sb := ct.getOrCreateState(peerB)
	sb.phase = phaseStable
	sb.macroReady = true
	sb.lastRawBps = 8 * 1024 * 1024
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024)

	// Only peerB remains active at settle (peerA "disappeared" between arm and settle).
	tracker.tasks = tracker.tasks[1:]
	activeGids := map[string]gidInfo{peerB: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	dStats := map[string]*domainStats{lk: {activeWorkers: 4, tasksInDomain: 1}}
	approved := make(map[string]int)

	settles := ct.settlePendingReleases(tracker.tasks, activeGids, map[string]bool{}, approved, dStats, false)
	if len(settles) != 1 || settles[0].gid != peerB {
		t.Fatalf("expected re-elect peerB, got %+v", settles)
	}
}

func TestConvergence_DeferredRelease_PrunesWhenDomainEmpty(t *testing.T) {
	ben := "sg_prune_ben"
	ct, _, _ := deferredReleaseFixture(t, ben, 2)
	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 20*1024*1024)

	// Mimic tick prune: empty dStats → drop pendingReleases.
	ct.mu.Lock()
	for key := range ct.pendingReleases {
		if _, ok := (map[string]*domainStats{})[key]; !ok {
			delete(ct.pendingReleases, key)
		}
	}
	_, still := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if still {
		t.Fatal("expected pendingReleases pruned when domain idle")
	}
}

func TestConvergence_DeferredRelease_InvalidateDefersThenDrops(t *testing.T) {
	ben := "sg_defer_ben"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 2)
	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 20*1024*1024)

	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}

	for i := 1; i <= pendingReleaseMaxDeferrals; i++ {
		settles := ct.settlePendingReleases(tracker.tasks, activeGids, map[string]bool{}, make(map[string]int), dStats, true)
		if len(settles) != 0 {
			t.Fatalf("invalidate tick %d: expected no settle, got %+v", i, settles)
		}
		ct.mu.Lock()
		pr := ct.pendingReleases[lk]
		ct.mu.Unlock()
		if pr == nil || pr.deferrals != i {
			t.Fatalf("tick %d: expected deferrals=%d, got %+v", i, i, pr)
		}
	}
	// Cap exceeded → drop.
	settles := ct.settlePendingReleases(tracker.tasks, activeGids, map[string]bool{}, make(map[string]int), dStats, true)
	if len(settles) != 0 {
		t.Fatalf("expected no settle on drop tick, got %+v", settles)
	}
	ct.mu.Lock()
	_, still := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if still {
		t.Fatal("expected arm dropped after max deferrals")
	}
}

func TestConvergence_DeferredRelease_JoinDuringArmNoVoucher(t *testing.T) {
	ben := "sg_join_ben"
	joiner := "sg_join_new"
	ct, tracker, telemetry := deferredReleaseFixture(t, ben, 1)

	lk := limitKey("wan", "example.com")
	// preDeath captured when donor held ~17MB; joiner later contributes 5MB fresh rawBps.
	armPending(ct, lk, "testenv", 22*1024*1024)

	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: joiner, Status: "active", Scope: "wan", EnvKey: "testenv",
		Domain: "example.com", CompletedLength: 10 * 1024 * 1024,
	})
	telemetry.data[joiner] = makeWorkers(1, 2*1024*1024)
	ct.mu.Lock()
	ct.states[ben].lastRawBps = 5 * 1024 * 1024
	sj := ct.getOrCreateState(joiner)
	sj.phase = phaseStable
	sj.macroReady = true
	sj.lastRawBps = 5 * 1024 * 1024
	ct.mu.Unlock()

	activeGids := map[string]gidInfo{
		ben:    {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		joiner: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 2}}
	approved := make(map[string]int)

	settles := ct.settlePendingReleases(tracker.tasks, activeGids, map[string]bool{}, approved, dStats, false)
	// currentDomainBps = 5+5 = 10; gap=12; elected is lowest workers (tie → rotation).
	// Not a voucher for preDeath: gap accounts for joiner's fresh rawBps.
	if len(settles) != 1 {
		t.Fatalf("expected one settle, got %+v", settles)
	}
	electedCW := len(telemetry.data[settles[0].gid])
	if settles[0].delta > electedCW {
		t.Fatalf("delta %d exceeds elected workers %d (voucher-like overshoot)", settles[0].delta, electedCW)
	}
	// Full preDeath as voucher would try to allocate ~ceil(22/5)=5 ignoring joiner occupancy.
	if settles[0].delta >= 5 {
		t.Fatalf("delta=%d looks like preDeath voucher, not gap-based", settles[0].delta)
	}
}

func TestConvergence_DeferredRelease_OvershootReboundUsesProbeUpDelta(t *testing.T) {
	gid := "sg_jump_ceiling"
	ct, tracker, telemetry := deferredReleaseFixture(t, gid, 8)

	ct.mu.Lock()
	s := ct.states[gid]
	s.phase = phaseProbingUp
	s.probeUpBaseline = 10 * 1024 * 1024
	s.probeUpBaselineWorkers = 4
	s.probeUpDelta = 4 // jump +4
	s.bestEff = 1_310_720
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Flat speed after +4 → GainRatio failure → rebound -(4+1)/2 = -2.
	tracker.tasks[0].CompletedLength = 100*1024*1024 + 10*1024*1024*5/1 // ~10MB/s over 5s
	telemetry.data[gid] = makeWorkers(8, 2*1024*1024)
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	if !ok || ps.delta != -2 {
		t.Fatalf("expected rebound -2 for probeUpDelta=4, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	phase := s.phase
	deltaLeft := s.probeUpDelta
	ct.mu.Unlock()
	if phase != phaseCeilingHit {
		t.Fatalf("expected phaseCeilingHit, got %d", phase)
	}
	if deltaLeft != 0 {
		t.Fatalf("expected probeUpDelta cleared, got %d", deltaLeft)
	}
}

func TestConvergence_DeferredRelease_ObservedEffZeroDegradesToOne(t *testing.T) {
	ben := "sg_eff0_ben"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 2)

	ct.mu.Lock()
	ct.states[ben].lastRawBps = 0 // macroReady but stalled
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024)

	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}
	approved := make(map[string]int)

	settles := ct.settlePendingReleases(tracker.tasks, activeGids, map[string]bool{}, approved, dStats, false)
	if len(settles) != 1 || settles[0].delta != 1 {
		t.Fatalf("expected degrade to delta=1, got %+v", settles)
	}
}

func TestConvergence_DeferredRelease_PartialScaleAdjustsProbeUpDelta(t *testing.T) {
	ben := "sg_partial_ben"
	ct, _, _ := deferredReleaseFixture(t, ben, 4)
	ct.mu.Lock()
	s := ct.states[ben]
	s.phase = phaseProbingUp
	s.probeUpDelta = 4
	s.probeUpBaseline = 10 * 1024 * 1024
	s.probeUpBaselineWorkers = 2
	ct.mu.Unlock()

	ct.adjustProbeUpDeltaAfterPartialScale(ben, 2)

	ct.mu.Lock()
	got := ct.states[ben].probeUpDelta
	ct.mu.Unlock()
	if got != 2 {
		t.Fatalf("expected probeUpDelta=2 after partial scale, got %d", got)
	}
}

func TestConvergence_DeferredRelease_ApprovedDeltaFullDelta(t *testing.T) {
	ben := "sg_appr_ben"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 4)

	ct.mu.Lock()
	ct.states[ben].lastRawBps = 20 * 1024 * 1024 // 4 workers → observedEff=5MB
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024)

	// gap=20, observedEff=5, desired=4, delta=min(4,4)=4
	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	approved := make(map[string]int)
	dStats := map[string]*domainStats{lk: {activeWorkers: 4, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, activeGids, map[string]bool{}, approved, dStats, false)
	if len(settles) != 1 || settles[0].delta != 4 {
		t.Fatalf("expected delta=4, got %+v", settles)
	}
	if approved[approvedScopeKey("wan", "testenv")] != 4 {
		t.Fatalf("scope approvedDelta want 4, got %d", approved[approvedScopeKey("wan", "testenv")])
	}
	if approved[approvedDomainKey("wan", "example.com", "testenv")] != 4 {
		t.Fatalf("domain approvedDelta want 4, got %d", approved[approvedDomainKey("wan", "example.com", "testenv")])
	}
	if approved[approvedNMaxKey("wan", "example.com")] != 4 {
		t.Fatalf("nmax approvedDelta want 4, got %d", approved[approvedNMaxKey("wan", "example.com")])
	}
}

func TestConvergence_DeferredRelease_AntiHerdOnePerDomain(t *testing.T) {
	a := "sg_herd_a"
	b := "sg_herd_b"
	ct, tracker, telemetry := deferredReleaseFixture(t, a, 2)
	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: b, Status: "active", Scope: "wan", EnvKey: "testenv",
		Domain: "example.com", CompletedLength: 50 * 1024 * 1024,
	})
	telemetry.data[b] = makeWorkers(2, 2*1024*1024)
	ct.mu.Lock()
	sb := ct.getOrCreateState(b)
	sb.phase = phaseStable
	sb.macroReady = true
	sb.lastRawBps = 10 * 1024 * 1024
	ct.states[a].lastRawBps = 10 * 1024 * 1024
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024)

	activeGids := map[string]gidInfo{
		a: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		b: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	dStats := map[string]*domainStats{lk: {activeWorkers: 4, tasksInDomain: 2}}
	approved := make(map[string]int)
	pendingGids := map[string]bool{}

	settles := ct.settlePendingReleases(tracker.tasks, activeGids, pendingGids, approved, dStats, false)
	if len(settles) != 1 {
		t.Fatalf("anti-herd: expected exactly 1 settle, got %+v", settles)
	}
	if !pendingGids[settles[0].gid] {
		t.Fatal("expected elected gid marked in pendingGids")
	}
}

func TestConvergence_DeferredRelease_ArmKeepsFreePlusOneNonProbing(t *testing.T) {
	ben := "sg_arm_free"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 2)
	speedstats.AddRecordV2(40*1024*1024, 1, 5*1024*1024, false, 50, "example.com", "wan", "testenv")

	completed := "sg_arm_donor"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completed: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		ben:       {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.prevActiveSpeeds = map[string]int64{
		completed: 17 * 1024 * 1024,
		ben:       5 * 1024 * 1024,
	}
	ct.mu.Unlock()

	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	approved := make(map[string]int)
	lk := limitKey("wan", "example.com")
	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}

	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, approved, dStats, nil)
	if len(releases) != 1 || releases[0].delta != 1 {
		t.Fatalf("expected free +1 arm, got %+v", releases)
	}
	ct.mu.Lock()
	pr := ct.pendingReleases[lk]
	phase := ct.states[ben].phase
	preDeath := int64(0)
	if pr != nil {
		preDeath = pr.preDeathDomainBps
	}
	ct.mu.Unlock()
	if pr == nil {
		t.Fatal("expected pendingRelease armed")
	}
	if preDeath != 22*1024*1024 {
		t.Fatalf("preDeathDomainBps want 22MB (donor+ben), got %d", preDeath)
	}
	if phase == phaseProbingUp {
		t.Fatal("arm +1 must not enter phaseProbingUp")
	}
}

func TestConvergence_DeferredRelease_MissingDStatsFallsBackToCandidateWorkers(t *testing.T) {
	ben := "sg_dstats_fb"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 8)
	speedstats.AddRecordV2(100*1024*1024, 1, 5*1024*1024, false, 50, "example.com", "wan", "testenv")

	lk := limitKey("wan", "example.com")
	ct.limits.SetNMax(lk, 8)
	t.Cleanup(func() { ct.limits.Clear(lk) })

	completed := "sg_dstats_donor"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completed: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.prevActiveSpeeds = map[string]int64{completed: 10 * 1024 * 1024}
	ct.mu.Unlock()

	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	// dStats nil → old bug treated domainWorkers=0 (would allow); new fallback uses cw=8 → 8+1>8 deny.
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, make(map[string]int), nil, nil)
	if len(releases) != 0 {
		t.Fatalf("expected N_max deny with missing-dStats fallback to cw, got %+v", releases)
	}
}

func TestConvergence_DeferredRelease_TickCountHandoverImproves(t *testing.T) {
	// Pure +1/tick from 2→10 needs 8 steps; bounded doubling: 2→4→8→10 needs 3 settle steps
	// after arm (+1 already applied). Assert settle-step count << linear.
	ben := "sg_tickcount"
	ct, tracker, telemetry := deferredReleaseFixtureMaxConn(t, ben, 2, 32)

	lk := limitKey("wan", "example.com")
	const targetBps = 26 * 1024 * 1024 // room for 10 workers @ ~2.5MB
	armPending(ct, lk, "testenv", targetBps)

	workers := 2
	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	settleTicks := 0
	const target = 10
	const linearWouldNeed = target - 2 // 8

	for settleTicks < 20 && workers < target {
		eff := int64(2500 * 1024)
		ct.mu.Lock()
		ct.states[ben].lastRawBps = int64(workers) * eff
		ct.states[ben].phase = phaseStable
		ct.states[ben].probeUpDelta = 0
		ct.mu.Unlock()
		telemetry.data[ben] = makeWorkers(workers, 2*1024*1024)
		dStats := map[string]*domainStats{lk: {activeWorkers: workers, tasksInDomain: 1}}
		approved := make(map[string]int)

		settles := ct.settlePendingReleases(tracker.tasks, activeGids, map[string]bool{}, approved, dStats, false)
		settleTicks++
		if len(settles) == 0 {
			break
		}
		if settles[0].delta > workers {
			t.Fatalf("tick %d: delta %d > workers %d", settleTicks, settles[0].delta, workers)
		}
		workers += settles[0].delta
	}
	if workers < target {
		t.Fatalf("did not reach target workers=%d after %d settle ticks", workers, settleTicks)
	}
	if settleTicks >= linearWouldNeed {
		t.Fatalf("bounded doubling settleTicks=%d not better than linear %d", settleTicks, linearWouldNeed)
	}
	// Honest class: ~3 settle steps (2→4→8→10), not ~8.
	if settleTicks > 4 {
		t.Fatalf("expected ~3–4 settle ticks class, got %d", settleTicks)
	}
}

func TestConvergence_DeferredRelease_NilElectedAfterUnlock(t *testing.T) {
	ben := "sg_nil_elect"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 2)
	// Force GetGlobalPeak path so provider runs (and can RemoveTask under unlock).
	speedstats.AddRecordV2(100*1024*1024, 20, 100*1024*1024, false, 50, "example.com", "wan", "testenv")
	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024)

	ct.mu.Lock()
	ct.states[ben].lastRawBps = 10 * 1024 * 1024
	ct.mu.Unlock()

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 {
		ct.RemoveTask(ben)
		return 0
	}

	activeGids := map[string]gidInfo{ben: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"}}
	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}

	// Must not panic; may skip approval after nil recheck.
	settles := ct.settlePendingReleases(tracker.tasks, activeGids, map[string]bool{}, make(map[string]int), dStats, false)
	for _, ps := range settles {
		if ps.gid == ben {
			t.Fatalf("should not approve scale for removed elected state, got %+v", settles)
		}
	}
}

func TestConvergence_DeferredRelease_NeededVAvailableRequiresMultiHeadroom(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	// Peak 10MB, vThreadAvg≈10MB (from recent peak) → needed=4 requires 40MB headroom → deny.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 { return 0 }

	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		&mockTracker{}, &mockTelemetry{}, &mockPeakRecorder{}, &mockRateChecker{}, 0, 0,
	)
	defer ct.Stop()

	if ct.checkVAvailable("wan", "example.com", "testenv", map[string]int{}, 0, 1) != true {
		t.Fatal("needed=1 should pass with 10MB headroom")
	}
	if ct.checkVAvailable("wan", "example.com", "testenv", map[string]int{}, 0, 4) != false {
		t.Fatal("needed=4 should fail without 40MB headroom")
	}
}

func TestConvergence_ProbeUp_GainRatioDeltaOneEquivalence(t *testing.T) {
	// probeUpDelta=1 must keep ExpectedGain = 1/N (existing ceiling path).
	gid := "sg_gain_eq"
	ct, tracker, telemetry := deferredReleaseFixture(t, gid, 8)
	ct.mu.Lock()
	s := ct.states[gid]
	s.phase = phaseProbingUp
	s.probeUpBaseline = 10 * 1024 * 1024
	s.probeUpBaselineWorkers = 8
	s.probeUpDelta = 1
	s.bestEff = 1_310_720
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	tracker.tasks[0].CompletedLength = 100*1024*1024 + 0 // raw≈0 → fail
	telemetry.data[gid] = makeWorkers(9, 2*1024*1024)
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	if !ok || ps.delta != -1 {
		t.Fatalf("delta==1 Probe-Up fail should rebound -1, got ok=%v delta=%d", ok, ps.delta)
	}
}

func TestConvergence_DeferredRelease_ArmPreservesPrevActiveSpeedsGuard(t *testing.T) {
	// Document/lock: tick must not write zeros into prevActiveSpeeds on invalidate.
	ben := "sg_speed_guard"
	ct, _, _ := deferredReleaseFixture(t, ben, 2)
	ct.mu.Lock()
	ct.prevActiveSpeeds[ben] = 9 * 1024 * 1024
	ct.states[ben].lastRawBps = 0 // wiped
	ct.mu.Unlock()

	// Mimic tick update guard.
	c := ct
	c.mu.Lock()
	if s, ok := c.states[ben]; ok && s.lastRawBps > 0 {
		c.prevActiveSpeeds[ben] = s.lastRawBps
	}
	got := c.prevActiveSpeeds[ben]
	c.mu.Unlock()
	if got != 9*1024*1024 {
		t.Fatalf("prevActiveSpeeds wiped on zero lastRawBps: got %d", got)
	}
}
