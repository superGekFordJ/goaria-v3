package rpc

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/types"
)

func TestSurgeEngine_MapStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"downloading", "active"},
		{"pausing", "paused"},
		{"paused", "paused"},
		{"queued", "waiting"},
		{"completed", "complete"},
		{"error", "error"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		got := mapStatus(tt.input)
		if got != tt.expected {
			t.Errorf("mapStatus(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// TestAddUri_SplitMinSplitSize_Passthrough is an integration test verifying
// that AddUri correctly maps options.Split → DownloadRequest.Workers and
// options.MinSplitSize → DownloadRequest.MinChunkSize through the full
// Enqueue → addFunc → local_service.add chain.
func TestAddUri_SplitMinSplitSize_Passthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "104857600")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	engine := NewSurgeEngine()
	defer engine.Close()

	outputDir := t.TempDir()
	gid, err := engine.AddUri(srv.URL+"/file.bin", AddURIOptions{
		Dir:          outputDir,
		Out:          "file.bin",
		Split:        8,
		MinSplitSize: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("AddUri failed: %v", err)
	}

	cfg := findSurgeConfigByID(engine, gid)
	if cfg == nil {
		t.Fatal("expected config in pool")
	}

	if cfg.Runtime.Workers != 8 {
		t.Errorf("Runtime.Workers = %d, want 8 (from options.Split)", cfg.Runtime.Workers)
	}
	if cfg.Runtime.MinChunkSize != 4*1024*1024 {
		t.Errorf("Runtime.MinChunkSize = %d, want %d (from options.MinSplitSize)",
			cfg.Runtime.MinChunkSize, 4*1024*1024)
	}
}

// TestAddUri_ZeroSplitMinSplitSize_Defaults verifies that when Split and
// MinSplitSize are zero (non-smart-thread mode), the engine falls back to
// √size heuristic (Workers=0) and global default MinChunkSize.
func TestAddUri_ZeroSplitMinSplitSize_Defaults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "104857600")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	engine := NewSurgeEngine()
	defer engine.Close()

	outputDir := t.TempDir()
	gid, err := engine.AddUri(srv.URL+"/file.bin", AddURIOptions{
		Dir: outputDir,
		Out: "file.bin",
	})
	if err != nil {
		t.Fatalf("AddUri failed: %v", err)
	}

	cfg := findSurgeConfigByID(engine, gid)
	if cfg == nil {
		t.Fatal("expected config in pool")
	}

	if cfg.Runtime.Workers != 0 {
		t.Errorf("Runtime.Workers = %d, want 0 (use √size heuristic)", cfg.Runtime.Workers)
	}
	if cfg.Runtime.MinChunkSize != types.MinChunk {
		t.Errorf("Runtime.MinChunkSize = %d, want default %d", cfg.Runtime.MinChunkSize, types.MinChunk)
	}
}

func findSurgeConfigByID(e *SurgeEngine, id string) *types.DownloadRecord {
	pool := e.getScheduler()
	if pool == nil {
		return nil
	}
	for _, cfg := range pool.GetAll() {
		if cfg.ID == id {
			return &cfg
		}
	}
	return nil
}

func TestSurgeEngine_SetResumeParamsHook(t *testing.T) {
	engine := NewSurgeEngine()
	defer engine.Close()

	var called bool
	var gotWorkers int
	var gotMinChunk int64

	engine.SetResumeParamsHook(func(cfg *types.DownloadRecord) {
		called = true
		gotWorkers = cfg.Runtime.Workers
		gotMinChunk = cfg.Runtime.MinChunkSize
		cfg.Runtime.Workers = 12
		cfg.Runtime.MinChunkSize = 8 * 1024 * 1024
	})

	hooks := engine.manager.GetEngineHooks()
	if hooks.RecomputeResumeParams == nil {
		t.Fatal("RecomputeResumeParams hook not set")
	}

	cfg := &types.DownloadRecord{
		ID:      "test-resume",
		Runtime: &types.RuntimeConfig{Workers: 4, MinChunkSize: 1024},
	}
	hooks.RecomputeResumeParams(cfg)

	if !called {
		t.Fatal("hook was not called")
	}
	if gotWorkers != 4 {
		t.Errorf("hook received Workers=%d, want 4", gotWorkers)
	}
	if gotMinChunk != 1024 {
		t.Errorf("hook received MinChunkSize=%d, want 1024", gotMinChunk)
	}
	if cfg.Runtime.Workers != 12 {
		t.Errorf("after hook Workers=%d, want 12", cfg.Runtime.Workers)
	}
	if cfg.Runtime.MinChunkSize != 8*1024*1024 {
		t.Errorf("after hook MinChunkSize=%d, want 8MB", cfg.Runtime.MinChunkSize)
	}
}

