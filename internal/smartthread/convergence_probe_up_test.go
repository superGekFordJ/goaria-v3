package smartthread

import (
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// ---------------------------------------------------------------------------
// Probe-Up tests (Task 10)
// ---------------------------------------------------------------------------

// setupProbeUpState creates a ticker and state primed for Probe-Up evaluation.
// bestEff and peakWorkers are set so the Probe-Up trigger can fire.
func setupProbeUpState(t *testing.T, gid string, bestEff int64, peakWorkers int) (*ConvergenceTicker, *mockTracker, *mockTelemetry, *monotonicMockPeakRecorder) {
	t.Helper()
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(8, 2*1024*1024),
		},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	recorder := &monotonicMockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 0)

	// Clear any leftover N_max from previous tests (global singleton).
	ct.limits.Clear(limitKey("wan", "example.com"))

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.bestEff = bestEff
	s.peakWorkers = peakWorkers
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	return ct, tracker, telemetry, recorder
}

// probeUpProcess is a helper that sets completed length and worker count, then
// calls processTask directly. Unlike the probe-down process helper, it does NOT
// pin N_max so Probe-Up can fire.
func probeUpProcess(ct *ConvergenceTicker, tracker *mockTracker, telemetry *mockTelemetry, gid string, completedLen int64, workerCount int) (pendingScale, bool) {
	tracker.tasks[0].CompletedLength = completedLen
	telemetry.data[gid] = makeWorkers(workerCount, 2*1024*1024)
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	return ct.processTask(tracker.tasks[0], false, nil)
}

// TestConvergence_ProbeUp_TriggersWhenStableAndEfficient verifies that
// phase==phaseStable, bestEff>0, newEff >= bestEff*0.95, and preheated triggers
// a +1 up-probe with phase set to phaseProbingUp and baseline saved.
func TestConvergence_ProbeUp_TriggersWhenStableAndEfficient(t *testing.T) {
	gid := "sg_probe_up_trigger"
	// bestEff = 1.25 MB/s per worker; 8 workers at 10 MB/s → newEff = 1.25 MB/s = bestEff
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)

	// rawBps = 10 MB/s = 10*1024*1024; delta over 5s = 50 MB
	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if !ok || ps.delta != 1 {
		t.Fatalf("expected probe-up +1, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseProbingUp {
		t.Fatalf("expected phase=phaseProbingUp, got %d", s.phase)
	}
	// Tolerance: dt may be 5s + small epsilon from real time.Now() calls.
	if s.probeUpBaseline < 10_480_000 || s.probeUpBaseline > 10*1024*1024 {
		t.Fatalf("expected probeUpBaseline≈10MB/s, got %d", s.probeUpBaseline)
	}
	if s.probeUpBaselineWorkers != 8 {
		t.Fatalf("expected probeUpBaselineWorkers=8, got %d", s.probeUpBaselineWorkers)
	}
}

// TestConvergence_ProbeUp_BlockedByBestEffZero verifies bestEff=0 blocks Probe-Up.
// With rawBps=0, D3 ratchet keeps bestEff=0 (0 > 0 is false), so the bestEff > 0
// gate in the Probe-Up trigger prevents premature up-probing during warmup.
func TestConvergence_ProbeUp_BlockedByBestEffZero(t *testing.T) {
	gid := "sg_probe_up_nobesteff"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 0, 8)

	// No progress → rawBps=0 → D3 keeps bestEff=0 → Probe-Up blocked
	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 10*1024*1024, 8)
	if ok && ps.delta > 0 {
		t.Fatalf("expected no probe-up when bestEff=0, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase == phaseProbingUp {
		t.Fatal("expected phase != phaseProbingUp when bestEff=0")
	}
}

