package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	surgeEvents "goaria-v3/internal/surge/types"
)

// TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback verifies that when a task
// completes without reaching the stable sample threshold (PeakSpeed==0),
// DownloadCompleteMsg.AvgSpeed is used as a fallback.

// TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback verifies that when a task
// completes without reaching the stable sample threshold (PeakSpeed==0),
// DownloadCompleteMsg.AvgSpeed is used as a fallback.
func TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	const gid = "sg_avg-fallback"

	// 1. Create tracked task via DownloadStartedMsg (no progress events → PeakSpeed stays 0)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: "avg-fallback",
		Total:      100000000, // >50MB
		URL:        "https://avg-fallback.example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv(gid, "wan", 50, "avg-fallback.example.com", "envA")

	before := speedstatsRecordCount()

	// 2. Complete without any ProgressMsg — PeakSpeed should be 0
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "avg-fallback",
		Total:      100000000,
		AvgSpeed:   5000000, // 5MB/s average
	})

	// 3. Verify the tracker's internal PeakSpeed is still 0 (fallback only on copy)
	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("Expected tracked task to exist")
	}
	if tracked.PeakSpeed != 0 {
		t.Errorf("Internal PeakSpeed = %d, want 0 (fallback only on copy)", tracked.PeakSpeed)
	}
	if tracked.TotalLength != 100000000 {
		t.Errorf("TotalLength = %d, want 100000000 (from DownloadCompleteMsg.Total)", tracked.TotalLength)
	}

	// 4. Verify processedComplete was set
	if !tracker.processedComplete[gid] {
		t.Error("Expected processedComplete to be set")
	}

	if after := speedstatsRecordCount(); after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d (before=%d, after=%d)", after-before, before, after)
	}
	rec := findRecordByDomain("avg-fallback.example.com")
	if rec == nil {
		t.Fatal("expected speedstats record")
	}
	if rec.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d, want 8 (EventStarted Workers)", rec.ThreadCount)
	}
	if rec.PeakSpeed != 5000000 {
		t.Errorf("PeakSpeed = %d, want 5000000 (AvgSpeed substitute)", rec.PeakSpeed)
	}
}

// TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback_RefreshesPeakEnvKeyToCurrent
// verifies AvgSpeed substitute-peak refreshes PeakEnvKey to Current on the
// complete copy only (resume changed Current; seed PeakEnvKey must not stick).
func TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback_RefreshesPeakEnvKeyToCurrent(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	const (
		downloadID = "avg-env-refresh"
		gid        = "sg_" + downloadID
		avgSpeed   = int64(7_000_000)
		total      = int64(100_000_000)
	)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: downloadID,
		Total:      total,
		URL:        "https://avg-env-refresh.example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv(gid, "wan", 50, "avg-env-refresh.example.com", "envA")

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected tracked task")
	}
	if tracked.PeakEnvKey != "envA" {
		t.Fatalf("PeakEnvKey seed = %q, want envA", tracked.PeakEnvKey)
	}
	if tracked.PeakSpeed != 0 {
		t.Fatalf("PeakSpeed before complete = %d, want 0", tracked.PeakSpeed)
	}
	// Resume refreshes Current only; PeakEnvKey seed stays until accept.
	tracked.CurrentEnvKey = "envB"

	before := speedstatsRecordCount()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: downloadID,
		Total:      total,
		AvgSpeed:   float64(avgSpeed),
	})

	if tracked.PeakSpeed != 0 {
		t.Errorf("tracker PeakSpeed = %d, want 0 (fallback only on copy)", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("tracker PeakEnvKey = %q, want envA (copy-only refresh)", tracked.PeakEnvKey)
	}

	if after := speedstatsRecordCount(); after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d (before=%d, after=%d)", after-before, before, after)
	}
	rec := findRecordByDomain("avg-env-refresh.example.com")
	if rec == nil {
		t.Fatal("expected speedstats record")
	}
	if rec.EnvKey != "envB" {
		t.Errorf("EnvKey = %q, want envB (AvgSpeed attributed to Current)", rec.EnvKey)
	}
	if rec.PeakSpeed != avgSpeed {
		t.Errorf("PeakSpeed = %d, want %d (AvgSpeed substitute)", rec.PeakSpeed, avgSpeed)
	}
	if rec.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d, want 8 (EventStarted Workers)", rec.ThreadCount)
	}
}

// TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback_EmptyCurrentDoesNotInventOrWipePeakEnvKey
// verifies empty CurrentEnvKey keeps the seed PeakEnvKey (no invent, no wipe).
func TestHandleSurgeEvent_CompleteMsg_AvgSpeedFallback_EmptyCurrentDoesNotInventOrWipePeakEnvKey(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	const (
		downloadID = "avg-env-empty-current"
		gid        = "sg_" + downloadID
		avgSpeed   = int64(6_000_000)
		total      = int64(100_000_000)
	)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: downloadID,
		Total:      total,
		URL:        "https://avg-empty-current.example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv(gid, "wan", 50, "avg-empty-current.example.com", "envA")

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected tracked task")
	}
	tracked.CurrentEnvKey = ""

	before := speedstatsRecordCount()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: downloadID,
		Total:      total,
		AvgSpeed:   float64(avgSpeed),
	})

	if tracked.PeakSpeed != 0 {
		t.Errorf("tracker PeakSpeed = %d, want 0", tracked.PeakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("tracker PeakEnvKey = %q, want envA", tracked.PeakEnvKey)
	}

	if after := speedstatsRecordCount(); after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d", after-before)
	}
	rec := findRecordByDomain("avg-empty-current.example.com")
	if rec == nil {
		t.Fatal("expected speedstats record")
	}
	if rec.EnvKey != "envA" {
		t.Errorf("EnvKey = %q, want envA (empty Current must not invent or wipe)", rec.EnvKey)
	}
	if rec.PeakSpeed != avgSpeed {
		t.Errorf("PeakSpeed = %d, want %d", rec.PeakSpeed, avgSpeed)
	}
	if rec.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d, want 8 (EventStarted Workers)", rec.ThreadCount)
	}
}

// TestHandleSurgeEvent_CompleteMsg_NoAvgSpeedRefreshWhenPeakSpeedAlreadySet verifies
// that an existing peak-time accept is not overwritten by AvgSpeed / Current.
func TestHandleSurgeEvent_CompleteMsg_NoAvgSpeedRefreshWhenPeakSpeedAlreadySet(t *testing.T) {
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	const (
		downloadID = "avg-env-no-refresh"
		gid        = "sg_" + downloadID
		peakSpeed  = int64(40_000_000)
		avgSpeed   = int64(5_000_000)
		total      = int64(200_000_000)
	)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: downloadID,
		Total:      total,
		URL:        "https://avg-no-refresh.example.com/large.zip",
		Workers:    8,
	})
	tracker.SetScopeAndEnv(gid, "wan", 50, "avg-no-refresh.example.com", "envA")
	tracker.RecordPeakEfficiency(gid, peakSpeed, 16)

	tracked := tracker.tasks[gid]
	if tracked == nil {
		t.Fatal("expected tracked task")
	}
	if tracked.PeakSpeed != peakSpeed {
		t.Fatalf("PeakSpeed after RecordPeak = %d, want %d", tracked.PeakSpeed, peakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Fatalf("PeakEnvKey after RecordPeak = %q, want envA", tracked.PeakEnvKey)
	}
	tracked.CurrentEnvKey = "envB"

	before := speedstatsRecordCount()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: downloadID,
		Total:      total,
		AvgSpeed:   float64(avgSpeed),
	})

	if tracked.PeakSpeed != peakSpeed {
		t.Errorf("tracker PeakSpeed = %d, want %d (AvgSpeed must not overwrite)", tracked.PeakSpeed, peakSpeed)
	}
	if tracked.PeakEnvKey != "envA" {
		t.Errorf("tracker PeakEnvKey = %q, want envA", tracked.PeakEnvKey)
	}

	if after := speedstatsRecordCount(); after != before+1 {
		t.Fatalf("expected 1 new speedstats record, got %d", after-before)
	}
	rec := findRecordByDomain("avg-no-refresh.example.com")
	if rec == nil {
		t.Fatal("expected speedstats record")
	}
	if rec.EnvKey != "envA" {
		t.Errorf("EnvKey = %q, want envA (no refresh when PeakSpeed already set)", rec.EnvKey)
	}
	if rec.PeakSpeed != peakSpeed {
		t.Errorf("PeakSpeed = %d, want %d", rec.PeakSpeed, peakSpeed)
	}
}

// TestHandleSurgeEvent_CompleteMsg_TotalEnrichesTrackedTask verifies that
// DownloadCompleteMsg.Total fills TotalLength when it was 0 from DownloadQueuedMsg.

// TestHandleSurgeEvent_CompleteMsg_TotalEnrichesTrackedTask verifies that
// DownloadCompleteMsg.Total fills TotalLength when it was 0 from DownloadQueuedMsg.
func TestHandleSurgeEvent_CompleteMsg_TotalEnrichesTrackedTask(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, tracker: tracker, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// 1. Queue with Total=0 (DownloadQueuedMsg typically has no size)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventQueued,
		DownloadID: "total-enrich",
		URL:        "https://example.com/queued.zip",
		Workers:    4,
	})

	tracked := tracker.tasks["sg_total-enrich"]
	if tracked == nil {
		t.Fatal("Expected tracked task after DownloadQueuedMsg")
	}
	if tracked.TotalLength != 0 {
		t.Errorf("TotalLength before complete = %d, want 0", tracked.TotalLength)
	}

	// 2. Complete with Total — should enrich TotalLength via EnsureTrackedFromEvent
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "total-enrich",
		Total:      200000000, // 200MB
		AvgSpeed:   10000000,  // 10MB/s
	})

	tracked = tracker.tasks["sg_total-enrich"]
	if tracked.TotalLength != 200000000 {
		t.Errorf("TotalLength after complete = %d, want 200000000", tracked.TotalLength)
	}
}
