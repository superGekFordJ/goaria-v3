package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// reactivationFixture builds a single-sg_ ticker whose tracker row carries a
// real activation anchor. A baseline tickAt(t0) then pins prevSampleAt at t0,
// and moving the row's ActivatedAt inside (t0, t0+5s) reproduces a
// pause→resume hidden inside one tick gap — membership never changed.
func reactivationFixture(t *testing.T, gid string, workers int, activatedAt time.Time) (
	*ConvergenceTicker, *mockTracker, *mockTelemetry, *mockPeakRecorder,
) {
	t.Helper()
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	tracker := &mockTracker{tasks: []TrackedTaskInfo{{
		GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv",
		Domain: "example.com", CompletedLength: 10 * 1024 * 1024,
		ActivatedAt: activatedAt,
	}}}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{
		gid: makeWorkers(workers, 2*1024*1024),
	}}
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry, recorder, &mockRateChecker{}, 0, 256,
	)
	// Defend against N_max leftovers in the shared store.
	ct.limits.Clear(limitKey("wan", "example.com"))
	t.Cleanup(func() { ct.Stop() })
	return ct, tracker, telemetry, recorder
}

// armProbeUp seeds an in-flight Probe-Up verdict that would fail on the
// diluted raw below (gainRatio 0.2 < 0.5 → would-be CeilingHit).
func armProbeUp(ct *ConvergenceTicker, gid string, baseline int64, workers int) {
	ct.mu.Lock()
	s := ct.states[gid]
	s.phase = phaseProbingUp
	s.probeUpBaseline = baseline
	s.probeUpBaselineWorkers = workers
	s.probeUpDelta = 1
	ct.mu.Unlock()
}

// Case 1: same-window pause→resume with an in-flight Probe-Up cancels the
// verdict unjudged — no CeilingHit, no ceiling evidence, no worker rollback —
// while D2 still publishes and long-term memory survives.
func TestConvergence_Reactivation_ProbeUpVerdictCancelled(t *testing.T) {
	mb := int64(1024 * 1024)
	gid := "sg_react_probeup"
	t0 := time.Now()
	ct, tracker, _, recorder := reactivationFixture(t, gid, 9, t0.Add(-time.Minute))

	ct.tickAt(t0) // baseline: prevSampleAt = t0

	armProbeUp(ct, gid, 40*mb, 8)
	ct.mu.Lock()
	s := ct.states[gid]
	s.bestEff = 8 * mb
	s.peakSpeed = 30 * mb
	s.peakWorkers = 8
	s.sustainCount = 5
	ct.mu.Unlock()

	// The GID never left the sampled set; pause→resume lands inside the
	// window. Diluted raw is 41MB/s — a would-be verdict failure.
	tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
	tracker.tasks[0].CompletedLength = 10*mb + 41*mb*5
	t1 := t0.Add(5 * time.Second)
	ct.tickAt(t1)

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state")
	}
	if s.phase != phaseStable {
		t.Fatalf("cancelled verdict must return to stable, phase=%d", s.phase)
	}
	if s.probeUpBaseline != 0 || s.probeUpBaselineWorkers != 0 || s.probeUpDelta != 0 {
		t.Fatalf("up verdict fields must clear: baseline=%d workers=%d delta=%d",
			s.probeUpBaseline, s.probeUpBaselineWorkers, s.probeUpDelta)
	}
	if s.phase == phaseCeilingHit || s.ceilingMemory != 0 || s.frozenCooldown != 0 {
		t.Fatalf("must not enter CeilingHit: phase=%d ceiling=%d cooldown=%d",
			s.phase, s.ceilingMemory, s.frozenCooldown)
	}
	if _, ok := recorder.records[gid]; ok {
		t.Error("dirty tick must not emit ceiling evidence")
	}
	if s.lastRawBps != 41*mb || !s.macroReady {
		t.Errorf("D2 must publish on the dirty tick: raw=%d ready=%v", s.lastRawBps, s.macroReady)
	}
	if !s.prevSampleAt.Equal(t1) {
		t.Errorf("sample endpoint must re-anchor to t1: %v", s.prevSampleAt)
	}
	if s.bestEff != 8*mb || s.peakSpeed != 30*mb || s.peakWorkers != 8 || s.sustainCount != 5 {
		t.Errorf("long-term memory must survive: bestEff=%d peak=%d@%d sustain=%d",
			s.bestEff, s.peakSpeed, s.peakWorkers, s.sustainCount)
	}
}