func TestSurgeEngine_SetResumeParamsHook_NilHook_PreservesValues(t *testing.T) {
	engine := NewSurgeEngine()
	defer engine.Close()

	// Without setting any hook, RecomputeResumeParams should be nil
	hooks := engine.manager.GetEngineHooks()
	if hooks.RecomputeResumeParams != nil {
		t.Fatal("RecomputeResumeParams should be nil when no hook set")
	}

	// Simulate what the engine does: if hook is nil, skip (preserve saved values)
	cfg := &types.DownloadRecord{
		ID:      "test-resume-no-hook",
		Runtime: &types.RuntimeConfig{Workers: 6, MinChunkSize: 4 * 1024 * 1024},
	}
	if hooks.RecomputeResumeParams != nil {
		hooks.RecomputeResumeParams(cfg)
	}

	// Values should be unchanged
	if cfg.Runtime.Workers != 6 {
		t.Errorf("Workers = %d, want 6 (preserved, no hook)", cfg.Runtime.Workers)
	}
	if cfg.Runtime.MinChunkSize != 4*1024*1024 {
		t.Errorf("MinChunkSize = %d, want 4MB (preserved, no hook)", cfg.Runtime.MinChunkSize)
	}
}

func TestSurgeEngine_SetTightenOnPickupHook(t *testing.T) {
	pool := scheduler.NewSchedulerForTesting(nil)
	engine := NewSurgeEngineForTesting(pool)

	var called bool
	engine.SetTightenOnPickupHook(func(cfg *types.DownloadRecord) {
		called = true
		cfg.Runtime.Workers = 1
	})

	hooks := engine.manager.GetEngineHooks()
	if hooks.TightenOnPickup == nil {
		t.Fatal("TightenOnPickup hook not set on EngineHooks")
	}
	if !pool.TightenOnPickupInstalled() {
		t.Fatal("SetTightenOnPickupHook must sync callback onto scheduler")
	}

	cfg := &types.DownloadRecord{
		ID:      "test-tighten",
		Runtime: &types.RuntimeConfig{Workers: 9, MinChunkSize: 1024},
	}
	pool.CallTightenOnPickup(cfg)
	if !called {
		t.Fatal("scheduler bridge did not invoke hook")
	}
	if cfg.Runtime.Workers != 1 {
		t.Errorf("after scheduler invoke Workers=%d, want 1", cfg.Runtime.Workers)
	}
}

func TestSurgeEngine_SetTightenOnPickupHook_PreservesResume(t *testing.T) {
	engine := NewSurgeEngine()
	defer engine.Close()

	engine.SetResumeParamsHook(func(cfg *types.DownloadRecord) {
		cfg.Runtime.Workers = 12
	})
	engine.SetTightenOnPickupHook(func(cfg *types.DownloadRecord) {
		cfg.Runtime.Workers = 1
	})

	hooks := engine.manager.GetEngineHooks()
	if hooks.RecomputeResumeParams == nil {
		t.Fatal("SetTighten must RMW-preserve RecomputeResumeParams")
	}
	if hooks.TightenOnPickup == nil {
		t.Fatal("TightenOnPickup missing after set")
	}
	if pool := engine.getScheduler(); pool == nil || !pool.TightenOnPickupInstalled() {
		t.Fatal("TightenOnPickup must remain installed on scheduler after RMW")
	}

	cfg := &types.DownloadRecord{Runtime: &types.RuntimeConfig{Workers: 4}}
	hooks.RecomputeResumeParams(cfg)
	if cfg.Runtime.Workers != 12 {
		t.Errorf("Resume hook Workers=%d, want 12", cfg.Runtime.Workers)
	}
}

