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
	activeBandwidthProvider = func(scope, envKey string, now time.Time) int64 { return 0 }

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

	pendingGids := map[string]bool{}
	approved := make(map[string]int)
	dStats := map[string]*domainStats{lk: {activeWorkers: 1, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, pendingGids, approved, dStats, nil, tracker.tasks, time.Now())
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

	pendingGids := map[string]bool{}
	approved := make(map[string]int)
	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, pendingGids, approved, dStats, nil, tracker.tasks, time.Now())
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

	settles2 := ct.settlePendingReleases(tracker.tasks, pendingGids, approved, dStats, nil, tracker.tasks, time.Now())
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
	dStats := map[string]*domainStats{lk: {activeWorkers: 4, tasksInDomain: 1}}
	approved := make(map[string]int)

	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, approved, dStats, nil, tracker.tasks, time.Now())
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

// TestConvergence_DeferredRelease_InvalidateDefersThenDrops verifies per-
// beneficiary dirty deferral: while the elected beneficiary sits inside the
// perturbation margin the arm postpones (no settle, arm kept), and the fourth
// consecutive dirty observation drops it. Unaffected elections never consume
// the deferral budget.
func TestConvergence_DeferredRelease_InvalidateDefersThenDrops(t *testing.T) {
	ben := "sg_defer_ben"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 2)
	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 20*1024*1024)

	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}
	affected := map[string]bool{ben: true}

	for i := 1; i <= pendingReleaseMaxDeferrals; i++ {
		settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, affected, tracker.tasks, time.Now())
		if len(settles) != 0 {
			t.Fatalf("dirty tick %d: expected no settle, got %+v", i, settles)
		}
		ct.mu.Lock()
		pr := ct.pendingReleases[lk]
		ct.mu.Unlock()
		if pr == nil || pr.deferrals != i {
			t.Fatalf("tick %d: expected deferrals=%d, got %+v", i, i, pr)
		}
	}
	// Cap exceeded → drop.
	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, affected, tracker.tasks, time.Now())
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

	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 2}}
	approved := make(map[string]int)

	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, approved, dStats, nil, tracker.tasks, time.Now())
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

	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, nil, time.Now())
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

	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}
	approved := make(map[string]int)

	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, approved, dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 1 || settles[0].delta != 1 {
		t.Fatalf("expected degrade to delta=1, got %+v", settles)
	}
}

// TestConvergence_DeferredRelease_GapZeroBeforeColdDegrade proves gap==0 disarms
// even when observedEff<=0 (SPEC_FLAW: cold degrade must not run first).
func TestConvergence_DeferredRelease_GapZeroBeforeColdDegrade(t *testing.T) {
	ben := "sg_gap0_cold"
	peer := "sg_gap0_peer"
	ct, tracker, telemetry := deferredReleaseFixture(t, ben, 1)

	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: peer, Status: "active", Scope: "wan", EnvKey: "testenv",
		Domain: "example.com", CompletedLength: 50 * 1024 * 1024,
	})
	telemetry.data[peer] = makeWorkers(4, 2*1024*1024)

	ct.mu.Lock()
	ct.states[ben].lastRawBps = 0 // stalled → observedEff=0
	peerState := ct.getOrCreateState(peer)
	peerState.macroReady = true
	peerState.lastRawBps = 40 * 1024 * 1024 // fills domain alone
	peerState.phase = phaseStable
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024) // target == current → gap=0

	dStats := map[string]*domainStats{lk: {activeWorkers: 5, tasksInDomain: 2}}
	approved := make(map[string]int)

	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, approved, dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 0 {
		t.Fatalf("gap==0 must disarm before cold +1, got %+v", settles)
	}
	ct.mu.Lock()
	_, stillArmed := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if stillArmed {
		t.Fatal("expected pendingReleases disarmed when gap==0")
	}
}

