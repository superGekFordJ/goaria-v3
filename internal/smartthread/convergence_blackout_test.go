package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 0)
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
	ct := NewConvergenceTicker(he, tracker, telemetry, recorder, &mockRateChecker{}, 0, 0)
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