// TestConvergence_ProbeUp_BlockedByNMax verifies currentWorkers >= nMax blocks Probe-Up
// while Probe-Down still fires (N_max only suppresses ScaleUp).
func TestConvergence_ProbeUp_BlockedByNMax(t *testing.T) {
	gid := "sg_probe_up_nmax"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)
	ct.limits.SetNMax(limitKey("wan", "example.com"), 8) // currentWorkers=8 >= nMax=8

	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if ok && ps.delta > 0 {
		t.Fatalf("expected no probe-up when currentWorkers >= nMax, got delta=%d", ps.delta)
	}
	// N_max only blocks Probe-Up; Probe-Down still fires when conditions met
	// (currentWorkers=8 > probeFloor, probeCooldown==0, peakWorkers>0).
	if !ok || ps.delta >= 0 {
		t.Fatalf("expected probe-down (delta<0) after N_max blocks probe-up, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase == phaseProbingUp {
		t.Fatal("expected phase != phaseProbingUp when N_max exceeded")
	}
	if s.phase != phaseSettling {
		t.Fatalf("expected phase=phaseSettling after probe-down, got %d", s.phase)
	}
}

// TestConvergence_ProbeUp_BlockedByRateLimit verifies rateLimited blocks Probe-Up.
func TestConvergence_ProbeUp_BlockedByRateLimit(t *testing.T) {
	gid := "sg_probe_up_ratelimit"
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	rateChecker := &mockRateChecker{limited: map[string]bool{gid: true}}
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, rateChecker, 0, 0)

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.bestEff = 1_310_720
	s.peakWorkers = 8
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if ok && ps.delta > 0 {
		t.Fatalf("expected no probe-up when rate-limited, got delta=%d", ps.delta)
	}
}