func TestConvergence_DeferredRelease_SkipsPhaseProbingUpElect(t *testing.T) {
	ben := "sg_probe_elect"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 2)

	ct.mu.Lock()
	ct.states[ben].phase = phaseProbingUp
	ct.states[ben].lastRawBps = 10 * 1024 * 1024
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024)

	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 0 {
		t.Fatalf("phaseProbingUp must be skipped in elect, got %+v", settles)
	}
	ct.mu.Lock()
	_, stillArmed := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if !stillArmed {
		t.Fatal("no candidate should keep arm (not disarm)")
	}
}

func TestConvergence_DeferredRelease_MaxConnectionsClampsSettle(t *testing.T) {
	ben := "sg_maxconn"
	ct, tracker, _ := deferredReleaseFixtureMaxConn(t, ben, 4, 4) // already at maxConnections

	ct.mu.Lock()
	ct.states[ben].lastRawBps = 20 * 1024 * 1024 // observedEff=5MB, gap large
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 80*1024*1024)

	dStats := map[string]*domainStats{lk: {activeWorkers: 4, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 0 {
		t.Fatalf("maxConnections headroom 0 should skip settle, got %+v", settles)
	}
	ct.mu.Lock()
	_, stillArmed := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if !stillArmed {
		t.Fatal("maxConnections deny should keep arm")
	}
}

func TestConvergence_DeferredRelease_RateLimitedKeepsArm(t *testing.T) {
	ben := "sg_ratelim"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 2)
	ct.rateChecker = &mockRateChecker{
		limited: map[string]bool{ben: true},
		bps:     map[string]int64{ben: 1_000_000},
	}

	ct.mu.Lock()
	ct.states[ben].lastRawBps = 10 * 1024 * 1024
	ct.mu.Unlock()

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024)

	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 0 {
		t.Fatalf("rateLimited should skip settle, got %+v", settles)
	}
	ct.mu.Lock()
	_, stillArmed := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if !stillArmed {
		t.Fatal("rateLimited deny should keep arm")
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
	approved := make(map[string]int)
	dStats := map[string]*domainStats{lk: {activeWorkers: 4, tasksInDomain: 1}}

	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, approved, dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 1 || settles[0].delta != 4 {
		t.Fatalf("expected delta=4, got %+v", settles)
	}
	if approved[approvedScopeKey("wan", "testenv")] != 4 {
		t.Fatalf("scope approvedDelta want 4, got %d", approved[approvedScopeKey("wan", "testenv")])
	}
	if approved[approvedDomainKey("wan", "example.com")] != 4 {
		t.Fatalf("domain approvedDelta want 4, got %d", approved[approvedDomainKey("wan", "example.com")])
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

	dStats := map[string]*domainStats{lk: {activeWorkers: 4, tasksInDomain: 2}}
	approved := make(map[string]int)
	pendingGids := map[string]bool{}

	settles := ct.settlePendingReleases(tracker.tasks, pendingGids, approved, dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 1 {
		t.Fatalf("anti-herd: expected exactly 1 settle, got %+v", settles)
	}
	if !pendingGids[settles[0].gid] {
		t.Fatal("expected elected gid marked in pendingGids")
	}
}

// TestConvergence_DeferredRelease_SettleSeesAdvancedRawAfterPrePass proves
// the settle re-overlay reads post-processTask lastRawBps, not the pre-pass
// snapshot: the same occTasks argument carries deliberately stale fields, so
// only a re-read of c.states can flip the outcome. Without the re-overlay the
// second call would still see the stale 5MB and settle a +1 into a full domain.
func TestConvergence_DeferredRelease_SettleSeesAdvancedRawAfterPrePass(t *testing.T) {
	ben := "sg_stale_ben"
	ct, tracker, _ := deferredReleaseFixture(t, ben, 1)

	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 40*1024*1024)

	// occTasks deliberately carries NO raw fields — a stale pre-pass snapshot
	// shape. State lastRawBps=5MB → occ 5MB → gap=35 → settle emits.
	dStats := map[string]*domainStats{lk: {activeWorkers: 1, tasksInDomain: 1}}
	approved := make(map[string]int)
	pendingGids := map[string]bool{}

	settles := ct.settlePendingReleases(tracker.tasks, pendingGids, approved, dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 1 {
		t.Fatalf("low raw: expected settle emit into gap, got %+v", settles)
	}
	ct.mu.Lock()
	_, armed := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if !armed {
		t.Fatal("arm must persist after a partial settle")
	}

	// processTask advanced lastRawBps to 40MB between pre-pass and settle —
	// simulate the fresh measurement directly (same field the tick writes).
	ct.mu.Lock()
	ct.states[ben].lastRawBps = 40 * 1024 * 1024
	ct.mu.Unlock()

	// Fresh per-tick maps: call 1 already marked ben in pendingGids, which
	// would exclude it from election and hide the disarm.
	settles = ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 0 {
		t.Fatalf("advanced raw must fill the domain gap; settle would overshoot: %+v", settles)
	}
	ct.mu.Lock()
	_, armed = ct.pendingReleases[lk]
	ct.mu.Unlock()
	if armed {
		t.Fatal("gap==0 on post-processTask raw must disarm")
	}
}

// TestConvergence_Tick_OccupancyProviderClaimsFeedDomainSettle drives a real
// tick() with the occupancy-provider seam: a waiting-claim task present only
// in the occupancy set (not the active set) must flow through TaskOccupancy
// into the settle domain evaluation — not via an injected domainMacro map.
// Control without the claim proves the disarm came from the claim.
func TestConvergence_Tick_OccupancyProviderClaimsFeedDomainSettle(t *testing.T) {
	lk := limitKey("wan", "example.com")

	claimTask := TrackedTaskInfo{
		GID: "sg_wait_claim", Status: "waiting", Scope: "wan", EnvKey: "testenv",
		Domain: "example.com", TargetBandwidth: 35 * 1024 * 1024,
	}

	// With the claim: 5MB raw + 35MB claim occupancy = target → gap=0 → disarm.
	t.Run("claim-fills-domain", func(t *testing.T) {
		ben := "sg_tickocc_ben"
		ct, tracker, _ := deferredReleaseFixture(t, ben, 1)
		ct.SetOccupancyProvider(&mockOccupancy{
			tasks: append([]TrackedTaskInfo{tracker.tasks[0]}, claimTask),
		})
		armPending(ct, lk, "testenv", 40*1024*1024)

		ct.tick()

		ct.mu.Lock()
		_, armed := ct.pendingReleases[lk]
		ct.mu.Unlock()
		if armed {
			t.Fatal("claim occupancy must fill the domain gap and disarm the release")
		}
	})

	// Without the claim: only 5MB raw → gap=35 → settle emits, arm kept.
	t.Run("no-claim-arm-survives", func(t *testing.T) {
		ben := "sg_tickocc_ben2"
		ct, tracker, _ := deferredReleaseFixture(t, ben, 1)
		ct.SetOccupancyProvider(&mockOccupancy{tasks: tracker.tasks})
		armPending(ct, lk, "testenv", 40*1024*1024)

		ct.tick()

		ct.mu.Lock()
		_, armed := ct.pendingReleases[lk]
		ct.mu.Unlock()
		if !armed {
			t.Fatal("without claim occupancy the release must stay armed (gap>0)")
		}
	})
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

	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, approved, dStats, nil, time.Now())
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
	releases := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, make(map[string]int), nil, nil, time.Now())
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

		settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, approved, dStats, nil, tracker.tasks, time.Now())
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
	activeBandwidthProvider = func(scope, envKey string, now time.Time) int64 {
		ct.RemoveTask(ben)
		return 0
	}

	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}

	// Must not panic; may skip approval after nil recheck.
	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, nil, tracker.tasks, time.Now())
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
	activeBandwidthProvider = func(scope, envKey string, now time.Time) int64 { return 0 }

	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		&mockTracker{}, &mockTelemetry{}, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256,
	)
	defer ct.Stop()

	if ct.checkVAvailable("wan", "example.com", "testenv", map[string]int{}, 0, 1, time.Now()) != true {
		t.Fatal("needed=1 should pass with 10MB headroom")
	}
	if ct.checkVAvailable("wan", "example.com", "testenv", map[string]int{}, 0, 4, time.Now()) != false {
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

	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, nil, time.Now())
	if !ok || ps.delta != -1 {
		t.Fatalf("delta==1 Probe-Up fail should rebound -1, got ok=%v delta=%d", ok, ps.delta)
	}
}

