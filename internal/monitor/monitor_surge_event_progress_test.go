package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/testutil"
	surgeEvents "goaria-v3/internal/surge/types"
)

func TestHandleSurgeEvent_ProgressMsg_QueuesProgressDelta(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventProgress,
		DownloadID: "test-1",
		Downloaded: 500,
		Total:      1000,
		Speed:      50.0,
	})

	pusher.mu.Lock()
	if len(pusher.pending) != 1 {
		pusher.mu.Unlock()
		t.Fatalf("expected 1 pending delta, got %d", len(pusher.pending))
	}
	delta := pusher.pending[0]
	pusher.mu.Unlock()

	if delta.Type != "progress" {
		t.Errorf("delta type = %q, want progress", delta.Type)
	}
	if delta.GID != "sg_test-1" {
		t.Errorf("delta GID = %q, want sg_test-1", delta.GID)
	}

	payload, ok := delta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", delta.Payload)
	}
	if payload["completedLength"] != "500" {
		t.Errorf("completedLength = %v, want 500", payload["completedLength"])
	}
	if payload["downloadSpeed"] != "50" {
		t.Errorf("downloadSpeed = %v, want 50", payload["downloadSpeed"])
	}
	if payload["totalLength"] != "1000" {
		t.Errorf("totalLength = %v, want 1000", payload["totalLength"])
	}
}

func TestHandleSurgeEvent_BatchProgressMsg_QueuesAllProgressDeltas(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventBatchProgress, BatchEvents: []surgeEvents.DownloadEvent{
		{DownloadID: "a", Downloaded: 100, Total: 200, Speed: 10.0},
		{DownloadID: "b", Downloaded: 300, Total: 600, Speed: 20.0},
	}})

	pusher.mu.Lock()
	if len(pusher.pending) != 2 {
		pusher.mu.Unlock()
		t.Fatalf("expected 2 pending deltas, got %d", len(pusher.pending))
	}
	gids := map[string]bool{pusher.pending[0].GID: true, pusher.pending[1].GID: true}
	pusher.mu.Unlock()

	if !gids["sg_a"] || !gids["sg_b"] {
		t.Errorf("expected gids sg_a and sg_b, got %v", gids)
	}
}

// TestHandleSurgeEvent_EventStartedPreservesRangeAcquisitionMode verifies
// EventStarted keeps RangeAcquisitionMode and SkipServerProbe from the
// queued cache entry the same way it keeps Mirrors.
func TestHandleSurgeEvent_EventStartedPreservesRangeAcquisitionMode(t *testing.T) {
	testutil.SetupStateDB(t)
	hub := events.NewHub(nil)
	se := rpc.NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
	se.UpsertMasterCacheEntry(surgeEvents.DownloadRecord{
		ID:                   "dl-keep-range",
		URL:                  "http://x/keep.bin",
		Mirrors:              []string{"http://m1"},
		RangeAcquisitionMode: surgeEvents.RangeAcquireRangeUnsupported,
		SkipServerProbe:      true,
		Status:               "queued",
	})

	m := &Monitor{hub: hub, pusher: NewPusher(hub), surgeEng: se}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: "dl-keep-range",
		URL:        "http://x/keep.bin",
		Total:      100000000,
		Workers:    16,
	})

	got, ok := se.GetMasterCacheEntry("dl-keep-range")
	if !ok {
		t.Fatal("expected cache entry after EventStarted")
	}
	if got.RangeAcquisitionMode != surgeEvents.RangeAcquireRangeUnsupported {
		t.Errorf("RangeAcquisitionMode = %q, want range_unsupported", got.RangeAcquisitionMode)
	}
	if !got.SkipServerProbe {
		t.Error("SkipServerProbe = false, want true")
	}
	if len(got.Mirrors) != 1 || got.Mirrors[0] != "http://m1" {
		t.Errorf("Mirrors = %v, want [http://m1]", got.Mirrors)
	}
}

