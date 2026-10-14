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

func TestApplyResume_Sources(t *testing.T) {
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
			before := tracker.LifecycleToken(full.GID)
			m.ApplyResume(full.GID)
			m.ApplyResume(full.GID) // duplicate ack is a no-op
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
				if tracker.LifecycleToken(full.GID) != before {
					t.Fatal("no-op resume moved the token")
				}
				return
			}
			if list != "waiting" || got.Status != "waiting" || got.CompletedLength != full.CompletedLength {
				t.Fatalf("apply resume: %#v %s", got, list)
			}
			if source != "history" {
				assertFullRowMetadata(t, got, full)
			}
			if _, exists := history.Get(full.GID); exists {
				t.Fatal("H0 not retired")
			}
			after := tracker.LifecycleToken(full.GID)
			if source == "paused" {
				if after != before {
					t.Fatalf("paused resume must not mint a token: before=%d after=%d", before, after)
				}
			} else if after <= before {
				t.Fatalf("stopped→live resume must mint a larger token exactly once: before=%d after=%d", before, after)
			}
			if tt, _ := tracker.GetTrackedTask(full.GID); !tt.ActivatedAt.IsZero() {
				t.Fatal("ack activated")
			}
		})
	}
}

// ApplyPause/ApplyResume are inert on nil or tracker-less monitors, blank and
// unknown GIDs: no cache row, no history, no tracker entry is manufactured.
func TestApplyPauseResume_InertInputs(t *testing.T) {
	resetHistoryForTest(t)
	m := &Monitor{tracker: NewTaskTracker()}
	for _, mon := range []*Monitor{nil, {}, m} {
		for _, gid := range []string{"", " ", "sg_missing", "ar_missing"} {
			mon.ApplyPause(gid)
			mon.ApplyResume(gid)
		}
	}
	if _, _, ok := Cache.GetTask("sg_missing"); ok {
		t.Fatal("apply manufactured a cache row")
	}
	if _, _, ok := Cache.GetTask("ar_missing"); ok {
		t.Fatal("apply manufactured an aria cache row")
	}
	if _, ok := history.Get("sg_missing"); ok {
		t.Fatal("apply manufactured history")
	}
	if tracked, ok := m.tracker.GetTrackedTask("sg_missing"); ok && tracked.Status != "" {
		t.Fatalf("apply wrote tracker status: %#v", tracked)
	}
}

