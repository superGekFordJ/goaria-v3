package smartthread

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

// mockTracker implements TrackerProvider for testing
type mockTracker struct {
	tasks []TrackedTaskInfo
}

func (m *mockTracker) GetActiveTrackedTasks() []TrackedTaskInfo {
	return m.tasks
}

func (m *mockTracker) GetScope(gid string) (scope, domain string, ok bool) {
	for _, t := range m.tasks {
		if t.GID == gid {
			return t.Scope, t.Domain, true
		}
	}
	return "", "", false
}

// mockTelemetry implements TelemetryProvider for testing
type mockTelemetry struct {
	data map[string][]types.WorkerSnapshot
}

func (m *mockTelemetry) Get(gid string) []types.WorkerSnapshot {
	return m.data[gid]
}

func TestConvergenceTicker_ScaleDownOnLowThroughput(t *testing.T) {
	speedstats.ResetRecordsForTest()
	// Seed speedstats so GetRecentPeakByScope returns a valid vThreadAvg
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_test123"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 100 * 1024},
				{WorkerID: 1, EMASpeed: 100 * 1024},
				{WorkerID: 2, EMASpeed: 100 * 1024},
				{WorkerID: 3, EMASpeed: 100 * 1024},
			},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := NewConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	for i := 0; i < scaleDownStableCycles; i++ {
		ct.tick()
	}

	ct.mu.Lock()
	s, exists := ct.states[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist")
	}
	if s.scaleDownCycles != 0 {
		t.Errorf("expected scaleDownCycles=0 after triggering, got %d", s.scaleDownCycles)
	}
	if s.releaseCycles != 1 {
		t.Errorf("expected releaseCycles=1 after one scale-down, got %d", s.releaseCycles)
	}
}

func TestConvergenceTicker_NoTelemetryNoOp(t *testing.T) {
	gid := "sg_notelemetry"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com"},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := NewConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	_, exists := ct.states[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected no convergence state when telemetry is nil")
	}
}

func TestConvergenceTicker_NonSurgeGidSkipped(t *testing.T) {
	gid := "ar_aria2task"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com"},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := NewConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	_, exists := ct.states[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected no convergence state for non-sg GID")
	}
}

func TestConvergenceTicker_RemoveTask(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_remove"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com"},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {
				{WorkerID: 0, EMASpeed: 100 * 1024},
				{WorkerID: 1, EMASpeed: 100 * 1024},
			},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := NewConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	ct.tick()

	ct.mu.Lock()
	_, exists := ct.states[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist after tick")
	}

	ct.RemoveTask(gid)

	ct.mu.Lock()
	_, exists = ct.states[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected convergence state to be removed")
	}
}

func TestConvergenceTicker_StartStop(t *testing.T) {
	tracker := &mockTracker{}
	telemetry := &mockTelemetry{data: map[string][]types.WorkerSnapshot{}}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := NewConvergenceTicker(he, tracker, telemetry)

	ct.Start()
	time.Sleep(10 * time.Millisecond)
	ct.Stop()
}

func TestConvergenceState_LaggedFiltering(t *testing.T) {
	s := &convergenceState{}

	for i := 0; i < scaleDownStableCycles-1; i++ {
		s.scaleDownCycles++
	}
	if s.scaleDownCycles != scaleDownStableCycles-1 {
		t.Fatalf("expected %d cycles, got %d", scaleDownStableCycles-1, s.scaleDownCycles)
	}

	s.scaleDownCycles++
	if s.scaleDownCycles != scaleDownStableCycles {
		t.Fatalf("expected %d cycles, got %d", scaleDownStableCycles, s.scaleDownCycles)
	}
}

func TestConvergenceTicker_SelfCleanupStaleStates(t *testing.T) {
	speedstats.ResetRecordsForTest()
	speedstats.AddRecordV2(2*1024*1024, 1, 10*1024*1024, false, 50, "example.com", "wan")

	gid := "sg_stale_test"
	tracker := &mockTracker{
		tasks: []TrackedTaskInfo{
			{GID: gid, Status: "active", Scope: "wan", Domain: "example.com", IsKeepAlive: true},
		},
	}
	telemetry := &mockTelemetry{
		data: map[string][]types.WorkerSnapshot{
			gid: {{WorkerID: 0, EMASpeed: 100 * 1024}},
		},
	}

	aria2 := &rpc.Aria2Engine{}
	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(aria2, surge)

	ct := NewConvergenceTicker(he, tracker, telemetry)
	defer ct.Stop()

	// First tick: creates a state entry for gid
	ct.tick()
	ct.mu.Lock()
	_, exists := ct.states[gid]
	ct.mu.Unlock()
	if !exists {
		t.Fatal("expected convergence state to exist after first tick")
	}

	// Remove task from active list — simulate task disappearing from engine
	tracker.tasks = nil
	telemetry.data = nil

	// Second tick: self-cleanup should remove the stale state
	ct.tick()
	ct.mu.Lock()
	_, exists = ct.states[gid]
	ct.mu.Unlock()
	if exists {
		t.Error("expected stale convergence state to be cleaned up by self-cleanup")
	}
}
