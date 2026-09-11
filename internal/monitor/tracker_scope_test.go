package monitor

import (
	"testing"
)

func TestTaskTracker_SetScope_ExistingTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-evt-009", 100000000, "https://example.com/file.zip", 8, "active")

	tracker.SetScope("sg-evt-009", "wan", 150, "example.com")

	tracked := tracker.tasks["sg-evt-009"]
	if tracked.Scope != "wan" {
		t.Errorf("Scope = %s, want wan", tracked.Scope)
	}
	if tracked.TTFBMs != 150 {
		t.Errorf("TTFBMs = %d, want 150", tracked.TTFBMs)
	}
	if tracked.Domain != "example.com" {
		t.Errorf("Domain = %s, want example.com", tracked.Domain)
	}
}

func TestTaskTracker_SetScope_NewTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScope("sg-evt-010", "lan", 50, "nas.local")

	tracked := tracker.tasks["sg-evt-010"]
	if tracked == nil {
		t.Fatal("Expected task to be created by SetScope")
	}
	if tracked.Scope != "lan" {
		t.Errorf("Scope = %s, want lan", tracked.Scope)
	}
	if tracked.TTFBMs != 50 {
		t.Errorf("TTFBMs = %d, want 50", tracked.TTFBMs)
	}
	if tracked.Domain != "nas.local" {
		t.Errorf("Domain = %s, want nas.local", tracked.Domain)
	}
}

