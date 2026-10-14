package monitor

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

func TestResumeConfirmation_SourceAndFreshness(t *testing.T) {
	for _, source := range []string{"paused", "stopped", "history", "unknown", "active"} {
		t.Run(source, func(t *testing.T) {
			resetHistoryForTest(t)
			m, _, _, tracker := newReconcileTestMonitor(t)
			full := fullRowForTest("sg_confirm")
			full.Status = source
			if source == "history" || source == "stopped" {
				history.Add(history.HistoryEntry{GID: full.GID, Status: "error", Path: full.Files[0].Path, TotalLength: full.TotalLength, CompletedLength: full.CompletedLength, DownloadGroup: full.DownloadGroup})
				tracker.MarkCompleteFromEvent(full.GID, "error")
			}
			switch source {
			case "paused":
				Cache.AddSgTask(full, "waiting")
			case "stopped":
				full.Status = "error"
				Cache.AddSgTask(full, "stopped")
			case "active":
				Cache.AddSgTask(full, "active")
			}
			ticket := m.BeginPauseResumeRequest(full.GID, "resume")
			m.ConfirmPauseResumeRequest(ticket)
			m.ConfirmPauseResumeRequest(ticket)
			got, list, ok := Cache.GetTask(full.GID)
			if source == "unknown" {
				if ok {
					t.Fatal("unknown created a row")
				}
				return
			}
			if source == "active" {
				if list != "active" || got.Status != "active" {
					t.Fatal("ack demoted start")
				}
				return
			}
			if list != "waiting" || got.Status != "waiting" || got.CompletedLength != full.CompletedLength {
				t.Fatalf("confirmation: %#v %s", got, list)
			}
			if source != "history" {
				assertFullRowMetadata(t, got, full)
			}
			if _, exists := history.Get(full.GID); exists {
				t.Fatal("H0 not retired")
			}
			wantGen := uint64(0)
			if source != "paused" {
				wantGen = 1
			}
			if tracker.LifecycleGeneration(full.GID) != wantGen {
				t.Fatal("duplicate reopen")
			}
			if tt, _ := tracker.GetTrackedTask(full.GID); !tt.ActivatedAt.IsZero() {
				t.Fatal("ack activated")
			}
		})
	}
	for _, first := range []string{"pause", "resume"} {
		m, _, _, _ := newReconcileTestMonitor(t)
		full := fullRowForTest("sg_supersede")
		full.Status = "paused"
		Cache.AddSgTask(full, "waiting")
		old := m.BeginPauseResumeRequest(full.GID, first)
		newer := m.BeginPauseResumeRequest(full.GID, "resume")
		if first == "resume" {
			newer = m.BeginPauseResumeRequest(full.GID, "pause")
		}
		m.ConfirmPauseResumeRequest(old)
		if m.ClearPauseResumeIntentionIfCurrent(old) || !m.RunIfPauseResumeRequestCurrent(newer, func() {}) {
			t.Fatal("old cleanup clobbered new request")
		}
		got, _, _ := Cache.GetTask(full.GID)
		if got.Status != "paused" {
			t.Fatal("stale write")
		}
	}
}

func TestResumeConfirmation_DetachedGateWaiter(t *testing.T) {
	tracker := NewTaskTracker()
	const gid = "sg_detached"
	old := tracker.getLifecycle(gid)
	old.mu.Lock()
	done := make(chan struct{})
	go tracker.RunUnderLifecycle(gid, func() {
		if tracker.getLifecycle(gid) == old {
			t.Error("waiter used detached entry")
		}
		close(done)
	})
	tracker.removeTaskLocked(gid)
	old.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("detached waiter stuck")
	}
}

func TestLifecycle_FreshnessStamp(t *testing.T) {
	tracker := NewTaskTracker()
	const gid = "sg_stamp"
	tracker.EnsureTrackedFromEvent(gid, 1000, "", 1, "active")
	stamp := tracker.captureLifecycleStamp(gid)
	tracker.ObserveActivation(gid, 0, true)
	tracker.UpdateProgressFromEvent(gid, 1000, 100)
	tracker.SetStatusFromEvent(gid, "paused")
	tracker.SetStatusFromEvent(gid, "waiting")
	if !tracker.lifecycleStampCurrent(gid, stamp) || tracker.LifecycleGeneration(gid) != 0 {
		t.Fatal("ordinary facts advanced freshness/generation")
	}
	for i := range 2 {
		stamp = tracker.captureLifecycleStamp(gid)
		accepted := tracker.MarkCompleteFromEvent(gid, "error")
		if (accepted != nil) != (i == 0) || tracker.lifecycleStampCurrent(gid, stamp) || tracker.LifecycleGeneration(gid) != 0 {
			t.Fatal("first/dedup terminal freshness changed acceptance or generation")
		}
	}
	stamp = tracker.captureLifecycleStamp(gid)
	tracker.ReopenAfterStoppedToLive(gid, "waiting")
	if tracker.lifecycleStampCurrent(gid, stamp) || tracker.LifecycleGeneration(gid) != 1 {
		t.Fatal("reopen did not advance freshness/generation")
	}
	stamp = tracker.captureLifecycleStamp(gid)
	tracker.RemoveTask(gid)
	tracker.EnsureTrackedFromEvent(gid, 1000, "", 1, "active")
	if tracker.lifecycleStampCurrent(gid, stamp) {
		t.Fatal("same-ID recreation retained stamp")
	}
}

