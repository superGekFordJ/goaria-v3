package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

// ==================== handleTaskComplete fallback chain + rate limit tests====================

// speedstatsRecordCount returns the number of in-memory speedstats records.

func TestHandleSurgeEvent_DiscardsStalePauseAfterResume(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{GID: "sg_test-1", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.BumpPauseResumeIntention("sg_test-1", PauseResumeIntentionPause)
	m.BumpPauseResumeIntention("sg_test-1", PauseResumeIntentionResume)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-1"})

	pusher.mu.Lock()
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-1" {
			pusher.mu.Unlock()
			t.Fatal("expected no pause delta for stale pause event")
		}
	}
	pusher.mu.Unlock()

	Cache.sgMu.RLock()
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_test-1" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected task NOT moved to waiting for stale pause")
		}
	}
	Cache.sgMu.RUnlock()
}

func TestHandleSurgeEvent_AcceptsPauseWhenLastIntentionIsPause(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{GID: "sg_test-2", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.BumpPauseResumeIntention("sg_test-2", PauseResumeIntentionPause)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-2"})

	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-2" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Fatal("expected pause delta when last intention is pause")
	}

	Cache.sgMu.RLock()
	foundInWaiting := false
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_test-2" {
			foundInWaiting = true
			break
		}
	}
	Cache.sgMu.RUnlock()
	if !foundInWaiting {
		t.Fatal("expected task in waiting list when last intention is pause")
	}
}

func TestHandleSurgeEvent_AcceptsPauseWithNoPriorIntention(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{GID: "sg_test-3", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-3"})

	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-3" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Fatal("expected pause delta with no prior intention")
	}
}

func TestHandleSurgeEvent_DiscardsPauseAgainstStoppedWithoutIntention(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_term-1", Status: "error",
		ErrorCode: "1", ErrorMessage: "fail",
	}}
	defer func() { Cache.sgStopped = nil; Cache.sgWaiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "term-1"})

	if !Cache.IsInStopped("sg_term-1") {
		t.Fatal("expected task to remain in stopped after late pause")
	}
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_term-1" {
			t.Fatal("expected late pause not to revive stopped task into waiting")
		}
	}
}

func TestHandleSurgeEvent_DiscardsPauseAgainstStoppedEvenWithPauseIntention(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:                   hub,
		pusher:                pusher,
		pauseResumeIntentions: make(map[string]string),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_term-batch", Status: "error",
		ErrorCode: "1", ErrorMessage: "fail",
	}}
	defer func() { Cache.sgStopped = nil; Cache.sgWaiting = nil; Cache.sgActive = nil }()

	// BatchPause re-arms pause intention on terminal GIDs; must still discard.
	m.BumpPauseResumeIntention("sg_term-batch", PauseResumeIntentionPause)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "term-batch"})

	if !Cache.IsInStopped("sg_term-batch") {
		t.Fatal("expected stopped task to remain stopped despite pause intention")
	}
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_term-batch" {
			t.Fatal("expected BatchPause-armed pause not to revive stopped→waiting")
		}
	}
}
