package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

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

	// First tick: establishes baseline, lastRawBps should stay 0 / not ready.
	ct.tick()

	bps, ready := ct.LastRawBps(gid)
	if ready || bps != 0 {
		t.Errorf("after first baseline tick: LastRawBps=(%d,%v), want (0,false)", bps, ready)
	}

	ct.mu.Lock()
	s := ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state after first tick")
	}
	if s.lastRawBps != 0 {
		t.Errorf("expected lastRawBps=0 after first sample, got %d", s.lastRawBps)
	}
	if s.macroReady {
		t.Error("expected macroReady=false after first baseline tick")
	}

	// Second tick: CompletedLength increases → rawBps computed → lastRawBps set.
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()

	ct.tick()

	bps, ready = ct.LastRawBps(gid)
	if !ready || bps <= 0 {
		t.Errorf("after second tick: LastRawBps=(%d,%v), want (bps>0, true)", bps, ready)
	}

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state after second tick")
	}
	if s.lastRawBps <= 0 {
		t.Errorf("expected lastRawBps > 0 after second tick, got %d", s.lastRawBps)
	}
	if !s.macroReady {
		t.Error("expected macroReady=true after second tick D2 sample")
	}
}

// TestConvergence_CompletedTaskGuard_EarlyReturn verifies that a task at 100%
// completion (completed == total) with workers present and probing ready is
// short-circuited by the completed-task guard — no probe-down fires.
func TestConvergence_CompletedTaskGuard_EarlyReturn(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_completed_guard"
	total := int64(100 * 1024 * 1024)
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true,
				CompletedLength: total, TotalLength: total,
			},
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

	// Pre-arm probing state so a probe-down would fire without the guard.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.probeMomentum = true
	s.probeCooldown = 0
	s.peakWorkers = 8
	s.bestEff = 2 * 1024 * 1024
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
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable (guard returned before probing), got %d", s.phase)
	}
	if s.lastStep != 0 {
		t.Errorf("expected lastStep=0 (no probe-down), got %d", s.lastStep)
	}
	if s.probeBaseline != 0 {
		t.Errorf("expected probeBaseline=0 (no probe-down), got %d", s.probeBaseline)
	}
}

// TestConvergence_CompletedTaskGuard_OverCounting verifies the >= guard covers
// over-counting (completed > total).
func TestConvergence_CompletedTaskGuard_OverCounting(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_completed_over"
	total := int64(100 * 1024 * 1024)
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true,
				CompletedLength: 105 * 1024 * 1024, TotalLength: total,
			},
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
	s.phase = phaseStable
	s.probeMomentum = true
	s.probeCooldown = 0
	s.peakWorkers = 8
	s.bestEff = 2 * 1024 * 1024
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
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable (over-counting guard), got %d", s.phase)
	}
	if s.lastStep != 0 {
		t.Errorf("expected lastStep=0 (no probe-down), got %d", s.lastStep)
	}
}

// TestConvergence_CompletedTaskGuard_TotalLengthZeroSkipsGuard verifies that
// TotalLength=0 (unknown size) skips the completed-task guard and allows
// normal probing.
func TestConvergence_CompletedTaskGuard_TotalLengthZeroSkipsGuard(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_completed_unknown"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true,
				CompletedLength: 100 * 1024 * 1024, TotalLength: 0,
			},
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

	// Pre-arm probing: prevCompleted=50MB, CompletedLength=100MB → rawBps>0.
	// bestEff set high so Probe-Up trigger (newEff >= bestEff*0.95) is false.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.probeMomentum = true
	s.probeCooldown = 0
	s.peakWorkers = 8
	s.bestEff = 10 * 1024 * 1024
	s.prevCompleted = 50 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Call processTask directly — ct.tick() would reset state when ScaleWorkers
	// returns 0 (test engine has no real pool).
	ps, ok := ct.processTask(tracker.tasks[0], false, nil)
	if !ok || ps.delta >= 0 {
		t.Fatalf("expected probe-down (delta<0), got ok=%v delta=%d", ok, ps.delta)
	}

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state to exist")
	}
	// Guard skipped (TotalLength=0) → normal probing → probe-down fires.
	if s.phase != phaseSettling {
		t.Errorf("expected phase=phaseSettling (guard skipped, probe-down fired), got %d", s.phase)
	}
	if s.lastStep == 0 {
		t.Error("expected lastStep>0 (probe-down executed)")
	}
}

// TestConvergence_CompletedTaskGuard_NoWorkersSkipsGuard verifies that when
// telemetry returns 0 workers, the len(stats)==0 early-return fires before the
// completed-task guard is reached.
func TestConvergence_CompletedTaskGuard_NoWorkersSkipsGuard(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_completed_noworkers"
	total := int64(100 * 1024 * 1024)
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true,
				CompletedLength: total, TotalLength: total,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	s, exists := ct.states[gid]
	ct.mu.Unlock()
	// No workers → telemetry early-return before mu.Lock → state not created.
	if exists && s != nil {
		if s.phase != phaseStable || s.lastStep != 0 {
			t.Errorf("expected untouched state, got phase=%d lastStep=%d", s.phase, s.lastStep)
		}
	}
}

