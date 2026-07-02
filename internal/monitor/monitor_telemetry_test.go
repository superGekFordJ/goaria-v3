package monitor

import (
	"sync"
	"testing"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/download"
	"goaria-v3/internal/surge/engine/types"
)

func TestTelemetryCache_SetGet(t *testing.T) {
	tc := NewTelemetryCache()

	stats := []types.WorkerSnapshot{
		{WorkerID: 0, EMASpeed: 1000, RetryCount: 1},
		{WorkerID: 1, EMASpeed: 2000, RetryCount: 0},
	}
	tc.Set("sg_abc", stats)

	got := tc.Get("sg_abc")
	if len(got) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(got))
	}
	if got[0].WorkerID != 0 || got[1].WorkerID != 1 {
		t.Errorf("unexpected worker IDs: %d, %d", got[0].WorkerID, got[1].WorkerID)
	}

	// Verify it's a copy
	got[0].EMASpeed = 999
	again := tc.Get("sg_abc")
	if again[0].EMASpeed != 1000 {
		t.Error("Get returned a reference, not a copy")
	}
}

func TestTelemetryCache_GetMissing(t *testing.T) {
	tc := NewTelemetryCache()
	if got := tc.Get("nonexistent"); got != nil {
		t.Errorf("expected nil for missing GID, got %v", got)
	}
}

func TestTelemetryCache_Remove(t *testing.T) {
	tc := NewTelemetryCache()
	tc.Set("sg_1", []types.WorkerSnapshot{{WorkerID: 0}})
	tc.Set("sg_2", []types.WorkerSnapshot{{WorkerID: 1}})

	tc.Remove("sg_1")

	if tc.Get("sg_1") != nil {
		t.Error("expected nil after Remove")
	}
	if tc.Get("sg_2") == nil {
		t.Error("expected sg_2 to still exist")
	}
}

func TestTelemetryCache_Clear(t *testing.T) {
	tc := NewTelemetryCache()
	tc.Set("sg_1", []types.WorkerSnapshot{{WorkerID: 0}})
	tc.Set("sg_2", []types.WorkerSnapshot{{WorkerID: 1}})

	tc.Clear()

	if tc.Get("sg_1") != nil || tc.Get("sg_2") != nil {
		t.Error("expected all entries cleared")
	}
}

func TestTelemetryCache_ActiveGIDs(t *testing.T) {
	tc := NewTelemetryCache()
	tc.Set("sg_a", []types.WorkerSnapshot{{WorkerID: 0}})
	tc.Set("sg_b", []types.WorkerSnapshot{{WorkerID: 1}})

	gids := tc.ActiveGIDs()
	if len(gids) != 2 {
		t.Fatalf("expected 2 active GIDs, got %d", len(gids))
	}

	// Verify both GIDs are present
	found := map[string]bool{}
	for _, g := range gids {
		found[g] = true
	}
	if !found["sg_a"] || !found["sg_b"] {
		t.Errorf("expected sg_a and sg_b, got %v", gids)
	}
}

func TestTelemetryCache_EmptyStatsReturnsNil(t *testing.T) {
	tc := NewTelemetryCache()
	tc.Set("sg_empty", []types.WorkerSnapshot{})

	if got := tc.Get("sg_empty"); got != nil {
		t.Errorf("expected nil for empty stats, got %v", got)
	}
}

func TestTelemetryCache_TimestampTracked(t *testing.T) {
	tc := NewTelemetryCache()
	tc.Set("sg_ts", []types.WorkerSnapshot{{WorkerID: 0}})

	ts, ok := tc.GetTimestamp("sg_ts")
	if !ok {
		t.Fatal("expected timestamp to be tracked")
	}
	if ts.IsZero() {
		t.Error("expected non-zero timestamp")
	}

	tc.Remove("sg_ts")
	if _, ok := tc.GetTimestamp("sg_ts"); ok {
		t.Error("expected timestamp to be removed")
	}
}

// --- collectTelemetry path tests ---

// TestCollectTelemetry_NonHybridEngine verifies that collectTelemetry is a no-op
// when the engine is not a *rpc.HybridEngine.
func TestCollectTelemetry_NonHybridEngine(t *testing.T) {
	m := &Monitor{
		engine:    &rpc.Aria2Engine{},
		telemetry: NewTelemetryCache(),
	}

	// Should not panic and should not populate telemetry
	m.collectTelemetry()

	if got := m.telemetry.Get("sg_123"); got != nil {
		t.Errorf("expected nil telemetry for non-HybridEngine, got %v", got)
	}
}

// TestCollectTelemetry_NonSurgeGids verifies that Aria2 GIDs (ar_ prefix) are skipped.
func TestCollectTelemetry_NonSurgeGids(t *testing.T) {
	se := &rpc.SurgeEngine{}
	he := rpc.NewHybridEngine(&rpc.Aria2Engine{}, se)

	m := &Monitor{
		engine:    he,
		telemetry: NewTelemetryCache(),
	}

	// Seed Cache with ar_ tasks only (no sg_ tasks)
	Cache.arActive = []rpc.Task{
		{GID: "ar_123", Status: "active"},
		{GID: "ar_456", Status: "active"},
	}
	defer func() { Cache.arActive = nil }()

	m.collectTelemetry()

	// No sg_ GIDs → no telemetry entries
	if gids := m.telemetry.ActiveGIDs(); len(gids) != 0 {
		t.Errorf("expected 0 telemetry entries for Aria2-only GIDs, got %v", gids)
	}
}

