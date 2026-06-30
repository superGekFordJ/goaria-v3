package monitor

import (
	"testing"
	"time"

	"goaria-v3/internal/rpc"
)

func TestActiveBandwidthByScope_ScopeMatching(t *testing.T) {
	// Setup tracker and cache
	tracker := NewTaskTracker()
	State.SetTracker(tracker)

	// Set scopes+envKeys for two active tasks
	tracker.SetScopeAndEnv("gid-bw-001", "wan", 100, "a.com", "env1")
	tracker.SetScopeAndEnv("gid-bw-002", "wan", 120, "b.com", "env1")
	tracker.SetScopeAndEnv("gid-bw-003", "lan", 80, "c.local", "env1")

	// Inject active tasks into cache
	Cache = &TaskCache{
		metadata:         make(map[string]*TaskMetadata),
		pendingStartGids: make(map[string]time.Time),
	}
	Cache.mu.Lock()
	Cache.active = []rpc.Task{
		{GID: "gid-bw-001", DownloadSpeed: "5000000"}, // 5MB/s
		{GID: "gid-bw-002", DownloadSpeed: "3000000"}, // 3MB/s
		{GID: "gid-bw-003", DownloadSpeed: "2000000"}, // 2MB/s (lan)
	}
	Cache.mu.Unlock()

	wanBw := ActiveBandwidthByScope("wan", "env1")
	if wanBw != 8000000 {
		t.Errorf("ActiveBandwidthByScope(wan, env1) = %d, want 8000000", wanBw)
	}

	lanBw := ActiveBandwidthByScope("lan", "env1")
	if lanBw != 2000000 {
		t.Errorf("ActiveBandwidthByScope(lan, env1) = %d, want 2000000", lanBw)
	}

	unknownBw := ActiveBandwidthByScope("unknown", "env1")
	if unknownBw != 0 {
		t.Errorf("ActiveBandwidthByScope(unknown, env1) = %d, want 0", unknownBw)
	}

	// Cross-env isolation: different envKey should not match
	crossEnvBw := ActiveBandwidthByScope("wan", "env2")
	if crossEnvBw != 0 {
		t.Errorf("ActiveBandwidthByScope(wan, env2) = %d, want 0 (cross-env isolation)", crossEnvBw)
	}
}

func TestActiveBandwidthByScope_ScopeMissingSkipped(t *testing.T) {
	tracker := NewTaskTracker()
	State.SetTracker(tracker)

	// Task without scope set should be skipped
	tracker.SetThreadInfo("gid-no-scope", 4, false)

	Cache = &TaskCache{
		metadata:         make(map[string]*TaskMetadata),
		pendingStartGids: make(map[string]time.Time),
	}
	Cache.mu.Lock()
	Cache.active = []rpc.Task{
		{GID: "gid-no-scope", DownloadSpeed: "9999999"},
	}
	Cache.mu.Unlock()

	bw := ActiveBandwidthByScope("wan", "env1")
	if bw != 0 {
		t.Errorf("ActiveBandwidthByScope(wan, env1) = %d, want 0 (scope missing task skipped)", bw)
	}
}

func TestActiveBandwidthByScope_NilSafety(t *testing.T) {
	// Save original state
	origTracker := State.GetTracker()
	origCache := Cache
	defer func() {
		State.SetTracker(origTracker)
		Cache = origCache
	}()

	// nil tracker and nil cache
	State.SetTracker(nil)
	Cache = nil

	bw := ActiveBandwidthByScope("wan", "env1")
	if bw != 0 {
		t.Errorf("ActiveBandwidthByScope with nil tracker/cache = %d, want 0", bw)
	}
}
