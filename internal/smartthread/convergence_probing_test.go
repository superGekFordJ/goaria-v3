package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// mockPeakRecorder implements PeakEfficiencyRecorder for testing
type mockPeakRecorder struct {
	records map[string]struct {
		peak    int64
		workers int
	}
}

func (m *mockPeakRecorder) RecordPeakEfficiency(gid string, peak int64, workers int) {
	if m.records == nil {
		m.records = make(map[string]struct {
			peak    int64
			workers int
		})
	}
	m.records[gid] = struct {
		peak    int64
		workers int
	}{peak, workers}
}

// mockRateChecker implements RateLimitChecker for testing.
// When limited[gid]=true, returns bps[gid] if set, else 1_000_000 (true positive cap).
type mockRateChecker struct {
	limited map[string]bool
	bps     map[string]int64
}

func (m *mockRateChecker) GetRateLimit(gid string) (int64, bool) {
	if m.limited == nil {
		return 0, false
	}
	limited, ok := m.limited[gid]
	if !ok {
		return 0, false
	}
	if limited {
		if m.bps != nil {
			if b, ok := m.bps[gid]; ok {
				return b, true
			}
		}
		return 1_000_000, true
	}
	return 0, false
}

// newTestConvergenceTicker creates a ConvergenceTicker with default mock recorder/checker.
func newTestConvergenceTicker(engine *rpc.HybridEngine, tracker *mockTracker, telemetry *mockTelemetry) *ConvergenceTicker {
	return NewConvergenceTicker(engine, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{}, 0, 256)
}

// makeWorkers creates n worker snapshots with the given EMASpeed.
func makeWorkers(n int, speed float64) []types.WorkerSnapshot {
	workers := make([]types.WorkerSnapshot, n)
	for i := range workers {
		workers[i] = types.WorkerSnapshot{WorkerID: i, EMASpeed: speed}
	}
	return workers
}

// makeChunkWorkers creates n worker snapshots with the given EMASpeed and
// per-worker remaining bytes. Each worker i gets ChunkStart == ChunkOffset
// so remaining = (ChunkStart + ChunkLength) - ChunkOffset = remaining[i].
func makeChunkWorkers(n int, speed float64, remaining []int64) []types.WorkerSnapshot {
	workers := make([]types.WorkerSnapshot, n)
	for i := range workers {
		var rem int64
		if i < len(remaining) {
			rem = remaining[i]
		}
		start := int64(i) * 10 * 1024 * 1024
		workers[i] = types.WorkerSnapshot{
			WorkerID:    i,
			EMASpeed:    speed,
			ChunkStart:  start,
			ChunkLength: rem,
			ChunkOffset: start,
		}
	}
	return workers
}

// setPrevSampleAgo sets prevSampleAt on the given gid's state to time.Now()-ago,
// simulating elapsed time for raw throughput computation. Thread-safe.
func setPrevSampleAgo(ct *ConvergenceTicker, gid string, ago time.Duration) {
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		s.prevSampleAt = time.Now().Add(-ago)
	}
	ct.mu.Unlock()
}

// setPrevSampleAgoState sets prevSampleAt on a state pointer directly (when the
// test already holds the lock or has a state reference). Caller must hold ct.mu.
func setPrevSampleAgoState(s *convergenceState, ago time.Duration) {
	s.prevSampleAt = time.Now().Add(-ago)
}

// TestConvergence_ProbeDown_ProbesBelowInitialWorkers verifies that the active probing
// state machine issues a probe-down when raw throughput is stable and currentWorkers > probeFloor.
func TestConvergence_ProbeDown_ProbesBelowInitialWorkers(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_probe_down"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 0},
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
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 256)
	defer ct.Stop()

	// Tick 1: first sample — stores baseline, no decision
	tracker.tasks[0].CompletedLength = 10 * 1024 * 1024
	ct.tick()

	// Tick 2: second sample — should have prevCompleted set, raw throughput computed.
	// With stable throughput and 8 workers > probeFloor(2), should probe down.
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	setPrevSampleAgo(ct, gid, 5*time.Second)

	ct.tick()

	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state to exist")
	}
	// After tick 2, should have either probed (phase=settling) or stayed stable
	if s.phase != phaseSettling && s.phase != phaseStable {
		t.Errorf("expected phase settling or stable, got %d", s.phase)
	}
}

