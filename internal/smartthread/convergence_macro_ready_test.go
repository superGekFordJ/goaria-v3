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

func TestConvergence_MacroReady_SurvivesWindowInvalidate(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	gid1 := "sg_macro_inv_1"
	gid2 := "sg_macro_inv_2"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid1, Status: "active", Scope: "wan", EnvKey: "env1", Domain: "example.com", CompletedLength: 10 * 1024 * 1024, MinChunk: 1024 * 1024},
			{GID: gid2, Status: "active", Scope: "wan", EnvKey: "env1", Domain: "example.com", CompletedLength: 10 * 1024 * 1024, MinChunk: 1024 * 1024},
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

	ct.tick()
	tracker.tasks[0].CompletedLength = 60 * 1024 * 1024
	ct.mu.Lock()
	if s, ok := ct.states[gid1]; ok {
		setPrevSampleAgoState(s, 5*time.Second)
	}
	ct.mu.Unlock()
	ct.tick()

	bps, ready := ct.LastRawBps(gid1)
	if !ready || bps <= 0 {
		t.Fatalf("pre-invalidate: LastRawBps=(%d,%v), want (bps>0,true)", bps, ready)
	}

	// Drop gid2 → active-set change clears lastRawBps but must keep macroReady.
	tracker.tasks = []TrackedTaskInfo{tracker.tasks[0]}
	delete(telemetry.data, gid2)
	ct.tick()

	ct.mu.Lock()
	s := ct.states[gid1]
	ct.mu.Unlock()
	if s == nil {
		t.Fatal("expected state after invalidate")
	}
	if s.lastRawBps != 0 {
		t.Errorf("after invalidate: lastRawBps=%d, want 0", s.lastRawBps)
	}
	if !s.macroReady {
		t.Error("after invalidate: macroReady cleared; must survive window invalidate")
	}
	bps, ready = ct.LastRawBps(gid1)
	if !ready || bps != 0 {
		t.Errorf("after invalidate: LastRawBps=(%d,%v), want (0,true)", bps, ready)
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