func TestConvergence_DeferredRelease_ArmPreservesPrevActiveSpeedsGuard(t *testing.T) {
	// Document/lock: tick must not write zeros into prevActiveSpeeds — the
	// map is delay compensation for pre-death domain sums, so it keeps the
	// last positive raw as capability evidence even when live raw is zero.
	ben := "sg_speed_guard"
	ct, _, _ := deferredReleaseFixture(t, ben, 2)
	ct.mu.Lock()
	ct.prevActiveSpeeds[ben] = 9 * 1024 * 1024
	ct.states[ben].lastRawBps = 0 // zero measurement does not clobber evidence
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

	// prevActiveOccupancy has the opposite contract: every sampled GID gets
	// a stored value, zero included, and a live zero must overwrite the
	// previous positive — exits consume the stored number verbatim.
	ct.mu.Lock()
	ct.states[ben].lastRawBps = 10 * 1024 * 1024
	ct.states[ben].macroReady = true
	ct.mu.Unlock()
	ct.tick()
	ct.mu.Lock()
	gotOcc, okOcc := ct.prevActiveOccupancy[ben]
	ct.mu.Unlock()
	if !okOcc || gotOcc != 10*1024*1024 {
		t.Fatalf("prevActiveOccupancy=(%d,%v), want (%d,true)", gotOcc, okOcc, 10*1024*1024)
	}
	ct.mu.Lock()
	ct.states[ben].lastRawBps = 0 // measured zero → occupancy 0 next tick
	ct.mu.Unlock()
	ct.tick()
	ct.mu.Lock()
	gotOcc, okOcc = ct.prevActiveOccupancy[ben]
	ct.mu.Unlock()
	if !okOcc || gotOcc != 0 {
		t.Fatalf("live zero must overwrite previous occupancy: got (%d,%v), want (0,true)", gotOcc, okOcc)
	}
}