// TestConvergence_ProbeUp_SuccessContinuesChain verifies that after a successful
// Probe-Up evaluation (GainRatio >= 0.5), the code falls through to the Probe-Up
// trigger and issues another +1.
func TestConvergence_ProbeUp_SuccessContinuesChain(t *testing.T) {
	gid := "sg_probe_up_chain"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)

	// First tick: trigger initial Probe-Up
	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if !ok || ps.delta != 1 {
		t.Fatalf("expected initial probe-up +1, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	if s.phase != phaseProbingUp {
		t.Fatalf("expected phase=phaseProbingUp, got %d", s.phase)
	}
	baseline := s.probeUpBaseline
	baselineWorkers := s.probeUpBaselineWorkers
	ct.mu.Unlock()

	// Simulate +1 worker (8 → 9) with throughput gain >= 50% of expected.
	// ExpectedGain = 1/8 = 0.125 (12.5%). Need actualGain >= 0.0625 (6.25%).
	// rawBps was 10MB/s. Need rawBps >= 10MB * 1.0625 = 10.625MB/s. Use 12MB/s.
	// actualGain = (12-10)/10 = 0.20. gainRatio = 0.20/0.125 = 1.6 >= 0.5 → success.
	// delta = 12MB * 5 = 60MB. prevCompleted = 60MB. CompletedLength = 120MB.
	newCompleted := int64(120 * 1024 * 1024)
	tracker.tasks[0].CompletedLength = newCompleted
	telemetry.data[gid] = makeWorkers(9, 2*1024*1024)
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok = ct.processTask(tracker.tasks[0], false, nil)
	if !ok || ps.delta != 1 {
		t.Fatalf("expected chain probe-up +1 after success, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	// After success, phase should be phaseProbingUp again (fell through to trigger)
	if s.phase != phaseProbingUp {
		t.Fatalf("expected phase=phaseProbingUp after chain, got %d", s.phase)
	}
	// Baseline should be updated to the new rawBps
	if s.probeUpBaseline == baseline {
		t.Fatal("expected probeUpBaseline to be updated after chain probe-up")
	}
	// rawBps = (120MB - 60MB) / 5s = 12 MB/s = 12*1024*1024
	if s.probeUpBaseline != 12*1024*1024 {
		t.Fatalf("expected probeUpBaseline=12MB/s (current rawBps), got %d", s.probeUpBaseline)
	}
	if s.probeUpBaselineWorkers != 9 {
		t.Fatalf("expected probeUpBaselineWorkers=9, got %d", s.probeUpBaselineWorkers)
	}
	_ = baselineWorkers
}

// TestConvergence_ProbeUp_GainRatioZeroRawBps verifies that probeUpBaseline > 0
// but rawBps=0 gives GainRatio=0 < 0.5 → CeilingHit rebound -1.
func TestConvergence_ProbeUp_GainRatioZeroRawBps(t *testing.T) {
	gid := "sg_probe_up_zero"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)

	// First tick: trigger initial Probe-Up
	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if !ok || ps.delta != 1 {
		t.Fatalf("expected initial probe-up +1, got ok=%v delta=%d", ok, ps.delta)
	}

	// Now simulate rawBps=0 (no progress). GainRatio=0 < 0.5 → CeilingHit.
	// CompletedLength stays the same (no progress), delta=0 → rawBps=0.
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024 // same as before → delta=0
	telemetry.data[gid] = makeWorkers(9, 2*1024*1024)
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok = ct.processTask(tracker.tasks[0], false, nil)
	if !ok || ps.delta != -1 {
		t.Fatalf("expected ceiling-hit rebound -1, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseCeilingHit {
		t.Fatalf("expected phase=phaseCeilingHit, got %d", s.phase)
	}
	if s.ceilingMemory != 0 {
		t.Fatalf("expected ceilingMemory=0 (rawBps was 0), got %d", s.ceilingMemory)
	}
	if s.frozenCooldown != ceilingHitCooldownCycles {
		t.Fatalf("expected frozenCooldown=%d, got %d", ceilingHitCooldownCycles, s.frozenCooldown)
	}
}

// TestConvergence_ProbeUp_MacroProviderNoDeadlock verifies that wiring a
// Macro-like provider (which calls LastRawBps → c.mu.Lock) does not deadlock
// Probe-Up when GetGlobalPeak is populated (the path that actually invokes
// activeBandwidthProvider under the former lock).
func TestConvergence_ProbeUp_MacroProviderNoDeadlock(t *testing.T) {
	gid := "sg_probe_up_macro_lock"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)
	defer ct.Stop()

	// Force checkVAvailable into the provider path. D2 writes lastRawBps≈10MB/s
	// before Probe-Up, so Macro occupancy ≈10MB; plant peak/threads so
	// globalPeak-activeBw >= vThreadAvg still holds.
	speedstats.AddRecordV2(100*1024*1024, 10, 100*1024*1024, false, 50, "example.com", "wan", "testenv")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	var providerCalls atomic.Int32
	activeBandwidthProvider = func(scope, envKey string) int64 {
		providerCalls.Add(1)
		bps, ready := ct.LastRawBps(gid)
		if ready {
			return bps
		}
		return 0
	}
	ct.InjectMacroOccupancyForTest(gid, 1_000_000, true)

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.probeCooldown = probeIntervalCycles
	s.probeMomentum = false
	ct.mu.Unlock()

	done := make(chan struct{})
	var ps pendingScale
	var ok bool
	go func() {
		defer close(done)
		ps, ok = probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: Probe-Up held c.mu while Macro provider re-entered LastRawBps")
	}
	if providerCalls.Load() < 1 {
		t.Fatal("expected activeBandwidthProvider to be invoked (GetGlobalPeak path)")
	}
	if !ok || ps.delta != 1 {
		t.Fatalf("expected probe-up +1 under Macro provider, got ok=%v delta=%d calls=%d", ok, ps.delta, providerCalls.Load())
	}
}

// TestConvergence_ProbeUp_BlockedByVAvailable verifies that when
// globalPeak - activeBw < vThreadAvg, the Probe-Up trigger does not fire.
func TestConvergence_ProbeUp_BlockedByVAvailable(t *testing.T) {
	gid := "sg_probe_up_vavail"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)

	// Plant a global peak so GetGlobalPeak("wan", "testenv") = 10 MB/s.
	// GetRecentPeakByDomain("example.com", "wan", "testenv") = 10MB/1 = 10 MB/s → vThreadAvg = 10 MB/s.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	// activeBw = 10 MB/s → globalPeak - activeBw = 0 < vThreadAvg(10MB) → blocked.
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope, envKey string) int64 {
		return 10 * 1024 * 1024
	}

	// Prevent Probe-Down so the test isolates the V_available check.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.probeCooldown = probeIntervalCycles
	s.probeMomentum = false
	ct.mu.Unlock()

	// rawBps = 10 MB/s, 8 workers → newEff = 1.25 MB/s >= bestEff*0.95.
	// Without V_available block, Probe-Up would fire.
	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if ok && ps.delta > 0 {
		t.Fatalf("expected no probe-up when V_available insufficient, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase == phaseProbingUp {
		t.Fatal("expected phase != phaseProbingUp when V_available insufficient")
	}
}
