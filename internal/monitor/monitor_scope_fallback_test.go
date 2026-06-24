package monitor

import (
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
)

func setupSpeedStatsForTest(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	speedstats.SetStatsPath(filepath.Join(tmpDir, "speed_stats.gob"))
	speedstats.SetSaveInterval(1 * time.Hour)
}

func clearSpeedStatsRecords() {
	speedstats.ResetRecordsForTest()
}

func newMonitorWithMockEngine() *Monitor {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	return &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}
}

func TestEnsureScopeDomain_FallbackFromLANURL(t *testing.T) {
	setupSpeedStatsForTest(t)
	clearSpeedStatsRecords()

	m := newMonitorWithMockEngine()
	task := &TrackedTask{
		GID:         "sg_lan_1",
		Status:      "complete",
		TotalLength: 100 * 1024 * 1024,
		PeakSpeed:   5_000_000,
		ThreadCount: 4,
		SourceURL:   "http://192.168.1.1/file.zip",
	}

	m.handleTaskComplete(task)

	if task.Domain != "192.168.1.1" {
		t.Errorf("Domain = %q, want 192.168.1.1", task.Domain)
	}
	if task.Scope != speedstats.ScopeLAN {
		t.Errorf("Scope = %q, want lan", task.Scope)
	}

	records := speedstats.GetAllRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Domain != "192.168.1.1" {
		t.Errorf("record Domain = %q, want 192.168.1.1", records[0].Domain)
	}
	if records[0].Scope != speedstats.ScopeLAN {
		t.Errorf("record Scope = %q, want lan", records[0].Scope)
	}
}

func TestEnsureScopeDomain_FallbackFromWANURL(t *testing.T) {
	setupSpeedStatsForTest(t)
	clearSpeedStatsRecords()

	m := newMonitorWithMockEngine()
	task := &TrackedTask{
		GID:         "sg_wan_1",
		Status:      "complete",
		TotalLength: 100 * 1024 * 1024,
		PeakSpeed:   5_000_000,
		ThreadCount: 4,
		SourceURL:   "http://example.com/file.zip",
	}

	m.handleTaskComplete(task)

	if task.Domain != "example.com" {
		t.Errorf("Domain = %q, want example.com", task.Domain)
	}
	if task.Scope != speedstats.ScopeWAN {
		t.Errorf("Scope = %q, want wan", task.Scope)
	}

	records := speedstats.GetAllRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Domain != "example.com" {
		t.Errorf("record Domain = %q, want example.com", records[0].Domain)
	}
}

func TestEnsureScopeDomain_SkipWhenNoURL(t *testing.T) {
	setupSpeedStatsForTest(t)
	clearSpeedStatsRecords()

	m := newMonitorWithMockEngine()
	task := &TrackedTask{
		GID:         "sg_nourl_1",
		Status:      "complete",
		TotalLength: 100 * 1024 * 1024,
		PeakSpeed:   5_000_000,
		ThreadCount: 4,
		SourceURL:   "",
	}

	m.handleTaskComplete(task)

	if task.Domain != "" {
		t.Errorf("Domain = %q, want empty (no fallback)", task.Domain)
	}
	if task.Scope != "" {
		t.Errorf("Scope = %q, want empty (no fallback)", task.Scope)
	}

	records := speedstats.GetAllRecords()
	if len(records) != 0 {
		t.Fatalf("expected 0 records (skipped), got %d", len(records))
	}
}

func TestEnsureScopeDomain_NoopWhenDomainSet(t *testing.T) {
	setupSpeedStatsForTest(t)
	clearSpeedStatsRecords()

	m := newMonitorWithMockEngine()
	task := &TrackedTask{
		GID:         "sg_existing_1",
		Status:      "complete",
		TotalLength: 100 * 1024 * 1024,
		PeakSpeed:   5_000_000,
		ThreadCount: 4,
		Domain:      "existing.com",
		Scope:       speedstats.ScopeWAN,
		SourceURL:   "http://other.com/file.zip",
	}

	m.handleTaskComplete(task)

	if task.Domain != "existing.com" {
		t.Errorf("Domain = %q, want existing.com (fallback should not trigger)", task.Domain)
	}
	if task.Scope != speedstats.ScopeWAN {
		t.Errorf("Scope = %q, want wan (unchanged)", task.Scope)
	}

	records := speedstats.GetAllRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Domain != "existing.com" {
		t.Errorf("record Domain = %q, want existing.com", records[0].Domain)
	}
}

func TestHandleTaskComplete_SkipsRecordWhenDomainEmpty(t *testing.T) {
	setupSpeedStatsForTest(t)
	clearSpeedStatsRecords()

	m := newMonitorWithMockEngine()
	task := &TrackedTask{
		GID:         "sg_skip_1",
		Status:      "complete",
		TotalLength: 100 * 1024 * 1024,
		PeakSpeed:   5_000_000,
		ThreadCount: 4,
		Domain:      "",
		SourceURL:   "",
	}

	m.handleTaskComplete(task)

	records := speedstats.GetAllRecords()
	if len(records) != 0 {
		t.Fatalf("expected 0 records (domain empty, no URL), got %d", len(records))
	}
}

func TestHandleTaskComplete_FallbackClassifiesAndRecords(t *testing.T) {
	setupSpeedStatsForTest(t)
	clearSpeedStatsRecords()

	m := newMonitorWithMockEngine()
	task := &TrackedTask{
		GID:         "sg_fallback_1",
		Status:      "complete",
		TotalLength: 100 * 1024 * 1024,
		PeakSpeed:   5_000_000,
		ThreadCount: 4,
		Domain:      "",
		SourceURL:   "http://example.com/file.zip",
	}

	m.handleTaskComplete(task)

	records := speedstats.GetAllRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Domain != "example.com" {
		t.Errorf("record Domain = %q, want example.com", records[0].Domain)
	}
	if records[0].Scope != speedstats.ScopeWAN {
		t.Errorf("record Scope = %q, want wan", records[0].Scope)
	}
}