func TestConvergence_DeferredRelease_HeadroomClampsRequestedDelta(t *testing.T) {
	ben := "sg_headroom_clamp"
	ct, tracker, _ := deferredReleaseFixtureMaxConn(t, ben, 6, 8)
	ct.mu.Lock()
	ct.states[ben].lastRawBps = 20 * 1024 * 1024
	ct.mu.Unlock()
	lk := limitKey("wan", "example.com")
	armPending(ct, lk, "testenv", 80*1024*1024)
	dStats := map[string]*domainStats{lk: {activeWorkers: 6, tasksInDomain: 1}}
	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, nil, tracker.tasks, time.Now())
	if len(settles) != 1 || settles[0].delta != 2 {
		t.Fatalf("requested 4 with headroom 2 want 2, got %+v", settles)
	}

	ct2, tracker2, _ := deferredReleaseFixtureMaxConn(t, ben+"z", 8, 8)
	ct2.mu.Lock()
	ct2.states[ben+"z"].lastRawBps = 20 * 1024 * 1024
	ct2.mu.Unlock()
	armPending(ct2, lk, "testenv", 80*1024*1024)
	dStats2 := map[string]*domainStats{lk: {activeWorkers: 8, tasksInDomain: 1}}
	none := ct2.settlePendingReleases(tracker2.tasks, map[string]bool{}, make(map[string]int), dStats2, nil, tracker2.tasks, time.Now())
	if len(none) != 0 {
		t.Fatalf("headroom 0 should skip, got %+v", none)
	}
}