func TestTaskTracker_SetMinChunk_ExistingTask(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg-minchunk-1", 4, false)

	minChunk := int64(4 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-1", minChunk)

	tracked := tracker.tasks["sg-minchunk-1"]
	if tracked == nil {
		t.Fatal("expected tracked task to exist")
	}
	if tracked.MinChunk != minChunk {
		t.Errorf("MinChunk = %d, want %d", tracked.MinChunk, minChunk)
	}
}

func TestTaskTracker_SetMinChunk_NewTask(t *testing.T) {
	tracker := NewTaskTracker()
	minChunk := int64(2 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-2", minChunk)

	tracked := tracker.tasks["sg-minchunk-2"]
	if tracked == nil {
		t.Fatal("Expected task to be created by SetMinChunk")
	}
	if tracked.MinChunk != minChunk {
		t.Errorf("MinChunk = %d, want %d", tracked.MinChunk, minChunk)
	}
}

// 第二次调用应覆盖第一次设置的值，防止未来重构丢失无条件覆盖语义。
func TestTaskTracker_SetMinChunk_OverwritesExistingValue(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetThreadInfo("sg-minchunk-ow", 4, false)

	first := int64(4 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-ow", first)
	if tracked := tracker.tasks["sg-minchunk-ow"]; tracked == nil || tracked.MinChunk != first {
		t.Fatalf("first SetMinChunk: got MinChunk %v, want %d", tracked, first)
	}

	second := int64(16 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-ow", second)
	tracked := tracker.tasks["sg-minchunk-ow"]
	if tracked == nil {
		t.Fatal("expected tracked task to exist after second SetMinChunk")
	}
	if tracked.MinChunk != second {
		t.Errorf("MinChunk = %d, want %d (second call should overwrite first)", tracked.MinChunk, second)
	}
}

func TestTrackerAdapter_GetActiveTrackedTasks_PassesMinChunkThrough(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg-minchunk-pass", 0, "", 4, "active")

	minChunk := int64(8 * 1024 * 1024)
	tracker.SetMinChunk("sg-minchunk-pass", minChunk)

	adapter := &trackerAdapter{TaskTracker: tracker}
	infos := adapter.GetActiveTrackedTasks()
	if len(infos) != 1 {
		t.Fatalf("got %d active tracked tasks, want 1", len(infos))
	}
	if infos[0].GID != "sg-minchunk-pass" {
		t.Errorf("GID = %s, want sg-minchunk-pass", infos[0].GID)
	}
	if infos[0].MinChunk != minChunk {
		t.Errorf("MinChunk = %d, want %d", infos[0].MinChunk, minChunk)
	}
}

func TestTaskTracker_GetScope(t *testing.T) {
	tracker := NewTaskTracker()

	// Before SetScope, GetScope returns ok=false
	_, _, ok := tracker.GetScope("gid-scope-001")
	if ok {
		t.Error("Expected ok=false before SetScope")
	}

	tracker.SetScope("gid-scope-001", "wan", 120, "example.com")

	scope, domain, ok := tracker.GetScope("gid-scope-001")
	if !ok {
		t.Fatal("Expected ok=true after SetScope")
	}
	if scope != "wan" {
		t.Errorf("scope = %s, want wan", scope)
	}
	if domain != "example.com" {
		t.Errorf("domain = %s, want example.com", domain)
	}

	// Unknown gid
	_, _, ok = tracker.GetScope("nonexistent")
	if ok {
		t.Error("Expected ok=false for nonexistent gid")
	}
}

func TestTaskTracker_GetScope_EmptyScope(t *testing.T) {
	tracker := NewTaskTracker()

	// SetThreadInfo creates a tracked task with empty Scope
	tracker.SetThreadInfo("gid-empty-scope", 4, false)

	_, _, ok := tracker.GetScope("gid-empty-scope")
	if ok {
		t.Error("Expected ok=false when Scope is empty")
	}
}

func TestSetScopeAndEnv_ZeroTTFB_PreservesExistingTTFB(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-001", "wan", 120, "example.com", "envA")

	tracker.SetScopeAndEnv("sg-ttfb-001", "wan", 0, "example.com", "envB")

	tracked := tracker.tasks["sg-ttfb-001"]
	if tracked == nil {
		t.Fatal("expected tracked task to exist")
	}
	if tracked.TTFBMs != 120 {
		t.Errorf("TTFBMs = %d, want 120 (zero ttfbMs must not overwrite existing probe)", tracked.TTFBMs)
	}
	if tracked.CurrentEnvKey != "envB" {
		t.Errorf("CurrentEnvKey = %q, want envB (envKey should still update)", tracked.CurrentEnvKey)
	}
}

func TestSetScopeAndEnv_NegativeTTFB_PreservesExistingTTFB(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-002", "wan", 120, "example.com", "envA")

	tracker.SetScopeAndEnv("sg-ttfb-002", "wan", -1, "example.com", "envB")

	tracked := tracker.tasks["sg-ttfb-002"]
	if tracked.TTFBMs != 120 {
		t.Errorf("TTFBMs = %d, want 120 (negative ttfbMs must not overwrite existing probe)", tracked.TTFBMs)
	}
}

func TestSetScopeAndEnv_PositiveTTFB_OverwritesExisting(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-003", "wan", 120, "example.com", "envA")

	tracker.SetScopeAndEnv("sg-ttfb-003", "wan", 200, "example.com", "envB")

	tracked := tracker.tasks["sg-ttfb-003"]
	if tracked.TTFBMs != 200 {
		t.Errorf("TTFBMs = %d, want 200 (positive ttfbMs should overwrite)", tracked.TTFBMs)
	}
}

func TestSetScopeAndEnv_NewTask_ZeroTTFB_AcceptsZero(t *testing.T) {
	tracker := NewTaskTracker()

	tracker.SetScopeAndEnv("sg-ttfb-004", "wan", 0, "example.com", "envA")

	tracked := tracker.tasks["sg-ttfb-004"]
	if tracked == nil {
		t.Fatal("expected new tracked task to be created")
	}
	if tracked.TTFBMs != 0 {
		t.Errorf("TTFBMs = %d, want 0 (new task with no probe should start at 0)", tracked.TTFBMs)
	}
}

func TestSetTTFB_Positive_OverwritesZero(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-set-1", "wan", 0, "example.com", "envA")

	tracker.SetTTFB("sg-ttfb-set-1", 80)

	tracked := tracker.tasks["sg-ttfb-set-1"]
	if tracked == nil {
		t.Fatal("expected tracked task to exist")
	}
	if tracked.TTFBMs != 80 {
		t.Errorf("TTFBMs = %d, want 80", tracked.TTFBMs)
	}
	if tracked.Scope != "wan" || tracked.Domain != "example.com" || tracked.CurrentEnvKey != "envA" {
		t.Errorf("scope/domain/envKey overwritten: scope=%q domain=%q envKey=%q", tracked.Scope, tracked.Domain, tracked.CurrentEnvKey)
	}
}

func TestSetTTFB_Zero_DoesNotOverwrite(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-set-2", "wan", 100, "example.com", "envA")

	tracker.SetTTFB("sg-ttfb-set-2", 0)

	tracked := tracker.tasks["sg-ttfb-set-2"]
	if tracked.TTFBMs != 100 {
		t.Errorf("TTFBMs = %d, want 100 (zero must not overwrite)", tracked.TTFBMs)
	}
}

func TestSetTTFB_NonExistentTask_SilentSkip(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetTTFB("nonexistent", 80)
}

func TestSetTTFB_DoesNotOverwriteScopeDomainEnvKey(t *testing.T) {
	tracker := NewTaskTracker()
	tracker.SetScopeAndEnv("sg-ttfb-set-3", "wan", 0, "example.com", "envA")

	tracker.SetTTFB("sg-ttfb-set-3", 80)

	tracked := tracker.tasks["sg-ttfb-set-3"]
	if tracked.Scope != "wan" {
		t.Errorf("Scope = %q, want wan", tracked.Scope)
	}
	if tracked.Domain != "example.com" {
		t.Errorf("Domain = %q, want example.com", tracked.Domain)
	}
	if tracked.CurrentEnvKey != "envA" {
		t.Errorf("CurrentEnvKey = %q, want envA", tracked.CurrentEnvKey)
	}
}
