package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

func TestResumeBackend_EventAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name, source, live, action string
		event                      surgeEvents.EventType
		want                       string
	}{
		{"early queued", "", "", "", surgeEvents.EventQueued, "waiting"},
		{"terminal queued refused", "error", "", "", surgeEvents.EventQueued, "error"},
		{"terminal real queued", "error", "queued", "", surgeEvents.EventQueued, "waiting"},
		{"running late queued", "active", "downloading", "", surgeEvents.EventQueued, "active"},
		{"terminal residual start", "error", "", "legacy", surgeEvents.EventStarted, "error"},
		{"terminal real start", "error", "downloading", "", surgeEvents.EventStarted, "active"},
		{"resume queued ack", "paused", "", "resume", surgeEvents.EventResumed, "waiting"},
		{"late ack after start", "active", "queued", "resume", surgeEvents.EventResumed, "active"},
		{"pause superseded", "active", "", "resume", surgeEvents.EventPaused, "active"},
		{"pause against running", "active", "downloading", "", surgeEvents.EventPaused, "active"},
		{"resume superseded", "paused", "queued", "pause", surgeEvents.EventResumed, "paused"},
		{"queued superseded", "paused", "queued", "pause", surgeEvents.EventQueued, "paused"},
		{"resume already running", "paused", "downloading", "resume", surgeEvents.EventResumed, "active"},
		{"resume live unknown row", "", "queued", "", surgeEvents.EventResumed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetHistoryForTest(t)
			m, reader, pusher, tracker := newReconcileTestMonitor(t)
			m.surgeEng = &rpc.SurgeEngine{}
			full := fullRowForTest("sg_accept")
			full.Status = tc.source
			if tc.source != "" {
				Cache.AddSgTask(full, engineListForStatus(tc.source))
			}
			if tc.source == "error" {
				tracker.MarkCompleteFromEvent(full.GID, "error")
			}
			m.chunkSnapshots = NewChunkSnapshotCache()
			m.chunkSnapshots.Set(full.GID, []byte{1}, 4, 100, nil)
			m.surgeEng.UpsertMasterCacheEntry(surgeEvents.DownloadRecord{ID: "accept", Status: "old", URL: "https://example.com/full", DestPath: "/dl/full", Filename: "full", TotalSize: 1000, Downloaded: 200, CreatedAt: 42, SkipServerProbe: true})
			reader.setLiveStatus("accept", rpc.SurgeLiveStatus{Status: tc.live, Known: tc.live != ""})
			if tc.action == "legacy" {
				m.BumpPauseResumeIntention(full.GID, "resume")
			} else if tc.action != "" {
				m.BeginPauseResumeRequest(full.GID, tc.action)
			}
			m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: tc.event, DownloadID: "accept"})
			got, _, _ := Cache.GetTask(full.GID)
			if got.Status != tc.want {
				t.Fatalf("got %s want %s", got.Status, tc.want)
			}
			master, _ := m.surgeEng.GetMasterCacheEntry("accept")
			if master.URL == "" || master.CreatedAt != 42 || master.Downloaded != 200 || !master.SkipServerProbe {
				t.Fatal("sparse event lost master")
			}
			if tc.source == tc.want {
				if master.Status != "old" || len(pusher.pending) != 0 {
					t.Fatal("rejected event wrote facts/UI")
				}
				if _, ok := m.chunkSnapshots.Get(full.GID); !ok {
					t.Fatal("rejected event cleared chunk")
				}
			} else if tc.source != "" {
				assertFullRowMetadata(t, got, full)
			}
		})
	}
}

func TestHandleSurgeEvent_PauseEvent_QueuesPauseDeltaAndPatchesCache(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid, tracker: NewTaskTracker()}

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
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid, tracker: NewTaskTracker()}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	// Seed cache with a paused task in waiting list
	Cache.sgWaiting = []rpc.Task{{GID: "sg_test-resume", Status: "paused"}}
	defer func() { Cache.sgWaiting = nil }()

	m.BeginPauseResumeRequest("sg_test-resume", PauseResumeIntentionResume)
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

	task, list, _ := Cache.GetTask("sg_test-resume")
	if list != "waiting" || task.Status != "waiting" {
		t.Fatalf("expected waiting acknowledgement: %#v", task)
	}
}

func TestHandleSurgeEvent_ResumeEvent_FromStopped_EmitsStoppedFrom(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid, tracker: NewTaskTracker()}

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

	m.BeginPauseResumeRequest("sg_test-resume-stopped", PauseResumeIntentionResume)
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventResumed,
		DownloadID: "test-resume-stopped",
	})

	if len(Cache.GetStopped()) != 0 {
		t.Fatalf("expected stopped empty, got %#v", Cache.GetStopped())
	}
	found := false
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_test-resume-stopped" {
			found = true
			if task.Status != "waiting" {
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
	if gotMove.From != "stopped" || gotMove.To != "waiting" {
		t.Errorf("move From/To = %q/%q, want stopped/active", gotMove.From, gotMove.To)
	}
}

func TestHandleSurgeEvent_ResumeEvent_UnknownGID_NoMove(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	se := &rpc.SurgeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{hub: hub, pusher: pusher, engine: hybrid, tracker: NewTaskTracker()}

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
		pauseResumeIntentions: make(map[string]pauseResumeIntention),
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