// localDeferralFixture builds a ticker whose tracker rows span the given
// (gid, domain, env, workers) tuples — each state primed stable+ready.
func localDeferralFixture(t *testing.T, rows []struct {
	gid, domain, env string
	workers          int
}) (*ConvergenceTicker, *mockTracker) {
	t.Helper()
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	var tasks []TrackedTaskInfo
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{}}
	for _, r := range rows {
		tasks = append(tasks, TrackedTaskInfo{
			GID: r.gid, Status: "active", Scope: "wan", EnvKey: r.env,
			Domain: r.domain, CompletedLength: 10 * 1024 * 1024,
		})
		telemetry.data[r.gid] = makeWorkers(r.workers, 2*1024*1024)
	}
	tracker := &mockTracker{tasks: tasks}
	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 32,
	)
	t.Cleanup(func() { ct.Stop() })
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string, n time.Time) int64 { return 0 }
	ct.mu.Lock()
	for _, r := range rows {
		s := ct.getOrCreateState(r.gid)
		s.phase = phaseStable
		s.macroReady = true
		s.lastRawBps = 10 * 1024 * 1024
	}
	ct.mu.Unlock()
	return ct, tracker
}

// TestConvergence_DeferredRelease_LocalDeferral proves per-beneficiary dirty
// deferral: only the domain whose elected beneficiary is affected postpones;
// a dirty elected winner is never swapped for a cleaner peer; no-candidate
// elections and denied paths keep the arm without consuming deferrals; a
// dirty gap==0 still keeps the arm; same-domain re-arms keep the count.
func TestConvergence_DeferredRelease_LocalDeferral(t *testing.T) {
	now := time.Now()
	ben := "sg_local_ben"
	other := "sg_local_other"
	lk := limitKey("wan", "example.com")
	lkOther := limitKey("wan", "other.com")
	ct, tracker := localDeferralFixture(t, []struct {
		gid, domain, env string
		workers          int
	}{
		{ben, "example.com", "testenv", 2},
		{other, "other.com", "testenv", 1},
	})
	dStats := map[string]*domainStats{
		lk:      {activeWorkers: 2, tasksInDomain: 1},
		lkOther: {activeWorkers: 1, tasksInDomain: 1},
	}

	// Arm both domains. other's target is already met by live domain
	// occupancy → clean gap==0 disarm; ben's arm defers because its elected
	// beneficiary is affected — unaffected elections never consume the count.
	armPending(ct, lk, "testenv", 20*1024*1024)
	armPending(ct, lkOther, "testenv", 5*1024*1024)
	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, map[string]bool{ben: true}, tracker.tasks, now)
	if len(settles) != 0 {
		t.Fatalf("affected election must defer, other must disarm: settles=%+v", settles)
	}
	ct.mu.Lock()
	prBen := ct.pendingReleases[lk]
	_, otherArmed := ct.pendingReleases[lkOther]
	ct.mu.Unlock()
	if prBen == nil || prBen.deferrals != 1 {
		t.Fatalf("affected arm must keep+count: %+v", prBen)
	}
	if otherArmed {
		t.Fatal("unaffected arm must settle/disarm this tick")
	}
}