// Case 2: both Probe-Down verdict shapes (settling, and stable+lastStep
// awaiting the marginal-drop verdict) are cancelled unjudged by a
// same-window re-activation; each positive cooldown ticks down once.
func TestConvergence_Reactivation_ProbeDownVerdictCancelled(t *testing.T) {
	mb := int64(1024 * 1024)
	for _, tc := range []struct {
		name  string
		phase int
	}{
		{"settling", phaseSettling},
		{"stable_lastStep", phaseStable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gid := "sg_react_down_" + tc.name
			t0 := time.Now()
			ct, tracker, _, _ := reactivationFixture(t, gid, 9, t0.Add(-time.Minute))

			ct.tickAt(t0)

			ct.mu.Lock()
			s := ct.states[gid]
			s.phase = tc.phase
			s.lastStep = 1
			s.probeBaseline = 40 * mb
			s.probeBaselineWorkers = 8
			s.probeMomentum = true
			s.probeCooldown = 2
			s.frozenCooldown = 1
			s.settleWaitTicks = 2
			ct.mu.Unlock()

			// Raw 20MB/s vs baseline 40MB/s would cross the knee for the
			// stable+lastStep shape; the settling shape just carries its
			// pending verdict forward. The dirty window drops both.
			tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
			tracker.tasks[0].CompletedLength = 10*mb + 20*mb*5
			t1 := t0.Add(5 * time.Second)
			ct.tickAt(t1)

			ct.mu.Lock()
			s = ct.states[gid]
			ct.mu.Unlock()
			if s == nil {
				t.Fatal("expected state")
			}
			if s.phase != phaseStable {
				t.Fatalf("cancelled down verdict must return to stable, phase=%d", s.phase)
			}
			if s.lastStep != 0 || s.probeBaseline != 0 || s.probeBaselineWorkers != 0 || s.probeMomentum {
				t.Fatalf("down verdict fields must clear: lastStep=%d baseline=%d@%d momentum=%v",
					s.lastStep, s.probeBaseline, s.probeBaselineWorkers, s.probeMomentum)
			}
			if s.settleWaitTicks != 0 {
				t.Fatalf("settle wait must reset on cancel, got %d", s.settleWaitTicks)
			}
			if s.phase == phaseFloorHit || s.phase == phaseFrozen || s.floorMemory != 0 || s.kneeFrozen {
				t.Fatalf("must not enter FloorHit: phase=%d floor=%d kneeFrozen=%v",
					s.phase, s.floorMemory, s.kneeFrozen)
			}
			if s.probeCooldown != 1 || s.frozenCooldown != 0 {
				t.Fatalf("cooldowns must tick once, saturated at zero: probe=%d frozen=%d",
					s.probeCooldown, s.frozenCooldown)
			}
			if s.lastRawBps != 20*mb || !s.macroReady {
				t.Errorf("D2 must publish: raw=%d ready=%v", s.lastRawBps, s.macroReady)
			}
			if !s.prevSampleAt.Equal(t1) {
				t.Errorf("endpoint must re-anchor to t1: %v", s.prevSampleAt)
			}
		})
	}

	// Empty telemetry on the re-activation tick still cancels the pending
	// down verdict — and the settle wait counter with it.
	t.Run("empty_stats", func(t *testing.T) {
		gid := "sg_react_down_gap"
		t0 := time.Now()
		ct, tracker, telemetry, _ := reactivationFixture(t, gid, 9, t0.Add(-time.Minute))
		ct.tickAt(t0)

		ct.mu.Lock()
		s := ct.states[gid]
		s.phase = phaseSettling
		s.lastStep = 1
		s.probeBaseline = 40 * mb
		s.probeBaselineWorkers = 8
		s.probeMomentum = true
		s.settleWaitTicks = 2
		s.prevCompleted = 10 * mb
		s.prevSampleAt = t0
		ct.mu.Unlock()

		delete(telemetry.data, gid)
		tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
		tracker.tasks[0].CompletedLength = 60 * mb
		ct.tickAt(t0.Add(5 * time.Second))

		ct.mu.Lock()
		s = ct.states[gid]
		ct.mu.Unlock()
		if s.phase != phaseStable || s.lastStep != 0 || s.probeBaseline != 0 || s.probeMomentum {
			t.Fatalf("gap tick must cancel verdict: phase=%d step=%d baseline=%d momentum=%v",
				s.phase, s.lastStep, s.probeBaseline, s.probeMomentum)
		}
		if s.settleWaitTicks != 0 {
			t.Fatalf("settle wait must reset on empty-stats cancel, got %d", s.settleWaitTicks)
		}
	})
}