func TestPauseResumeTicket_CurrentAndStale(t *testing.T) {
	changes := []struct {
		name   string
		dedup  bool
		change func(*Monitor, string)
	}{
		{"new resume", false, func(m *Monitor, gid string) { m.BeginPauseResumeRequest(gid, "resume") }},
		{"new pause", false, func(m *Monitor, gid string) { m.BeginPauseResumeRequest(gid, "pause") }},
		{"legacy bump", false, func(m *Monitor, gid string) { m.BumpPauseResumeIntention(gid, "resume") }},
		{"legacy clear", false, func(m *Monitor, gid string) { m.ClearPauseResumeIntention(gid) }},
		{"direct intention delete", false, func(m *Monitor, gid string) {
			m.pauseResumeVersionMu.Lock()
			delete(m.pauseResumeIntentions, gid)
			m.pauseResumeVersionMu.Unlock()
		}},
		{"invalidate", false, func(m *Monitor, gid string) { m.InvalidateTask(gid) }},
		{"terminal", false, func(m *Monitor, gid string) { m.tracker.MarkCompleteFromEvent(gid, "error") }},
		{"dedup tracker terminal", true, func(m *Monitor, gid string) { m.tracker.MarkCompleteFromEvent(gid, "error") }},
		{"dedup terminal", true, func(m *Monitor, gid string) { m.markCompleteAndHandle(gid, "error", nil) }},
		{"poll terminal", false, func(m *Monitor, gid string) { m.tracker.Update(nil, nil, []rpc.Task{{GID: gid, Status: "error"}}) }},
		{"dedup poll terminal", true, func(m *Monitor, gid string) { m.tracker.Update(nil, nil, []rpc.Task{{GID: gid, Status: "error"}}) }},
		{"reopen", true, func(m *Monitor, gid string) { m.tracker.ReopenAfterStoppedToLive(gid, "waiting") }},
		{"remove same ID", true, func(m *Monitor, gid string) {
			m.tracker.RemoveTask(gid)
			m.tracker.EnsureTrackedFromEvent(gid, 1000, "", 1, "paused")
			m.tracker.MarkCompleteFromEvent(gid, "error")
		}},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			resetHistoryForTest(t)
			m := &Monitor{tracker: NewTaskTracker(), deletedGids: make(map[string]time.Time)}
			full := fullRowForTest("sg_ticket")
			cleanupSgCache(t, full.GID)
			list := "waiting"
			full.Status = "paused"
			if tc.dedup {
				list, full.Status = "stopped", "error"
			}
			Cache.AddSgTask(full, list)
			m.tracker.EnsureTrackedFromEvent(full.GID, 1000, "", 1, full.Status)
			if tc.dedup {
				m.tracker.MarkCompleteFromEvent(full.GID, "error")
			}
			history.Add(history.HistoryEntry{GID: full.GID, Path: full.Files[0].Path, Status: "error"})
			ticket := m.BeginPauseResumeRequest(full.GID, "resume")
			if !m.RunIfPauseResumeRequestCurrent(ticket, func() {}) {
				t.Fatal("initial live/stopped ticket rejected")
			}
			tc.change(m, full.GID)
			if m.RunIfPauseResumeRequestCurrent(ticket, func() { Cache.RemoveTask(full.GID); history.Remove(full.GID) }) {
				t.Fatal("stale confirmation ran")
			}
			got, _, ok := Cache.GetTask(full.GID)
			if !ok {
				t.Fatal("stale callback removed full")
			}
			assertFullRowMetadata(t, got, full)
			if entry, ok := history.Get(full.GID); !ok || entry.Status != "error" {
				t.Fatal("stale callback deleted H0")
			}
			newTicket := m.BeginPauseResumeRequest(full.GID, "pause")
			if newTicket.seq <= ticket.seq || m.ClearPauseResumeIntentionIfCurrent(ticket) || !m.RunIfPauseResumeRequestCurrent(newTicket, func() {}) {
				t.Fatal("old failure cleared newer request or version reused")
			}
			m.tracker.MarkCompleteFromEvent(full.GID, "error")
			if !m.ClearPauseResumeIntentionIfCurrent(newTicket) || m.RunIfPauseResumeRequestCurrent(newTicket, func() {}) {
				t.Fatal("same-version expired cleanup failed")
			}
		})
	}
}

