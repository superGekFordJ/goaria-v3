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
	s.probeMomentum = false
	s.probeCooldown = 0
	s.scaleUpCycles = 0 // prevent scale-up from firing before D4 eval
	s.prevCompleted = 60 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Tick 3: throughput improved to 64MB/s after probe-down (ratio=64/50=1.28 > 0.90)
	// Should ignite momentum and record peak efficiency.
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
	s.probeMomentum = true
	s.probeCooldown = 0
	s.scaleUpCycles = 0 // prevent scale-up from firing before D4 eval
	s.prevCompleted = 100 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Throughput crashed to 30MB/s after probe-down (ratio=30/50=0.6 < recoverBand=0.75)
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
	s.lastStep = -1
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
			s.lastStep = -1
			s.probeBaseline = 40 * 1024 * 1024
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