// Case 3: continuous tasks are never marked by a peer's re-activation — a
// would-be CeilingHit verdict still runs, and a margin-less peer on the same
// dirty tick still completes its clean D2/D3 (the !ok fallback is not widened).
func TestConvergence_Reactivation_ContinuousTaskNotDirty(t *testing.T) {
	mb := int64(1024 * 1024)

	t.Run("continuous_verdict_still_judged", func(t *testing.T) {
		gid := "sg_react_cont_up"
		t0 := time.Now()
		ct, tracker, _, _ := reactivationFixture(t, gid, 9, t0.Add(-time.Minute))
		ct.tickAt(t0)
		armProbeUp(ct, gid, 40*mb, 8)

		// Anchor unchanged — the would-be failure must still be judged.
		tracker.tasks[0].CompletedLength = 10*mb + 41*mb*5
		ct.tickAt(t0.Add(5 * time.Second))

		ct.mu.Lock()
		s := ct.states[gid]
		ct.mu.Unlock()
		if s.phase != phaseCeilingHit || s.ceilingMemory != 41*mb {
			t.Fatalf("clean verdict must run: phase=%d ceiling=%d", s.phase, s.ceilingMemory)
		}
	})

	t.Run("marginless_peer_not_dragged_dirty", func(t *testing.T) {
		gid1 := "sg_react_peer_a"
		gid2 := "sg_react_peer_b"
		t0 := time.Now()

		speedstats.ResetRecordsForTest()
		t.Cleanup(speedstats.ResetRecordsForTest)
		tracker := &mockTracker{tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
				CompletedLength: 10 * mb, ActivatedAt: t0.Add(-time.Minute)},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
				CompletedLength: 10 * mb, ActivatedAt: t0.Add(-time.Minute)},
		}}
		telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{
			gid1: makeWorkers(9, 2*1024*1024),
			gid2: makeWorkers(4, 2*1024*1024),
		}}
		ct := newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker, telemetry,
		)
		ct.limits.Clear(limitKey("wan", "example.com"))
		t.Cleanup(func() { ct.Stop() })

		ct.tickAt(t0)
		armProbeUp(ct, gid1, 40*mb, 8)

		// Pure re-activation, E=0: only gid1 moves its activation anchor.
		tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
		tracker.tasks[0].CompletedLength = 10*mb + 41*mb*5
		tracker.tasks[1].CompletedLength = 10*mb + 10*mb*5 // raw 10MB/s → newEff 2.5MB
		ct.tickAt(t0.Add(5 * time.Second))

		ct.mu.Lock()
		s1 := ct.states[gid1]
		s2 := ct.states[gid2]
		ct.mu.Unlock()
		if s1.phase != phaseStable || s1.probeUpBaseline != 0 {
			t.Fatalf("re-activated gid must be dirty: phase=%d baseline=%d", s1.phase, s1.probeUpBaseline)
		}
		// gid2 has no trustworthy margin (cold state). A clean D3 proves the
		// re-activation did not widen the margin-less fallback onto it.
		if s2.lastRawBps != 10*mb || !s2.macroReady {
			t.Fatalf("peer D2 must publish: raw=%d ready=%v", s2.lastRawBps, s2.macroReady)
		}
		if s2.bestEff <= 0 || s2.sustainCount != 1 {
			t.Fatalf("margin-less peer must still run D3: bestEff=%d sustain=%d", s2.bestEff, s2.sustainCount)
		}
	})
}