// TestConvergence_DeferredRelease_LocalDeferral_NoSwapAndGuards covers: the
// dirty elected winner is never swapped for a cleaner peer; no-candidate
// keeps the arm without consuming deferrals; dirty gap==0 keeps the arm;
// same-domain re-arm preserves the accumulated count.
func TestConvergence_DeferredRelease_LocalDeferral_NoSwapAndGuards(t *testing.T) {
	lk := limitKey("wan", "example.com")
	now := time.Now()
	ben1 := "sg_ns_1" // 1 worker — lowest → always elected first
	ben2 := "sg_ns_2" // 9 workers — the would-be clean swap target
	ct, tracker := localDeferralFixture(t, []struct {
		gid, domain, env string
		workers          int
	}{
		{ben1, "example.com", "testenv", 1},
		{ben2, "example.com", "testenv", 9},
	})
	activeGids := map[string]gidInfo{
		ben1: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		ben2: {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	dStats := map[string]*domainStats{lk: {activeWorkers: 10, tasksInDomain: 2}}
	affected := map[string]bool{ben1: true}

	// No-swap: elected ben1 is dirty → defer; clean ben2 is never substituted.
	armPending(ct, lk, "testenv", 40*1024*1024)
	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, affected, tracker.tasks, now)
	if len(settles) != 0 {
		t.Fatalf("dirty elected winner must defer, not swap: settles=%+v", settles)
	}
	ct.mu.Lock()
	pr := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if pr == nil || pr.deferrals != 1 {
		t.Fatalf("deferrals=%+v, want 1", pr)
	}

	// No-candidate: every candidate burned by pendingGids → keep arm, no
	// deferral consumed.
	settles = ct.settlePendingReleases(tracker.tasks,
		map[string]bool{ben1: true, ben2: true}, make(map[string]int), dStats, affected, tracker.tasks, now)
	if len(settles) != 0 {
		t.Fatalf("no-candidate must produce nothing: %+v", settles)
	}
	ct.mu.Lock()
	pr = ct.pendingReleases[lk]
	ct.mu.Unlock()
	if pr == nil || pr.deferrals != 1 {
		t.Fatalf("no-candidate keep must not consume deferrals: %+v", pr)
	}

	// Dirty gap==0 still keeps: the domain is already filled, which would
	// disarm a clean arm — but the dirty defer runs first.
	ct.mu.Lock()
	ct.pendingReleases[lk].preDeathDomainBps = 1 // gap guaranteed <= 0
	ct.mu.Unlock()
	settles = ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, affected, tracker.tasks, now)
	ct.mu.Lock()
	pr = ct.pendingReleases[lk]
	ct.mu.Unlock()
	if len(settles) != 0 || pr == nil || pr.deferrals != 2 {
		t.Fatalf("dirty gap=0 must keep+count (defer wins over disarm): settles=%v pr=%+v", settles, pr)
	}

	// Same-domain re-arm keeps the accumulated count (arm updated, not
	// replaced). Simulate a second disappearance re-arming the live arm.
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		ben1:          {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		ben2:          {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
		"sg_ns_donor": {Domain: "example.com", Scope: "wan", EnvKey: "testenv"},
	}
	ct.prevActiveSpeeds = map[string]int64{"sg_ns_donor": 5 * 1024 * 1024}
	ct.mu.Unlock()
	ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil, nil, nil, now)
	ct.mu.Lock()
	pr = ct.pendingReleases[lk]
	ct.mu.Unlock()
	if pr == nil || pr.deferrals != 2 {
		t.Fatalf("same-domain re-arm must keep deferrals: %+v", pr)
	}
}

