package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// ---------------------------------------------------------------------------
// CeilingHit tests (Task 11)
// ---------------------------------------------------------------------------

// setupCeilingHitState creates a ticker with state primed in phaseCeilingHit.
func setupCeilingHitState(t *testing.T, gid string, ceilingMemory int64, cooldown int) *ConvergenceTicker {
	t.Helper()
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseCeilingHit
	s.ceilingMemory = ceilingMemory
	s.ceilingHitCount = 0
	s.frozenCooldown = cooldown
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	return ct
}

// ceilingHitProcess calls processTask with the given completed length and worker count.
func ceilingHitProcess(ct *ConvergenceTicker, tracker *mockTracker, telemetry *mockTelemetry, gid string, completedLen int64, workerCount int) (pendingScale, bool) {
	tracker.tasks[0].CompletedLength = completedLen
	telemetry.data[gid] = makeWorkers(workerCount, 2*1024*1024)
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	return ct.processTask(tracker.tasks[0], false, nil, nil, nil, time.Now())
}

// TestConvergence_CeilingHit_SmartUnlock verifies consecutive 2 ticks with
// rawBps > ceilingMemory*1.05 → ceiling-unlocked, phase back to phaseStable.
func TestConvergence_CeilingHit_SmartUnlock(t *testing.T) {
	gid := "sg_ceiling_unlock"
	ceilingMem := int64(10 * 1024 * 1024) // 10 MB/s
	ct := setupCeilingHitState(t, gid, ceilingMem, ceilingHitCooldownCycles)
	tracker := ct.tracker.(*mockTracker)
	telemetry := ct.telemetry.(*mockTelemetry)

	// Tick 1: rawBps = 11 MB/s > 10*1.05 = 10.5 MB/s → ceilingHitCount=1 (not yet 2)
	// delta = 11MB * 5 = 55MB. prevCompleted=10MB. CompletedLength=65MB.
	ps, ok := ceilingHitProcess(ct, tracker, telemetry, gid, 65*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale during ceiling-hit, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.ceilingHitCount != 1 {
		t.Fatalf("expected ceilingHitCount=1 after tick 1, got %d", s.ceilingHitCount)
	}
	if s.phase != phaseCeilingHit {
		t.Fatalf("expected still phaseCeilingHit after tick 1, got %d", s.phase)
	}

	// Tick 2: rawBps = 12 MB/s > 10.5 MB/s → ceilingHitCount=2 → unlock
	// prevCompleted now = 65MB. delta = 12MB*5 = 60MB. CompletedLength = 125MB.
	ps, ok = ceilingHitProcess(ct, tracker, telemetry, gid, 125*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale on unlock, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Fatalf("expected phase=phaseStable after unlock, got %d", s.phase)
	}
	if s.ceilingMemory != 0 {
		t.Fatalf("expected ceilingMemory=0 after unlock, got %d", s.ceilingMemory)
	}
	if s.kneeFrozen {
		t.Error("expected kneeFrozen=false after ceiling unlock")
	}
}

// TestConvergence_CeilingHit_SingleFluctuationNoUnlock verifies a single
// high-rawBps tick followed by a normal tick does not unlock.
func TestConvergence_CeilingHit_SingleFluctuationNoUnlock(t *testing.T) {
	gid := "sg_ceiling_fluctuation"
	ceilingMem := int64(10 * 1024 * 1024)
	ct := setupCeilingHitState(t, gid, ceilingMem, ceilingHitCooldownCycles)
	tracker := ct.tracker.(*mockTracker)
	telemetry := ct.telemetry.(*mockTelemetry)

	// Tick 1: rawBps = 12 MB/s > 10.5 → ceilingHitCount=1
	ps, ok := ceilingHitProcess(ct, tracker, telemetry, gid, 70*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.ceilingHitCount != 1 {
		t.Fatalf("expected ceilingHitCount=1, got %d", s.ceilingHitCount)
	}

	// Tick 2: rawBps = 9 MB/s < 10.5 → ceilingHitCount resets to 0
	ps, ok = ceilingHitProcess(ct, tracker, telemetry, gid, 115*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.ceilingHitCount != 0 {
		t.Fatalf("expected ceilingHitCount=0 after fluctuation, got %d", s.ceilingHitCount)
	}
	if s.phase != phaseCeilingHit {
		t.Fatalf("expected still phaseCeilingHit, got %d", s.phase)
	}
}

// TestConvergence_CeilingHit_CooldownExpired verifies frozenCooldown countdown
// reaches 0 without speedup → ceiling-cooldown-expired, phase back to phaseStable.
func TestConvergence_CeilingHit_CooldownExpired(t *testing.T) {
	gid := "sg_ceiling_cooldown"
	ceilingMem := int64(10 * 1024 * 1024)
	ct := setupCeilingHitState(t, gid, ceilingMem, 1) // cooldown=1 → expires next tick
	tracker := ct.tracker.(*mockTracker)
	telemetry := ct.telemetry.(*mockTelemetry)

	// Tick: rawBps = 10 MB/s (not > 10.5) → no unlock. cooldown 1→0 → expired.
	ps, ok := ceilingHitProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale on cooldown expiry, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Fatalf("expected phase=phaseStable after cooldown expiry, got %d", s.phase)
	}
	if s.ceilingMemory != 0 {
		t.Fatalf("expected ceilingMemory=0 after cooldown expiry, got %d", s.ceilingMemory)
	}
}

// TestConvergence_CeilingHit_EarlyReturnNoProbe verifies phase==phaseCeilingHit
// causes processTask to early-return without triggering Probe-Up or Probe-Down.
func TestConvergence_CeilingHit_EarlyReturnNoProbe(t *testing.T) {
	gid := "sg_ceiling_earlyreturn"
	ceilingMem := int64(10 * 1024 * 1024)
	ct := setupCeilingHitState(t, gid, ceilingMem, ceilingHitCooldownCycles)
	tracker := ct.tracker.(*mockTracker)
	telemetry := ct.telemetry.(*mockTelemetry)

	// Even with high efficiency (would trigger Probe-Up if stable), CeilingHit
	// early-returns. Set bestEff below the incoming newEff so the D3 ratchet
	// update is observable, and peakWorkers > 0 to make Probe-Up conditions
	// otherwise satisfiable.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.bestEff = 500_000 // below newEff so D3 ratchet raises it
	s.peakWorkers = 8
	ct.mu.Unlock()

	// rawBps = 10 MB/s, 8 workers → newEff = 1.25 MB/s >= bestEff*0.95
	ps, ok := ceilingHitProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale during ceiling-hit early return, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseCeilingHit {
		t.Fatalf("expected phase=phaseCeilingHit (early return preserves phase), got %d", s.phase)
	}
	if s.frozenCooldown != ceilingHitCooldownCycles-1 {
		t.Fatalf("expected frozenCooldown=%d after one tick, got %d", ceilingHitCooldownCycles-1, s.frozenCooldown)
	}
	// D3 ratchet must run before the early-return: bestEff raised to ~newEff.
	// Tolerance: dt may be 5s + small epsilon from real time.Now() calls.
	if s.bestEff < 1_310_000 || s.bestEff > 1_310_720 {
		t.Fatalf("expected bestEff≈1310720 (D3 ratchet ran before early-return), got %d", s.bestEff)
	}
}

// TestConvergence_CeilingHit_ReboundRefusedByEngine verifies that when a CeilingHit
// rebound (-1) is refused by the engine (ScaleWorkers returns 0), the tick() no-op
// path preserves phaseCeilingHit and the cooldown instead of resetting to phaseStable.
func TestConvergence_CeilingHit_ReboundRefusedByEngine(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_ceiling_rebound_refused"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(9, 2*1024*1024),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Set up state: in phaseProbingUp with baseline, simulating a +1 already done.
	// rawBps will be low so GainRatio < 0.5 → CeilingHit rebound -1.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseProbingUp
	s.probeUpBaseline = 10 * 1024 * 1024 // 10 MB/s before +1
	s.probeUpBaselineWorkers = 8
	s.bestEff = 1_310_720
	s.peakWorkers = 8
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// rawBps = 10.1MB/5s ≈ 2MB/s. actualGain = (2-10)/10 < 0 → 0.
	// gainRatio = 0 < 0.5 → CeilingHit, rebound -1.
	// ScaleWorkers returns 0 (nil pool) → tick() no-op path must preserve phaseCeilingHit.
	tracker.tasks[0].CompletedLength = 110 * 1024 * 1024 // +10MB in 5s = 2MB/s
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseCeilingHit {
		t.Fatalf("expected phase=phaseCeilingHit after rebound refused by engine, got %d", s.phase)
	}
	if s.frozenCooldown != ceilingHitCooldownCycles {
		t.Fatalf("expected frozenCooldown=%d (preserved), got %d", ceilingHitCooldownCycles, s.frozenCooldown)
	}
}

// TestConvergence_LocalWindow_LockEvidenceAndCooldown proves lock evidence is
// never erased by a dirty tick: ceiling/floor/frozen phases keep memory,
// smart-unlock counters freeze, cooldowns decrement exactly once saturated
// at zero — a cooldown reaching zero on a dirty tick does not unlock; the
// next clean tick performs the normal expiry transition.
func TestConvergence_LocalWindow_LockEvidenceAndCooldown(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	mb := int64(1024 * 1024)
	gid := "sg_lock_dirty"
	// fixture primes state under lock, then runs one processTask call.
	prime := func(mut func(*convergenceState)) (*ConvergenceTicker, TrackedTaskInfo, time.Time) {
		tracker := &mockTracker{tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com",
				CompletedLength: 60 * mb,
			},
		}}
		telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(4, 2*1024*1024),
		}}
		ct := newTestConvergenceTicker(
			rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
			tracker, telemetry,
		)
		t.Cleanup(func() { ct.Stop() })
		now := time.Now()
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.prevCompleted = 50 * mb
		s.prevSampleAt = now.Add(-5 * time.Second)
		s.macroReady = true
		s.lastRawBps = 10 * mb
		s.sustainCount = 5
		s.peakSpeed = 30 * mb
		s.peakWorkers = 8
		s.bestEff = 7 * mb
		if mut != nil {
			mut(s)
		}
		ct.mu.Unlock()
		return ct, tracker.tasks[0], now
	}
	load := func(ct *ConvergenceTicker) *convergenceState {
		ct.mu.Lock()
		defer ct.mu.Unlock()
		return ct.states[gid]
	}

	// Each locked phase: dirty tick freezes counters, decrements cooldowns
	// once, keeps memory + phase.
	for _, tc := range []struct {
		name  string
		mut   func(*convergenceState)
		check func(*testing.T, *convergenceState)
	}{
		{"ceiling_hit", func(s *convergenceState) {
			s.phase = phaseCeilingHit
			s.ceilingMemory = 9 * mb
			s.ceilingHitCount = 1
			s.kneeFrozen = true
			s.frozenCooldown = 5
			s.probeCooldown = 3
		}, func(t *testing.T, s *convergenceState) {
			if s.phase != phaseCeilingHit || s.ceilingMemory != 9*mb || s.ceilingHitCount != 1 {
				t.Errorf("ceiling evidence frozen: phase=%d mem=%d count=%d", s.phase, s.ceilingMemory, s.ceilingHitCount)
			}
			if !s.kneeFrozen || s.frozenCooldown != 4 || s.probeCooldown != 2 {
				t.Errorf("cooldowns must tick once, lock kept: knee=%v fc=%d pc=%d", s.kneeFrozen, s.frozenCooldown, s.probeCooldown)
			}
		}},
		{"floor_hit", func(s *convergenceState) {
			s.phase = phaseFloorHit
			s.floorMemory = 11 * mb
			s.floorHitCount = 1
			s.kneeFrozen = true
			s.frozenCooldown = 5
			s.probeCooldown = 1
		}, func(t *testing.T, s *convergenceState) {
			if s.phase != phaseFloorHit || s.floorMemory != 11*mb || s.floorHitCount != 1 {
				t.Errorf("floor evidence frozen: phase=%d mem=%d count=%d", s.phase, s.floorMemory, s.floorHitCount)
			}
			if !s.kneeFrozen || s.frozenCooldown != 4 || s.probeCooldown != 0 {
				t.Errorf("cooldowns must tick once saturated at 0: knee=%v fc=%d pc=%d", s.kneeFrozen, s.frozenCooldown, s.probeCooldown)
			}
		}},
		{"frozen", func(s *convergenceState) {
			s.phase = phaseFrozen
			s.kneeFrozen = true
			s.probeMomentum = true
			s.frozenCooldown = 5
			s.probeCooldown = 0
		}, func(t *testing.T, s *convergenceState) {
			if s.phase != phaseFrozen || !s.kneeFrozen || !s.probeMomentum {
				t.Errorf("frozen lock kept: phase=%d knee=%v momentum=%v", s.phase, s.kneeFrozen, s.probeMomentum)
			}
			if s.frozenCooldown != 4 || s.probeCooldown != 0 {
				t.Errorf("cooldowns must tick once: fc=%d pc=%d", s.frozenCooldown, s.probeCooldown)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct, task, now := prime(tc.mut)
			ct.processTask(task, true, nil, nil, nil, now)
			s := load(ct)
			tc.check(t, s)
			// Common to all locked phases: memory preserved, D2 published.
			if s.sustainCount != 5 || s.peakSpeed != 30*mb || s.bestEff != 7*mb {
				t.Errorf("memory must survive dirty tick: sustain=%d peak=%d bestEff=%d", s.sustainCount, s.peakSpeed, s.bestEff)
			}
			if s.lastRawBps != 2*mb || !s.macroReady {
				t.Errorf("D2 must publish on dirty tick: raw=%d ready=%v", s.lastRawBps, s.macroReady)
			}
		})
	}

	// A dirty tick that lands the cooldown on zero does not unlock — the
	// transition belongs to the next clean observation.
	t.Run("dirty_to_zero_no_unlock_then_clean_expiry", func(t *testing.T) {
		ct, task, now := prime(func(s *convergenceState) {
			s.phase = phaseFrozen
			s.kneeFrozen = true
			s.frozenCooldown = 1
		})
		ct.processTask(task, true, nil, nil, nil, now)
		s := load(ct)
		if s.frozenCooldown != 0 || s.phase != phaseFrozen || !s.kneeFrozen {
			t.Fatalf("dirty tick must decrement without unlocking: fc=%d phase=%d knee=%v",
				s.frozenCooldown, s.phase, s.kneeFrozen)
		}
		ct.processTask(task, false, nil, nil, nil, now.Add(5*time.Second))
		s = load(ct)
		if s.phase != phaseStable || s.kneeFrozen {
			t.Fatalf("clean tick must run normal expiry: phase=%d knee=%v", s.phase, s.kneeFrozen)
		}
		if s.probeCooldown != probeIntervalCycles {
			t.Fatalf("expiry must arm probe cooldown: pc=%d want %d", s.probeCooldown, probeIntervalCycles)
		}
	})

	// Clean control: identical ceiling fixture, unaffected → counters advance
	// and the second consecutive speed-up confirm unlocks on schedule.
	t.Run("clean_tick_advances_counters", func(t *testing.T) {
		ct, task, now := prime(func(s *convergenceState) {
			s.phase = phaseCeilingHit
			s.ceilingMemory = 9 * mb
			s.ceilingHitCount = 1
			s.kneeFrozen = true
			s.frozenCooldown = 5
		})
		// raw=10MB > 9MB*1.05 → second consecutive speed-up confirm → unlock.
		task.CompletedLength = 50*mb + 50*mb
		ct.processTask(task, false, nil, nil, nil, now)
		s := load(ct)
		if s.phase != phaseStable || s.ceilingHitCount != 0 || s.kneeFrozen {
			t.Fatalf("clean speed-up must unlock on schedule: phase=%d count=%d knee=%v",
				s.phase, s.ceilingHitCount, s.kneeFrozen)
		}
	})
}