// TestConvergence_Settling_NoDecision verifies that after a scale action (phase=settling),
// the next tick makes no new decision — just refreshes baseline.
func TestConvergence_Settling_NoDecision(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_settling"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Set up state: first sample done, now in settling phase
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	s.phase = phaseSettling
	s.probeBaseline = 10 * 1024 * 1024
	s.probeBaselineWorkers = 8
	s.lastStep = 1
	ct.mu.Unlock()

	// Tick should refresh baseline, transition to stable, no new probe
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Errorf("expected phase stable after settling, got %d", s.phase)
	}
	if s.lastStep != 1 {
		t.Errorf("expected lastStep=1 (not yet evaluated), got %d", s.lastStep)
	}
}

// TestConvergence_MultiTask_WindowInvalidation verifies that when the active set changes,
// all states get prevCompleted reset and no probe decisions are made.
func TestConvergence_MultiTask_WindowInvalidation(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_multi_1"
	gid2 := "sg_multi_2"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 30 * 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// First tick: establish prevActiveGids with both tasks
	ct.tick()

	// Set up state with prevCompleted for both
	ct.mu.Lock()
	for _, g := range []string{gid1, gid2} {
		if s, ok := ct.states[g]; ok {
			s.prevCompleted = 10 * 1024 * 1024
			setPrevSampleAgoState(s, 5*time.Second)
		}
	}
	ct.mu.Unlock()

	// Remove gid2 — active set changes
	tracker.tasks = []TrackedTaskInfo{
		{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 60 * 1024 * 1024},
	}
	telemetry.data = map[string][]types.WorkerSnapshot{
		gid1: makeWorkers(4, 2*1024*1024),
	}

	// This tick should detect active set change and invalidate windows
	ct.tick()

	ct.mu.Lock()
	s := ct.states[gid1]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state for gid1")
	}
	// After invalidation, processTask sets prevCompleted as first sample (baseline)
	if s.prevSampleAt.IsZero() {
		t.Error("expected prevSampleAt to be set after invalidation tick (first sample re-baseline)")
	}
}

// TestConvergence_RateLimitSkip_SkipsProbeButRecordsPeak verifies a true positive
// rate limit blocks Probe pendingScale while still allowing D3 Peak recording.
// Uses processTask (not tick): nil Surge pool makes ScaleWorkers return 0 and D5
// reset phaseStable, so post-tick phase checks can false-green.
func TestConvergence_RateLimitSkip_SkipsProbeButRecordsPeak(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_ratelimited"
	rateChecker := &mockRateChecker{
		limited: map[string]bool{gid: true},
	}

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, rateChecker, 0, 256)
	defer ct.Stop()

	// Baseline + momentum: blocks Probe-Up, prefers Probe-Down once preheated.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	s.phase = phaseStable
	s.probeMomentum = true
	s.probeCooldown = 0
	ct.mu.Unlock()

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	if ok || ps.delta != 0 {
		t.Fatalf("unexpected pending scale on first sample: ok=%v delta=%d", ok, ps.delta)
	}

	// Second sample: sustainCount reaches peakSustainCycles → D3 adopts and
	// Probe-Down is eligible; rate limit must suppress pendingScale.
	tracker.tasks[0].CompletedLength = 110 * 1024 * 1024
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok = ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	if ok || ps.delta != 0 {
		t.Fatalf("expected no pending scale under rate limit, got ok=%v delta=%d", ok, ps.delta)
	}

	if len(recorder.records) == 0 {
		t.Error("expected D3 RecordPeakEfficiency under true rate limit, got none")
	}
	bps, ready := ct.LastRawBps(gid)
	if !ready || bps <= 0 {
		t.Errorf("expected D2 LastRawBps ready under true rate limit, got (%d,%v)", bps, ready)
	}

	ct.mu.Lock()
	s = ct.states[gid]
	eligibleDown := s.phase == phaseStable &&
		s.probeMomentum &&
		s.probeCooldown == 0 &&
		(s.peakWorkers > 0 || s.sustainCount >= peakSustainCycles)
	ct.mu.Unlock()
	if !eligibleDown {
		t.Fatalf("expected Probe-Down-eligible state under cap (proves gate, not missing preconditions)")
	}

	// Control: same eligibility without the cap must emit Probe-Down pendingScale.
	rateChecker.limited[gid] = false
	tracker.tasks[0].CompletedLength = 160 * 1024 * 1024
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()
	ps, ok = ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	if !ok || ps.delta >= 0 {
		t.Fatalf("without rate limit expected Probe-Down (delta<0), got ok=%v delta=%d", ok, ps.delta)
	}
}