func TestPauseResumeTicket_Barriers(t *testing.T) {
	resetHistoryForTest(t)
	const gid = "sg_ticket_barrier"
	cleanupSgCache(t, gid)
	full := fullRowForTest(gid)
	full.Status = "paused"
	Cache.AddSgTask(full, "waiting")
	m := &Monitor{tracker: NewTaskTracker()}
	m.tracker.Update(nil, []rpc.Task{full}, nil)
	old := m.BeginPauseResumeRequest(gid, "resume")
	ack, done := make(chan struct{}), make(chan bool)
	go func() {
		<-ack
		done <- m.RunIfPauseResumeRequestCurrent(old, func() { Cache.RemoveTask(gid); history.Remove(gid) })
	}()
	m.BeginPauseResumeRequest(gid, "resume")
	m.markCompleteAndHandle(gid, "error", nil)
	close(ack)
	if <-done {
		t.Fatal("late ack accepted")
	}
	got, _, _ := Cache.GetTask(gid)
	assertFullRowMetadata(t, got, full)
	if entry, ok := history.Get(gid); !ok || entry.Path != full.Files[0].Path || entry.Status != "error" {
		t.Fatal("H1 changed by old ack")
	}

	ticket := m.BeginPauseResumeRequest(gid, "resume")
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		done <- m.RunIfPauseResumeRequestCurrent(ticket, func() {
			close(entered)
			<-release
			Cache.MoveTaskToActive(gid, "active")
		})
	}()
	<-entered
	if gate := m.tracker.getLifecycle(gid); gate.mu.TryLock() {
		gate.mu.Unlock()
		t.Fatal("callback did not hold lifecycle gate")
	}
	begun, scheduled := make(chan PauseResumeTicket), make(chan struct{})
	go func() { close(scheduled); begun <- m.BeginPauseResumeRequest(gid, "pause") }()
	<-scheduled
	close(release)
	if !<-done {
		t.Fatal("current guard failed")
	}
	next := <-begun
	if next.seq <= ticket.seq || m.RunIfPauseResumeRequestCurrent(ticket, func() {}) || !m.RunIfPauseResumeRequestCurrent(next, func() {}) {
		t.Fatal("Begin did not supersede the guarded request")
	}
}

func TestPauseResumeTicket_FailClosedAndNoDataWrites(t *testing.T) {
	resetHistoryForTest(t)
	m := &Monitor{tracker: NewTaskTracker()}
	for _, mon := range []*Monitor{nil, {}, m} {
		for _, input := range [][2]string{{"", "resume"}, {" ", "resume"}, {"sg_missing", "invalid"}} {
			ticket := mon.BeginPauseResumeRequest(input[0], input[1])
			if mon.RunIfPauseResumeRequestCurrent(ticket, func() { t.Error("inert callback") }) || mon.ClearPauseResumeIntentionIfCurrent(ticket) {
				t.Fatal("inert ticket accepted")
			}
		}
	}
	noTracker := &Monitor{}
	if ticket := noTracker.BeginPauseResumeRequest("sg_missing", "resume"); ticket != (PauseResumeTicket{}) {
		t.Fatal("missing gate accepted")
	}
	noTracker.BumpPauseResumeIntention("sg_missing", "resume")
	if !noTracker.shouldDiscardStalePause("sg_missing") {
		t.Fatal("legacy nil-tracker bump changed")
	}
	noTracker.ClearPauseResumeIntention("sg_missing")
	if noTracker.shouldDiscardStalePause("sg_missing") {
		t.Fatal("legacy clear changed")
	}
	for _, withHistory := range []bool{false, true} {
		const gid = "sg_ticket_unknown"
		if withHistory {
			history.Add(history.HistoryEntry{GID: gid, Path: "/old", Status: "error"})
		}
		ticket := m.BeginPauseResumeRequest(gid, "resume")
		if _, _, ok := Cache.GetTask(gid); ok {
			t.Fatal("Begin manufactured cache")
		}
		if _, ok := history.Get(gid); ok != withHistory {
			t.Fatal("Begin changed history")
		}
		if m.RunIfPauseResumeRequestCurrent(ticket, nil) {
			t.Fatal("nil callback accepted")
		}
		if !m.ClearPauseResumeIntentionIfCurrent(ticket) {
			t.Fatal("current cleanup failed")
		}
	}
}