// TestConvergence_ProbeDown_SkipsOnZeroRawBps verifies that a zero-speed task
// (rawBps=0) with probing ready does not trigger a probe-down, preventing the
// probeBaseline=0 dead zone.
func TestConvergence_ProbeDown_SkipsOnZeroRawBps(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_zero_rawbps"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true,
				CompletedLength: 500 * 1024 * 1024, TotalLength: 1024 * 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(6, 0),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// Pre-arm probing: prevCompleted == CompletedLength → rawBps = 0.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.probeMomentum = true
	s.probeCooldown = 0
	s.peakWorkers = 6
	s.bestEff = 2 * 1024 * 1024
	s.prevCompleted = 500 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state to exist")
	}
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable (zero-rawBps skip), got %d", s.phase)
	}
	if s.lastStep != 0 {
		t.Errorf("expected lastStep=0 (no probe-down), got %d", s.lastStep)
	}
	if s.probeBaseline != 0 {
		t.Errorf("expected probeBaseline=0 (not set to rawBps=0), got %d", s.probeBaseline)
	}
}

// TestConvergence_ProbeDown_ProceedsOnPositiveRawBps is the control test for
// the zero-rawBps guard: with positive rawBps, probe-down fires normally.
func TestConvergence_ProbeDown_ProceedsOnPositiveRawBps(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_positive_rawbps"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true,
				CompletedLength: 500 * 1024 * 1024, TotalLength: 1024 * 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(6, 2*1024*1024),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// prevCompleted=480MB, CompletedLength=500MB → +20MB/5s = 4MB/s > 0.
	// bestEff set high so Probe-Up trigger (newEff >= bestEff*0.95) is false.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.probeMomentum = true
	s.probeCooldown = 0
	s.peakWorkers = 6
	s.bestEff = 10 * 1024 * 1024
	s.prevCompleted = 480 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	// Call processTask directly — ct.tick() would reset state when ScaleWorkers
	// returns 0 (test engine has no real pool).
	ps, ok := ct.processTask(tracker.tasks[0], false, nil)
	if !ok || ps.delta >= 0 {
		t.Fatalf("expected probe-down (delta<0), got ok=%v delta=%d", ok, ps.delta)
	}

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state to exist")
	}
	if s.phase != phaseSettling {
		t.Errorf("expected phase=phaseSettling (probe-down fired), got %d", s.phase)
	}
	if s.lastStep == 0 {
		t.Error("expected lastStep>0 (probe-down executed)")
	}
}

// TestConvergence_ProbeDown_CooldownDecrementsWhenNotProbing verifies that the
// cooldown decrement runs normally when shouldProbe is false. With cooldown=2
// and momentum=false, the else branch decrements cooldown to 1 and no probe
// fires, so the zero-rawBps guard (inside `if shouldProbe`) is never reached.
func TestConvergence_ProbeDown_CooldownDecrementsWhenNotProbing(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_zero_cooldown"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true,
				CompletedLength: 500 * 1024 * 1024, TotalLength: 1024 * 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(6, 0),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// cooldown=2, momentum=false → shouldProbe=false (cooldown not yet 0).
	// The zero-rawBps guard is inside `if shouldProbe`, so it is not reached.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.probeMomentum = false
	s.probeCooldown = 2
	s.peakWorkers = 6
	s.bestEff = 2 * 1024 * 1024
	s.prevCompleted = 500 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state to exist")
	}
	if s.probeCooldown != 1 {
		t.Errorf("expected probeCooldown=1 (decremented), got %d", s.probeCooldown)
	}
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable, got %d", s.phase)
	}
	if s.lastStep != 0 {
		t.Errorf("expected lastStep=0 (no probe-down), got %d", s.lastStep)
	}
}

// TestConvergence_ProbeDown_ZeroRawBpsColdCycleSkip verifies the zero-rawBps
// guard on the cold probe-down path (momentum=false, cooldown=0). Here the else
// branch runs: cooldown is already 0 so it is not decremented, shouldProbe
// becomes true, and the guard fires — skipping the probe-down to avoid the
// probeBaseline=0 dead zone. This mirrors the production scenario where a task
// dies long after momentum has expired, so the cold-cycle path is taken.
func TestConvergence_ProbeDown_ZeroRawBpsColdCycleSkip(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_zero_cold_cycle"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true,
				CompletedLength: 500 * 1024 * 1024, TotalLength: 1024 * 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeWorkers(6, 0),
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)
	ct := newTestConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// momentum=false, cooldown=0 → else branch: cooldown already 0 (no
	// decrement), shouldProbe=true. rawBps=0 (prevCompleted == CompletedLength)
	// so the zero-rawBps guard fires and skips the probe-down.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.phase = phaseStable
	s.probeMomentum = false
	s.probeCooldown = 0
	s.peakWorkers = 6
	s.bestEff = 2 * 1024 * 1024
	s.prevCompleted = 500 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ct.tick()

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state to exist")
	}
	if s.phase != phaseStable {
		t.Errorf("expected phase=phaseStable (zero-rawBps skip), got %d", s.phase)
	}
	if s.lastStep != 0 {
		t.Errorf("expected lastStep=0 (no probe-down), got %d", s.lastStep)
	}
	if s.probeBaseline != 0 {
		t.Errorf("expected probeBaseline=0 (not set to rawBps=0), got %d", s.probeBaseline)
	}
	if s.probeCooldown != 0 {
		t.Errorf("expected probeCooldown=0 (unchanged), got %d", s.probeCooldown)
	}
}