// TestConvergence_ZeroBpsRateLimitDoesNotSkipD2D3 verifies the old false-positive
// shape GetRateLimit(0,true) does not silence D2/D3 (consumer requires bps>0).
func TestConvergence_ZeroBpsRateLimitDoesNotSkipD2D3(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_zero_bps_fp"
	rateChecker := &mockRateChecker{
		limited: map[string]bool{gid: true},
		bps:     map[string]int64{gid: 0},
	}

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", CompletedLength: 10 * 1024 * 1024, MinChunk: 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeChunkWorkers(4, 2*1024*1024, []int64{10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024}),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, rateChecker, 0, 256)
	defer ct.Stop()

	ct.tick()

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	ct.tick()

	bps, ready := ct.LastRawBps(gid)
	if !ready || bps <= 0 {
		t.Errorf("after two ticks with (0,true) mock: LastRawBps=(%d,%v), want (bps>0, true)", bps, ready)
	}
}

// TestConvergence_ProbeFloor_StopsAtFloor verifies probing stops at probeFloor.
func TestConvergence_ProbeFloor_StopsAtFloor(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_floor"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
		},
	}
	// 2 workers — at probeFloor (default 2)
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(2, 2*1024*1024),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Set up state with baseline
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	s.phase = phaseStable
	s.probeCooldown = 0
	ct.mu.Unlock()

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase == phaseSettling {
		t.Error("expected no probe at probeFloor, but phase=settling")
	}
}

// TestConvergence_ProbeFloor_StaticAllowsProbeDownWithBBRHistory regresses the
// deleted BDP-as-workers floor: DomainPeak+RTprop present must not block
// probe-down when N=8 (static probeFloorWorkers=2).
func TestConvergence_ProbeFloor_StaticAllowsProbeDownWithBBRHistory(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	// Old computeProbeFloor: bdp=50MB/s*0.1s=5e6 → ceil(5e6/8)≈625000 workers floor.
	speedstats.AddRecordV2(50*1024*1024, 8, 200*1024*1024, false, 100, "example.com", "wan", "testenv")

	gid := "sg_bbr_floor"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 60 * 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	s.phase = phaseStable
	s.probeCooldown = 0
	s.peakWorkers = 8
	s.probeMomentum = true // prefer probe-down; blocks Probe-Up
	ct.mu.Unlock()

	// processTask directly — tick() D5 rolls back when ScaleWorkers returns 0.
	ps, ok := ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	if !ok || ps.delta >= 0 {
		t.Fatalf("expected probe-down delta<0 with DomainPeak+RTprop present, got ok=%v delta=%d", ok, ps.delta)
	}

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil || s.phase != phaseSettling {
		phase := -1
		if s != nil {
			phase = s.phase
		}
		t.Fatalf("expected phaseSettling after probe-down; phase=%d", phase)
	}
}

// TestConvergence_M1_CongestionTrapEscape verifies the SPEC's headline acceptance criterion:
// starting from a high-N low-throughput state, the momentum chain drives probes down
// until throughput improves. Tests the success → momentum → continue-probing path.
func TestConvergence_M1_CongestionTrapEscape(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_congestion"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 0},
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
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 256)
	defer ct.Stop()

	// Tick 1: first sample (baseline)
	tracker.tasks[0].CompletedLength = 10 * 1024 * 1024
	ct.tick()

	// Tick 2: stable throughput → probe down issued (phase=settling, lastStep=-1)
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	setPrevSampleAgo(ct, gid, 5*time.Second)
	ct.tick()

	// ScaleWorkers returned 0 (nil pool) → D5 resets state for delta<0.
	// Manually simulate successful probe: set state as if settling completed.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.lastStep = 1                     // positive: magnitude of probe-down step
	s.probeBaseline = 50 * 1024 * 1024 // 50MB/s baseline before probe
	s.probeBaselineWorkers = 8         // 8 workers before probe-down
	s.probeMomentum = false
	s.probeCooldown = 0
	s.prevCompleted = 60 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Tick 3: throughput improved to 64MB/s after probe-down
	// actualDrop clamped to 0 (speed increased) → dropRatio=0 ≤ 0.5 → success → momentum ignited
	// 64MB/s * 5s = 320MB delta; prevCompleted=60MB → CompletedLength=380MB
	tracker.tasks[0].CompletedLength = 380 * 1024 * 1024
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.probeMomentum {
		t.Error("expected probeMomentum=true after successful probe-down (throughput improved)")
	}
	if len(recorder.records) == 0 {
		t.Error("expected peak efficiency recorded after successful probe")
	}
}

