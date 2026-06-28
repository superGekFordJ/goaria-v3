package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

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
	// D3 ratchet must run before the early-return: bestEff raised to ~newEff.
	// Tolerance: dt may be 5s + small epsilon from real time.Now() calls.
	if s.bestEff < 1_310_000 || s.bestEff > 1_310_720 {
		t.Fatalf("expected bestEff≈1310720 (D3 ratchet ran before early-return), got %d", s.bestEff)
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