// Case 4: anchor-less legacy active rows never match re-activation; their
// verdicts still run and the sample endpoint anchors at now.
func TestConvergence_Reactivation_LegacyNoAnchorNotDirty(t *testing.T) {
	mb := int64(1024 * 1024)
	gid := "sg_react_legacy"
	t0 := time.Now()
	ct, tracker, _, _ := reactivationFixture(t, gid, 9, time.Time{})

	ct.tickAt(t0)
	armProbeUp(ct, gid, 40*mb, 8)

	tracker.tasks[0].CompletedLength = 10*mb + 41*mb*5
	t1 := t0.Add(5 * time.Second)
	ct.tickAt(t1)

	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseCeilingHit || s.ceilingMemory != 41*mb {
		t.Fatalf("legacy anchor-less row must not be marked dirty: phase=%d ceiling=%d", s.phase, s.ceilingMemory)
	}
	if !s.prevSampleAt.Equal(t1) {
		t.Errorf("zero activation anchor writes now: prevSampleAt=%v want %v", s.prevSampleAt, t1)
	}
}

// Case 5: empty telemetry on a re-activation tick still cancels the pending
// verdict and re-bases the window while keeping the last measured evidence —
// including when the caller's affected flag is false — and never mints state.
func TestConvergence_Reactivation_EmptyStatsCancelsVerdict(t *testing.T) {
	mb := int64(1024 * 1024)

	t.Run("via_tick", func(t *testing.T) {
		gid := "sg_react_gaptick"
		t0 := time.Now()
		ct, tracker, telemetry, _ := reactivationFixture(t, gid, 9, t0.Add(-time.Minute))
		ct.tickAt(t0)

		armProbeUp(ct, gid, 40*mb, 8)
		ct.mu.Lock()
		s := ct.states[gid]
		s.probeCooldown = 2
		s.frozenCooldown = 1
		s.lastRawBps = 10 * mb
		s.macroReady = true
		ct.mu.Unlock()

		delete(telemetry.data, gid)
		tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
		tracker.tasks[0].CompletedLength = 60 * mb
		t1 := t0.Add(5 * time.Second)
		ct.tickAt(t1)

		ct.mu.Lock()
		s = ct.states[gid]
		ct.mu.Unlock()
		if s.phase != phaseStable || s.probeUpBaseline != 0 || s.probeUpDelta != 0 {
			t.Fatalf("gap tick must cancel verdict: phase=%d baseline=%d delta=%d",
				s.phase, s.probeUpBaseline, s.probeUpDelta)
		}
		if s.probeCooldown != 1 || s.frozenCooldown != 0 {
			t.Fatalf("cooldowns tick once, saturated: probe=%d frozen=%d", s.probeCooldown, s.frozenCooldown)
		}
		if !s.prevSampleAt.Equal(t1) || s.prevCompleted != 60*mb {
			t.Errorf("window must re-base to the gap endpoint: prev=(%d,%v)", s.prevCompleted, s.prevSampleAt)
		}
		if s.lastRawBps != 10*mb || !s.macroReady {
			t.Errorf("last measured evidence must be kept: raw=%d ready=%v", s.lastRawBps, s.macroReady)
		}
	})

	t.Run("direct_call_external_clean", func(t *testing.T) {
		gid := "sg_react_gapdirect"
		t0 := time.Now()
		ct, tracker, _, _ := reactivationFixture(t, gid, 9, t0.Add(-time.Minute))

		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseProbingUp
		s.probeUpBaseline = 40 * mb
		s.probeUpBaselineWorkers = 8
		s.probeUpDelta = 1
		s.prevCompleted = 10 * mb
		s.prevSampleAt = t0
		ct.mu.Unlock()

		tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
		t1 := t0.Add(5 * time.Second)
		// Internal detection must fire even with affected=false.
		if ps, ok := ct.processTaskStats(tracker.tasks[0], nil, false, nil, nil, nil, t1); ok || ps.delta != 0 {
			t.Fatalf("gap path never emits scale: ok=%v delta=%d", ok, ps.delta)
		}

		ct.mu.Lock()
		s = ct.states[gid]
		ct.mu.Unlock()
		if s.phase != phaseStable || s.probeUpBaseline != 0 {
			t.Fatalf("internal detection must cancel verdict: phase=%d baseline=%d",
				s.phase, s.probeUpBaseline)
		}
		if !s.prevSampleAt.Equal(t1) {
			t.Errorf("endpoint re-anchors to max(now,ActivatedAt): %v", s.prevSampleAt)
		}
	})

	t.Run("no_state_no_create", func(t *testing.T) {
		gid := "sg_react_gapnone"
		t0 := time.Now()
		ct, tracker, _, _ := reactivationFixture(t, gid, 9, t0.Add(-time.Minute))
		tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
		// Even an externally dirty flag must not mint state on a gap path.
		ct.processTaskStats(tracker.tasks[0], nil, true, nil, nil, nil, t0.Add(5*time.Second))
		ct.mu.Lock()
		_, exists := ct.states[gid]
		ct.mu.Unlock()
		if exists {
			t.Fatal("no-telemetry path must not create state")
		}
	})
}