// TestConvergence_DeferredRelease_CrossEnvConservativeOneShot covers the
// cross-env conservative settle: an envA arm targets envA evidence (preDeath
// or the env-matched domain peak) while currentDomainBps counts envA+envB
// occupancy; clean gap==0 or gap<eff disarms permanently (no self re-arm),
// gap==eff still permits +1, dirty defers before any disarm check, and a
// peak recorded under another env is never summed into the envA target.
func TestConvergence_DeferredRelease_CrossEnvConservativeOneShot(t *testing.T) {
	lk := limitKey("wan", "example.com")
	now := time.Now()
	benA := "sg_ce_a"  // envA beneficiary
	peerB := "sg_ce_b" // envB peer sharing the domain
	ct, tracker := localDeferralFixture(t, []struct {
		gid, domain, env string
		workers          int
	}{
		{benA, "example.com", "envA", 2},
		{peerB, "example.com", "envB", 3},
	})
	// Domain fill = envA 10MB + envB 15MB = 25MB (settle re-overlays states).
	ct.mu.Lock()
	ct.states[peerB].lastRawBps = 15 * 1024 * 1024
	ct.mu.Unlock()
	dStats := map[string]*domainStats{lk: {activeWorkers: 5, tasksInDomain: 2}}

	// envA target = preDeath 20MB; domain already holds 25MB → clean gap==0
	// disarm — envB occupancy counts toward the domain fill.
	armPending(ct, lk, "envA", 20*1024*1024)
	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, nil, tracker.tasks, now)
	if len(settles) != 0 {
		t.Fatalf("cross-env gap==0 must disarm, got %+v", settles)
	}
	ct.mu.Lock()
	_, armed := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if armed {
		t.Fatal("gap==0 must disarm (envB occupancy counts toward the domain fill)")
	}

	// Re-settle: no arm exists → nothing rebuilds it (one-shot, not a
	// persistent speed chaser).
	settles = ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, nil, tracker.tasks, now)
	if len(settles) != 0 {
		t.Fatalf("disarmed domain must not resurrect, got %+v", settles)
	}

	// gap<observedEff disarms too: target 29 vs domain 25 → gap 4 < eff 5.
	armPending(ct, lk, "envA", 29*1024*1024)
	settles = ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, nil, tracker.tasks, now)
	ct.mu.Lock()
	_, armed = ct.pendingReleases[lk]
	ct.mu.Unlock()
	if len(settles) != 0 || armed {
		t.Fatalf("gap<eff must disarm: settles=%+v armed=%v", settles, armed)
	}

	// gap==eff boundary still permits +1: target 30, domain 25 → gap 5 = eff.
	armPending(ct, lk, "envA", 30*1024*1024)
	settles = ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, nil, tracker.tasks, now)
	if len(settles) != 1 || settles[0].delta != 1 || settles[0].gid != benA {
		t.Fatalf("gap==eff must settle +1 on the envA beneficiary, got %+v", settles)
	}

	// Dirty elected beneficiary defers before any gap/disarm evaluation.
	armPending(ct, lk, "envA", 20*1024*1024) // gap==0 conditions again
	settles = ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, map[string]bool{benA: true}, tracker.tasks, now)
	ct.mu.Lock()
	pr := ct.pendingReleases[lk]
	ct.mu.Unlock()
	if len(settles) != 0 || pr == nil || pr.deferrals != 1 {
		t.Fatalf("dirty must defer before disarm: settles=%v pr=%+v", settles, pr)
	}

	// Voucher rule: an envA-matched peak (10MB, below the 25MB domain fill)
	// sets the target — the larger envB peak (50MB) is never summed in nor
	// used as the envA voucher. target = max(preDeath, envA peak) = 10MB
	// → gap == 0 → disarm; any foreign-env leak would produce a settle.
	ct.mu.Lock()
	delete(ct.pendingReleases, lk)
	ct.mu.Unlock()
	speedstats.AddRecordV2(10*1024*1024, 2, 10*1024*1024, false, 50, "example.com", "wan", "envA")
	speedstats.AddRecordV2(50*1024*1024, 9, 10*1024*1024, false, 50, "example.com", "wan", "envB")
	armPending(ct, lk, "envA", 5*1024*1024)
	settles = ct.settlePendingReleases(tracker.tasks, map[string]bool{},
		make(map[string]int), dStats, nil, tracker.tasks, now)
	ct.mu.Lock()
	_, armed = ct.pendingReleases[lk]
	ct.mu.Unlock()
	if len(settles) != 0 || armed {
		t.Fatalf("envA target must ignore summed/foreign envB peak: settles=%+v armed=%v", settles, armed)
	}
}