// TestCollectTelemetry_SurgeGidsPopulated verifies that Surge GIDs are collected
// from the SurgeEngine and populated in the telemetry cache.
func TestCollectTelemetry_SurgeGidsPopulated(t *testing.T) {
	// Build a real SurgeEngine with a mock pool containing an active download
	state := types.NewProgressState("test-dl", 1024*1024)
	state.SetWorkerStats([]types.WorkerSnapshot{
		{WorkerID: 0, EMASpeed: 500000, ChunkStart: 0, ChunkOffset: 512, ChunkLength: 1024},
	})

	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{
		"abc": {ID: "abc", State: state},
	})

	se := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(&rpc.Aria2Engine{}, se)

	m := &Monitor{
		engine:    he,
		telemetry: NewTelemetryCache(),
	}

	// Seed Cache with sg_ and ar_ tasks
	Cache.sgActive = []rpc.Task{
		{GID: "sg_abc", Status: "active"},
	}
	Cache.arActive = []rpc.Task{
		{GID: "ar_xyz", Status: "active"},
	}
	defer func() { Cache.sgActive = nil; Cache.arActive = nil }()

	m.collectTelemetry()

	// sg_abc should have telemetry
	got := m.telemetry.Get("sg_abc")
	if got == nil {
		t.Fatal("expected telemetry for sg_abc, got nil")
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 worker snapshot, got %d", len(got))
	}
	if got[0].WorkerID != 0 {
		t.Errorf("WorkerID = %d, want 0", got[0].WorkerID)
	}

	// ar_xyz should not have telemetry
	if g := m.telemetry.Get("ar_xyz"); g != nil {
		t.Errorf("expected no telemetry for ar_xyz, got %v", g)
	}
}

// TestCollectTelemetry_RemovesStaleEntries verifies that telemetry for GIDs
// no longer in the active list is removed, while active GIDs with stats are retained.
func TestCollectTelemetry_RemovesStaleEntries(t *testing.T) {
	// Build a real pool with an active download for "active" GID
	state := types.NewProgressState("active-dl", 1024*1024)
	state.SetWorkerStats([]types.WorkerSnapshot{{WorkerID: 0, EMASpeed: 1000}})

	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{
		"active": {ID: "active", State: state},
	})

	se := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(&rpc.Aria2Engine{}, se)

	m := &Monitor{
		engine:    he,
		telemetry: NewTelemetryCache(),
	}

	// Pre-populate with a stale entry that won't be in the active list
	m.telemetry.Set("sg_stale", []types.WorkerSnapshot{{WorkerID: 0}})

	// Seed Cache with sg_active but not sg_stale
	Cache.sgActive = []rpc.Task{
		{GID: "sg_active", Status: "active"},
	}
	defer func() { Cache.sgActive = nil }()

	m.collectTelemetry()

	// sg_stale should be removed
	if g := m.telemetry.Get("sg_stale"); g != nil {
		t.Errorf("expected stale telemetry to be removed, got %v", g)
	}

	// sg_active should be retained with data
	got := m.telemetry.Get("sg_active")
	if got == nil {
		t.Fatal("expected telemetry for sg_active to be retained, got nil")
	}
	if len(got) != 1 || got[0].WorkerID != 0 {
		t.Errorf("unexpected telemetry for sg_active: %v", got)
	}
}

// TestCollectTelemetry_ActiveButNilStats_StaleRemoved verifies that an active
// Surge GID whose GetWorkerStats returns nil (e.g., single-thread fallback after
// SessionReset) has its stale telemetry cleared to prevent the convergence ticker
// from acting on outdated worker snapshots.
func TestCollectTelemetry_ActiveButNilStats_StaleRemoved(t *testing.T) {
	// Pool has no download for "ghost" GID → GetWorkerStats returns nil
	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{})

	se := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(&rpc.Aria2Engine{}, se)

	m := &Monitor{
		engine:    he,
		telemetry: NewTelemetryCache(),
	}

	// Pre-populate telemetry for sg_ghost (simulates stale data from concurrent phase)
	m.telemetry.Set("sg_ghost", []types.WorkerSnapshot{{WorkerID: 0, EMASpeed: 500}})

	// Seed Cache with sg_ghost as active
	Cache.sgActive = []rpc.Task{
		{GID: "sg_ghost", Status: "active"},
	}
	defer func() { Cache.sgActive = nil }()

	m.collectTelemetry()

	// sg_ghost is active but has nil stats → stale telemetry should be removed
	if g := m.telemetry.Get("sg_ghost"); g != nil {
		t.Error("expected stale telemetry for active sg_ghost to be removed when stats are nil, got data")
	}
}

// TestCollectTelemetry_ConcurrentWithRemove exercises concurrent collectTelemetry
// (Set/ActiveGIDs/Remove) and direct telemetry Remove (as InvalidateTask does)
// to verify no data race under -race.
func TestCollectTelemetry_ConcurrentWithRemove(t *testing.T) {
	state := types.NewProgressState("race-dl", 1024*1024)
	state.SetWorkerStats([]types.WorkerSnapshot{{WorkerID: 0, EMASpeed: 1000}})

	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{
		"race": {ID: "race", State: state},
	})

	se := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(&rpc.Aria2Engine{}, se)

	m := &Monitor{
		engine:    he,
		telemetry: NewTelemetryCache(),
	}

	// Seed Cache with sg_ tasks
	Cache.sgActive = []rpc.Task{
		{GID: "sg_race", Status: "active"},
		{GID: "sg_other", Status: "active"},
	}
	defer func() { Cache.sgActive = nil }()

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: repeatedly call collectTelemetry
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			m.collectTelemetry()
		}
	}()

	// Goroutine 2: repeatedly remove entries (simulating InvalidateTask telemetry cleanup)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			m.telemetry.Remove("sg_race")
			m.telemetry.Remove("sg_other")
			_ = m.telemetry.Get("sg_race")
		}
	}()

	wg.Wait()
}
