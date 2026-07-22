package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

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
	// Tolerance: dt may be 5s + small epsilon from real time.Now() calls.
	if rec.peak < 10_480_000 || rec.peak > 10*1024*1024 {
		t.Fatalf("expected recorded peak≈10MB/s, got %d", rec.peak)
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 0)

	// Clear any leftover N_max from previous tests (global singleton).
	ct.limits.Clear("wan|example.com")

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
	ct.limits.SetNMax("wan|example.com", 8)

	// rawBps = 10 MB/s, 8 workers. Probe-Down fires (step=1, delta=-1).
	ps, ok := probeUpProcess(ct, tracker, telemetry, gid, 60*1024*1024, 8)
	if !ok || ps.delta >= 0 {
		t.Fatalf("expected probe-down (delta<0), got ok=%v delta=%d", ok, ps.delta)
	}
	rec, ok := recorder.records[gid]
	if !ok {
		t.Fatal("expected RecordPeakEfficiency called before probe-down return")
	}
	// Tolerance: dt may be 5s + small epsilon from real time.Now() calls.
	if rec.peak < 10_480_000 || rec.peak > 10*1024*1024 {
		t.Fatalf("expected recorded peak≈10MB/s, got %d", rec.peak)
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 0)

	// Clear any leftover N_max from previous tests (global singleton).
	ct.limits.Clear("wan|example.com")

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

	// Trigger Probe-Up with rawBps comfortably above the 0.95 threshold.
	// bestEff = 12.5 MB/s per worker; bestEff*0.95 = 11.875 MB/s = 12,451,840.
	// Use rawBps = 97 MB/s → newEff = 12.125 MB/s > 11.875 with ~2% headroom,
	// robust against dt = 5s + small epsilon from real time.Now() calls.
	// delta = 97MB*5 = 485MB. prevCompleted=10MB. CompletedLength=495MB.
	tracker.tasks[0].CompletedLength = 495 * 1024 * 1024
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
	// The monotonic mock should retain 100 MB/s, not overwrite with 97 MB/s
	if rec.peak != 100*1024*1024 {
		t.Fatalf("expected monotonic ratchet to retain 100MB/s peak, got %d", rec.peak)
	}
}
