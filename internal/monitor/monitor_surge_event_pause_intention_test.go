package monitor

import (
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

// A late Paused event loses to known live pool evidence (queued/downloading):
// cache, list membership, master mirror and deltas stay untouched.
func TestHandleSurgeEvent_DiscardsLatePauseAgainstLivePool(t *testing.T) {
	for _, live := range []string{"queued", "downloading"} {
		t.Run(live, func(t *testing.T) {
			hub := events.NewHub(nil)
			pusher := NewPusher(hub)
			reader := &mockSurgeListReader{}
			surgeEng := rpc.NewSurgeEngineForTesting(nil)
			t.Cleanup(surgeEng.Close)
			m := &Monitor{
				hub:             hub,
				pusher:          pusher,
				tracker:         NewTaskTracker(),
				surgeEng:        surgeEng,
				surgePollReader: reader,
			}

			prevWindow := State.HasWindow()
			State.SetWindowExists(true)
			defer State.SetWindowExists(prevWindow)

			gid := "sg_stale-pause"
			Cache.sgActive = []rpc.Task{{GID: gid, Status: "active", DownloadSpeed: "100"}}
			defer func() { Cache.sgActive = nil; Cache.sgWaiting = nil }()
			surgeEng.UpsertMasterCacheEntry(surgeEvents.DownloadRecord{ID: "stale-pause", Status: "downloading"})
			reader.setLiveStatus("stale-pause", rpc.SurgeLiveStatus{Known: true, Status: live})

			m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "stale-pause"})

			for _, d := range pusher.pendingSnapshot() {
				if d.GID == gid {
					t.Fatalf("rejected pause emitted a %q delta", d.Type)
				}
			}
			got, list, _ := Cache.GetTask(gid)
			if list != "active" || got.Status != "active" {
				t.Fatalf("late pause moved the row: %#v %s", got, list)
			}
			if master, ok := surgeEng.GetMasterCacheEntry("stale-pause"); !ok || master.Status != "downloading" {
				t.Fatalf("rejected pause rewrote the master mirror: %#v ok=%v", master, ok)
			}
		})
	}
}

// A real Paused event lands on a live row: accepted under live paused/pausing
// and under an unknown live read (pause acknowledgements legitimately precede
// the pool status write).
func TestHandleSurgeEvent_AcceptsPauseOnLivePausedOrUnknown(t *testing.T) {
	for _, live := range []rpc.SurgeLiveStatus{
		{Known: true, Status: "paused"},
		{Known: true, Status: "pausing"},
		{},
	} {
		hub := events.NewHub(nil)
		pusher := NewPusher(hub)
		reader := &mockSurgeListReader{}
		m := &Monitor{
			hub:             hub,
			pusher:          pusher,
			tracker:         NewTaskTracker(),
			surgePollReader: reader,
		}

		prevWindow := State.HasWindow()
		State.SetWindowExists(true)

		gid := "sg_pause-accept"
		Cache.sgActive = []rpc.Task{{GID: gid, Status: "active", DownloadSpeed: "100"}}
		reader.setLiveStatus("pause-accept", live)

		m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventPaused, DownloadID: "pause-accept"})

		State.SetWindowExists(prevWindow)
		Cache.sgActive = nil

		found := false
		for _, d := range pusher.pendingSnapshot() {
			if d.Type == "pause" && d.GID == gid {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected pause delta under live %#v", live)
		}
		got, list, _ := Cache.GetTask(gid)
		if list != "waiting" || got.Status != "paused" {
			t.Fatalf("pause did not land paused in waiting under live %#v: %#v %s", live, got, list)
		}
		Cache.sgWaiting = nil
	}
}

// A Paused event against a terminal stopped row is refused: no waiting
// revival, no error-field churn, no delta.
func TestHandleSurgeEvent_DiscardsPauseAgainstStopped(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:     hub,
		pusher:  pusher,
		tracker: NewTaskTracker(),
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
	if got, _, _ := Cache.GetTask("sg_term-1"); got.ErrorCode != "1" || got.ErrorMessage != "fail" {
		t.Fatalf("rejected pause rewrote error fields: %#v", got)
	}
	for _, d := range pusher.pendingSnapshot() {
		if d.GID == "sg_term-1" {
			t.Fatalf("rejected pause emitted a %q delta", d.Type)
		}
	}
}