func TestResumeConfirmation_DetachedGateWaiter(t *testing.T) {
	tracker := NewTaskTracker()
	const gid = "sg_detached"
	old := tracker.getLifecycle(gid)
	oldToken := tracker.LifecycleToken(gid)
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
	if tok := tracker.LifecycleToken(gid); tok <= oldToken {
		t.Fatalf("rebuilt entry must mint a larger token: old=%d new=%d", oldToken, tok)
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
	seeded := tracker.LifecycleToken(gid)
	for _, invalid := range []string{"", "unknown", "complete"} {
		tracker.RunUnderLifecycle(gid, func() { retireHistoryAndReopenToStatusLocked(tracker, gid, invalid) })
	}
	if _, ok := history.Get(gid); !ok || tracker.LifecycleToken(gid) != seeded {
		t.Fatal("invalid target retired history or minted a token")
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
	reopened := tracker.LifecycleToken(gid)
	if list != "waiting" || tracked.Status != "waiting" || got.ErrorCode != "" || got.ErrorMessage != "" ||
		!tracked.ActivatedAt.IsZero() || !tracked.FirstProgressAt.IsZero() || reopened <= seeded {
		t.Fatal("waiting reopen facts wrong")
	}
	if _, ok := history.Get(gid); ok {
		t.Fatal("H0 not retired")
	}
	if group := GetStoredTaskGroup(gid); group == nil || group.ID != full.DownloadGroup.ID {
		t.Fatal("group not re-homed")
	}
	Cache.AddSgTask(rpc.Task{GID: gid, Status: "waiting"}, "waiting")
	tracker.SetStatusFromEvent(gid, "waiting")
	if tracker.LifecycleToken(gid) != reopened {
		t.Fatal("same-list patch reopened")
	}

	m := &Monitor{tracker: tracker}
	m.moveToStoppedAndHandleGate(gid, "error", "1", "new error", 1000, nil, nil)
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
	Cache.SetMetadataForTest(gid, &TaskMetadata{GID: gid, Files: []string{"/tmp/a.bin"}, Dir: "/tmp"})
	t.Cleanup(func() { Cache.DeleteMetadataForTest(gid) })
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
	Cache.SetMetadataForTest(gid, &TaskMetadata{GID: gid, Files: []string{"/tmp/b.bin"}, Dir: "/tmp"})
	t.Cleanup(func() { Cache.DeleteMetadataForTest(gid) })
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

func TestLifecycle_NoTokenBumpOnPauseOrWaiting(t *testing.T) {
	tracker := NewTaskTracker()
	const gid = "sg_life_pause"
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/p.bin", 0, "active")
	if first := tracker.MarkCompleteFromEvent(gid, "error"); first == nil {
		t.Fatal("expected terminal seed")
	}
	before := tracker.LifecycleToken(gid)

	tracker.SetStatusFromEvent(gid, "paused")
	if tracker.LifecycleToken(gid) != before {
		t.Fatalf("pause must not mint a token: before=%d after=%d", before, tracker.LifecycleToken(gid))
	}
	if !tracker.processedComplete[gid] {
		t.Fatal("pause must not clear terminal acceptance")
	}

	tracker.SetStatusFromEvent(gid, "waiting")
	if tracker.LifecycleToken(gid) != before {
		t.Fatalf("waiting status must not mint a token")
	}

	tracker.ReopenAfterStoppedToLive(gid, "active")
	after := tracker.LifecycleToken(gid)
	if after <= before {
		t.Fatalf("stopped→live reopen must mint a larger token: before=%d after=%d", before, after)
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
	oldToken := tracker.LifecycleToken(gid)
	tracker.RemoveTask(gid)
	if tracker.HasLifecycleEntry(gid) {
		t.Fatal("RemoveTask must drop lifecycle map entry")
	}
	if tracker.processedComplete[gid] {
		t.Fatal("RemoveTask must clear processedComplete")
	}
	// Same-GID rebuild never reuses the retired token; a different GID draws
	// its own from the same global counter.
	tracker.EnsureTrackedFromEvent(gid, 100, "", 0, "active")
	newToken := tracker.LifecycleToken(gid)
	if newToken <= oldToken {
		t.Fatalf("rebuilt entry reused token: old=%d new=%d", oldToken, newToken)
	}
	if other := tracker.LifecycleToken("sg_life_other"); other == newToken {
		t.Fatal("cross-GID token collision")
	}
}

func TestLifecycle_ErrorResumeError_ViaMarkCompleteAndHandle(t *testing.T) {
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	tracker := NewTaskTracker()
	m := &Monitor{
		hub:     hub,
		pusher:  NewPusher(hub),
		tracker: tracker,
	}
	prevMon := State.GetMonitor()
	State.SetMonitor(m)
	t.Cleanup(func() { State.SetMonitor(prevMon) })

	Cache.SetMetadataForTest("sg_life_err", &TaskMetadata{
		GID: "sg_life_err", Files: []string{"/tmp/reerr.bin"}, Dir: "/tmp",
	})
	defer Cache.DeleteMetadataForTest("sg_life_err")

	tracker.EnsureTrackedFromEvent("sg_life_err", 1000, "https://example.com/reerr.bin", 0, "error")
	m.markCompleteAndHandle("sg_life_err", "error", nil)
	if entries := historyEntriesForGID("sg_life_err"); len(entries) != 1 {
		t.Fatalf("first terminal: want 1 history, got %d", len(entries))
	}

	cleanupSgCache(t, "sg_life_err")
	Cache.AddSgTask(rpc.Task{GID: "sg_life_err", Status: "error", ErrorCode: "1", ErrorMessage: "old"}, "stopped")
	m.ApplyResume("sg_life_err")
	if _, ok := history.Get("sg_life_err"); ok {
		t.Fatal("expected history retired")
	}
	if _, list, ok := Cache.GetTask("sg_life_err"); !ok || list != "waiting" {
		t.Fatalf("expected confirmed resume to land in waiting, got list=%q ok=%v", list, ok)
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
	Cache.SetMetadataForTest(gid, &TaskMetadata{GID: gid, Files: []string{"/tmp/tick.bin"}, Dir: "/tmp"})
	t.Cleanup(func() { Cache.DeleteMetadataForTest(gid) })

	completed := tracker.Update(nil, nil, []rpc.Task{{
		GID: gid, Status: "complete",
		TotalLength: "100", CompletedLength: "100",
		Files: []rpc.File{{Path: "/tmp/tick.bin"}},
	}})
	if len(completed) != 1 {
		t.Fatalf("Update should detect first complete, got %d", len(completed))
	}

	// No concurrent retire: the stamped copy is still current → finish writes
	// history. finishTerminalCopy validates the copy under its own gate.
	for _, task := range completed {
		m.finishTerminalCopy(task)
	}
	if entries := historyEntriesForGID(gid); len(entries) != 1 {
		t.Fatalf("want 1 history from tick finish, got %d", len(entries))
	}

	// Retire between Update and finish: generation advanced, acceptance cleared.
	// The stale copy fails TerminalCopyCurrent inside finishTerminalCopy (no
	// ghost history / no new-gen marker).
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
	tokAfterUpdate := tracker.LifecycleToken(gid)
	tracker.RunUnderLifecycle(gid, func() { retireHistoryAndReopenToStatusLocked(tracker, gid, "active") })
	if tracker.LifecycleToken(gid) <= tokAfterUpdate {
		t.Fatal("expected retire to mint a larger token after Update accept")
	}
	for _, task := range completed2 {
		m.finishTerminalCopy(task)
	}
	if entries := historyEntriesForGID(gid); len(entries) != 0 {
		t.Fatalf("after retire-between-Update-and-handle want 0 history, got %d %#v", len(entries), entries)
	}
	if tracker.TerminalAcceptedInCurrentGeneration(gid) {
		t.Fatal("stale Update must not plant terminal marker on new token")
	}
	// Real terminal for the new token must still be accept-able.
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
	Cache.SetMetadataForTest(gid, &TaskMetadata{GID: gid, Files: []string{path}, Dir: "/tmp"})
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
		Cache.DeleteMetadataForTest(gid)
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
		m.ApplyResume(gid)
	})
	<-started

	terminalScheduled := make(chan struct{})
	wg.Go(func() {
		close(terminalScheduled)
		m.moveToStoppedAndHandleGate(gid, "complete", "", "", 1000, nil, nil)
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
	m.moveToStoppedAndHandleGate(gid, "complete", "", "", 1000, nil, nil)
	if entries := historyEntriesForGID(gid); len(entries) != 1 || entries[0].Status != "error" {
		t.Fatalf("old-generation terminal must remain H0 error, got %#v", entries)
	}

	m.ApplyResume(gid)
	if _, ok := history.Get(gid); ok {
		t.Fatal("expected H0 retired after resume")
	}

	m.moveToStoppedAndHandleGate(gid, "complete", "", "", 1000, nil, nil)
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

		var moves []events.TaskMove
		m.hub.SubscribeTaskMove(func(mv events.TaskMove) {
			if mv.GID == gid {
				moves = append(moves, mv)
			}
		})
		before := tracker.LifecycleToken(gid)
		m.ApplyResume(gid)
		if len(moves) != 1 || moves[0].From != "stopped" || moves[0].To != "waiting" {
			t.Fatalf("moves = %#v, want one stopped->waiting", moves)
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
		reopened := tracker.LifecycleToken(gid)
		if reopened <= before {
			t.Fatalf("reopen must mint a larger token: before=%d after=%d", before, reopened)
		}
		if tt, _ := tracker.GetTrackedTask(gid); !tt.ActivatedAt.IsZero() {
			t.Fatal("resume ack fabricated activation")
		}

		// A duplicate ack must not resurrect or re-reopen.
		m.ApplyResume(gid)
		got2, list2, _ := Cache.GetTask(gid)
		if list2 != "waiting" || got2.Status != "waiting" {
			t.Fatalf("repeat apply disturbed waiting row: %#v", got2)
		}
		if tracker.LifecycleToken(gid) != reopened {
			t.Fatal("repeat apply re-reopened the task")
		}
		if _, exists := history.Get(gid); exists {
			t.Fatal("repeat apply resurrected history")
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

		before := tracker.LifecycleToken(gid)
		m.ApplyResume(gid)
		if state := <-observed; state != "waiting" {
			t.Fatalf("mid-transition snapshot saw %q, want waiting", state)
		}
		if _, exists := history.Get(gid); exists {
			t.Fatal("H0 not retired")
		}
		if tok := tracker.LifecycleToken(gid); tok <= before {
			t.Fatalf("reopen must mint a larger token: before=%d after=%d", before, tok)
		}
	})
}

// TestApplyPauseResume_AriaNoLiveSeam covers the aria2 path: no live seam
// exists, so an ack folds unconditionally — active→paused→waiting keeps the
// token and full metadata, a duplicate ack is a no-op, and a resume ack on an
// already active row never demotes it.
func TestApplyPauseResume_AriaNoLiveSeam(t *testing.T) {
	resetHistoryForTest(t)
	m, _, _, tracker := newReconcileTestMonitor(t)
	const gid = "ar_no_seam"
	t.Cleanup(func() { Cache.RemoveTask(gid) })
	full := fullRowForTest(gid)
	full.Status = "active"
	Cache.UpdateFromAria2([]rpc.Task{full}, nil, nil)
	tracker.EnsureTrackedFromEvent(gid, 1000, "https://example.com/f.bin", 0, "active")
	before := tracker.LifecycleToken(gid)

	m.ApplyPause(gid)
	got, list, ok := Cache.GetTask(gid)
	if !ok || list != "waiting" || got.Status != "paused" {
		t.Fatalf("pause ack: %#v %s", got, list)
	}
	assertFullRowMetadata(t, got, full)
	m.ApplyPause(gid) // duplicate ack is a no-op
	m.ApplyResume(gid)
	m.ApplyResume(gid)
	got, list, ok = Cache.GetTask(gid)
	if !ok || list != "waiting" || got.Status != "waiting" {
		t.Fatalf("resume ack: %#v %s", got, list)
	}
	assertFullRowMetadata(t, got, full)
	if tok := tracker.LifecycleToken(gid); tok != before {
		t.Fatalf("pause/resume must not mint a token: before=%d after=%d", before, tok)
	}
	if _, exists := history.Get(gid); exists {
		t.Fatal("pause/resume manufactured history")
	}

	// An ack on a live active row never demotes it.
	full.Status = "active"
	Cache.UpdateFromAria2([]rpc.Task{full}, nil, nil)
	m.ApplyResume(gid)
	if got, list, _ := Cache.GetTask(gid); list != "active" || got.Status != "active" {
		t.Fatalf("ack demoted live active row: %#v %s", got, list)
	}
}
