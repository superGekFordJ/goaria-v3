package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

func TestConvergence_LastRawBps_MissingGID(t *testing.T) {
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		&mockTracker{},
		&mockTelemetry{data: map[string][]types.WorkerSnapshot{}},
	)
	defer ct.Stop()

	bps, ready := ct.LastRawBps("sg_missing")
	if ready || bps != 0 {
		t.Errorf("missing gid: LastRawBps=(%d,%v), want (0,false)", bps, ready)
	}

	var nilCT *ConvergenceTicker
	bps, ready = nilCT.LastRawBps("sg_x")
	if ready || bps != 0 {
		t.Errorf("nil receiver: LastRawBps=(%d,%v), want (0,false)", bps, ready)
	}
}

func TestConvergence_MacroReady_SampledZero(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_macro_zero"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", CompletedLength: 50 * 1024 * 1024, MinChunk: 1024 * 1024},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: makeChunkWorkers(4, 2*1024*1024, []int64{10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024}),
		},
	}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry,
	)
	defer ct.Stop()

	ct.tick()

	// Second window with ΔCL=0 → rawBps=0 but macroReady must latch.
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	ct.tick()

	bps, ready := ct.LastRawBps(gid)
	if !ready || bps != 0 {
		t.Errorf("sampled zero: LastRawBps=(%d,%v), want (0,true)", bps, ready)
	}
}

// TestConvergence_MacroReady_SurvivesWindowInvalidate verifies the local
// contract: a dirty membership tick still runs D2, so lastRawBps/macroReady
// are refreshed — never reset. A real zero increment publishes a measured
// zero with ready=true through the same dirty window.
func TestConvergence_MacroReady_SurvivesWindowInvalidate(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_macro_inv_1"
	gid2 := "sg_macro_inv_2"
	gid3 := "sg_macro_inv_3"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "env1", Domain: "example.com", CompletedLength: 10 * 1024 * 1024, MinChunk: 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "env1", Domain: "example.com", CompletedLength: 10 * 1024 * 1024, MinChunk: 1024 * 1024,
				TelemetryBps: 10 * 1024 * 1024, TelemetryValid: true},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeChunkWorkers(4, 2*1024*1024, []int64{10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024}),
			gid2: makeChunkWorkers(4, 2*1024*1024, []int64{10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024}),
		},
	}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry,
	)
	defer ct.Stop()

	t0 := time.Now()
	ct.tickAt(t0)
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.tickAt(t0.Add(5 * time.Second))

	bps, ready := ct.LastRawBps(gid1)
	if !ready || bps <= 0 {
		t.Fatalf("pre-invalidate: LastRawBps=(%d,%v), want (bps>0,true)", bps, ready)
	}

	// Drop gid2 (10MB occupancy) → E≥margin → dirty tick for gid1. D2 still
	// publishes: (70-60)MB / 5s — the measurement survives verdict cancel.
	tracker.tasks[0].CompletedLength = 70 * 1024 * 1024
	tracker.tasks = tracker.tasks[:1]
	delete(telemetry.data, gid2)
	ct.tickAt(t0.Add(10 * time.Second))

	ct.mu.Lock()
	s := ct.states[gid1]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state after invalidate")
	}
	if s.lastRawBps != 2*1024*1024 || !s.macroReady {
		t.Errorf("after dirty tick: lastRawBps=%d macroReady=%v, want (%d,true) — D2 must not be suppressed",
			s.lastRawBps, s.macroReady, 2*1024*1024)
	}
	bps, ready = ct.LastRawBps(gid1)
	if !ready || bps != 2*1024*1024 {
		t.Errorf("after dirty tick: LastRawBps=(%d,%v), want (%d,true)", bps, ready, 2*1024*1024)
	}

	// Real zero increment through a second dirty window: gid3 enters with
	// occupancy → E>0 → gid1 dirty; completed unchanged → measured zero.
	tracker.tasks = append(tracker.tasks, TrackedTaskInfo{
		GID: gid3, Status: "active", Scope: "wan", EnvKey: "env1", Domain: "example.com",
		CompletedLength: 5 * 1024 * 1024, MinChunk: 1024 * 1024,
		TelemetryBps: 10 * 1024 * 1024, TelemetryValid: true,
	})
	telemetry.data[gid3] = makeChunkWorkers(4, 2*1024*1024, []int64{10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024, 10 * 1024 * 1024})
	ct.tickAt(t0.Add(15 * time.Second))

	bps, ready = ct.LastRawBps(gid1)
	if !ready || bps != 0 {
		t.Errorf("zero-increment dirty tick: LastRawBps=(%d,%v), want (0,true)", bps, ready)
	}
}

