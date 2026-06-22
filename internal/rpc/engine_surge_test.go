package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"goaria-v3/internal/surge/engine/types"
)

func TestSurgeEngine_MapStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"downloading", "active"},
		{"pausing", "active"},
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
