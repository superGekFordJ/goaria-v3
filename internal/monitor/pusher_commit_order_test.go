package monitor

// Regression tests for the stale-delta demotion family: pusher dedup must
// follow commit order (Queue serializes under p.mu), and per-GID publishes
// must emit inside the lifecycle gate so concurrent commits cannot invert
// the wire stream the frontend receives.

import (
	"sync"
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
)

// Dedup is commit-ordered: a pause commit at T0 queued its pause delta; a
// resume commit at T1 (T0<T1<T0+50ms) queues a resume delta that must
// replace it — the resume is the newer truth.
func TestPusherDedup_NewerResumeReplacesPendingPause(t *testing.T) {
	hub := events.NewHub(nil)
	p := NewPusher(hub)

	p.Queue(events.TaskDelta{Type: "pause", GID: "sg_a", Payload: rpc.Task{GID: "sg_a", Status: "paused"}})
	p.Queue(events.TaskDelta{Type: "resume", GID: "sg_a", Payload: rpc.Task{GID: "sg_a", Status: "active"}})

	deltas := p.pendingSnapshot()
	if len(deltas) != 1 {
		t.Fatalf("expected exactly 1 queued delta, got %d (%+v)", len(deltas), deltas)
	}
	if deltas[0].Type != "resume" {
		t.Fatalf("newest committed fact was lost: pending delta is %q (stale pause survives)", deltas[0].Type)
	}
}

// Same ordering in the other direction: a pending terminal delta (error or
// complete) must yield to a newer resume ack, or the frontend would fold a
// live row into stopped even though Cache moved it to waiting/active.
func TestPusherDedup_NewerResumeReplacesPendingTerminal(t *testing.T) {
	for _, terminal := range []string{"error", "complete"} {
		hub := events.NewHub(nil)
		p := NewPusher(hub)

		p.Queue(events.TaskDelta{Type: terminal, GID: "sg_b", Payload: rpc.Task{GID: "sg_b", Status: terminal}})
		p.Queue(events.TaskDelta{Type: "resume", GID: "sg_b", Payload: rpc.Task{GID: "sg_b", Status: "waiting"}})

		deltas := p.pendingSnapshot()
		if len(deltas) != 1 || deltas[0].Type != "resume" {
			t.Fatalf("%s→resume: newest fact lost, pending=%+v", terminal, deltas)
		}
	}
}

// And an older pause must yield to a newer add (requeue) delta.
func TestPusherDedup_NewerAddReplacesPendingPause(t *testing.T) {
	hub := events.NewHub(nil)
	p := NewPusher(hub)

	p.Queue(events.TaskDelta{Type: "pause", GID: "sg_c", Payload: rpc.Task{GID: "sg_c", Status: "paused"}})
	p.Queue(events.TaskDelta{Type: "add", GID: "sg_c", Payload: rpc.Task{GID: "sg_c", Status: "waiting"}})

	deltas := p.pendingSnapshot()
	if len(deltas) != 1 || deltas[0].Type != "add" {
		t.Fatalf("newer add eaten by stale pause: pending=%+v", deltas)
	}
}

// End-to-end through the real Apply path: ApplyPause commits
// paused-in-waiting, then ApplyResume with live=downloading commits active.
// Wire stream must end with move(a→w), move(w→a), and the debounced batch
// carries only the newest committed fact — the resume delta.
func TestApplyPauseThenApplyResume_ResumeSurvivesDedup(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)

	const gid, id = "sg_pause_resume", "pause_resume"
	Cache.AddSgTask(rpc.Task{GID: gid, Status: "active", TotalLength: "1000", CompletedLength: "400"}, "active")

	var moves []string
	var mu sync.Mutex
	m.hub.SubscribeTaskMove(func(mv events.TaskMove) {
		if mv.GID != gid {
			return
		}
		mu.Lock()
		moves = append(moves, mv.From+">"+mv.To)
		mu.Unlock()
	})

	reader.setLiveStatus(id, rpc.SurgeLiveStatus{Known: true, Status: "paused"})
	m.ApplyPause(gid)

	reader.setLiveStatus(id, rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	m.ApplyResume(gid)

	got, list, _ := Cache.GetTask(gid)
	if list != "active" || got.Status != "active" {
		t.Fatalf("cache = %q in %s, want active/active", got.Status, list)
	}

	mu.Lock()
	gotMoves := append([]string(nil), moves...)
	mu.Unlock()
	t.Logf("moves emitted: %v", gotMoves)
	if len(gotMoves) != 2 || gotMoves[0] != "active>waiting" || gotMoves[1] != "waiting>active" {
		t.Fatalf("unexpected move stream: %v", gotMoves)
	}

	deltas := pusher.pendingSnapshot()
	t.Logf("pending deltas after debounce window: %+v", deltas)
	if len(deltas) != 1 || deltas[0].Type != "resume" {
		t.Fatalf("expected the newer resume delta to survive dedup, got %+v", deltas)
	}
}

// Move/delta publishes run inside the GID gate, so a move listener observed
// mid-pause-emit cannot let a concurrent resume overtake it: the resume
// commit waits on the gate, lands second, and emits second. Wire order must
// stay [a→w, w→a] and the surviving queued delta is the resume.
func TestPublishOrder_InGateEmitPreservesCommitOrder(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)

	const gid, id = "sg_ingate", "ingate"
	Cache.AddSgTask(rpc.Task{GID: gid, Status: "active", TotalLength: "1000"}, "active")

	var once sync.Once
	resumeDone := make(chan struct{})
	var moves []string
	m.hub.SubscribeTaskMove(func(mv events.TaskMove) {
		if mv.GID != gid {
			return
		}
		moves = append(moves, mv.From+">"+mv.To)
		if mv.To == "waiting" {
			// Start the resume while the pause emit still holds the gate:
			// its RunUnderLifecycle must wait, so its emit lands after this
			// one even though it runs on another goroutine.
			once.Do(func() {
				go func() {
					defer close(resumeDone)
					reader.setLiveStatus(id, rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
					m.ApplyResume(gid)
				}()
			})
		}
	})

	reader.setLiveStatus(id, rpc.SurgeLiveStatus{Known: true, Status: "paused"})
	m.ApplyPause(gid) // commits waiting and emits a→w inside the gate
	<-resumeDone

	t.Logf("move emit order: %v", moves)
	if len(moves) != 2 || moves[0] != "active>waiting" || moves[1] != "waiting>active" {
		t.Fatalf("emit order diverged from commit order: %v", moves)
	}
	if deltas := pusher.pendingSnapshot(); len(deltas) != 1 || deltas[0].Type != "resume" {
		t.Fatalf("expected newest committed resume delta to survive dedup, got %+v", deltas)
	}
}
