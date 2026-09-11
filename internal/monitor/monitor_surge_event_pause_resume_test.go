package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

func TestHandleSurgeEvent_PauseEvent_QueuesPauseDeltaAndPatchesCache(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Seed cache with an active task
	Cache.sgActive = []rpc.Task{{GID: "sg_test-pause", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventPaused,
		DownloadID: "test-pause",
	})

	// Verify pusher queued a pause delta
	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-pause" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Error("expected pause delta in pusher pending queue")
	}

	// Verify task was moved from active to waiting with status=paused
	Cache.sgMu.RLock()
	for _, task := range Cache.sgActive {
		if task.GID == "sg_test-pause" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected task removed from active list")
		}
	}
	foundInWaiting := false
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_test-pause" {
			if task.Status != "paused" {
				t.Errorf("expected status 'paused', got %q", task.Status)
			}
			if task.DownloadSpeed != "0" {
				t.Errorf("expected DownloadSpeed '0', got %q", task.DownloadSpeed)
			}
			foundInWaiting = true
			break
		}
	}
	Cache.sgMu.RUnlock()
	if !foundInWaiting {
		t.Error("expected task in waiting list after pause")
	}
}

func TestHandleSurgeEvent_ResumeEvent_QueuesResumeDeltaAndPatchesCache(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Seed cache with a paused task in waiting list
	Cache.sgWaiting = []rpc.Task{{GID: "sg_test-resume", Status: "paused"}}
	defer func() { Cache.sgWaiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventResumed,
		DownloadID: "test-resume",
	})

	// Verify pusher queued a resume delta
	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "resume" && d.GID == "sg_test-resume" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Error("expected resume delta in pusher pending queue")
	}

	// Verify task was moved from waiting to active with status=active
	Cache.sgMu.RLock()
	for _, task := range Cache.sgWaiting {
		if task.GID == "sg_test-resume" {
			Cache.sgMu.RUnlock()
			t.Fatal("expected task removed from waiting list")
		}
	}
	foundInActive := false
	for _, task := range Cache.sgActive {
		if task.GID == "sg_test-resume" {
			if task.Status != "active" {
				t.Errorf("expected status 'active', got %q", task.Status)
			}
			foundInActive = true
			break
		}
	}
	Cache.sgMu.RUnlock()
	if !foundInActive {
		t.Error("expected task in active list after resume")
	}
}

func TestHandleSurgeEvent_ResumeEvent_FromStopped_EmitsStoppedFrom(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_test-resume-stopped", Status: "error",
		ErrorCode: "9", ErrorMessage: "fail",
		Files: []rpc.File{{Path: "/dl/a.bin"}}, Dir: "/dl",
	}}
	Cache.sgActive = nil
	Cache.sgWaiting = nil
	defer func() {
		Cache.sgStopped = nil
		Cache.sgActive = nil
		Cache.sgWaiting = nil
	}()

	var gotMove *events.TaskMove
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		cp := move
		gotMove = &cp
	})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventResumed,
		DownloadID: "test-resume-stopped",
	})

	if len(Cache.GetStopped()) != 0 {
		t.Fatalf("expected stopped empty, got %#v", Cache.GetStopped())
	}
	found := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_test-resume-stopped" {
			found = true
			if task.Status != "active" {
				t.Errorf("Status = %q, want active", task.Status)
			}
			if task.ErrorCode != "" || task.ErrorMessage != "" {
				t.Errorf("expected cleared errors, got code=%q msg=%q", task.ErrorCode, task.ErrorMessage)
			}
		}
	}
	if !found {
		t.Fatal("expected task in active after resume from stopped")
	}
	if gotMove == nil {
		t.Fatal("expected task:move emission")
	}
	if gotMove.From != "stopped" || gotMove.To != "active" {
		t.Errorf("move From/To = %q/%q, want stopped/active", gotMove.From, gotMove.To)
	}
}

func TestHandleSurgeEvent_ResumeEvent_UnknownGID_NoMove(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = nil
	Cache.sgWaiting = nil
	Cache.sgStopped = nil

	var moveCount int
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		if move.GID == "sg_test-resume-missing" {
			moveCount++
		}
	})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventResumed,
		DownloadID: "test-resume-missing",
	})

	if moveCount != 0 {
		t.Fatalf("expected no task:move for unknown GID, got %d", moveCount)
	}
}

func TestHandleSurgeEvent_PauseResumePauseSequence(t *testing.T) {
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

	Cache.sgActive = []rpc.Task{{GID: "sg_test-4", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.BumpPauseResumeIntention("sg_test-4", PauseResumeIntentionPause)
	m.BumpPauseResumeIntention("sg_test-4", PauseResumeIntentionResume)
	m.BumpPauseResumeIntention("sg_test-4", PauseResumeIntentionPause)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-4"})

	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-4" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Fatal("expected pause delta when last intention is pause in pause-resume-pause sequence")
	}
}

func TestHandleSurgeEvent_NilMonitorIntentionMaps(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{hub: hub, pusher: pusher}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{GID: "sg_test-5", Status: "active", DownloadSpeed: "100"}}
	defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "test-5"})

	pusher.mu.Lock()
	found := false
	for _, d := range pusher.pending {
		if d.Type == "pause" && d.GID == "sg_test-5" {
			found = true
			break
		}
	}
	pusher.mu.Unlock()
	if !found {
		t.Fatal("expected pause delta with nil intention maps (accept by default)")
	}
}