// TestHandleSurgeEvent_RangeUnsupportedOnStoreSkipsAfterEventStarted verifies
// persistRangeUnsupported (gob list) still suppresses speedstats when
// EventStarted has replaced the cache with an empty-mode hit.
func TestHandleSurgeEvent_RangeUnsupportedOnStoreSkipsAfterEventStarted(t *testing.T) {
	testutil.SetupStateDB(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)

	const (
		downloadID = "evt-range-unsup-store"
		gid        = "sg_" + downloadID
		avgSpeed   = int64(40_000_000)
		total      = int64(100_000_000)
	)

	se := rpc.NewSurgeEngineForTesting(nil)
	testutil.SeedMasterList(t, surgeEvents.DownloadRecord{
		ID:                   downloadID,
		URL:                  "https://evt-range-unsup.example.com/seq.bin",
		RangeAcquisitionMode: surgeEvents.RangeAcquireRangeUnsupported,
	})
	if _, ok := se.GetMasterCacheEntry(downloadID); ok {
		t.Fatal("setup: cache must not already hold the store-only mode")
	}

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:      hub,
		pusher:   NewPusher(hub),
		tracker:  tracker,
		engine:   rpc.NewHybridEngine(nil, se),
		surgeEng: se,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventStarted,
		DownloadID: downloadID,
		Total:      total,
		URL:        "https://evt-range-unsup.example.com/seq.bin",
		Workers:    16,
	})
	entry, ok := se.GetMasterCacheEntry(downloadID)
	if !ok {
		t.Fatal("expected EventStarted to insert a cache hit")
	}
	if entry.RangeAcquisitionMode != "" {
		t.Fatalf("cache mode = %q, want empty (store-only sequential signal)", entry.RangeAcquisitionMode)
	}

	tracker.SetScopeAndEnv(gid, "wan", 50, "evt-range-unsup.example.com", "envA")

	before := speedstatsRecordCount()
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: downloadID,
		Total:      total,
		AvgSpeed:   float64(avgSpeed),
	})
	if after := speedstatsRecordCount(); after != before {
		t.Fatalf("expected no speedstats record for store range_unsupported, got %d new", after-before)
	}
}

// mockSurgeActiveEngine wraps Aria2Engine but reports IsSurgeActive()=true,
// simulating production where SurgeEngine always has a non-nil service.

func TestHandleSurgeEvent_ProgressMsg_NoWindow_DoesNotPush(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(false)
	defer State.SetWindowExists(prevWindow)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventProgress,
		DownloadID: "test-4",
		Downloaded: 100,
		Total:      200,
		Speed:      10.0,
	})

	pusher.mu.Lock()
	count := len(pusher.pending)
	pusher.mu.Unlock()

	if count != 0 {
		t.Errorf("expected 0 pending deltas when window is hidden, got %d", count)
	}
}

func TestHandleSurgeEvent_BatchProgress_LiveConnectionsDecoupled(t *testing.T) {
	prevWindow := State.HasWindow()
	defer State.SetWindowExists(prevWindow)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:      hub,
		pusher:   pusher,
		tracker:  tracker,
		stopChan: make(chan struct{}),
	}

	gid := "sg_batch_drain"
	rawID := "batch_drain"
	tracker.SetThreadInfo(gid, 8, false)

	// 1. Under HasWindow(false), batch progress updates LiveConnections but not ThreadCount
	State.SetWindowExists(false)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventBatchProgress,
		BatchEvents: []surgeEvents.DownloadEvent{
			{DownloadID: rawID, Downloaded: 500, Total: 1000, Speed: 50, Connections: 3},
		},
	})
	if live := tracker.GetLiveConnections(gid); live != 3 {
		t.Fatalf("expected LiveConnections = 3, got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 8 {
		t.Fatalf("expected ThreadCount = 8, got %d", tc)
	}

	// 2. Under HasWindow(true), batch progress updates LiveConnections and queues pusher delta
	State.SetWindowExists(true)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventBatchProgress,
		BatchEvents: []surgeEvents.DownloadEvent{
			{DownloadID: rawID, Downloaded: 900, Total: 1000, Speed: 50, Connections: 1},
		},
	})
	if live := tracker.GetLiveConnections(gid); live != 1 {
		t.Fatalf("expected LiveConnections = 1, got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 8 {
		t.Fatalf("expected ThreadCount = 8, got %d", tc)
	}

	// 3. Under HasWindow(true), batch progress with Connections=0 updates LiveConnections to 0 and queues threads="0"
	pusher = NewPusher(hub)
	m.pusher = pusher
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventBatchProgress,
		BatchEvents: []surgeEvents.DownloadEvent{
			{DownloadID: rawID, Downloaded: 1000, Total: 1000, Speed: 0, Connections: 0},
		},
	})
	if live := tracker.GetLiveConnections(gid); live != 0 {
		t.Fatalf("expected LiveConnections = 0, got %d", live)
	}
	if tc, _, _ := tracker.GetThreadInfo(gid); tc != 8 {
		t.Fatalf("expected ThreadCount = 8, got %d", tc)
	}
	pusher.mu.Lock()
	if len(pusher.pending) == 0 {
		pusher.mu.Unlock()
		t.Fatal("expected pending delta in pusher for batch progress")
	}
	batchDelta := pusher.pending[len(pusher.pending)-1]
	pusher.mu.Unlock()

	batchPayload, ok := batchDelta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", batchDelta.Payload)
	}
	if batchPayload["threads"] != "0" {
		t.Fatalf("expected batch payload threads = \"0\", got %q", batchPayload["threads"])
	}
}