func TestSurgeEngine_SetResumeParamsHook_PreservesTighten(t *testing.T) {
	engine := NewSurgeEngine()
	defer engine.Close()

	engine.SetTightenOnPickupHook(func(cfg *types.DownloadRecord) {
		cfg.Runtime.Workers = 1
	})
	engine.SetResumeParamsHook(func(cfg *types.DownloadRecord) {
		cfg.Runtime.Workers = 12
	})

	hooks := engine.manager.GetEngineHooks()
	if hooks.TightenOnPickup == nil {
		t.Fatal("SetResumeParamsHook must RMW-preserve TightenOnPickup")
	}
	if pool := engine.getScheduler(); pool == nil || !pool.TightenOnPickupInstalled() {
		t.Fatal("scheduler TightenOnPickup must survive SetResumeParamsHook RMW")
	}
}

func TestSurgeEngine_SetTightenOnPickupHook_NilClears(t *testing.T) {
	pool := scheduler.NewSchedulerForTesting(nil)
	engine := NewSurgeEngineForTesting(pool)

	engine.SetTightenOnPickupHook(func(cfg *types.DownloadRecord) {})
	if !pool.TightenOnPickupInstalled() {
		t.Fatal("expected scheduler hook after set")
	}
	engine.SetTightenOnPickupHook(nil)
	hooks := engine.manager.GetEngineHooks()
	if hooks.TightenOnPickup != nil {
		t.Fatal("nil SetTightenOnPickupHook should clear EngineHooks field")
	}
	if pool.TightenOnPickupInstalled() {
		t.Fatal("nil SetTightenOnPickupHook must clear scheduler callback")
	}
	cfg := &types.DownloadRecord{Runtime: &types.RuntimeConfig{Workers: 7}}
	pool.CallTightenOnPickup(cfg)
	if cfg.Runtime.Workers != 7 {
		t.Fatalf("nil-cleared scheduler invoke mutated Workers to %d", cfg.Runtime.Workers)
	}
}

// TestSurgeEngine_KillWorker_Delegation verifies KillWorker delegates to the
// pool and routes to the correct ProgressState. Returns false for unknown ids.
func TestSurgeEngine_KillWorker_Delegation(t *testing.T) {
	var mu sync.Mutex
	var killedID int
	state := progress.New("dl-x", 1000)
	state.SetKillWorkerFn(func(workerID int) bool {
		mu.Lock()
		killedID = workerID
		mu.Unlock()
		return true
	})

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"dl-x": {ID: "dl-x", ProgressState: state},
	})
	engine := NewSurgeEngineForTesting(pool)

	if ok := engine.KillWorker("dl-x", 9); !ok {
		t.Error("KillWorker(dl-x, 9) = false, want true")
	}
	mu.Lock()
	got := killedID
	mu.Unlock()
	if got != 9 {
		t.Errorf("delegated workerID = %d, want 9", got)
	}
	if ok := engine.KillWorker("unknown", 1); ok {
		t.Error("KillWorker(unknown) = true, want false")
	}
}

// TestSurgeEngine_SetSlowWorkerThreshold_Delegation verifies
// SetSlowWorkerThreshold delegates to the pool and routes to the correct
// ProgressState. No-op for unknown ids.
func TestSurgeEngine_SetSlowWorkerThreshold_Delegation(t *testing.T) {
	var mu sync.Mutex
	var gotVal float64
	state := progress.New("dl-y", 1000)
	state.SetSetSlowThresholdFn(func(v float64) {
		mu.Lock()
		gotVal = v
		mu.Unlock()
	})

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"dl-y": {ID: "dl-y", ProgressState: state},
	})
	engine := NewSurgeEngineForTesting(pool)

	engine.SetSlowWorkerThreshold("dl-y", 0.0)
	mu.Lock()
	got := gotVal
	mu.Unlock()
	if got != 0.0 {
		t.Errorf("delegated threshold = %v, want 0", got)
	}
	// Unknown id must not panic.
	engine.SetSlowWorkerThreshold("unknown", 0.5)
}

