package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

func TestHandleSurgeEvent_ResumeFromStopped_RetiresHistory(t *testing.T) {
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

	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	Cache.sgStopped = []rpc.Task{{
		GID: "sg_hist-1", Status: "error",
		ErrorCode: "1", ErrorMessage: "fail",
		Files: []rpc.File{{Path: "/tmp/hist.bin"}},
	}}
	defer func() { Cache.sgStopped = nil; Cache.sgActive = nil }()

	history.Add(history.HistoryEntry{
		GID: "sg_hist-1", Path: "/tmp/hist.bin", Status: "error",
	})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventResumed, DownloadID: "hist-1"})

	if Cache.IsInStopped("sg_hist-1") {
		t.Fatal("expected task moved out of stopped on resume")
	}
	foundActive := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_hist-1" {
			foundActive = true
		}
	}
	if !foundActive {
		t.Fatal("expected task in active after resume from stopped")
	}
	if _, ok := history.Get("sg_hist-1"); ok {
		t.Fatal("expected history entry retired after stopped→active resume")
	}
}

func TestRetireHistoryIfResumedFromStopped_RehomesDownloadGroup(t *testing.T) {
	setupTaskGroupStoreTest(t)
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	group := testDownloadGroup("dg-retire-home")
	history.Add(history.HistoryEntry{
		GID:           "sg_grp_resume",
		Path:          "/tmp/g.bin",
		Status:        "error",
		DownloadGroup: copyDownloadGroup(&group),
	})
	history.SetGroupCleanupHooks(RemoveTaskGroup, RemoveTaskGroups, ClearTaskGroups)

	Cache.sgActive = []rpc.Task{{GID: "sg_grp_resume", Status: "active"}}
	defer func() { Cache.sgActive = nil; Cache.metadata = make(map[string]*TaskMetadata) }()

	RetireHistoryIfResumedFromStopped("sg_grp_resume", "stopped")

	if _, ok := history.Get("sg_grp_resume"); ok {
		t.Fatal("expected history removed")
	}
	stored := GetStoredTaskGroup("sg_grp_resume")
	if stored == nil || stored.ID != group.ID {
		t.Fatalf("expected download_group re-homed to group store, got %#v", stored)
	}
}

func TestTaskCache_UpdateFromAria2_RetiresHistoryOnStoppedToLive(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	tracker := NewTaskTracker()
	tracker.EnsureTrackedFromEvent("ar_resume_hist", 1000, "https://example.com/ar.bin", 0, "error")
	if completed := tracker.MarkCompleteFromEvent("ar_resume_hist", "error"); completed == nil {
		t.Fatal("expected first terminal")
	}
	prevMon := State.GetMonitor()
	State.SetMonitor(&Monitor{tracker: tracker})
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	cache := &TaskCache{metadata: make(map[string]*TaskMetadata)}
	cache.UpdateFromAria2(nil, nil, []rpc.Task{{
		GID: "ar_resume_hist", Status: "error", ErrorCode: "1",
		Files: []rpc.File{{Path: "/tmp/ar.bin"}},
	}})
	history.Add(history.HistoryEntry{
		GID: "ar_resume_hist", Path: "/tmp/ar.bin", Status: "error",
	})

	cache.UpdateFromAria2([]rpc.Task{{
		GID: "ar_resume_hist", Status: "active",
		Files: []rpc.File{{Path: "/tmp/ar.bin"}},
	}}, nil, nil)

	if _, ok := history.Get("ar_resume_hist"); ok {
		t.Fatal("expected Aria2 stopped→active to retire history")
	}
	if tracker.processedComplete["ar_resume_hist"] {
		t.Fatal("expected Aria2 stopped→live to reopen tracker processedComplete")
	}
}
