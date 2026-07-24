package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// TestProbeUp_BlockedDuringMomentum verifies that Probe-Up does not fire while
// probeMomentum is true (an active down-probe combo). The !probeMomentum gate
// prevents greedy Probe-Up from interrupting the down-probe sequence.
func TestProbeUp_BlockedDuringMomentum(t *testing.T) {
	gid := "sg_probe_up_momentum_block"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)

	// Simulate an active down-probe combo: momentum is true.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.probeMomentum = true
	ct.mu.Unlock()

	// Same conditions that would normally trigger Probe-Up.
	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if ok && ps.delta > 0 {
		t.Fatalf("expected Probe-Up blocked during momentum, got delta=%d", ps.delta)
	}

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase == phaseProbingUp {
		t.Fatal("expected phase != phaseProbingUp while probeMomentum=true")
	}

	// Momentum keeps the down-probe combo alive: with probeCooldown==0 and
	// currentWorkers>probeFloor, the down-probe fires (delta<0, phase enters
	// settling) instead of the blocked Probe-Up.
	if ps.delta >= 0 {
		t.Errorf("expected probe-down delta<0 during momentum, got delta=%d", ps.delta)
	}
	if s.phase != phaseSettling {
		t.Errorf("expected phase=phaseSettling from probe-down, got %d", s.phase)
	}
}

// TestProbeMomentum_ClearedOnFloorReached verifies that when a down-probe combo
// reaches the probe floor (currentWorkers <= probeFloor), probeMomentum is
// cleared and probeCooldown is reset to probeIntervalCycles.
func TestProbeMomentum_ClearedOnFloorReached(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_probe_floor_clear"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", EnvKey: "testenv", Domain: "example.com", IsKeepAlive: true, CompletedLength: 0},
		},
	}
	// probeFloor is probeFloorWorkers(2) when no speedstats data exists.
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

	ct.limits.Clear(limitKey("wan", "example.com"))

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.probeMomentum = true
	s.probeCooldown = 0
	s.peakWorkers = 8
	s.bestEff = 1_310_720
	s.prevCompleted = 10 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.probeMomentum {
		t.Error("expected probeMomentum=false after floor reached")
	}
	if s.probeCooldown != probeIntervalCycles {
		t.Errorf("expected probeCooldown=%d, got %d", probeIntervalCycles, s.probeCooldown)
	}
	// Clearing momentum on floor reached must not disturb the phase.
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable after floor clear, got %d", s.phase)
	}
}

// TestProbeUp_FiresAfterFloorMomentumCleared verifies that after probeMomentum
// is cleared (e.g. by floor-reached), Probe-Up can fire normally on the next
// evaluation.
func TestProbeUp_FiresAfterFloorMomentumCleared(t *testing.T) {
	gid := "sg_probe_up_after_floor"
	ct, tracker, telemetry, _ := setupProbeUpState(t, gid, 1_310_720, 8)

	// Momentum already cleared (floor-reached happened previously).
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.probeMomentum = false
	ct.mu.Unlock()

	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if !ok || ps.delta != 1 {
		t.Fatalf("expected Probe-Up +1 after momentum cleared, got ok=%v delta=%d", ok, ps.delta)
	}

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s.phase != phaseProbingUp {
		t.Fatalf("expected phase=phaseProbingUp, got %d", s.phase)
	}
}