func TestSurgeEngine_InvalidateListCache_ForcesFreshFetch(t *testing.T) {
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"dl-cache-1": {ID: "dl-cache-1"},
	})
	engine := NewSurgeEngineForTesting(pool)

	// Populate the cache
	first, err := engine.getDownloadList()
	if err != nil {
		t.Fatalf("first getDownloadList: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("expected at least one item in list")
	}

	// Cache hit: second call should return the same slice (same backing array)
	cached, err := engine.getDownloadList()
	if err != nil {
		t.Fatalf("cached getDownloadList: %v", err)
	}
	if len(cached) != len(first) {
		t.Errorf("cached len = %d, want %d (should return cached)", len(cached), len(first))
	}

	// Invalidate
	engine.InvalidateListCache()

	// After invalidation, the cache timestamp should be zero
	engine.listCacheMu.Lock()
	cacheAt := engine.listCacheAt
	engine.listCacheMu.Unlock()
	if !cacheAt.IsZero() {
		t.Errorf("listCacheAt = %v, want zero time after invalidation", cacheAt)
	}

	// Next call should fetch fresh data (not panic, return same count)
	fresh, err := engine.getDownloadList()
	if err != nil {
		t.Fatalf("fresh getDownloadList after invalidation: %v", err)
	}
	if len(fresh) != len(first) {
		t.Errorf("fresh len = %d, want %d", len(fresh), len(first))
	}
}

// TestNewSurgeEngine_WiresIsNameActiveAndEventBusProgress verifies D1/D3 wiring:
// IsNameActive inspects in-flight destinations, and Enqueue ProgressCh is EventBus.InputCh.
func TestNewSurgeEngine_WiresIsNameActiveAndEventBusProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "64")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	engine := NewSurgeEngine()
	defer engine.Close()

	dir := t.TempDir()
	gid, err := engine.AddUri(srv.URL+"/active.bin", AddURIOptions{
		Dir: dir,
		Out: "active.bin",
	})
	if err != nil {
		t.Fatalf("AddUri failed: %v", err)
	}

	if !engine.manager.IsNameActive(dir, "active.bin") {
		t.Fatal("expected IsNameActive after enqueue")
	}
	if engine.manager.IsNameActive(dir, "other.bin") {
		t.Fatal("did not expect unrelated name to be active")
	}
	if engine.manager.IsNameActive(t.TempDir(), "active.bin") {
		t.Fatal("did not expect same name in a different directory")
	}

	cfg := findSurgeConfigByID(engine, gid)
	if cfg == nil {
		t.Fatal("expected config in pool")
	}
	bus := engine.manager.GetEventBus()
	if bus == nil {
		t.Fatal("expected EventBus")
	}
	if cfg.ProgressCh == nil {
		t.Fatal("expected ProgressCh to be set")
	}
	if cfg.ProgressCh != bus.InputCh {
		t.Fatal("ProgressCh should be EventBus.InputCh (no orphan default channel)")
	}
}

// TestNewSurgeEngineForTesting_IsNameActiveRenamesMemoryOnlyCollision covers the
// in-memory-without-disk case that disk/.surge checks alone miss.
func TestNewSurgeEngineForTesting_IsNameActiveRenamesMemoryOnlyCollision(t *testing.T) {
	dir := t.TempDir()
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"seed": {
			ID:         "seed",
			Filename:   "memory.bin",
			OutputPath: dir,
		},
	})
	engine := NewSurgeEngineForTesting(pool)

	if !engine.manager.IsNameActive(dir, "memory.bin") {
		t.Fatal("expected seeded in-memory name to be active")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "64")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	gid, err := engine.AddUri(srv.URL+"/memory.bin", AddURIOptions{
		Dir: dir,
		Out: "memory.bin",
	})
	if err != nil {
		t.Fatalf("AddUri failed: %v", err)
	}
	cfg := findSurgeConfigByID(engine, gid)
	if cfg == nil {
		t.Fatal("expected enqueued config in pool")
	}
	if cfg.Filename != "memory(1).bin" {
		t.Fatalf("Filename = %q, want memory(1).bin (IsNameActive rename)", cfg.Filename)
	}
}