func TestConvergence_MacroReady_ClearedOnRemoveTask(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_macro_remove"
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
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry,
	)
	defer ct.Stop()

	ct.tick()
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.mu.Lock()
	if s, ok := ct.states[gid]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	ct.tick()

	if _, ready := ct.LastRawBps(gid); !ready {
		t.Fatal("expected ready before RemoveTask")
	}

	ct.RemoveTask(gid)
	bps, ready := ct.LastRawBps(gid)
	if ready || bps != 0 {
		t.Errorf("after RemoveTask: LastRawBps=(%d,%v), want (0,false)", bps, ready)
	}
}

func TestConvergence_Blackout_PreservesLastRawBpsAcrossWindowInvalidate(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_blackout_occ_1"
	gid2 := "sg_blackout_occ_2"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{
				GID: gid1, Status: "active", Scope: "wan", EnvKey: "env1", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 100 * 1024 * 1024, MinChunk: 1024 * 1024,
			},
			{
				GID: gid2, Status: "active", Scope: "wan", EnvKey: "env1", Domain: "example.com",
				IsKeepAlive: true, CompletedLength: 50 * 1024 * 1024, MinChunk: 1024 * 1024,
			},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid1: makeChunkWorkers(4, 2*1024*1024, []int64{256 * 1024, 256 * 1024, 256 * 1024, 256 * 1024}),
			gid2: makeChunkWorkers(4, 2*1024*1024, []int64{1024 * 1024, 1024 * 1024, 1024 * 1024, 1024 * 1024}),
		},
	}
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry,
	)
	defer ct.Stop()

	// Trigger blackout on gid1 with a computable finalRawBps.
	ct.mu.Lock()
	s := ct.getOrCreateState(gid1)
	s.prevCompleted = 90 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()
	ct.tick()

	preBps, ready := ct.LastRawBps(gid1)
	if !ready || preBps <= 0 {
		t.Fatalf("pre-invalidate blackout: LastRawBps=(%d,%v), want (bps>0,true)", preBps, ready)
	}
	ct.mu.Lock()
	if s = ct.states[gid1]; s == nil || !s.blackout {
		ct.mu.Unlock()
		t.Fatal("expected blackout=true before invalidate")
	}
	ct.mu.Unlock()

	// Drop gid2 → active-set change. Blackout occupancy must survive.
	tracker.tasks = []TrackedTaskInfo{tracker.tasks[0]}
	delete(telemetry.data, gid2)
	ct.tick()

	bps, ready := ct.LastRawBps(gid1)
	if !ready {
		t.Fatal("after invalidate: macroReady cleared; want preserved")
	}
	if bps != preBps {
		t.Errorf("after invalidate: LastRawBps=%d, want preserved %d (blackout carve-out)", bps, preBps)
	}
	if bps == 0 {
		t.Error("after invalidate: blackout occupancy zeroed; contradicts SPEC-180")
	}
}

func TestConvergence_Blackout_WritesLastRawBpsAndMacroReady(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid := "sg_blackout_macro"
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
	ct := newTestConvergenceTicker(
		rpc.NewHybridEngine(&rpc.Aria2Engine{}, rpc.NewSurgeEngineForTesting(nil)),
		tracker, telemetry,
	)
	defer ct.Stop()

	ct.mu.Lock()
	s := ct.getOrCreateState(gid)
	s.prevCompleted = 90 * 1024 * 1024
	setPrevSampleAgoState(s, 5*time.Second)
	ct.mu.Unlock()

	ct.tick()

	bps, ready := ct.LastRawBps(gid)
	if !ready {
		t.Fatal("expected macroReady=true after blackout trigger with computable finalRawBps")
	}
	if bps < 0 {
		t.Errorf("lastRawBps=%d, want >= 0", bps)
	}
	// ΔCL = 10MiB / ~5s ≈ 2MiB/s
	if bps == 0 {
		t.Error("expected nonzero finalRawBps from 10MiB delta over 5s")
	}

	ct.mu.Lock()
	s = ct.states[gid]
	ct.mu.Unlock()
	if s == nil || !s.blackout {
		t.Fatal("expected blackout=true")
	}
	if s.lastRawBps != bps || !s.macroReady {
		t.Errorf("state lastRawBps=%d macroReady=%v, want lastRawBps=%d macroReady=true",
			s.lastRawBps, s.macroReady, bps)
	}
}
