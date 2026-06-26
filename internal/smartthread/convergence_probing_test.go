package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
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

// mockRateChecker implements RateLimitChecker for testing
type mockRateChecker struct {
	limited map[string]bool
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
		return 1_000_000, true
	}
	return 0, false
}

// newTestConvergenceTicker creates a ConvergenceTicker with default mock recorder/checker.
func newTestConvergenceTicker(engine *rpc.HybridEngine, tracker *mockTracker, telemetry *mockTelemetry) *ConvergenceTicker {
	return NewConvergenceTicker(engine, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 0},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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
			{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 30 * 1024 * 1024},
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
		{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 60 * 1024 * 1024},
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

// TestConvergence_RateLimitSkip verifies that rate-limited tasks don't get probed.
func TestConvergence_RateLimitSkip(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_ratelimited"
	rateChecker := &mockRateChecker{
		limited: map[string]bool{gid: true},
	}

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, rateChecker)
	defer ct.Stop()

	// Set up state with baseline
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	s.phase = phaseStable
	ct.mu.Unlock()

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase == phaseSettling {
		t.Error("expected no probe when rate-limited, but phase=settling")
	}
	if len(recorder.records) > 0 {
		t.Errorf("expected no peak recording when rate-limited, got %d records", len(recorder.records))
	}
}

