package monitor

// Regression test for terminal-delta enqueue order: complete/error/remove
// deltas are queued inside the same GID gate that commits the terminal
// state, before the post-gate finishTerminalCopy (which can block on an
// engine TellStatus when metadata is missing). Commit-ordered dedup relies
// on Queue order matching commit order; this test parks an error event in
// the blocking finish and asserts its stale delta cannot displace a newer
// committed resume on the wire.

import (
	"errors"
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/types"
)

// blockingTellStatusEngine blocks TellStatus until released, letting the
// test park an error event in finishTerminalCopy's post-gate finish after
// its commit and in-gate delta enqueue have already completed.
type blockingTellStatusEngine struct {
	mockSurgeActiveEngine
	called  chan struct{}
	release chan struct{}
}

func (e *blockingTellStatusEngine) TellStatus(gid string, keys []string) (rpc.Task, error) {
	e.called <- struct{}{}
	<-e.release
	return rpc.Task{
		GID:   gid,
		Files: []rpc.File{{Path: "/downloads/" + gid + ".bin"}},
		Dir:   "/downloads",
	}, nil
}

// Invariant: an EventError commits stopped and queues its delta under the
// GID gate, then parks inside finishTerminalCopy's blocking TellStatus. A
// same-GID ApplyResume commits waiting and queues its resume delta under
// the gate while the error event is parked — commit order and queue order
// both say the resume is newer. The pending batch must end with the resume
// alone; shipping the stale error would fold the row back into stopped on
// the frontend while Cache holds it in waiting.
func TestHandleSurgeEvent_StaleTerminalDeltaYieldsToNewerResume(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	reader := &mockSurgeListReader{}
	eng := &blockingTellStatusEngine{
		called:  make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	m := &Monitor{
		hub:               hub,
		pusher:            pusher,
		tracker:           tracker,
		engine:            rpc.NewHybridEngine(nil, eng),
		surgePollReader:   reader,
		surgePollInterval: 0,
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	t.Cleanup(func() {
		State.SetWindowExists(prevWindow)
		ResetDownloadGroupNamerForTest()
		resetCacheSg()
	})
	resetHistoryForTest(t)

	const gid, id = "sg_term_stale", "term_stale"
	Cache.AddSgTask(rpc.Task{GID: gid, Status: "active", TotalLength: "1000"}, "active")

	errorDone := make(chan struct{})
	go func() {
		defer close(errorDone)
		m.handleSurgeEvent(surgeEvents.DownloadEvent{
			Type:       surgeEvents.EventError,
			DownloadID: id,
			Err:        errors.New("injected error"),
		})
	}()

	// Wait until the error event is parked inside finishTerminalCopy's
	// blocking TellStatus — commit done, delta queued, gate released.
	<-eng.called

	// The resume commits under the gate now: waiting + move s→w + resume
	// delta, all published in commit order inside the gate.
	m.ApplyResume(gid)

	_, list, _ := Cache.GetTask(gid)
	if list != "waiting" {
		t.Fatalf("cache = %q, want waiting after resume commit", list)
	}

	close(eng.release)
	<-errorDone

	deltas := pusher.pendingSnapshot()
	t.Logf("pending deltas: %+v", deltas)

	// The newest committed fact (resume/waiting) must survive; the stale
	// terminal delta — queued inside its own commit gate before the blocking
	// finish — lands first in dedup and is replaced by the newer resume.
	if len(deltas) == 1 && deltas[0].Type == "error" {
		t.Fatalf("stale error delta displaced the newer resume; wire ships error while cache is waiting")
	}
	if len(deltas) != 1 || deltas[0].Type != "resume" {
		t.Fatalf("unexpected pending stream: %+v", deltas)
	}
}