func TestLifecycle_ExplicitWaitingHistoryTarget(t *testing.T) {
	resetHistoryForTest(t)
	const gid = "sg_waiting_history"
	cleanupSgCache(t, gid)
	ResetTaskGroupStoreForTest("", true)
	t.Cleanup(func() { ResetTaskGroupStoreForTest("", true) })
	full := fullRowForTest(gid)
	Cache.AddSgTask(full, "stopped")
	tracker := NewTaskTracker()
	tracker.Update(nil, nil, []rpc.Task{full})
	history.Add(history.HistoryEntry{GID: gid, Path: full.Files[0].Path, Status: "error", DownloadGroup: full.DownloadGroup})
	for _, invalid := range []string{"", "unknown", "complete"} {
		tracker.RunUnderLifecycle(gid, func() { retireHistoryAndReopenToStatusLocked(tracker, gid, invalid) })
	}
	if _, ok := history.Get(gid); !ok || tracker.LifecycleGeneration(gid) != 0 {
		t.Fatal("invalid target retired history")
	}
	tracker.RunUnderLifecycle(gid, func() {
		if from := Cache.MoveTaskToWaiting(gid, "waiting"); from != "stopped" {
			t.Errorf("from=%s", from)
		}
		retireHistoryAndReopenToStatusLocked(tracker, gid, "waiting")
	})
	got, list, _ := Cache.GetTask(gid)
	assertFullRowMetadata(t, got, full)
	tracked, _ := tracker.GetTrackedTask(gid)
	if list != "waiting" || tracked.Status != "waiting" || got.ErrorCode != "" || got.ErrorMessage != "" ||
		!tracked.ActivatedAt.IsZero() || !tracked.FirstProgressAt.IsZero() || tracker.LifecycleGeneration(gid) != 1 {
		t.Fatal("waiting reopen facts wrong")
	}
	if _, ok := history.Get(gid); ok {
		t.Fatal("H0 not retired")
	}
	if group := GetStoredTaskGroup(gid); group == nil || group.ID != full.DownloadGroup.ID {
		t.Fatal("group not re-homed")
	}
	stamp := tracker.captureLifecycleStamp(gid)
	Cache.AddSgTask(rpc.Task{GID: gid, Status: "waiting"}, "waiting")
	tracker.SetStatusFromEvent(gid, "waiting")
	if !tracker.lifecycleStampCurrent(gid, stamp) || tracker.LifecycleGeneration(gid) != 1 {
		t.Fatal("same-list patch reopened")
	}

	m := &Monitor{tracker: tracker}
	m.moveToStoppedAndHandle(gid, "error", "1", "new error", 1000, nil)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	tracker.retireBetweenReopenAndRemove = func(string) { close(entered); <-release }
	go func() {
		tracker.RunUnderLifecycle(gid, func() {
			Cache.MoveTaskToWaiting(gid, "waiting")
			retireHistoryAndReopenToStatusLocked(tracker, gid, "waiting")
		})
		close(done)
	}()
	<-entered
	terminalScheduled, terminalDone := make(chan struct{}), make(chan struct{})
	go func() { close(terminalScheduled); m.markCompleteAndHandle(gid, "complete", nil); close(terminalDone) }()
	<-terminalScheduled
	close(release)
	<-done
	<-terminalDone
	if entries := historyEntriesForGID(gid); len(entries) != 1 || entries[0].Status != "complete" || entries[0].Path != full.Files[0].Path {
		t.Fatalf("H1 lost: %#v", entries)
	}
}

func historyEntriesForGID(gid string) []history.HistoryEntry {
	var out []history.HistoryEntry
	for _, e := range history.GetAll() {
		if e.GID == gid {
			out = append(out, e)
		}
	}
	return out
}

func TestLifecycle_Interleave_ReopenThenTerminalThenRemove(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{hub: hub, pusher: NewPusher(hub), tracker: tracker}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	const gid = "sg_life_a"
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/a.bin", 0, "error")
	if first := tracker.MarkCompleteFromEvent(gid, "error"); first == nil {
		t.Fatal("expected first terminal")
	}
	Cache.metadata[gid] = &TaskMetadata{GID: gid, Files: []string{"/tmp/a.bin"}, Dir: "/tmp"}
	t.Cleanup(func() { delete(Cache.metadata, gid) })
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/a.bin", Status: "error"})

	started := make(chan struct{})
	release := make(chan struct{})
	tracker.retireBetweenReopenAndRemove = func(string) {
		close(started)
		<-release
	}
	t.Cleanup(func() { tracker.retireBetweenReopenAndRemove = nil })

	var wg sync.WaitGroup
	wg.Go(func() {
		tracker.RunUnderLifecycle(gid, func() { retireHistoryAndReopenToStatusLocked(tracker, gid, "active") })
	})

	<-started
	terminalScheduled := make(chan struct{})
	wg.Go(func() {
		close(terminalScheduled)
		m.markCompleteAndHandle(gid, "complete", nil)
	})
	<-terminalScheduled
	for range 64 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	entries := historyEntriesForGID(gid)
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 history entry after reopen→terminal→remove serialize, got %d %#v", len(entries), entries)
	}
	if entries[0].Status != "complete" {
		t.Fatalf("want replacement complete entry, got %#v", entries[0])
	}
}