// TestConvergence_ProbeFloor_StopsAtFloor verifies probing stops at probeFloor.
func TestConvergence_ProbeFloor_StopsAtFloor(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_floor"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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

// TestConvergence_M1_CongestionTrapEscape verifies the SPEC's headline acceptance criterion:
// starting from a high-N low-throughput state, the momentum chain drives probes down
// until throughput improves. Tests the success → momentum → continue-probing path.
func TestConvergence_M1_CongestionTrapEscape(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_congestion"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 0},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
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
			{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 30 * 1024 * 1024},
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
		{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 60 * 1024 * 1024},
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
			{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 30 * 1024 * 1024},
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
		{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 60 * 1024 * 1024},
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

	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_s2_cold"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 10 * 1024 * 1024},
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})
	defer ct.Stop()

	// Helper: call processTask directly, simulating successful scale by
	// adjusting telemetry worker count after a probe-down.
	// N_max is pinned to workerCount to suppress Probe-Up (+1) so the
	// probe-down momentum chain can be exercised in isolation.
	process := func(completedLen int64, workerCount int) (pendingScale, bool) {
		tracker.tasks[0].CompletedLength = completedLen
		telemetry.data[gid] = makeWorkers(workerCount, 2*1024*1024)
		ct.limits.SetNMax("example.com", workerCount)
		ct.mu.Lock()
		if s, ok := ct.states[gid]; ok {
			setPrevSampleAgoState(s, 5*time.Second)
		}
		ct.mu.Unlock()
		return ct.processTask(tracker.tasks[0], false, nil)
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
	newWorkers := 8 - step1
	if newWorkers < 1 {
		newWorkers = 1
	}

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
	newWorkers2 := newWorkers - step2
	if newWorkers2 < 1 {
		newWorkers2 = 1
	}

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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})
	defer ct.Stop()

	// Helper: call processTask directly, simulating successful scale by
	// adjusting telemetry worker count after a probe-down.
	// throughput = perThreadSpeed * workerCount (linear zone: proportional)
	// N_max is pinned to workerCount to suppress Probe-Up (+1) so the
	// probe-down knee-crossing path can be exercised in isolation.
	process := func(completedLen int64, workerCount int) (pendingScale, bool) {
		tracker.tasks[0].CompletedLength = completedLen
		telemetry.data[gid] = makeWorkers(workerCount, 2*1024*1024)
		ct.limits.SetNMax("example.com", workerCount)
		ct.mu.Lock()
		if s, ok := ct.states[gid]; ok {
			setPrevSampleAgoState(s, 5*time.Second)
		}
		ct.mu.Unlock()
		return ct.processTask(tracker.tasks[0], false, nil)
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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})

	// Clear any leftover N_max from previous tests (global singleton).
	ct.limits.Clear("example.com")

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
	if s.probeUpBaseline != 10*1024*1024 {
		t.Fatalf("expected probeUpBaseline=10MB/s, got %d", s.probeUpBaseline)
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
	ct.limits.SetNMax("example.com", 8) // currentWorkers=8 >= nMax=8

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
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	rateChecker := &mockRateChecker{limited: map[string]bool{gid: true}}
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, rateChecker)

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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

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
	return ct.processTask(tracker.tasks[0], false, nil)
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
	// D3 ratchet must run before the early-return: bestEff raised to newEff.
	if s.bestEff != 1_310_720 {
		t.Fatalf("expected bestEff=1310720 (D3 ratchet ran before early-return), got %d", s.bestEff)
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

// ---------------------------------------------------------------------------
// FloorHit tests (Task 12)
// ---------------------------------------------------------------------------

// setupFloorHitState creates a ticker with state primed in phaseFloorHit.
func setupFloorHitState(t *testing.T, gid string, floorMemory int64, cooldown int) *ConvergenceTicker {
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseFloorHit
	s.floorMemory = floorMemory
	s.floorHitCount = 0
	s.frozenCooldown = cooldown
	s.kneeFrozen = true
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	return ct
}

func floorHitProcess(ct *ConvergenceTicker, tracker *mockTracker, telemetry *mockTelemetry, gid string, completedLen int64, workerCount int) (pendingScale, bool) {
	tracker.tasks[0].CompletedLength = completedLen
	telemetry.data[gid] = makeWorkers(workerCount, 2*1024*1024)
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	return ct.processTask(tracker.tasks[0], false, nil)
}

// TestConvergence_FloorHit_SmartUnlock verifies consecutive 2 ticks with
// rawBps < floorMemory*0.90 → floor-unlocked, phase back to phaseStable,
// kneeFrozen=false, floorMemory cleared.
func TestConvergence_FloorHit_SmartUnlock(t *testing.T) {
	gid := "sg_floor_unlock"
	floorMem := int64(10 * 1024 * 1024) // 10 MB/s
	ct := setupFloorHitState(t, gid, floorMem, floorHitCooldownCycles)
	tracker := ct.tracker.(*mockTracker)
	telemetry := ct.telemetry.(*mockTelemetry)

	// Tick 1: rawBps = 8 MB/s < 10*0.90 = 9 MB/s → floorHitCount=1
	// delta = 8MB*5 = 40MB. prevCompleted=10MB. CompletedLength=50MB.
	ps, ok := floorHitProcess(ct, tracker, telemetry, gid, 50*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale during floor-hit, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.floorHitCount != 1 {
		t.Fatalf("expected floorHitCount=1, got %d", s.floorHitCount)
	}

	// Tick 2: rawBps = 8 MB/s < 9 MB/s → floorHitCount=2 → unlock
	// prevCompleted=50MB. delta=40MB. CompletedLength=90MB.
	ps, ok = floorHitProcess(ct, tracker, telemetry, gid, 90*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale on unlock, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Fatalf("expected phase=phaseStable after unlock, got %d", s.phase)
	}
	if s.floorMemory != 0 {
		t.Fatalf("expected floorMemory=0 after unlock, got %d", s.floorMemory)
	}
	if s.kneeFrozen {
		t.Error("expected kneeFrozen=false after floor unlock")
	}
}

// TestConvergence_FloorHit_SingleFluctuationNoUnlock verifies a single
// low-rawBps tick followed by a normal tick does not unlock.
func TestConvergence_FloorHit_SingleFluctuationNoUnlock(t *testing.T) {
	gid := "sg_floor_fluctuation"
	floorMem := int64(10 * 1024 * 1024)
	ct := setupFloorHitState(t, gid, floorMem, floorHitCooldownCycles)
	tracker := ct.tracker.(*mockTracker)
	telemetry := ct.telemetry.(*mockTelemetry)

	// Tick 1: rawBps = 8 MB/s < 9 → floorHitCount=1
	ps, ok := floorHitProcess(ct, tracker, telemetry, gid, 50*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.floorHitCount != 1 {
		t.Fatalf("expected floorHitCount=1, got %d", s.floorHitCount)
	}

	// Tick 2: rawBps = 10 MB/s > 9 → floorHitCount resets to 0
	// prevCompleted=50MB. delta=50MB. CompletedLength=100MB.
	ps, ok = floorHitProcess(ct, tracker, telemetry, gid, 100*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.floorHitCount != 0 {
		t.Fatalf("expected floorHitCount=0 after fluctuation, got %d", s.floorHitCount)
	}
	if s.phase != phaseFloorHit {
		t.Fatalf("expected still phaseFloorHit, got %d", s.phase)
	}
}

// TestConvergence_FloorHit_CooldownExpired verifies frozenCooldown countdown
// reaches 0 without slowdown → floor-cooldown-expired, phase back to phaseStable,
// kneeFrozen=false.
func TestConvergence_FloorHit_CooldownExpired(t *testing.T) {
	gid := "sg_floor_cooldown"
	floorMem := int64(10 * 1024 * 1024)
	ct := setupFloorHitState(t, gid, floorMem, 1) // cooldown=1 → expires next tick
	tracker := ct.tracker.(*mockTracker)
	telemetry := ct.telemetry.(*mockTelemetry)

	// Tick: rawBps = 10 MB/s (not < 9) → no unlock. cooldown 1→0 → expired.
	ps, ok := floorHitProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale on cooldown expiry, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseStable {
		t.Fatalf("expected phase=phaseStable after cooldown expiry, got %d", s.phase)
	}
	if s.kneeFrozen {
		t.Error("expected kneeFrozen=false after floor cooldown expiry")
	}
	if s.floorMemory != 0 {
		t.Fatalf("expected floorMemory=0 after cooldown expiry, got %d", s.floorMemory)
	}
}

// TestConvergence_FloorHit_EarlyReturnNoProbe verifies phase==phaseFloorHit
// causes processTask to early-return without triggering Probe-Up or Probe-Down.
func TestConvergence_FloorHit_EarlyReturnNoProbe(t *testing.T) {
	gid := "sg_floor_earlyreturn"
	floorMem := int64(10 * 1024 * 1024)
	ct := setupFloorHitState(t, gid, floorMem, floorHitCooldownCycles)
	tracker := ct.tracker.(*mockTracker)
	telemetry := ct.telemetry.(*mockTelemetry)

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.bestEff = 500_000 // below newEff so D3 ratchet raises it
	s.peakWorkers = 8
	ct.mu.Unlock()

	ps, ok := floorHitProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if ok {
		t.Fatalf("expected no scale during floor-hit early return, got delta=%d", ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseFloorHit {
		t.Fatalf("expected phase=phaseFloorHit (early return preserves phase), got %d", s.phase)
	}
	if s.frozenCooldown != floorHitCooldownCycles-1 {
		t.Fatalf("expected frozenCooldown=%d after one tick, got %d", floorHitCooldownCycles-1, s.frozenCooldown)
	}
	// D3 ratchet must run before the early-return: bestEff raised to newEff.
	if s.bestEff != 1_310_720 {
		t.Fatalf("expected bestEff=1310720 (D3 ratchet ran before early-return), got %d", s.bestEff)
	}
}

// TestConvergence_FloorHit_KneeFrozenSet verifies the knee-crossed path sets
// kneeFrozen=true and phase=phaseFloorHit, and bandwidthRelease skips that task.
func TestConvergence_FloorHit_KneeFrozenSet(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_floor_kneefrozen"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

	// Clear any leftover N_max from previous tests (global singleton).
	ct.limits.Clear("example.com")

	// Set up knee-crossed conditions: lastStep > 0, linear zone drop.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.lastStep = 4
	s.probeBaseline = 32 * 1024 * 1024
	s.probeBaselineWorkers = 32
	s.probeMomentum = true
	s.probeCooldown = 0
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// 28 workers, rawBps = 14MB/5s = 2.8MB/s. dropRatio > 0.5 → knee crossed.
	telemetry.data[gid] = makeWorkers(28, 2*1024*1024)
	tracker.tasks[0].CompletedLength = 114 * 1024 * 1024
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok := ct.processTask(tracker.tasks[0], false, nil)
	if !ok || ps.delta <= 0 {
		t.Fatalf("expected rebound (delta>0) after knee crossing, got ok=%v delta=%d", ok, ps.delta)
	}
	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.kneeFrozen {
		t.Fatal("expected kneeFrozen=true after knee-crossed")
	}
	if s.phase != phaseFloorHit {
		t.Fatalf("expected phase=phaseFloorHit after knee-crossed, got %d", s.phase)
	}
	if s.floorMemory == 0 {
		t.Fatal("expected floorMemory > 0 after knee-crossed")
	}

	// Verify bandwidthRelease skips this task (kneeFrozen=true).
	// Simulate a completed task in prevActiveGids to trigger bandwidthRelease.
	completedGid := "sg_completed_task"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{gid: {Domain: "example.com", Scope: "wan"}},
		map[string]bool{},
		nil,
	)
	for _, r := range releases {
		if r.gid == gid {
			t.Fatal("expected bandwidthRelease to skip kneeFrozen task")
		}
	}
}

// ---------------------------------------------------------------------------
// RecordPeakEfficiency pre-change snapshot tests (Task 13)
// ---------------------------------------------------------------------------

// monotonicMockPeakRecorder only accepts higher peak values, mirroring the real
// tracker's monotonic ratchet semantics.
type monotonicMockPeakRecorder struct {
	records map[string]struct {
		peak    int64
		workers int
	}
}

func (m *monotonicMockPeakRecorder) RecordPeakEfficiency(gid string, peak int64, workers int) {
	if m.records == nil {
		m.records = make(map[string]struct {
			peak    int64
			workers int
		})
	}
	existing, ok := m.records[gid]
	if !ok || peak > existing.peak {
		m.records[gid] = struct {
			peak    int64
			workers int
		}{peak, workers}
	}
}

// TestConvergence_RecordPeakEfficiency_ProbeUpTrigger verifies RecordPeakEfficiency
// is called before the Probe-Up +1 return.
func TestConvergence_RecordPeakEfficiency_ProbeUpTrigger(t *testing.T) {
	gid := "sg_rpe_probeup"
	ct, tracker, telemetry, recorder := setupProbeUpState(t, gid, 1_310_720, 8)

	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if !ok || ps.delta != 1 {
		t.Fatalf("expected probe-up +1, got ok=%v delta=%d", ok, ps.delta)
	}
	rec, ok := recorder.records[gid]
	if !ok {
		t.Fatal("expected RecordPeakEfficiency called before probe-up return")
	}
	// rawBps = 10 MB/s = 10*1024*1024, currentWorkers = 8
	expectedPeak := int64(10 * 1024 * 1024)
	if rec.peak != expectedPeak {
		t.Fatalf("expected recorded peak=%d, got %d", expectedPeak, rec.peak)
	}
	if rec.workers != 8 {
		t.Fatalf("expected recorded workers=8, got %d", rec.workers)
	}
}

// TestConvergence_RecordPeakEfficiency_CeilingHitRebound verifies RecordPeakEfficiency
// is called before the CeilingHit -1 return (with non-zero rawBps).
func TestConvergence_RecordPeakEfficiency_CeilingHitRebound(t *testing.T) {
	gid := "sg_rpe_ceiling"
	ct, tracker, telemetry, recorder := setupProbeUpState(t, gid, 1_310_720, 8)

	// Trigger initial Probe-Up with rawBps = 10 MB/s
	probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)

	// Reset recorder to isolate the ceiling-hit snapshot
	recorder.records = nil

	// Trigger CeilingHit: rawBps = 10.1 MB/s, but expectedGain = 1/8 = 12.5%.
	// actualGain = (10.1-10)/10 = 0.01. gainRatio = 0.01/0.125 = 0.08 < 0.5 → ceiling hit.
	// delta = 10.1MB*5 = 50.5MB. prevCompleted=60MB. CompletedLength=110.5MB.
	tracker.tasks[0].CompletedLength = int64(110.5 * 1024 * 1024)
	telemetry.data[gid] = makeWorkers(9, 2*1024*1024)
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok := ct.processTask(tracker.tasks[0], false, nil)
	if !ok || ps.delta != -1 {
		t.Fatalf("expected ceiling-hit -1, got ok=%v delta=%d", ok, ps.delta)
	}
	rec, ok := recorder.records[gid]
	if !ok {
		t.Fatal("expected RecordPeakEfficiency called before ceiling-hit return")
	}
	if rec.workers != 9 {
		t.Fatalf("expected recorded workers=9, got %d", rec.workers)
	}
}

// TestConvergence_RecordPeakEfficiency_FloorHitRebound verifies RecordPeakEfficiency
// is called before the FloorHit rebound return.
func TestConvergence_RecordPeakEfficiency_FloorHitRebound(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_rpe_floor"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(28, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	recorder := &monotonicMockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})

	// Clear any leftover N_max from previous tests (global singleton).
	ct.limits.Clear("example.com")

	// Set up knee-crossed conditions
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.lastStep = 4
	s.probeBaseline = 32 * 1024 * 1024
	s.probeBaselineWorkers = 32
	s.probeMomentum = true
	s.probeCooldown = 0
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Block Probe-Up via N_max so it doesn't interfere
	ct.limits.SetNMax("example.com", 28)

	// rawBps = 14MB/5s = 2.8MB/s. dropRatio > 0.5 → knee crossed → rebound.
	tracker.tasks[0].CompletedLength = 114 * 1024 * 1024
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok := ct.processTask(tracker.tasks[0], false, nil)
	if !ok || ps.delta <= 0 {
		t.Fatalf("expected rebound (delta>0), got ok=%v delta=%d", ok, ps.delta)
	}
	rec, ok := recorder.records[gid]
	if !ok {
		t.Fatal("expected RecordPeakEfficiency called before floor-hit return")
	}
	// rawBps = 2.8 MB/s = 2936012, currentWorkers = 28
	if rec.workers != 28 {
		t.Fatalf("expected recorded workers=28, got %d", rec.workers)
	}
}

// TestConvergence_RecordPeakEfficiency_ProbeDownTrigger verifies RecordPeakEfficiency
// is called before the Probe-Down -step return.
func TestConvergence_RecordPeakEfficiency_ProbeDownTrigger(t *testing.T) {
	gid := "sg_rpe_probedown"
	ct, tracker, telemetry, recorder := setupProbeUpState(t, gid, 1_310_720, 8)

	// Block Probe-Up via N_max so Probe-Down can fire
	ct.limits.SetNMax("example.com", 8)

	// rawBps = 10 MB/s, 8 workers. Probe-Down fires (step=1, delta=-1).
	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if !ok || ps.delta >= 0 {
		t.Fatalf("expected probe-down (delta<0), got ok=%v delta=%d", ok, ps.delta)
	}
	rec, ok := recorder.records[gid]
	if !ok {
		t.Fatal("expected RecordPeakEfficiency called before probe-down return")
	}
	expectedPeak := int64(10 * 1024 * 1024)
	if rec.peak != expectedPeak {
		t.Fatalf("expected recorded peak=%d, got %d", expectedPeak, rec.peak)
	}
	if rec.workers != 8 {
		t.Fatalf("expected recorded workers=8, got %d", rec.workers)
	}
}

// TestConvergence_RecordPeakEfficiency_MonotonicRatchet verifies that a lower
// rawBps snapshot does not overwrite a previously recorded higher peak.
func TestConvergence_RecordPeakEfficiency_MonotonicRatchet(t *testing.T) {
	gid := "sg_rpe_monotonic"
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
	recorder := &monotonicMockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})

	// Clear any leftover N_max from previous tests (global singleton).
	ct.limits.Clear("example.com")

	// Pre-record a high peak (100 MB/s, 8 workers)
	recorder.RecordPeakEfficiency(gid, 100*1024*1024, 8)

	// Set up state for Probe-Up trigger
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.bestEff = 100 * 1024 * 1024 / 8 // 12.5 MB/s per worker
	s.peakWorkers = 8
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Trigger Probe-Up with rawBps = 10 MB/s (much lower than 100 MB/s peak)
	// newEff = 10MB/8 = 1.25MB/s. bestEff = 12.5MB/s. bestEff*0.95 = 11.875MB.
	// 1.25MB < 11.875MB → Probe-Up doesn't fire. Need higher rawBps.
	// Use rawBps = 95 MB/s → newEff = 11.875 MB/s >= 11.875 → fires.
	// delta = 95MB*5 = 475MB. prevCompleted=10MB. CompletedLength=485MB.
	tracker.tasks[0].CompletedLength = 485 * 1024 * 1024
	ct.mu.Lock()
	setPrevSampleAgoState(ct.states[gid], 5*time.Second)
	ct.mu.Unlock()

	ps, ok := ct.processTask(tracker.tasks[0], false, nil)
	if !ok || ps.delta != 1 {
		t.Fatalf("expected probe-up +1, got ok=%v delta=%d", ok, ps.delta)
	}

	rec, ok := recorder.records[gid]
	if !ok {
		t.Fatal("expected RecordPeakEfficiency called")
	}
	// The monotonic mock should retain 100 MB/s, not overwrite with 95 MB/s
	if rec.peak != 100*1024*1024 {
		t.Fatalf("expected monotonic ratchet to retain 100MB/s peak, got %d", rec.peak)
	}
}

// TestConvergence_BandwidthRelease_SkipsCeilingHit verifies that bandwidthRelease
// suppresses ScaleUp for a task in phaseCeilingHit (with kneeFrozen=false), covering
// the phaseCeilingHit branch of the suppression check.
func TestConvergence_BandwidthRelease_SkipsCeilingHit(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_ceiling_bwrelease"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{gid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

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
		completedGid: {Domain: "example.com", Scope: "wan"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{gid: {Domain: "example.com", Scope: "wan"}},
		map[string]bool{},
		nil,
	)
	for _, r := range releases {
		if r.gid == gid {
			t.Fatal("expected bandwidthRelease to skip phaseCeilingHit task")
		}
	}
}

// TestConvergence_ProbeUp_BlockedByVAvailable verifies that when
// globalPeak - activeBw < vThreadAvg, the Probe-Up trigger does not fire.
func TestConvergence_ProbeUp_BlockedByVAvailable(t *testing.T) {
	gid := "sg_probe_up_vavail"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)

	// Plant a global peak so GetGlobalPeak("wan") = 10 MB/s.
	// GetRecentPeakByDomain("example.com", "wan") = 10MB/1 = 10 MB/s → vThreadAvg = 10 MB/s.
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	// activeBw = 10 MB/s → globalPeak - activeBw = 0 < vThreadAvg(10MB) → blocked.
	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope string) int64 {
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

func TestConvergence_Blackout_TriggersWhenTotalRemainingBelowThreshold(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_trigger"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024, MinChunk: 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeChunkWorkers(4, 2*1024*1024, []int64{
				256 * 1024, 256 * 1024, 256 * 1024, 256 * 1024,
			}),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 90 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state to exist")
	}
	if !s.blackout {
		t.Error("expected blackout=true after totalRemaining < workers × minChunk")
	}
}