// TestSurgeEngine_GetRateLimit_ZeroUnlimitedNotLimited verifies limited==(bps>0):
// RateLimitSet=true with RateLimit=0 is not limited; positive cap is limited; missing is not.
func TestSurgeEngine_GetRateLimit_ZeroUnlimitedNotLimited(t *testing.T) {
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"unlimited-set": {
			ID:            "unlimited-set",
			RateLimit:     0,
			RateLimitSet:  true,
			ProgressState: progress.New("unlimited-set", 1000),
		},
		"capped": {
			ID:            "capped",
			RateLimit:     1_000_000,
			RateLimitSet:  true,
			ProgressState: progress.New("capped", 1000),
		},
	})
	engine := NewSurgeEngineForTesting(pool)

	bps, limited := engine.GetRateLimit("unlimited-set")
	if limited || bps != 0 {
		t.Errorf("explicit unlimited: GetRateLimit=(%d,%v), want (0,false)", bps, limited)
	}

	bps, limited = engine.GetRateLimit("capped")
	if !limited || bps != 1_000_000 {
		t.Errorf("positive cap: GetRateLimit=(%d,%v), want (1000000,true)", bps, limited)
	}

	bps, limited = engine.GetRateLimit("missing")
	if limited || bps != 0 {
		t.Errorf("missing: GetRateLimit=(%d,%v), want (0,false)", bps, limited)
	}
}

// TestSurgeEngine_buildDownloadList_ActiveErrorAfterDone verifies the B3b
// TOCTOU recheck: when Done=true and GetError() returns non-nil, the status
// must be "error" not "completed". Without the recheck, the switch block
// overrides GetStatus's "error" with "completed" because Done.Load() is true.
func TestSurgeEngine_buildDownloadList_ActiveErrorAfterDone(t *testing.T) {
	state := progress.New("toctou-id", 1000)
	state.Bytes.VerifiedProgress.Store(500)
	state.Done.Store(true)
	state.SetError(errors.New("worker failed after Done"))

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"toctou-id": {
			ID:            "toctou-id",
			URL:           "http://example.com/file.bin",
			Filename:      "file.bin",
			ProgressState: state,
		},
	})
	engine := NewSurgeEngineForTesting(pool)

	list, err := engine.getDownloadList()
	if err != nil {
		t.Fatalf("getDownloadList: %v", err)
	}

	var found *types.DownloadStatus
	for i := range list {
		if list[i].ID == "toctou-id" {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatal("entry not found in list")
	}

	if found.Status != "error" {
		t.Errorf("Status=%q, want error (TOCTOU recheck after Done)", found.Status)
	}
	if found.Error != "worker failed after Done" {
		t.Errorf("Error=%q, want 'worker failed after Done'", found.Error)
	}
}

// TestSurgeEngine_buildDownloadList_ErrorOverridesPausing verifies the B3b
// recheck overrides "pausing" when GetError() returns non-nil. Without the
// recheck, the switch block overrides GetStatus's "error" with "pausing".
func TestSurgeEngine_buildDownloadList_ErrorOverridesPausing(t *testing.T) {
	state := progress.New("toctou-pause-id", 1000)
	state.Bytes.VerifiedProgress.Store(500)
	state.SetPausing(true)
	state.SetError(errors.New("worker failed while pausing"))

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"toctou-pause-id": {
			ID:            "toctou-pause-id",
			URL:           "http://example.com/pause.bin",
			Filename:      "pause.bin",
			ProgressState: state,
		},
	})
	engine := NewSurgeEngineForTesting(pool)

	list, err := engine.getDownloadList()
	if err != nil {
		t.Fatalf("getDownloadList: %v", err)
	}

	var found *types.DownloadStatus
	for i := range list {
		if list[i].ID == "toctou-pause-id" {
			found = &list[i]
			break
		}
	}
	if found == nil {
		t.Fatal("entry not found in list")
	}

	if found.Status != "error" {
		t.Errorf("Status=%q, want error (recheck overrides pausing)", found.Status)
	}
	if found.Error != "worker failed while pausing" {
		t.Errorf("Error=%q, want 'worker failed while pausing'", found.Error)
	}
}