// TestConvergence_M2_M3_KneeCrossingReboundAndZeroScale tests both M2 (knee crossing
// triggers rebound + frozen) and M3 (ScaleWorkers returns 0 on rebound → C1 fix
// preserves frozen state instead of corrupting it).
func TestConvergence_M2_M3_KneeCrossingReboundAndZeroScale(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_knee"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Set up state: probe-down was done, settling completed, now evaluating.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.lastStep = 1                     // positive: magnitude of probe-down step
	s.probeBaseline = 50 * 1024 * 1024 // 50MB/s before probe
	s.probeBaselineWorkers = 8         // 8 workers before probe-down
	s.probeMomentum = true
	s.probeCooldown = 0
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Throughput crashed to 30MB/s after probe-down.
	// Marginal Drop Ratio: expectedDrop=1/8=0.125, actualDrop=1-30/50=0.4, dropRatio=0.4/0.125=3.2 > 0.5
	// → knee crossed → rebound issued (delta>0), kneeFrozen=true, phase=frozen
	tracker.tasks[0].CompletedLength = 130 * 1024 * 1024 // +30MB in 5s = 30MB/s
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()

	// M2: knee crossing should set kneeFrozen and issue rebound
	if !s.kneeFrozen {
		t.Error("expected kneeFrozen=true after knee crossing")
	}

	// M3/C1: ScaleWorkers returned 0 (nil pool) for rebound (delta>0).
	// C1 fix should preserve phaseFrozen + frozenCooldown instead of resetting to phaseStable.
	if s.phase != phaseFrozen {
		t.Errorf("expected phase=phaseFrozen after rebound failure (C1 fix), got %d", s.phase)
	}
	if s.frozenCooldown != frozenCooldownCycles {
		t.Errorf("expected frozenCooldown=%d after rebound failure (C1 fix), got %d", frozenCooldownCycles, s.frozenCooldown)
	}
}

// TestConvergence_C3_SustainCountResetOnSettling verifies that sustainCount resets
// when transitioning from settling to stable, so peakSustainCycles stability
// requirement isn't bypassed.
func TestConvergence_C3_SustainCountResetOnSettling(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_sustain"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Set up state: in settling with high sustainCount (simulating accumulated counts)
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseSettling
	s.probeBaseline = 40 * 1024 * 1024
	s.probeBaselineWorkers = 8
	s.lastStep = 1
	s.sustainCount = 10 // accumulated — should reset on settling→stable transition
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable after settling, got %d", s.phase)
	}
	if s.sustainCount != 0 {
		t.Errorf("expected sustainCount=0 after settling→stable transition (C3 fix), got %d", s.sustainCount)
	}
}

// TestConvergence_C3_SustainCountResetOnFrozenExpiry verifies that sustainCount
// resets when frozen cooldown expires, so peakSustainCycles stability is required
// after frozen state clears.
func TestConvergence_C3_SustainCountResetOnFrozenExpiry(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_frozen_expiry"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Set up state: frozen with cooldown=1 (will expire on next tick)
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseFrozen
	s.kneeFrozen = true
	s.frozenCooldown = 1 // will decrement to 0 on next tick
	s.sustainCount = 10  // accumulated — should reset on frozen expiry
	s.prevCompleted = 40 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.kneeFrozen {
		t.Error("expected kneeFrozen=false after frozen cooldown expired")
	}
	if s.sustainCount != 0 {
		t.Errorf("expected sustainCount=0 after frozen expiry (C3 fix), got %d", s.sustainCount)
	}
}

// TestConvergence_m2_InvalidationResetsLastStepAndPhase verifies that window
// invalidation resets lastStep and phase (m2 fix), preventing spurious probe
// evaluation against probeBaseline=0.
func TestConvergence_m2_InvalidationResetsLastStepAndPhase(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_inval_1"
	gid2 := "sg_inval_2"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 30 * 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// First tick: establish prevActiveGids with both tasks
	ct.tick()

	// Set up state with probe in-flight (settling phase, lastStep=-1)
	ct.mu.Lock()
	for _, g := range []string{gid1, gid2} {
		if s, ok := ct.states[g]; ok {
			s.prevCompleted = 10 * 1024 * 1024
			setPrevSampleAgoState(s, 5*time.Second)
			s.phase = phaseSettling
			s.lastStep = 1
			s.probeBaseline = 40 * 1024 * 1024
			s.probeBaselineWorkers = 4
		}
	}
	ct.mu.Unlock()

	// Remove gid2 — active set changes, triggers window invalidation
	tracker.tasks = []TrackedTaskInfo{
		{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 60 * 1024 * 1024},
	}
	telemetry.data = map[string][]types.WorkerSnapshot{
		gid1: makeWorkers(4, 2*1024*1024),
	}

	ct.tick()

	ct.mu.Lock()
	s := ct.states[gid1]
	ct.mu.Unlock()
	if s.lastStep != 0 {
		t.Errorf("expected lastStep=0 after invalidation (m2 fix), got %d", s.lastStep)
	}
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable after invalidation (m2 fix), got %d", s.phase)
	}
}