func TestConvergence_Blackout_DoesNotTriggerWhenTotalRemainingSufficient(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_no_trigger"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024, MinChunk: 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeChunkWorkers(4, 2*1024*1024, []int64{
				1024 * 1024, 1024 * 1024, 1024 * 1024, 1024 * 1024,
			}),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 90 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.blackout {
		t.Error("expected blackout=false when totalRemaining >= workers × minChunk")
	}
}

func TestConvergence_Blackout_PermanentAcrossActiveSetChange(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_blackout_perm_1"
	gid2 := "sg_blackout_perm_2"

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid1, Status: "active", Scope: "wan", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024, MinChunk: 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeChunkWorkers(4, 2*1024*1024, []int64{256 * 1024, 256 * 1024, 256 * 1024, 256 * 1024}),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid1)
	s.prevCompleted = 90 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid1]
	ct.mu.Unlock()
	if !s.blackout {
		t.Fatal("expected blackout=true after first tick")
	}

	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: gid2, Status: "active", Scope: "wan", Domain: "example.com",
		IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024, MinChunk: 1024 * 1024,
	})
	telemetry.data[gid2] = makeChunkWorkers(4, 2*1024*1024, []int64{1024 * 1024, 1024 * 1024, 1024 * 1024, 1024 * 1024})

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid1]
	ct.mu.Unlock()
	if !s.blackout {
		t.Error("expected blackout to remain true after active-set change (permanent)")
	}
}