func TestLifecycle_Interleave_RemoveThenTerminalConcurrent(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{hub: hub, pusher: NewPusher(hub), tracker: tracker}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	const gid = "sg_life_b"
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/b.bin", 0, "error")
	if first := tracker.MarkCompleteFromEvent(gid, "error"); first == nil {
		t.Fatal("expected first terminal")
	}
	Cache.metadata[gid] = &TaskMetadata{GID: gid, Files: []string{"/tmp/b.bin"}, Dir: "/tmp"}
	t.Cleanup(func() { delete(Cache.metadata, gid) })
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/b.bin", Status: "error"})

	// Concurrent retire vs terminal: force retire to hold the gate through
	// reopen+remove while terminal blocks; after Remove, terminal must write
	// the replacement (assessment Remove→terminal window closed by the gate).
	started := make(chan struct{})
	release := make(chan struct{})
	tracker.retireBetweenReopenAndRemove = func(string) {
		close(started)
		<-release
	}
	t.Cleanup(func() { tracker.retireBetweenReopenAndRemove = nil })

	var wg sync.WaitGroup
	wg.Go(func() {
		tracker.RunUnderLifecycle(gid, func() { retireHistoryAndReopenToStatusLocked(tracker, gid, "active") })
	})
	<-started

	terminalScheduled := make(chan struct{})
	wg.Go(func() {
		close(terminalScheduled)
		m.markCompleteAndHandle(gid, "error", nil)
	})
	<-terminalScheduled
	for range 64 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	entries := historyEntriesForGID(gid)
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 history entry after concurrent retire+terminal, got %d %#v", len(entries), entries)
	}
	if entries[0].Status != "error" {
		t.Fatalf("want error entry, got %#v", entries[0])
	}
}

func TestLifecycle_NoGenerationBumpOnPauseOrWaiting(t *testing.T) {
	tracker := NewTaskTracker()
	const gid = "sg_life_pause"
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/p.bin", 0, "active")
	if first := tracker.MarkCompleteFromEvent(gid, "error"); first == nil {
		t.Fatal("expected terminal seed")
	}
	before := tracker.LifecycleGeneration(gid)

	tracker.SetStatusFromEvent(gid, "paused")
	if tracker.LifecycleGeneration(gid) != before {
		t.Fatalf("pause must not bump generation: before=%d after=%d", before, tracker.LifecycleGeneration(gid))
	}
	if !tracker.processedComplete[gid] {
		t.Fatal("pause must not clear terminal acceptance")
	}

	tracker.SetStatusFromEvent(gid, "waiting")
	if tracker.LifecycleGeneration(gid) != before {
		t.Fatalf("waiting status must not bump generation")
	}

	tracker.ReopenAfterStoppedToLive(gid, "active")
	after := tracker.LifecycleGeneration(gid)
	if after != before+1 {
		t.Fatalf("stopped→live reopen should bump once: before=%d after=%d", before, after)
	}
}

func TestLifecycle_RemoveTaskCleansLifecycleEntry(t *testing.T) {
	tracker := NewTaskTracker()
	const gid = "sg_life_rm"
	tracker.EnsureTrackedFromEvent(gid, 100, "", 0, "active")
	tracker.ReopenAfterStoppedToLive(gid, "active")
	if !tracker.HasLifecycleEntry(gid) {
		t.Fatal("expected lifecycle entry after reopen")
	}
	tracker.RemoveTask(gid)
	if tracker.HasLifecycleEntry(gid) {
		t.Fatal("RemoveTask must drop lifecycle map entry")
	}
	if tracker.processedComplete[gid] {
		t.Fatal("RemoveTask must clear processedComplete")
	}
}

func TestLifecycle_GroupIPC_PackageRetireSharesGate(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	tracker := NewTaskTracker()
	prevMon := State.GetMonitor()
	State.SetMonitor(&Monitor{tracker: tracker})
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	const gid = "sg_life_grp"
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/g.bin", 0, "error")
	if first := tracker.MarkCompleteFromEvent(gid, "error"); first == nil {
		t.Fatal("expected first terminal")
	}
	before := tracker.LifecycleGeneration(gid)
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/g.bin", Status: "error"})

	cleanupSgCache(t, gid)
	Cache.AddSgTask(rpc.Task{GID: gid, Status: "waiting"}, "waiting")
	RetireHistoryIfResumedFromStopped(gid, "stopped")
	RetireHistoryIfResumedFromStopped(gid, "stopped")
	if task, _ := tracker.GetTrackedTask(gid); task.Status != "waiting" {
		t.Fatal("lost actual target")
	}

	if _, ok := history.Get(gid); ok {
		t.Fatal("expected history removed via package retire")
	}
	after := tracker.LifecycleGeneration(gid)
	if after != before+1 {
		t.Fatalf("package retire must bump generation once: before=%d after=%d", before, after)
	}
	if tracker.processedComplete[gid] {
		t.Fatal("expected terminal marker cleared")
	}

	// Second package call without intervening terminal still reopens once more
	// (idempotent API for from!=stopped only); from==stopped is a real transition.
	RetireHistoryIfResumedFromStopped(gid, "waiting")
	if tracker.LifecycleGeneration(gid) != after {
		t.Fatal("from!=stopped must not bump generation")
	}
}

func TestLifecycle_RetireHistory_MethodNoDoubleReopen(t *testing.T) {
	tracker := NewTaskTracker()
	m := &Monitor{tracker: tracker}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	const gid = "sg_life_e7"
	tracker.EnsureTrackedFromEvent(gid, 100, "", 0, "error")
	_ = tracker.MarkCompleteFromEvent(gid, "error")
	before := tracker.LifecycleGeneration(gid)

	cleanupSgCache(t, gid)
	Cache.AddSgTask(rpc.Task{GID: gid, Status: "paused"}, "waiting")
	m.RetireHistoryIfResumedFromStopped(gid, "stopped")
	after := tracker.LifecycleGeneration(gid)
	if after != before+1 {
		t.Fatalf("method retire must reopen exactly once (E7): before=%d after=%d", before, after)
	}
}

