package monitor

import (
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/types"
)

// TestHandleTaskComplete_PeakThreadCountFallback verifies that handleTaskComplete
// uses PeakThreadCount (from convergence) as the primary source for speedstats ThreadCount.
func TestHandleTaskComplete_PeakThreadCountFallback(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_peak-tc", 200000000, "https://example.com/large.zip", 32, "active")

	// Simulate convergence recording a peak at 22 workers
	tracker.RecordPeakEfficiency("sg_peak-tc", 50*1024*1024, 22)

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_peak-tc"]
	task.Status = "complete"
	task.PeakSpeed = 50 * 1024 * 1024
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\large.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d (before=%d, after=%d)", after-before, before, after)
	}

	rec := findRecordByDomain("example.com")
	if rec == nil {
		t.Fatal("expected to find speedstats record for example.com")
	}
	if rec.ThreadCount != 22 {
		t.Errorf("ThreadCount = %d, want 22 (from PeakThreadCount)", rec.ThreadCount)
	}
	if rec.PeakSpeed != 50*1024*1024 {
		t.Errorf("PeakSpeed = %d, want %d", rec.PeakSpeed, 50*1024*1024)
	}
}

// TestHandleTaskComplete_ThreadCountFallback verifies that when PeakThreadCount is 0,
// handleTaskComplete does not write a speedstats record (no initial-N denominator).
func TestHandleTaskComplete_ThreadCountFallback(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_tc-fallback", 200000000, "https://example.com/medium.zip", 16, "active")

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_tc-fallback"]
	task.Status = "complete"
	task.PeakSpeed = 30 * 1024 * 1024
	task.ThreadCount = 16
	task.PeakThreadCount = 0 // Not set by convergence
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\medium.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before {
		t.Fatalf("expected no speedstats record when PeakThreadCount==0, got %d new", after-before)
	}
}

// TestHandleTaskComplete_ConfigFallback verifies that when both PeakThreadCount and
// ThreadCount are 0, handleTaskComplete does not write a speedstats record.
func TestHandleTaskComplete_ConfigFallback(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Ensure config is set with a known MaxConnections
	origConfig := config.Get()
	config.SetTestConfig(&config.AppConfig{MaxConnections: "12"})
	defer func() { config.SetTestConfig(origConfig) }()

	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_cfg-fallback", 200000000, "https://example.com/config.zip", 0, "active")

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_cfg-fallback"]
	task.Status = "complete"
	task.PeakSpeed = 20 * 1024 * 1024
	task.ThreadCount = 0
	task.PeakThreadCount = 0
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\config.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before {
		t.Fatalf("expected no speedstats record when PeakThreadCount==0, got %d new", after-before)
	}
}

// TestHandleTaskComplete_RateLimitSkip verifies that rate-limited tasks skip
// AddRecordV2 to avoid polluting speedstats with throttled throughput.
func TestHandleTaskComplete_RateLimitSkip(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Create a WorkerPool with a rate-limited download entry
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"ratelimited": {
			URL:           "https://example.com/limited.zip",
			ID:            "ratelimited",
			RateLimit:     1_000_000, // 1MB/s rate limit
			RateLimitSet:  true,
			ProgressState: progress.New("ratelimited", 200000000),
		},
	})
	surge := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_ratelimited", 200000000, "https://example.com/limited.zip", 8, "active")

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_ratelimited"]
	task.Status = "complete"
	task.PeakSpeed = 1 * 1024 * 1024 // 1MB/s (rate-limited)
	task.ThreadCount = 8
	task.PeakThreadCount = 8
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\limited.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before {
		t.Errorf("expected no new speedstats record when rate-limited, got %d new records", after-before)
	}
}

// TestHandleTaskComplete_ZeroRateLimitStillRecords verifies RateLimitSet=true with
// RateLimit=0 (explicit unlimited / false-positive shape) still AddRecordV2.
func TestHandleTaskComplete_ZeroRateLimitStillRecords(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"zero-cap": {
			URL:           "https://example.com/fast.zip",
			ID:            "zero-cap",
			RateLimit:     0,
			RateLimitSet:  true,
			ProgressState: progress.New("zero-cap", 200000000),
		},
	})
	surge := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_zero-cap", 200000000, "https://example.com/fast.zip", 8, "active")

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_zero-cap"]
	task.Status = "complete"
	task.PeakSpeed = 50 * 1024 * 1024
	task.ThreadCount = 8
	task.PeakThreadCount = 8
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\fast.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before+1 {
		t.Errorf("expected 1 new speedstats record for zero-cap unlimited, got %d", after-before)
	}
}

// TestHandleTaskComplete_RateLimitNotSet_RecordsNormally verifies that when no rate
// limit is active, AddRecordV2 proceeds normally.
func TestHandleTaskComplete_RateLimitNotSet_RecordsNormally(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Create a WorkerPool with a non-rate-limited download entry
	pool := scheduler.NewSchedulerForTesting(map[string]types.DownloadRecord{
		"unlimited": {
			URL:           "https://example.com/fast.zip",
			ID:            "unlimited",
			RateLimit:     0, // No rate limit
			RateLimitSet:  false,
			ProgressState: progress.New("unlimited", 200000000),
		},
	})
	surge := rpc.NewSurgeEngineForTesting(pool)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_unlimited", 200000000, "https://example.com/fast.zip", 8, "active")

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_unlimited"]
	task.Status = "complete"
	task.PeakSpeed = 50 * 1024 * 1024 // 50MB/s (not rate-limited)
	task.ThreadCount = 8
	task.PeakThreadCount = 8
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\fast.zip"
	task.PeakEnvKey = "testenv"

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before+1 {
		t.Errorf("expected 1 new speedstats record when not rate-limited, got %d", after-before)
	}
}

// TestHandleTaskComplete_EmptyEnvKeySkipsRecording verifies that a task with
// PeakEnvKey="" (external RPC or wake-up path) does NOT produce a speedstats
// record — empty envKey is a dirty-data signal that would pollute env-aware buckets.
func TestHandleTaskComplete_EmptyEnvKeySkipsRecording(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	surge := rpc.NewSurgeEngineForTesting(nil)
	he := rpc.NewHybridEngine(nil, surge)

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("sg_no_envkey", 200000000, "https://example.com/large.zip", 8, "active")

	m := &Monitor{engine: he, tracker: tracker}

	task := tracker.tasks["sg_no_envkey"]
	task.Status = "complete"
	task.PeakSpeed = 50 * 1024 * 1024
	task.ThreadCount = 8
	task.Domain = "example.com"
	task.Scope = "wan"
	task.FilePath = "D:\\Downloads\\large.zip"
	task.PeakEnvKey = "" // no envKey — should skip AddRecordV2

	before := speedstatsRecordCount()
	m.handleTaskComplete(task)
	after := speedstatsRecordCount()

	if after != before {
		t.Fatalf("expected 0 new speedstats records (empty envKey skipped), got %d (before=%d, after=%d)", after-before, before, after)
	}
}