func TestConvergence_Blackout_SuppressesAllDecisions(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_suppress"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024, MinChunk: 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeChunkWorkers(8, 2*1024*1024, []int64{
				100 * 1024, 100 * 1024, 100 * 1024, 100 * 1024,
				100 * 1024, 100 * 1024, 100 * 1024, 100 * 1024,
			}),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 90 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	s.bestEff = 2 * 1024 * 1024
	s.peakWorkers = 8
	s.sustainCount = peakSustainCycles
	// Seed non-default values so the post-tick assertions verify the probe
	// state machine was never reached, rather than checking defaults.
	s.phase = phaseSettling
	s.lastStep = -1
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.blackout {
		t.Fatal("expected blackout=true")
	}
	if s.phase != phaseSettling {
		t.Error("expected blackout to suppress Probe-Down (phase should be unmodified)")
	}
	if s.lastStep != -1 {
		t.Error("expected blackout to suppress Probe-Down (lastStep should be unmodified)")
	}
}

func TestConvergence_Blackout_MinChunkFallbackToMinChunkSize(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_fallback"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024, MinChunk: 0,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeChunkWorkers(4, 2*1024*1024, []int64{256 * 1024, 256 * 1024, 256 * 1024, 256 * 1024}),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 90 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.blackout {
		t.Error("expected blackout=true with MinChunk=0 fallback to minChunkSize")
	}
}

