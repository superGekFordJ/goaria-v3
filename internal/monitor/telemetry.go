package monitor

import (
	"sync"
	"time"

	"goaria-v3/internal/surge/types"
)

// TelemetryCache stores per-worker telemetry snapshots keyed by download GID.
// It is concurrency-safe and updated during monitor ticks.
type TelemetryCache struct {
	mu   sync.RWMutex
	data map[string][]types.WorkerSnapshot
	ts   map[string]time.Time
}

// NewTelemetryCache creates a new TelemetryCache.
func NewTelemetryCache() *TelemetryCache {
	return &TelemetryCache{
		data: make(map[string][]types.WorkerSnapshot),
		ts:   make(map[string]time.Time),
	}
}

// Set stores worker stats for the given GID with the current timestamp.
func (tc *TelemetryCache) Set(gid string, stats []types.WorkerSnapshot) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.data[gid] = stats
	tc.ts[gid] = time.Now()
}

// Get returns worker stats for the given GID, or nil if not found.
func (tc *TelemetryCache) Get(gid string) []types.WorkerSnapshot {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	stats, ok := tc.data[gid]
	if !ok || len(stats) == 0 {
		return nil
	}
	result := make([]types.WorkerSnapshot, len(stats))
	copy(result, stats)
	return result
}

// GetTimestamp returns the last update time for the given GID.
func (tc *TelemetryCache) GetTimestamp(gid string) (time.Time, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	ts, ok := tc.ts[gid]
	return ts, ok
}

// Remove deletes telemetry for the given GID.
func (tc *TelemetryCache) Remove(gid string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	delete(tc.data, gid)
	delete(tc.ts, gid)
}

// Clear removes all telemetry entries.
func (tc *TelemetryCache) Clear() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.data = make(map[string][]types.WorkerSnapshot)
	tc.ts = make(map[string]time.Time)
}

// ActiveGIDs returns the set of GIDs that currently have telemetry.
func (tc *TelemetryCache) ActiveGIDs() []string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	gids := make([]string, 0, len(tc.data))
	for gid := range tc.data {
		gids = append(gids, gid)
	}
	return gids
}