// Case 6: an armed pendingRelease whose elected beneficiary re-activated
// inside the window defers in place — arm kept, deferrals counted, no jump
// baseline written.
func TestConvergence_Reactivation_PendingReleaseDefers(t *testing.T) {
	mb := int64(1024 * 1024)
	gid := "sg_react_ben"
	lk := limitKey("wan", "example.com")
	t0 := time.Now()
	ct, tracker, _, _ := reactivationFixture(t, gid, 2, t0.Add(-time.Minute))
	ct.tickAt(t0) // baseline → prevActiveOccupancy non-nil, prevSampleAt=t0

	armPending(ct, lk, "testenv", 40*mb)

	tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
	tracker.tasks[0].CompletedLength = 10*mb + 2*mb*5 // diluted raw 2MB/s
	t1 := t0.Add(5 * time.Second)
	ct.tickAt(t1)

	ct.mu.Lock()
	pr := ct.pendingReleases[lk]
	s := ct.states[gid]
	ct.mu.Unlock()
	if pr == nil || pr.deferrals != 1 {
		t.Fatalf("dirty beneficiary must defer once and keep the arm: %+v", pr)
	}
	if s.probeUpBaseline != 0 || s.probeUpBaselineWorkers != 0 || s.probeUpDelta != 0 {
		t.Fatalf("deferred settle must not write jump baseline: baseline=%d workers=%d delta=%d",
			s.probeUpBaseline, s.probeUpBaselineWorkers, s.probeUpDelta)
	}
	if s.phase == phaseProbingUp {
		t.Fatal("deferred settle must not enter phaseProbingUp")
	}

	// Control: without the dirty flag the same arm/gap settles delta>1 —
	// the deferral was the only thing holding it back.
	dStats := map[string]*domainStats{lk: {activeWorkers: 2, tasksInDomain: 1}}
	settles := ct.settlePendingReleases(tracker.tasks, map[string]bool{}, make(map[string]int), dStats, nil, tracker.tasks, t1)
	if len(settles) != 1 || settles[0].delta <= 1 {
		t.Fatalf("precondition: clean settle would emit delta>1, got %+v", settles)
	}
}