func TestLifecycle_ErrorResumeError_ViaMarkCompleteAndHandle(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:                   hub,
		pusher:                NewPusher(hub),
		tracker:               tracker,
		pauseResumeIntentions: make(map[string]pauseResumeIntention),
	}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	Cache.metadata["sg_life_err"] = &TaskMetadata{
		GID: "sg_life_err", Files: []string{"/tmp/reerr.bin"}, Dir: "/tmp",
	}
	defer delete(Cache.metadata, "sg_life_err")

	tracker.EnsureTrackedFromEvent("sg_life_err", 1000, "https://example.com/reerr.bin", 0, "error")
	m.markCompleteAndHandle("sg_life_err", "error", nil)
	if entries := historyEntriesForGID("sg_life_err"); len(entries) != 1 {
		t.Fatalf("first terminal: want 1 history, got %d", len(entries))
	}

	cleanupSgCache(t, "sg_life_err")
	Cache.AddSgTask(rpc.Task{GID: "sg_life_err", Status: "waiting"}, "waiting")
	m.RetireHistoryIfResumedFromStopped("sg_life_err", "stopped")
	if _, ok := history.Get("sg_life_err"); ok {
		t.Fatal("expected history retired")
	}

	m.markCompleteAndHandle("sg_life_err", "error", nil)
	entries := historyEntriesForGID("sg_life_err")
	if len(entries) != 1 || entries[0].Status != "error" {
		t.Fatalf("second terminal: want 1 error entry, got %#v", entries)
	}
}

func TestLifecycle_TickReacceptUnderGate(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{hub: hub, pusher: NewPusher(hub), tracker: tracker, engine: &mockSafeEngine{}}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	const gid = "ar_life_tick"
	Cache.metadata[gid] = &TaskMetadata{GID: gid, Files: []string{"/tmp/tick.bin"}, Dir: "/tmp"}
	t.Cleanup(func() { delete(Cache.metadata, gid) })

	completed := tracker.Update(nil, nil, []rpc.Task{{
		GID: gid, Status: "complete",
		TotalLength: "100", CompletedLength: "100",
		Files: []rpc.File{{Path: "/tmp/tick.bin"}},
	}})
	if len(completed) != 1 {
		t.Fatalf("Update should detect first complete, got %d", len(completed))
	}

	// No concurrent retire: current-generation acceptance still valid → handle.
	for _, task := range completed {
		tracker.RunUnderLifecycle(task.GID, func() {
			if tracker.TerminalAcceptedInCurrentGeneration(task.GID) {
				m.handleTaskComplete(task)
			}
		})
	}
	if entries := historyEntriesForGID(gid); len(entries) != 1 {
		t.Fatalf("want 1 history from tick handle, got %d", len(entries))
	}

	// Retire between Update and handle: generation advanced, acceptance cleared.
	// Tick must drop the stale snapshot (no ghost history / no new-gen marker).
	history.Clear()
	tracker.ReopenAfterStoppedToLive(gid, "active")
	tracker.tasks[gid].Status = "active"
	completed2 := tracker.Update(nil, nil, []rpc.Task{{
		GID: gid, Status: "complete",
		TotalLength: "100", CompletedLength: "100",
		Files: []rpc.File{{Path: "/tmp/tick.bin"}},
	}})
	if len(completed2) != 1 {
		t.Fatal("expected second Update complete after reopen")
	}
	genAfterUpdate := tracker.LifecycleGeneration(gid)
	tracker.RunUnderLifecycle(gid, func() { retireHistoryAndReopenToStatusLocked(tracker, gid, "active") })
	if tracker.LifecycleGeneration(gid) != genAfterUpdate+1 {
		t.Fatal("expected retire to bump generation after Update accept")
	}
	for _, task := range completed2 {
		tracker.RunUnderLifecycle(task.GID, func() {
			if tracker.TerminalAcceptedInCurrentGeneration(task.GID) {
				m.handleTaskComplete(task)
			}
		})
	}
	if entries := historyEntriesForGID(gid); len(entries) != 0 {
		t.Fatalf("after retire-between-Update-and-handle want 0 history, got %d %#v", len(entries), entries)
	}
	if tracker.TerminalAcceptedInCurrentGeneration(gid) {
		t.Fatal("stale Update must not plant terminal marker on new generation")
	}
	// Real terminal for the new generation must still be accept-able.
	if second := tracker.MarkCompleteFromEvent(gid, "error"); second == nil {
		t.Fatal("expected real terminal after dropped stale tick snapshot")
	}
}