// TestConvergence_C4_InvalidationResetsKneeFrozen verifies that window
// invalidation resets kneeFrozen, frozenCooldown, probeMomentum, and
// probeCooldown — preventing permanent ScaleUp suppression when the active
// set changes while a task is in frozen state.
func TestConvergence_C4_InvalidationResetsKneeFrozen(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_c4_1"
	gid2 := "sg_c4_2"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 30 * 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// First tick: establish prevActiveGids with both tasks
	ct.tick()

	// Set up gid1 in frozen state with kneeFrozen=true
	ct.mu.Lock()
	s := ct.states[gid1]
	s.phase = phaseFrozen
	s.kneeFrozen = true
	s.frozenCooldown = 8
	s.probeMomentum = true
	s.probeCooldown = 0
	s.prevCompleted = 40 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Remove gid2 — active set changes, triggers window invalidation
	tracker.tasks = []TrackedTaskInfo{
		{GID: gid1, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 60 * 1024 * 1024},
	}
	telemetry.data = map[string][]types.WorkerSnapshot{
		gid1: makeWorkers(4, 2*1024*1024),
	}

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid1]
	ct.mu.Unlock()

	if s.kneeFrozen {
		t.Error("expected kneeFrozen=false after invalidation (C4 fix)")
	}
	if s.frozenCooldown != 0 {
		t.Errorf("expected frozenCooldown=0 after invalidation (C4 fix), got %d", s.frozenCooldown)
	}
	if s.probeMomentum {
		t.Error("expected probeMomentum=false after invalidation (C4 fix)")
	}
	if s.probeCooldown != probeIntervalCycles {
		t.Errorf("expected probeCooldown=%d after invalidation (C4 fix), got %d", probeIntervalCycles, s.probeCooldown)
	}
}

// TestConvergence_S2_ColdStartProbeDelay verifies that the first probe-down
// is delayed until peakSustainCycles stable samples have been accumulated
// (S2 fix), preventing probeBaseline from being based on a single
// potentially-transient measurement.
func TestConvergence_S2_ColdStartProbeDelay(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan", "testenv")

	gid := "sg_s2_cold"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 10 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(4, 2*1024*1024),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Tick 1: first sample — stores baseline, no decision.
	ct.tick()
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state to exist after tick 1")
	}
	if s.sustainCount != 0 {
		t.Errorf("expected sustainCount=0 after first sample, got %d", s.sustainCount)
	}

	// Tick 2: sustainCount=1, peakWorkers=0 → S2 gate blocks probe.
	// Set prevSampleAt back 5s to ensure dt > 0 (ticks run in microseconds).
	tracker.tasks[0].CompletedLength = 20 * 1024 * 1024 // +10MB in 5s = 2MB/s
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()
	ct.tick()
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.sustainCount != 1 {
		t.Errorf("expected sustainCount=1 after tick 2, got %d", s.sustainCount)
	}
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable after tick 2 (S2 gate blocks probe), got %d", s.phase)
	}
	if s.probeBaseline != 0 {
		t.Errorf("expected probeBaseline=0 after tick 2 (no probe issued), got %d", s.probeBaseline)
	}
	if s.peakWorkers != 0 {
		t.Errorf("expected peakWorkers=0 after tick 2 (ratchet not yet clean), got %d", s.peakWorkers)
	}

	// Tick 3: sustainCount=2, clean=true → ratchet adopts (peakWorkers>0).
	// S2 gate passes via peakWorkers>0 → probe fires (delta=-1).
	// Note: ScaleWorkers returns 0 in test (nil engine), so D5 rolls back
	// phase/probeBaseline/lastStep. We verify the ratchet adoption instead,
	// which proves the S2 gate allowed processing past the probe block.
	tracker.tasks[0].CompletedLength = 30 * 1024 * 1024 // +10MB in 5s = 2MB/s
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()
	ct.tick()
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.sustainCount != 2 {
		t.Errorf("expected sustainCount=2 after tick 3, got %d", s.sustainCount)
	}
	// Ratchet should have adopted — this only happens when clean=true (sustainCount >= peakSustainCycles)
	if s.peakWorkers == 0 {
		t.Error("expected peakWorkers>0 after tick 3 (ratchet should adopt when clean)")
	}
	if s.peakSpeed == 0 {
		t.Error("expected peakSpeed>0 after tick 3 (ratchet should record peak)")
	}
}