// Case 7: a hidden re-activation marks the domain dirty — the N_max unlock
// confirmation resets to zero instead of counting the poisoned tick clean,
// and only later clean ticks rebuild it.
func TestConvergence_Reactivation_NMaxUnlockConfirmReset(t *testing.T) {
	mb := int64(1024 * 1024)
	gid := "sg_react_nmax"
	lk := limitKey("wan", "example.com")
	t0 := time.Now()
	ct, tracker, _, _ := reactivationFixture(t, gid, 3, t0.Add(-time.Minute))
	ct.limits = NewServerLimitStore() // isolate from the shared store
	ct.tickAt(t0)

	ct.limits.SetNMax(lk, 3)
	ct.mu.Lock()
	ct.domainUnlockTicks[lk] = 1
	ct.mu.Unlock()

	tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
	tracker.tasks[0].CompletedLength = 10*mb + 10*mb*5
	t1 := t0.Add(5 * time.Second)
	ct.tickAt(t1)

	ct.mu.Lock()
	counter := ct.domainUnlockTicks[lk]
	ct.mu.Unlock()
	if counter != 0 {
		t.Fatalf("dirty tick must reset the confirm counter, got %d", counter)
	}
	if _, has := ct.limits.GetNMax(lk); !has {
		t.Fatal("N_max must survive — the dirty tick is not a clean confirm")
	}

	// Only clean ticks rebuild the confirmation.
	tracker.tasks[0].CompletedLength = 10*mb + 10*mb*10
	ct.tickAt(t1.Add(5 * time.Second))
	ct.mu.Lock()
	counter = ct.domainUnlockTicks[lk]
	ct.mu.Unlock()
	if counter != 1 {
		t.Fatalf("first clean tick after reset must count 1, got %d", counter)
	}
	if _, has := ct.limits.GetNMax(lk); !has {
		t.Fatal("still locked after a single clean confirm")
	}
	tracker.tasks[0].CompletedLength = 10*mb + 10*mb*15
	ct.tickAt(t1.Add(10 * time.Second))
	if _, has := ct.limits.GetNMax(lk); has {
		t.Fatal("two clean confirms must unlock normally")
	}
}

// Case 8: an activation anchor µs after the tick instant anchors the
// baseline at ActivatedAt — the same anchor never re-fires detection, so the
// next tick's D2/D3 run clean.
func TestConvergence_Reactivation_MicrosecondAnchorSkew(t *testing.T) {
	mb := int64(1024 * 1024)
	gid := "sg_react_usskew"
	t0 := time.Now()
	ct, tracker, _, _ := reactivationFixture(t, gid, 4, t0.Add(time.Microsecond))

	ct.tickAt(t0) // bootstrap baseline

	ct.mu.Lock()
	s := ct.states[gid]
	anchor := s.prevSampleAt
	ct.mu.Unlock()
	if !anchor.Equal(tracker.tasks[0].ActivatedAt) {
		t.Fatalf("baseline anchor must equal ActivatedAt: anchor=%v activated=%v",
			anchor, tracker.tasks[0].ActivatedAt)
	}

	tracker.tasks[0].CompletedLength = 10*mb + 10*mb*5
	t1 := t0.Add(5 * time.Second)
	ct.tickAt(t1)

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.lastRawBps <= 0 || !s.macroReady {
		t.Fatalf("clean D2 must publish: raw=%d ready=%v", s.lastRawBps, s.macroReady)
	}
	if s.sustainCount != 1 || s.bestEff <= 0 {
		t.Fatalf("same activation must not re-detect: sustain=%d bestEff=%d", s.sustainCount, s.bestEff)
	}
	if !s.prevSampleAt.Equal(t1) {
		t.Errorf("endpoint re-anchors to t1: %v", s.prevSampleAt)
	}
}