func seedStoppedTerminalHistory(t *testing.T, tracker *TaskTracker, gid, path string) {
	t.Helper()
	Cache.sgMu.Lock()
	Cache.sgStopped = []rpc.Task{{
		GID: gid, Status: "error", ErrorCode: "1", ErrorMessage: "old",
		TotalLength: "1000", CompletedLength: "1000",
		Files: []rpc.File{{Path: path}},
	}}
	Cache.sgActive = nil
	Cache.sgWaiting = nil
	Cache.sgMu.Unlock()
	Cache.metadata[gid] = &TaskMetadata{GID: gid, Files: []string{path}, Dir: "/tmp"}
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/"+gid, 0, "error")
	if first := tracker.MarkCompleteFromEvent(gid, "error"); first == nil {
		t.Fatal("expected seed terminal mark")
	}
	history.Add(history.HistoryEntry{GID: gid, Path: path, Status: "error"})
}

func cleanupSgCache(t *testing.T, gid string) {
	t.Helper()
	t.Cleanup(func() {
		Cache.sgMu.Lock()
		Cache.sgStopped = nil
		Cache.sgActive = nil
		Cache.sgWaiting = nil
		Cache.sgMu.Unlock()
		delete(Cache.metadata, gid)
	})
}

// Documents the pre-fix gap: MoveTaskToActive then retire without one gate lets
// a concurrent terminal reject on the old marker and then lose H0 on retire.
func TestLifecycle_UnprotectedMoveRetireGap_LosesHistory(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{hub: hub, pusher: NewPusher(hub), tracker: tracker, engine: &mockSafeEngine{}}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	const gid = "sg_gap_unprot"
	cleanupSgCache(t, gid)
	seedStoppedTerminalHistory(t, tracker, gid, "/tmp/unprot.bin")

	from := Cache.MoveTaskToActive(gid, "active")
	if from != "stopped" {
		t.Fatalf("from=%q, want stopped", from)
	}
	// Terminal races after cache is live but before retire clears the marker.
	Cache.MoveTaskToStopped(gid, "complete")
	m.markCompleteAndHandle(gid, "complete", nil)
	tracker.RunUnderLifecycle(gid, func() { retireHistoryAndReopenToStatusLocked(tracker, gid, "active") })

	if entries := historyEntriesForGID(gid); len(entries) != 0 {
		t.Fatalf("unprotected gap should leave 0 history, got %#v", entries)
	}
}

func TestLifecycle_CacheMoveRetire_SerializesWithTerminal(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{hub: hub, pusher: NewPusher(hub), tracker: tracker, engine: &mockSafeEngine{}}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	const gid = "sg_gap_fix"
	cleanupSgCache(t, gid)
	seedStoppedTerminalHistory(t, tracker, gid, "/tmp/fix.bin")

	started := make(chan struct{})
	release := make(chan struct{})
	tracker.afterMoveBeforeRetire = func(string) {
		close(started)
		<-release
	}
	t.Cleanup(func() { tracker.afterMoveBeforeRetire = nil })

	var wg sync.WaitGroup
	wg.Go(func() {
		from := m.moveToActiveAndRetireIfStopped(gid, "active")
		if from != "stopped" {
			t.Errorf("from=%q, want stopped", from)
		}
	})
	<-started

	terminalScheduled := make(chan struct{})
	wg.Go(func() {
		close(terminalScheduled)
		m.moveToStoppedAndHandle(gid, "complete", "", "", 1000, nil)
	})
	<-terminalScheduled
	for range 64 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	entries := historyEntriesForGID(gid)
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 H1 after gated move+retire vs terminal, got %d %#v", len(entries), entries)
	}
	if entries[0].Status != "complete" {
		t.Fatalf("want new complete H1, got %#v", entries[0])
	}
	if entries[0].Path == "" {
		t.Fatal("expected durable path on H1")
	}
}

func TestLifecycle_TerminalThenResumeThenTerminal_SingleHistory(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{hub: hub, pusher: NewPusher(hub), tracker: tracker, engine: &mockSafeEngine{}}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	const gid = "sg_gap_opp"
	cleanupSgCache(t, gid)
	seedStoppedTerminalHistory(t, tracker, gid, "/tmp/opp.bin")

	// Opposite order: terminal attempted while still on old generation (no-op),
	// then resume retires H0, then a real new-generation terminal writes H1.
	m.moveToStoppedAndHandle(gid, "complete", "", "", 1000, nil)
	if entries := historyEntriesForGID(gid); len(entries) != 1 || entries[0].Status != "error" {
		t.Fatalf("old-generation terminal must remain H0 error, got %#v", entries)
	}

	from := m.moveToActiveAndRetireIfStopped(gid, "active")
	if from != "stopped" {
		t.Fatalf("from=%q, want stopped", from)
	}
	if _, ok := history.Get(gid); ok {
		t.Fatal("expected H0 retired after resume")
	}

	m.moveToStoppedAndHandle(gid, "complete", "", "", 1000, nil)
	entries := historyEntriesForGID(gid)
	if len(entries) != 1 || entries[0].Status != "complete" {
		t.Fatalf("want single H1 complete after resume+terminal, got %#v", entries)
	}
}