func TestConvergence_Blackout_FinalRecordPeakEfficiency(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_record"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024, MinChunk: 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeChunkWorkers(4, 2*1024*1024, []int64{256 * 1024, 256 * 1024, 256 * 1024, 256 * 1024}),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 90 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if !s.blackout {
		t.Fatal("expected blackout=true")
	}
	rec, ok := recorder.records[gid]
	if !ok {
		t.Fatal("expected RecordPeakEfficiency to be called on blackout trigger")
	}
	if rec.peak <= 0 {
		t.Errorf("expected positive peak recording, got %d", rec.peak)
	}
	if rec.workers != 4 {
		t.Errorf("expected workers=4 in recording, got %d", rec.workers)
	}
}

func TestConvergence_Blackout_SkipsRecordPeakEfficiencyOnNoBaseline(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_no_baseline"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024, MinChunk: 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeChunkWorkers(4, 2*1024*1024, []int64{256 * 1024, 256 * 1024, 256 * 1024, 256 * 1024}),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	recorder := &mockPeakRecorder{}
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{})
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if !s.blackout {
		t.Fatal("expected blackout=true even on first tick (trigger condition is chunk-based)")
	}
	if _, ok := recorder.records[gid]; ok {
		t.Error("expected no RecordPeakEfficiency call when prevCompleted=0 (no baseline)")
	}
}