// Case 9: first blackout inside a re-activated window latches the terminal
// measurement but never writes the final peak record — on the tick path and
// on a direct call with an externally clean flag.
func TestConvergence_Reactivation_DirtyBlackoutNoPeak(t *testing.T) {
	mb := int64(1024 * 1024)
	tail := []int64{256 * 1024, 256 * 1024, 256 * 1024, 256 * 1024} // 4×256KB < 4×1MB minChunk

	t.Run("via_tick", func(t *testing.T) {
		gid := "sg_react_blackout"
		t0 := time.Now()
		ct, tracker, telemetry, recorder := reactivationFixture(t, gid, 4, t0.Add(-time.Minute))
		ct.tickAt(t0) // baseline with plain workers — no blackout yet

		telemetry.data[gid] = makeChunkWorkers(4, 2*1024*1024, tail)
		tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
		tracker.tasks[0].CompletedLength = 10*mb + 20*mb*5
		t1 := t0.Add(5 * time.Second)
		ct.tickAt(t1)

		ct.mu.Lock()
		s := ct.states[gid]
		ct.mu.Unlock()
		if !s.blackout {
			t.Fatal("blackout must latch even on a dirty tick")
		}
		if s.lastRawBps != 20*mb || !s.macroReady {
			t.Errorf("terminal measurement must publish: raw=%d ready=%v", s.lastRawBps, s.macroReady)
		}
		if !s.prevSampleAt.Equal(t1) {
			t.Errorf("endpoint re-anchored: %v", s.prevSampleAt)
		}
		if _, ok := recorder.records[gid]; ok {
			t.Error("dirty-window blackout must not write the terminal peak record")
		}
	})

	t.Run("direct_call_external_clean", func(t *testing.T) {
		gid := "sg_react_blackout_d"
		t0 := time.Now()
		ct, tracker, _, recorder := reactivationFixture(t, gid, 4, t0.Add(-time.Minute))

		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.prevCompleted = 10 * mb
		s.prevSampleAt = t0
		ct.mu.Unlock()

		tracker.tasks[0].ActivatedAt = t0.Add(2 * time.Second)
		tracker.tasks[0].CompletedLength = 10*mb + 20*mb*5
		t1 := t0.Add(5 * time.Second)
		// Internal detection runs before blackout even with affected=false.
		ct.processTaskStats(tracker.tasks[0], makeChunkWorkers(4, 2*1024*1024, tail), false, nil, nil, nil, t1)

		ct.mu.Lock()
		s = ct.states[gid]
		ct.mu.Unlock()
		if !s.blackout || s.lastRawBps != 20*mb {
			t.Fatalf("blackout must latch with terminal raw: blackout=%v raw=%d", s.blackout, s.lastRawBps)
		}
		if _, ok := recorder.records[gid]; ok {
			t.Error("internal detection must suppress the terminal peak record")
		}
	})
}

// Case 10: a same-window re-activation on a lingering 100%-complete row must
// not count as the domain's dirty membership change — the complete guard
// intercepts before the sample anchor can re-base, so an unguarded record
// would re-fire every tick and pin the N_max unlock confirmation at zero for
// as long as the row stays listed.
func TestConvergence_Reactivation_CompleteRowNotDirty(t *testing.T) {
	mb := int64(1024 * 1024)
	gidLive := "sg_react_live"
	gidDone := "sg_react_done"
	lk := limitKey("wan", "example.com")
	t0 := time.Now()

	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	tracker := &mockTracker{tasks: []TrackedTaskInfo{
		{GID: gidLive, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
			CompletedLength: 10 * mb, ActivatedAt: t0.Add(-time.Minute)},
		{GID: gidDone, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
			TotalLength: 100 * mb, CompletedLength: 90 * mb, ActivatedAt: t0.Add(-time.Minute)},
	}}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{
		gidLive: makeWorkers(3, 2*1024*1024),
		gidDone: makeWorkers(2, 2*1024*1024),
	}}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry,
	)
	ct.limits = NewServerLimitStore() // isolate from the shared store
	t.Cleanup(func() { ct.Stop() })

	ct.tickAt(t0) // baseline → prevActiveOccupancy non-nil, prevSampleAt=t0

	ct.limits.SetNMax(lk, 3)

	// gidDone reaches 100% but stays listed, then pause→resumes inside the
	// gap: its anchor is stuck at t0 because the complete guard never
	// re-bases it.
	tracker.tasks[0].CompletedLength = 10*mb + 10*mb*5
	tracker.tasks[1].ActivatedAt = t0.Add(2 * time.Second)
	tracker.tasks[1].CompletedLength = 100 * mb
	t1 := t0.Add(5 * time.Second)
	ct.tickAt(t1)

	ct.mu.Lock()
	counter := ct.domainUnlockTicks[lk]
	ct.mu.Unlock()
	if counter != 1 {
		t.Fatalf("lingering complete row must not pin the unlock counter: got %d, want 1", counter)
	}
	if _, has := ct.limits.GetNMax(lk); !has {
		t.Fatal("one clean confirm must not unlock yet")
	}

	// The row lingers at 100% — the next tick must keep confirming to unlock.
	tracker.tasks[0].CompletedLength = 10*mb + 10*mb*10
	ct.tickAt(t1.Add(5 * time.Second))
	if _, has := ct.limits.GetNMax(lk); has {
		t.Fatal("two clean confirms must unlock despite the lingering re-activated row")
	}
}