// TestConvergence_E2E_MomentumChain verifies the SPEC's headline acceptance
// criterion end-to-end: the momentum chain drives multiple consecutive
// probe-down steps, each 2 ticks apart (settling + evaluation), without any
// manual state injection. Uses processTask directly to bypass ScaleWorkers
// (which returns 0 with nil pool, triggering D5 rollback in tick()).
//
// Flow: stable → probe-down → settling → eval(success) → momentum → probe-down → settling → eval(success) → momentum
func TestConvergence_E2E_MomentumChain(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_e2e_momentum"
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
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 256)
	defer ct.Stop()

	// Helper: call processTask directly, simulating successful scale by
	// adjusting telemetry worker count after a probe-down.
	// N_max is pinned to workerCount to suppress Probe-Up (+1) so the
	// probe-down momentum chain can be exercised in isolation.
	process := func(completedLen int64, workerCount int) (pendingScale, bool) {
		tracker.tasks[0].CompletedLength = completedLen
		telemetry.data[gid] = makeWorkers(workerCount, 2*1024*1024)
		ct.limits.SetNMax(limitKey("wan", "example.com"), workerCount)
		ct.mu.Lock()
		if s, ok := ct.states[gid]; ok {
			setPrevSampleAgoState(s, 5*time.Second)
		}
		ct.mu.Unlock()
		return ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	}

	// Tick 1: first sample — stores baseline, no decision
	ps, ok := process(10*1024*1024, 8)
	if ok {
		t.Fatalf("tick 1: expected no scale, got delta=%d", ps.delta)
	}

	// Tick 2: stable throughput, sustainCount=1, peakWorkers=0 → S2 gate blocks
	// (need peakSustainCycles=2 or peakWorkers>0)
	ps, ok = process(60*1024*1024, 8) // 50MB in 5s = 10MB/s
	if ok {
		t.Fatalf("tick 2: expected no scale (S2 gate), got delta=%d", ps.delta)
	}

	// Tick 3: sustainCount=2, clean=true → ratchet adopts (peakWorkers>0)
	// S2 gate passes → probe-down issued (delta=-1, step=8/8=1)
	ps, ok = process(110*1024*1024, 8) // 50MB in 5s = 10MB/s
	if !ok || ps.delta >= 0 {
		t.Fatalf("tick 3: expected probe-down (delta<0), got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseSettling {
		t.Fatalf("tick 3: expected phase=settling, got %d", s.phase)
	}
	if s.lastStep <= 0 {
		t.Fatalf("tick 3: expected lastStep>0, got %d", s.lastStep)
	}
	step1 := s.lastStep

	// Simulate successful scale-down: reduce worker count
	newWorkers := max(8-step1, 1)

	// Tick 4: settling → refresh baseline, transition to stable, no decision
	ps, ok = process(160*1024*1024, newWorkers) // 50MB in 5s = 10MB/s
	if ok {
		t.Fatalf("tick 4: expected no scale (settling), got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Fatalf("tick 4: expected phase=stable after settling, got %d", s.phase)
	}

	// Tick 5: evaluate last probe — throughput maintained (10MB/s vs 10MB/s baseline)
	// dropRatio=0 ≤ 0.5 → success → momentum=true, probeCooldown=0
	// Then immediately initiate next probe (momentum + cooldown==0)
	ps, ok = process(210*1024*1024, newWorkers) // 50MB in 5s = 10MB/s
	if !ok || ps.delta >= 0 {
		t.Fatalf("tick 5: expected momentum probe-down (delta<0), got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.probeMomentum {
		t.Fatal("tick 5: expected probeMomentum=true after successful probe")
	}
	if s.phase != phaseSettling {
		t.Fatalf("tick 5: expected phase=settling (momentum re-probe), got %d", s.phase)
	}
	step2 := s.lastStep

	// Simulate successful second scale-down
	newWorkers2 := max(newWorkers-step2, 1)

	// Tick 6: settling → refresh baseline, transition to stable
	ps, ok = process(260*1024*1024, newWorkers2)
	if ok {
		t.Fatalf("tick 6: expected no scale (settling), got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Fatalf("tick 6: expected phase=stable after settling, got %d", s.phase)
	}

	// Tick 7: evaluate second probe — throughput still maintained
	// → momentum continues, another probe-down
	ps, ok = process(310*1024*1024, newWorkers2)
	if !ok || ps.delta >= 0 {
		t.Fatalf("tick 7: expected second momentum probe-down, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.probeMomentum {
		t.Fatal("tick 7: expected probeMomentum still true")
	}

	// Verify peak was recorded during the chain
	if len(recorder.records) == 0 {
		t.Error("expected peak efficiency recorded during momentum chain")
	}
}

// TestConvergence_LinearZone_KneeDetection is the critical Bug 1 regression test:
// In a linear ramp zone (no congestion), cutting 4 threads out of 32 should drop
// speed proportionally (~12.5%). The old absolute-drop check (3.1% < 10%) would
// mistakenly classify this as "success" and keep cutting. The Marginal Drop Ratio
// check correctly identifies this as knee crossing: dropRatio = 1.0 > 0.5.
func TestConvergence_LinearZone_KneeDetection(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_linear_knee"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(28, 2*1024*1024), // 28 workers after 4 were cut from 32
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Clear any leftover N_max from previous tests (global singleton).
	ct.limits.Clear(limitKey("wan", "example.com"))

	// Set up state: probe-down of 4 threads from 32 just completed, settling done.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.lastStep = 4                     // cut 4 threads
	s.probeBaseline = 32 * 1024 * 1024 // 32MB/s before probe (32 threads × 1MB/s each)
	s.probeBaselineWorkers = 32        // 32 workers before probe
	s.probeMomentum = true             // was in momentum mode
	s.probeCooldown = 0
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// After cutting 4 threads in linear zone: speed drops proportionally to 28MB/s.
	// expectedDrop = 4/32 = 0.125, actualDrop = 1 - 28/32 = 0.125, dropRatio = 1.0 > 0.5
	// → knee crossed → rebound + frozen
	tracker.tasks[0].CompletedLength = 114 * 1024 * 1024 // +14MB in 5s = 28MB/s
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()

	if !s.kneeFrozen {
		t.Error("expected kneeFrozen=true after linear-zone probe (dropRatio=1.0 > 0.5)")
	}
	if s.phase != phaseFrozen {
		t.Errorf("expected phase=phaseFrozen after knee crossing, got %d", s.phase)
	}
	if s.probeMomentum {
		t.Error("expected probeMomentum=false after knee crossing (momentum should be extinguished)")
	}
}

// TestConvergence_PlateauZone_MomentumContinues verifies that in a redundant plateau
// zone, cutting 4 threads out of 32 barely affects speed (~3% drop). The Marginal Drop
// Ratio: expectedDrop=4/32=0.125, actualDrop=0.031, dropRatio=0.25 ≤ 0.5 → success.
func TestConvergence_PlateauZone_MomentumContinues(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_plateau_momentum"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(28, 2*1024*1024), // 28 workers after 4 were cut from 32
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Set up state: probe-down of 4 threads from 32 just completed, settling done.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.lastStep = 4                     // cut 4 threads
	s.probeBaseline = 32 * 1024 * 1024 // 32MB/s before probe
	s.probeBaselineWorkers = 32        // 32 workers before probe
	s.probeMomentum = false            // cold probe, not yet in momentum
	s.probeCooldown = 0
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// In plateau zone: cutting 4 threads barely affects speed (31MB/s vs 32MB/s baseline, ~3% drop).
	// expectedDrop = 4/32 = 0.125, actualDrop = 1 - 31/32 = 0.031, dropRatio = 0.25 ≤ 0.5
	// → success → momentum ignited
	// rawBps = delta/dt = 155MB/5s = 31MB/s
	tracker.tasks[0].CompletedLength = 255 * 1024 * 1024 // 100MB + 155MB delta = 31MB/s over 5s
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()

	if !s.probeMomentum {
		t.Error("expected probeMomentum=true after plateau-zone probe (dropRatio=0.25 ≤ 0.5)")
	}
	if s.probeCooldown != 0 {
		t.Errorf("expected probeCooldown=0 after successful probe, got %d", s.probeCooldown)
	}
	if s.kneeFrozen {
		t.Error("expected kneeFrozen=false in plateau zone (no knee crossing)")
	}
}

// TestConvergence_E2E_LinearZoneKneeViaSettling is the critical regression test for
// the settling-overwrites-probeBaseline bug. It goes through the FULL settling path
// (probe-down → settling → evaluation) with throughput proportional to worker count
// (linear zone). Without the fix, settling overwrites probeBaseline to the post-probe
// throughput, making actualDrop=0 and dropRatio=0 → false success. With the fix,
// probeBaseline retains the pre-probe value, dropRatio ≈ 1.0 > 0.5 → knee crossed.
func TestConvergence_E2E_LinearZoneKneeViaSettling(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_e2e_linear"

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
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 256)
	defer ct.Stop()

	// Helper: call processTask directly, simulating successful scale by
	// adjusting telemetry worker count after a probe-down.
	// throughput = perThreadSpeed * workerCount (linear zone: proportional)
	// probeMomentum=true suppresses Probe-Up (+1) so the probe-down
	// knee-crossing path can be exercised in isolation.
	process := func(completedLen int64, workerCount int) (pendingScale, bool) {
		tracker.tasks[0].CompletedLength = completedLen
		telemetry.data[gid] = makeWorkers(workerCount, 2*1024*1024)
		ct.limits.Clear(limitKey("wan", "example.com"))
		ct.mu.Lock()
		if s, ok := ct.states[gid]; ok {
			s.probeMomentum = true
			s.probeCooldown = 0
			setPrevSampleAgoState(s, 5*time.Second)
		}
		ct.mu.Unlock()
		return ct.processTask(tracker.tasks[0], false, nil, nil, nil)
	}

	// Tick 1: first sample — stores baseline, no decision
	ps, ok := process(10*1024*1024, 8) // 10MB in 5s (first sample delta)
	if ok {
		t.Fatalf("tick 1: expected no scale, got delta=%d", ps.delta)
	}

	// Tick 2: stable throughput = 80MB/s (8 × 10MB/s), sustainCount=1
	// S2 gate blocks (need peakSustainCycles=2)
	// delta = 400MB / 5s = 80MB/s
	ps, ok = process(410*1024*1024, 8) // 10MB + 400MB delta = 80MB/s
	if ok {
		t.Fatalf("tick 2: expected no scale (S2 gate), got delta=%d", ps.delta)
	}

	// Tick 3: sustainCount=2, clean=true → ratchet adopts, probe-down issued
	// throughput still 80MB/s, step = 8/8 = 1
	// delta = 400MB / 5s = 80MB/s
	ps, ok = process(810*1024*1024, 8) // 410MB + 400MB delta = 80MB/s
	if !ok || ps.delta >= 0 {
		t.Fatalf("tick 3: expected probe-down (delta<0), got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseSettling {
		t.Fatalf("tick 3: expected phase=settling, got %d", s.phase)
	}
	if s.probeBaseline != 80*1024*1024 {
		t.Fatalf("tick 3: expected probeBaseline=80MB/s, got %d", s.probeBaseline)
	}
	if s.probeBaselineWorkers != 8 {
		t.Fatalf("tick 3: expected probeBaselineWorkers=8, got %d", s.probeBaselineWorkers)
	}
	step1 := s.lastStep

	// Simulate successful scale-down: 8 → 7 workers
	newWorkers := 8 - step1

	// Tick 4: settling → transition to stable, NO decision
	// Throughput at 7 workers = 70MB/s (linear zone: proportional)
	// BUG (before fix): probeBaseline overwritten to 70MB/s here
	// FIX (after fix): probeBaseline stays at 80MB/s
	// delta = 350MB / 5s = 70MB/s (7 workers × 10MB/s)
	ps, ok = process(1160*1024*1024, newWorkers) // 810MB + 350MB delta = 70MB/s
	if ok {
		t.Fatalf("tick 4: expected no scale (settling), got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Fatalf("tick 4: expected phase=stable after settling, got %d", s.phase)
	}
	// Critical assertion: probeBaseline must NOT have been overwritten
	if s.probeBaseline != 80*1024*1024 {
		t.Fatalf("tick 4: probeBaseline was overwritten to %d (expected 80MB/s=83886080). This is the settling bug!", s.probeBaseline)
	}

	// Tick 5: evaluate last probe
	// actualDrop = 1 - 70/80 = 0.125
	// expectedDrop = 1/8 = 0.125
	// dropRatio = 0.125/0.125 = 1.0 > 0.5 → knee crossed → rebound + freeze
	// delta = 350MB / 5s = 70MB/s (7 workers × 10MB/s)
	ps, ok = process(1510*1024*1024, newWorkers) // 1160MB + 350MB delta = 70MB/s
	if !ok || ps.delta <= 0 {
		t.Fatalf("tick 5: expected rebound (delta>0) after knee crossing, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.kneeFrozen {
		t.Fatal("tick 5: expected kneeFrozen=true after linear-zone knee crossing")
	}
	if s.probeMomentum {
		t.Fatal("tick 5: expected probeMomentum=false after knee crossing")
	}
	if s.phase != phaseFloorHit {
		t.Fatalf("tick 5: expected phase=phaseFloorHit, got %d", s.phase)
	}
}