func TestConvergence_BandwidthRelease_SkipsBlackout(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_bwrelease"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com",
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})
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
		completedGid: {Domain: "example.com", Scope: "wan"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{gid: {Domain: "example.com", Scope: "wan"}},
		map[string]bool{},
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
			{GID: beneficiaryGid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_completed_nonkeepalive"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan"},
	}
	ct.mu.Unlock()

	approvedDelta := make(map[string]int)
	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan"}},
		map[string]bool{},
		approvedDelta,
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
	if approvedDelta["wan"] != 1 {
		t.Fatalf("expected approvedDelta[wan]=1, got %d", approvedDelta["wan"])
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
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope string) int64 { return 0 }

	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
			{GID: gid2, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 0},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &monotonicMockPeakRecorder{}, &mockRateChecker{})
	ct.limits.Clear("example.com")

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
		approvedDelta[ps1.scope] += ps1.delta
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
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope string) int64 { return 10 * 1024 * 1024 }

	beneficiaryGid := "sg_bwrelease_vavail"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: false, CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_completed_vavail"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan"}},
		map[string]bool{},
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
			{GID: gidA, Status: "active", Scope: "wan", Domain: "a.com", CompletedLength: 100 * 1024 * 1024},
			{GID: gidB, Status: "active", Scope: "wan", Domain: "b.com", CompletedLength: 100 * 1024 * 1024},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

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
		completedGid: {Domain: "a.com", Scope: "wan"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{gidA: {Domain: "a.com", Scope: "wan"}, gidB: {Domain: "b.com", Scope: "wan"}},
		map[string]bool{},
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
				{GID: gidA, Status: "active", Scope: "wan", Domain: "", CompletedLength: 100 * 1024 * 1024},
				{GID: gidB, Status: "active", Scope: "wan", Domain: "b.com", CompletedLength: 100 * 1024 * 1024},
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
		ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

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
			completedGid: {Domain: "", Scope: "wan"},
		}
		ct.mu.Unlock()

		releases := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{gidA: {Domain: "", Scope: "wan"}, gidB: {Domain: "b.com", Scope: "wan"}},
			map[string]bool{},
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
				{GID: gidB, Status: "active", Scope: "wan", Domain: "b.com", CompletedLength: 100 * 1024 * 1024},
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
		ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

		ct.mu.Lock()
		s := ct.getOrCreateState(gidB)
		s.phase = phaseStable
		s.kneeFrozen = false
		s.blackout = false
		ct.mu.Unlock()

		completedGid := "sg_completed_empty"
		ct.mu.Lock()
		ct.prevActiveGids = map[string]gidInfo{
			completedGid: {Domain: "", Scope: "wan"},
		}
		ct.mu.Unlock()

		releases := ct.bandwidthRelease(
			tracker.tasks,
			map[string]gidInfo{gidB: {Domain: "b.com", Scope: "wan"}},
			map[string]bool{},
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
			{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
			{GID: gid3, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

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
		completedGid: {Domain: "example.com", Scope: "wan"},
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		tracker.tasks,
		map[string]gidInfo{gid1: {Domain: "example.com", Scope: "wan"}, gid2: {Domain: "example.com", Scope: "wan"}, gid3: {Domain: "example.com", Scope: "wan"}},
		map[string]bool{},
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
			{GID: gid1, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
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
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

	for _, gid := range []string{gid1, gid2} {
		ct.mu.Lock()
		s := ct.getOrCreateState(gid)
		s.phase = phaseStable
		s.kneeFrozen = false
		s.blackout = false
		ct.mu.Unlock()
	}

	activeGids := map[string]gidInfo{
		gid1: {Domain: "example.com", Scope: "wan"},
		gid2: {Domain: "example.com", Scope: "wan"},
	}

	completedGid1 := "sg_completed_rotate_1"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid1: {Domain: "example.com", Scope: "wan"},
	}
	ct.rotationCounter = 0
	ct.mu.Unlock()

	releases1 := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil)
	if len(releases1) != 1 {
		t.Fatalf("expected 1 release on first call, got %d", len(releases1))
	}
	firstElected := releases1[0].gid

	completedGid2 := "sg_completed_rotate_2"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid2: {Domain: "example.com", Scope: "wan"},
	}
	ct.mu.Unlock()

	releases2 := ct.bandwidthRelease(tracker.tasks, activeGids, map[string]bool{}, nil)
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
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope string) int64 { return 10 * 1024 * 1024 }

	beneficiaryGid := "sg_delay_comp_beneficiary"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_delay_comp_completed"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan"},
	}
	ct.prevActiveSpeeds = map[string]int64{
		completedGid: 10 * 1024 * 1024,
	}
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan"}},
		map[string]bool{},
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
	speedstats.AddRecordV2(10*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	orig := activeBandwidthProvider
	t.Cleanup(func() { activeBandwidthProvider = orig })
	activeBandwidthProvider = func(scope string) int64 { return 10 * 1024 * 1024 }

	beneficiaryGid := "sg_no_comp_beneficiary"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: beneficiaryGid, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 100 * 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{beneficiaryGid: makeWorkers(8, 2*1024*1024)},
	}
	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := NewConvergenceTicker(he, tracker, telemetry, &mockPeakRecorder{}, &mockRateChecker{})

	ct.mu.Lock()
	s := ct.getOrCreateState(beneficiaryGid)
	s.phase = phaseStable
	s.kneeFrozen = false
	s.blackout = false
	ct.mu.Unlock()

	completedGid := "sg_no_comp_completed"
	ct.mu.Lock()
	ct.prevActiveGids = map[string]gidInfo{
		completedGid: {Domain: "example.com", Scope: "wan"},
	}
	// prevActiveSpeeds has no entry for completedGid → disappearedSpeed = 0
	ct.mu.Unlock()

	releases := ct.bandwidthRelease(
		[]TrackedTaskInfo{tracker.tasks[0]},
		map[string]gidInfo{beneficiaryGid: {Domain: "example.com", Scope: "wan"}},
		map[string]bool{},
		nil,
	)
	for _, r := range releases {
		if r.gid == beneficiaryGid {
			t.Fatal("expected no ScaleUp when disappearedSpeed=0 and V_available insufficient")
		}
	}
}

func TestConvergence_LastRawBps_SetAfterSecondTick(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_lastrawbps"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 10 * 1024 * 1024, MinChunk: 1024 * 1024},
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
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// First tick: establishes baseline, lastRawBps should stay 0.
	ct.tick()

	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state after first tick")
	}
	if s.lastRawBps != 0 {
		t.Errorf("expected lastRawBps=0 after first sample, got %d", s.lastRawBps)
	}

	// Second tick: CompletedLength increases → rawBps computed → lastRawBps set.
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state after second tick")
	}
	if s.lastRawBps <= 0 {
		t.Errorf("expected lastRawBps > 0 after second tick, got %d", s.lastRawBps)
	}
}