// TestResumeConfirmation_AriaHistoryAtomic covers the D01 fix: an
// Aria2 history-only resume lands in waiting under one arMu critical
// section — the intermediate append-to-stopped state is unobservable —
// while lifecycle from="stopped" semantics and the single retire/reopen
// are preserved.
func TestResumeConfirmation_AriaHistoryAtomic(t *testing.T) {
	t.Run("history-only restore lands waiting and retires exactly once", func(t *testing.T) {
		resetHistoryForTest(t)
		m, _, _, tracker := newReconcileTestMonitor(t)
		const gid = "ar_hist_atomic"
		t.Cleanup(func() { Cache.RemoveTask(gid) })
		group := testDownloadGroup("dg-aria-atomic")
		history.Add(history.HistoryEntry{
			GID: gid, Title: "aria title", Dir: "/downloads",
			Path: "/downloads/a.bin", TotalLength: "1000", CompletedLength: "500",
			Status: "error", DownloadGroup: &group,
		})
		tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/a.bin", 0, "error")
		if first := tracker.MarkCompleteFromEvent(gid, "error"); first == nil {
			t.Fatal("expected terminal mark")
		}

		var change acceptedTaskChange
		ticket := m.BeginPauseResumeRequest(gid, "resume")
		m.RunIfPauseResumeRequestCurrent(ticket, func() {
			change = m.confirmPauseResumeRequestLocked(gid)
		})
		if change.from != "stopped" || change.to != "waiting" || change.kind != "resume" {
			t.Fatalf("change = %#v, want stopped->waiting resume", change)
		}
		got, list, ok := Cache.GetTask(gid)
		if !ok || list != "waiting" || got.Status != "waiting" || got.DownloadSpeed != "0" {
			t.Fatalf("restored: %#v in %q", got, list)
		}
		if got.Title != "aria title" || got.Dir != "/downloads" || got.TotalLength != "1000" ||
			got.CompletedLength != "500" || got.DownloadGroup == nil || got.DownloadGroup.ID != group.ID {
			t.Fatalf("history metadata lost: %#v", got)
		}
		if got.ErrorCode != "" || got.ErrorMessage != "" {
			t.Fatalf("stale terminal error kept: %#v", got)
		}
		if _, exists := history.Get(gid); exists {
			t.Fatal("H0 not retired")
		}
		if gen := tracker.LifecycleGeneration(gid); gen != 1 {
			t.Fatalf("generation = %d, want exactly one reopen", gen)
		}
		if tt, _ := tracker.GetTrackedTask(gid); !tt.ActivatedAt.IsZero() {
			t.Fatal("resume ack fabricated activation")
		}

		// Repeat/expired confirmation must not resurrect or duplicate.
		m.ConfirmPauseResumeRequest(ticket)
		got2, list2, _ := Cache.GetTask(gid)
		if list2 != "waiting" || got2.Status != "waiting" {
			t.Fatalf("repeat confirm disturbed waiting row: %#v", got2)
		}
		if tracker.LifecycleGeneration(gid) != 1 {
			t.Fatal("repeat confirm re-reopened the task")
		}
		if _, exists := history.Get(gid); exists {
			t.Fatal("repeat confirm resurrected history")
		}
	})

	t.Run("post-move pre-retire window only exposes waiting", func(t *testing.T) {
		resetHistoryForTest(t)
		m, _, _, tracker := newReconcileTestMonitor(t)
		const gid = "ar_hist_barrier"
		t.Cleanup(func() { Cache.RemoveTask(gid) })
		history.Add(history.HistoryEntry{
			GID: gid, Path: "/downloads/b.bin", TotalLength: "1000",
			CompletedLength: "1000", Status: "complete",
		})
		tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/b.bin", 0, "complete")
		if first := tracker.MarkCompleteFromEvent(gid, "complete"); first == nil {
			t.Fatal("expected terminal mark")
		}

		started := make(chan struct{})
		release := make(chan struct{})
		tracker.afterMoveBeforeRetire = func(string) {
			close(started)
			<-release
		}
		t.Cleanup(func() { tracker.afterMoveBeforeRetire = nil })

		observed := make(chan string, 1)
		go func() {
			<-started
			active, waiting, stopped := Cache.GetTaskLists()
			switch {
			case containsTaskGID(stopped, gid):
				observed <- "stopped"
			case containsTaskGID(active, gid):
				observed <- "active"
			case !containsTaskGID(waiting, gid):
				observed <- "missing"
			default:
				observed <- "waiting"
			}
			close(release)
		}()

		ticket := m.BeginPauseResumeRequest(gid, "resume")
		m.ConfirmPauseResumeRequest(ticket)
		if state := <-observed; state != "waiting" {
			t.Fatalf("mid-transition snapshot saw %q, want waiting", state)
		}
		if _, exists := history.Get(gid); exists {
			t.Fatal("H0 not retired")
		}
		if gen := tracker.LifecycleGeneration(gid); gen != 1 {
			t.Fatalf("generation = %d, want exactly one reopen", gen)
		}
	})
}
