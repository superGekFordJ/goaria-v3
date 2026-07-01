package rpc

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"goaria-v3/internal/surge/core"
	"goaria-v3/internal/surge/download"
	"goaria-v3/internal/surge/engine/types"
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

func findSurgeConfigByID(e *SurgeEngine, id string) *types.DownloadConfig {
	for _, cfg := range e.service.Pool.GetAll() {
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

	engine.SetResumeParamsHook(func(cfg *types.DownloadConfig) {
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

	cfg := &types.DownloadConfig{
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
	cfg := &types.DownloadConfig{
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

// TestSurgeEngine_KillWorker_Delegation verifies KillWorker delegates to the
// pool and routes to the correct ProgressState. Returns false for unknown ids.
func TestSurgeEngine_KillWorker_Delegation(t *testing.T) {
	var mu sync.Mutex
	var killedID int
	state := types.NewProgressState("dl-x", 1000)
	state.SetKillWorkerFn(func(workerID int) bool {
		mu.Lock()
		killedID = workerID
		mu.Unlock()
		return true
	})

	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{
		"dl-x": {ID: "dl-x", State: state},
	})
	engine := &SurgeEngine{service: &core.LocalDownloadService{Pool: pool}}

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
	state := types.NewProgressState("dl-y", 1000)
	state.SetSetSlowThresholdFn(func(v float64) {
		mu.Lock()
		gotVal = v
		mu.Unlock()
	})

	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{
		"dl-y": {ID: "dl-y", State: state},
	})
	engine := &SurgeEngine{service: &core.LocalDownloadService{Pool: pool}}

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
	pool := download.NewWorkerPoolForTesting(map[string]types.DownloadConfig{
		"dl-cache-1": {ID: "dl-cache-1"},
	})
	engine := &SurgeEngine{service: &core.LocalDownloadService{Pool: pool}}

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
