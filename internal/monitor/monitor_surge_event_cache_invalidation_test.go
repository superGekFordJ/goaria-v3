package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

// TestHandleSurgeEvent_InvalidatesListCacheOnPause verifies that pause/resume/
// complete/error events call SurgeEngine.InvalidateListCache, clearing the 1s
// TTL list cache so TellWaiting/TellStopped fetch fresh data on the next tick.
func TestHandleSurgeEvent_InvalidatesListCacheOnPause(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		engine:                hybrid,
		surgeEng:              surgeEng,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	Cache.sgActive = []rpc.Task{{GID: "sg_inv-1", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil; Cache.sgStopped = nil }()

	// Populate the list cache by calling TellWaiting (which uses getDownloadList)
	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}
	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtBefore := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if cacheAtBefore.IsZero() {
		t.Fatal("expected cache to be populated before event")
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "inv-1"})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if !cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = %v, want zero (cache should be invalidated on pause)", cacheAtAfter)
	}
}

func TestHandleSurgeEvent_InvalidatesListCacheOnResume(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:      hub,
		pusher:   pusher,
		engine:   hybrid,
		surgeEng: surgeEng,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	Cache.sgWaiting = []rpc.Task{{GID: "sg_inv-2", Status: "paused", DownloadSpeed: "0"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil; Cache.sgStopped = nil }()

	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventResumed, DownloadID: "inv-2"})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if !cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = %v, want zero (cache should be invalidated on resume)", cacheAtAfter)
	}
}

func TestHandleSurgeEvent_InvalidatesListCacheOnComplete(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:      hub,
		pusher:   pusher,
		engine:   hybrid,
		surgeEng: surgeEng,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	Cache.sgActive = []rpc.Task{{GID: "sg_inv-3", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil; Cache.sgStopped = nil }()

	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventComplete, DownloadID: "inv-3", Total: 1000, AvgSpeed: 500})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if !cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = %v, want zero (cache should be invalidated on complete)", cacheAtAfter)
	}
}

func TestHandleSurgeEvent_InvalidatesListCacheOnError(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:      hub,
		pusher:   pusher,
		engine:   hybrid,
		surgeEng: surgeEng,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)
	Cache.sgActive = []rpc.Task{{GID: "sg_inv-4", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil; Cache.sgStopped = nil }()

	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventError, DownloadID: "inv-4"})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if !cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = %v, want zero (cache should be invalidated on error)", cacheAtAfter)
	}
}

// TestHandleSurgeEvent_NoInvalidationOnProgress verifies that ProgressMsg
// (which returns early before the switch deltaType) does NOT invalidate the cache.

// TestHandleSurgeEvent_NoInvalidationOnProgress verifies that ProgressMsg
// (which returns early before the switch deltaType) does NOT invalidate the cache.
func TestHandleSurgeEvent_NoInvalidationOnProgress(t *testing.T) {
	surgeEng := rpc.NewSurgeEngine()
	defer surgeEng.Close()
	hybrid := rpc.NewHybridEngine(nil, surgeEng)

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:    hub,
		pusher: pusher,
		engine: hybrid,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	if _, err := surgeEng.TellWaiting(0, -1); err != nil {
		t.Fatalf("TellWaiting to populate cache: %v", err)
	}
	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtBefore := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventProgress,
		DownloadID: "inv-5",
		Downloaded: 100,
		Total:      1000,
		Speed:      50.0,
	})

	surgeEng.ListCacheMuForTesting().Lock()
	cacheAtAfter := surgeEng.ListCacheAtForTesting()
	surgeEng.ListCacheMuForTesting().Unlock()
	if cacheAtAfter.IsZero() {
		t.Errorf("listCacheAt = zero, want %v (progress should not invalidate cache)", cacheAtBefore)
	}
}
