package monitor

import (
	"runtime"
	"sync"
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
)

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
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.RetireHistoryIfResumedFromStopped(gid, "stopped")
	}()

	<-started
	terminalScheduled := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(terminalScheduled)
		m.markCompleteAndHandle(gid, "complete", nil)
	}()
	<-terminalScheduled
	for i := 0; i < 64; i++ {
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.RetireHistoryIfResumedFromStopped(gid, "stopped")
	}()
	<-started

	terminalScheduled := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(terminalScheduled)
		m.markCompleteAndHandle(gid, "error", nil)
	}()
	<-terminalScheduled
	for i := 0; i < 64; i++ {
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

	// Group IPC path uses the package helper (same gate as Surge event retire).
	RetireHistoryIfResumedFromStopped(gid, "stopped")

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
		pauseResumeIntentions: make(map[string]string),
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
	m.RetireHistoryIfResumedFromStopped(gid, "stopped")
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
	m.RetireHistoryIfResumedFromStopped(gid, "stopped")

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
	wg.Add(1)
	go func() {
		defer wg.Done()
		from := m.moveToActiveAndRetireIfStopped(gid, "active")
		if from != "stopped" {
			t.Errorf("from=%q, want stopped", from)
		}
	}()
	<-started

	terminalScheduled := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(terminalScheduled)
		m.moveToStoppedAndHandle(gid, "complete", "", "", 1000, nil)
	}()
	<-terminalScheduled
	for i := 0; i < 64; i++ {
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
