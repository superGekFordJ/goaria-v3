package monitor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/testutil"
	surgeEvents "goaria-v3/internal/surge/types"
)

// mockSurgeListReader is a test-only surgeListReader that returns
// configurable active/waiting/stopped task lists.
type mockSurgeListReader struct {
	mu       sync.Mutex
	active   []rpc.Task
	waiting  []rpc.Task
	stopped  []rpc.Task
	activeFn func() ([]rpc.Task, error)
	err      error
	live     map[string]rpc.SurgeLiveStatus
	liveFn   func(string) rpc.SurgeLiveStatus
}

func (m *mockSurgeListReader) ReadLiveStatus(id string) rpc.SurgeLiveStatus {
	m.mu.Lock()
	live, fn := m.live[id], m.liveFn
	m.mu.Unlock()
	if fn != nil {
		return fn(id)
	}
	return live
}

func (m *mockSurgeListReader) setLiveStatus(id string, live rpc.SurgeLiveStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.live == nil {
		m.live = make(map[string]rpc.SurgeLiveStatus)
	}
	m.live[id] = live
}

func (m *mockSurgeListReader) TellActive() ([]rpc.Task, error) {
	m.mu.Lock()
	fn, err, active := m.activeFn, m.err, copyTaskSlice(m.active)
	m.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return active, err
}

func (m *mockSurgeListReader) TellWaiting(offset, num int) ([]rpc.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return copyTaskSlice(m.waiting), nil
}

func (m *mockSurgeListReader) TellStopped(offset, num int) ([]rpc.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return copyTaskSlice(m.stopped), nil
}

func (m *mockSurgeListReader) setLists(active, waiting, stopped []rpc.Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = active
	m.waiting = waiting
	m.stopped = stopped
}

func TestResumePollSafety_RealPoolConfirmedResume(t *testing.T) {
	testutil.SetupStateDB(t)
	m, _, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	setupTaskGroupStoreTest(t)
	full := fullRowForTest("sg_pool_resume")
	full.Files[0].Uris[0].Status = "used"
	Cache.AddSgTask(full, "stopped")
	tracker.Update(nil, nil, []rpc.Task{full})
	history.Add(history.HistoryEntry{GID: full.GID, Path: full.Files[0].Path, Status: "error", DownloadGroup: full.DownloadGroup})
	testutil.SeedMasterList(t, surgeEvents.DownloadRecord{ID: "pool_resume", URL: full.Files[0].Uris[0].Uri,
		DestPath: full.Files[0].Path, Filename: "a", Status: "error", TotalSize: 1000, Downloaded: 500})
	testutil.SeedMasterList(t, surgeEvents.DownloadRecord{ID: "master_only", URL: "https://example.com/history", Status: "queued"})
	history.Add(history.HistoryEntry{GID: "sg_master_only", Path: "/downloads/history", Status: "error"})
	pool := scheduler.NewSchedulerForTesting(nil)
	se := rpc.NewSurgeEngineForTesting(pool)
	t.Cleanup(se.Close)
	m.surgeEng, m.surgePollReader = se, se
	m.engine = rpc.NewHybridEngine(nil, se)
	ticket := m.BeginPauseResumeRequest(full.GID, "resume")
	if err := se.Resume("pool_resume"); err != nil {
		t.Fatal(err)
	}
	m.ConfirmPauseResumeRequest(ticket)
	if live := se.ReadLiveStatus("pool_resume"); !live.Known || live.Status != "queued" {
		t.Fatalf("not actually queued: %#v", live)
	}
	for range 2 {
		m.reconcileSurgeCache()
		got, list, _ := Cache.GetTask(full.GID)
		assertFullRowMetadata(t, got, full)
		tracked, _ := tracker.GetTrackedTask(full.GID)
		if list != "waiting" || got.Status != "waiting" || got.TotalLength != "1000" || got.CompletedLength != "500" ||
			got.ErrorCode != "" || tracked.Status != "waiting" || !tracked.ActivatedAt.IsZero() || tracker.LifecycleGeneration(full.GID) != 1 {
			t.Fatalf("confirmed queue regressed: %#v / %#v", got, tracked)
		}
		if _, ok := history.Get(full.GID); ok {
			t.Fatal("H0 restored")
		}
		if group := GetStoredTaskGroup(full.GID); group == nil || group.ID != full.DownloadGroup.ID {
			t.Fatal("group not re-homed")
		}
		if se.ReadLiveStatus("master_only").Known || !Cache.IsInStopped("sg_master_only") {
			t.Fatal("master-only row admitted")
		}
		if _, ok := history.Get("sg_master_only"); !ok {
			t.Fatal("master-only history retired")
		}
		master, _ := se.GetMasterCacheEntry("pool_resume")
		if master.Status != "error" {
			t.Fatal("old disk mirror was not exercised")
		}
	}
	if disk, err := store.GetDownload("pool_resume"); err != nil || disk == nil || disk.Status != "error" {
		t.Fatalf("disk status changed: %#v %v", disk, err)
	}
	retry := fullRowForTest("sg_pool_retry")
	Cache.AddSgTask(retry, "stopped")
	Cache.mu.Lock()
	Cache.ensureMetadata(retry)
	Cache.mu.Unlock()
	tracker.Update(nil, nil, []rpc.Task{retry})
	history.Add(history.HistoryEntry{GID: retry.GID, Path: retry.Files[0].Path, Status: "error", DownloadGroup: retry.DownloadGroup})
	if !pool.Add(surgeEvents.DownloadRecord{ID: "pool_retry"}) {
		t.Fatal("real Add refused")
	}
	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventQueued, DownloadID: "pool_retry"})
	got, list, _ := Cache.GetTask(retry.GID)
	assertFullRowMetadata(t, got, retry)
	if list != "waiting" || got.Status != "waiting" || tracker.LifecycleGeneration(retry.GID) != 1 {
		t.Fatal("real queued event did not reopen full stopped row")
	}
	if _, ok := history.Get(retry.GID); ok {
		t.Fatal("queued event retained H0")
	}
}

func TestResumePollSafety_StaleReadBarriers(t *testing.T) {
	for _, before := range []string{"paused", "error", "waiting", "empty"} {
		t.Run(before, func(t *testing.T) {
			m, reader, pusher, tracker := newReconcileTestMonitor(t)
			resetCacheSg()
			resetHistoryForTest(t)
			full := fullRowForTest("sg_poll_barrier")
			full.Status = before
			m.chunkSnapshots = NewChunkSnapshotCache()
			m.chunkSnapshots.Set(full.GID, []byte{1}, 4, 100, nil)
			if before != "empty" {
				Cache.AddSgTask(full, engineListForStatus(before))
				tracker.EnsureTrackedFromEvent(full.GID, 1000, "", 1, before)
				if before == "error" {
					tracker.MarkCompleteFromEvent(full.GID, "error")
					history.Add(history.HistoryEntry{GID: full.GID, Path: full.Files[0].Path, Status: "error", DownloadGroup: full.DownloadGroup})
					reader.setLists(nil, nil, []rpc.Task{{GID: "poll_barrier", Status: "error"}})
				} else {
					reader.setLists(nil, []rpc.Task{{GID: "poll_barrier", Status: before}}, nil)
				}
			}
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releasePoll := sync.OnceFunc(func() { close(release) })
			reader.activeFn = func() ([]rpc.Task, error) {
				close(entered)
				select {
				case <-release:
					return nil, nil
				case <-time.After(2 * time.Second):
					return nil, context.DeadlineExceeded
				}
			}
			t.Cleanup(func() {
				releasePoll()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("poll did not exit")
				}
			})
			go func() { m.reconcileSurgeCache(); close(done) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("poll did not reach bulk read")
			}
			m.surgeEng = &rpc.SurgeEngine{}
			m.surgeEng.UpsertMasterCacheEntry(surgeEvents.DownloadRecord{ID: "poll_barrier", Status: "old"})
			want, masterStatus := "waiting", "old"
			if before == "waiting" {
				m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventStarted, DownloadID: "poll_barrier"})
				want, masterStatus = "active", "downloading"
			} else {
				if before == "empty" {
					full.Status = "paused"
					Cache.AddSgTask(full, "waiting")
				}
				m.ConfirmPauseResumeRequest(m.BeginPauseResumeRequest(full.GID, "resume"))
			}
			m.chunkSnapshots.Set(full.GID, []byte{2}, 4, 100, nil)
			m.chunkSnapshots.Set("sg_new_chunk", []byte{2}, 4, 100, nil)
			m.pauseResumeVersionMu.RLock()
			intention := m.pauseResumeIntentions[full.GID]
			m.pauseResumeVersionMu.RUnlock()
			releasePoll()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("poll stuck")
			}
			got, list, _ := Cache.GetTask(full.GID)
			assertFullRowMetadata(t, got, full)
			tracked, _ := tracker.GetTrackedTask(full.GID)
			master, _ := m.surgeEng.GetMasterCacheEntry("poll_barrier")
			m.pauseResumeVersionMu.RLock()
			intentionStable := m.pauseResumeIntentions[full.GID] == intention
			m.pauseResumeVersionMu.RUnlock()
			if got.Status != want || list != want || tracked.Status != want || master.Status != masterStatus ||
				!intentionStable {
				t.Fatal("old poll overwrote accepted facts")
			}
			if _, ok := history.Get(full.GID); ok {
				t.Fatal("old poll restored H0")
			}
			for _, gid := range []string{full.GID, "sg_new_chunk"} {
				if snap, ok := m.chunkSnapshots.Get(gid); !ok || snap.Bitmap[0] != 2 {
					t.Fatal("old poll removed new chunk")
				}
			}
			for _, delta := range pusher.pendingSnapshot() {
				if delta.GID == full.GID {
					if task, ok := delta.Payload.(rpc.Task); !ok || task.Status != want {
						t.Fatalf("stale payload: %#v", delta)
					}
				}
			}
		})
	}
}

func TestResumePollSafety_MasterPausedIsNotLive(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	full := fullRowForTest("sg_retained_waiting")
	full.Status = "waiting"
	Cache.AddSgTask(full, "waiting")
	reader.setLists(nil, []rpc.Task{{GID: "retained_waiting", Status: "paused"}}, nil)
	m.reconcileSurgeCache()
	got, list, _ := Cache.GetTask(full.GID)
	assertFullRowMetadata(t, got, full)
	if got.Status != "waiting" || list != "waiting" || len(pusher.pendingSnapshot()) != 0 {
		t.Fatal("master-only paused downgraded waiting")
	}
}

func TestResumePollSafety_CurrentLiveFacts(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	full := fullRowForTest("sg_poll_live")
	full.Status = "paused"
	Cache.AddSgTask(full, "waiting")
	m.ConfirmPauseResumeRequest(m.BeginPauseResumeRequest(full.GID, "resume"))
	reader.setLists(nil, nil, []rpc.Task{{GID: "poll_live", Status: "error", CompletedLength: "0"}})
	var activated time.Time
	for _, status := range []string{"queued", "downloading", "downloading", "paused"} {
		if status == "paused" {
			m.BeginPauseResumeRequest(full.GID, "pause")
		}
		reader.setLiveStatus("poll_live", rpc.SurgeLiveStatus{Known: true, Status: status})
		m.reconcileSurgeCache()
		got, _, _ := Cache.GetTask(full.GID)
		assertFullRowMetadata(t, got, full)
		tracked, _ := tracker.GetTrackedTask(full.GID)
		if got.Status != m.readSurgeLiveStatus(full.GID).Status || tracked.Status != got.Status || got.CompletedLength != full.CompletedLength {
			t.Fatal("list projection overrode live status/checkpoint")
		}
		if status == "downloading" {
			if tracked.ActivatedAt.IsZero() {
				t.Fatal("running did not activate")
			}
			if !activated.IsZero() && !activated.Equal(tracked.ActivatedAt) {
				t.Fatal("duplicate poll renewed activation")
			}
			activated = tracked.ActivatedAt
		} else if !tracked.ActivatedAt.IsZero() {
			t.Fatal("queue/pause retained activation")
		}
		if _, ok := history.Get(full.GID); ok {
			t.Fatal("old terminal list wrote history")
		}
	}
	reader.setLiveStatus("poll_live", rpc.SurgeLiveStatus{Known: true, Status: "queued"})
	m.reconcileSurgeCache()
	if got, _, _ := Cache.GetTask(full.GID); got.Status != "paused" {
		t.Fatal("queued snapshot undid current Pause")
	}
	pending := pusher.pendingSnapshot()
	if len(pending) == 0 {
		t.Fatal("missing actual-status delta")
	}
	if task, ok := pending[len(pending)-1].Payload.(rpc.Task); !ok || task.Status != "paused" {
		t.Fatal("missing actual-status delta")
	}
}

func TestSurgeLiveStatus_IndependentStrictFacts(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetHistoryForTest(t)
	full := fullRowForTest("sg_live_facts")
	Cache.AddSgTask(full, "waiting")
	reader.setLists([]rpc.Task{{GID: "live_facts", Status: "active"}}, nil, nil)
	if m.readSurgeLiveStatus(full.GID).Known {
		t.Fatal("list active became live evidence")
	}
	for raw, want := range map[string]string{"queued": "waiting", "downloading": "active", "pausing": "paused", "paused": "paused", "completed": "complete", "error": "error", "unknown": "", "waiting": ""} {
		reader.setLiveStatus("live_facts", rpc.SurgeLiveStatus{Status: raw, Known: true, TotalLength: 1000, TotalKnown: true, CompletedKnown: true})
		live := m.readSurgeLiveStatus(full.GID)
		if live.Known != (want != "") || live.Status != want || (live.Known && (!live.TotalKnown || !live.CompletedKnown || live.CompletedLength != 0)) {
			t.Fatalf("strict %s: %#v", raw, live)
		}
	}
	reader.setLists(nil, []rpc.Task{{GID: "live_facts", Status: "waiting"}}, nil)
	reader.setLiveStatus("live_facts", rpc.SurgeLiveStatus{Status: "downloading", Known: true})
	if live := m.readSurgeLiveStatus(full.GID); !live.Known || live.Status != "active" {
		t.Fatal("waiting list overrode running live")
	}
	reader.setLists([]rpc.Task{{GID: "live_facts", Status: "active"}}, nil, nil)
	reader.setLiveStatus("live_facts", rpc.SurgeLiveStatus{Status: "queued", Known: true})
	if live := m.readSurgeLiveStatus(full.GID); live.Status != "waiting" || live.TotalKnown || live.CompletedKnown {
		t.Fatal("active list overrode queued live")
	}
	for _, gid := range []string{"", "sg_", "ar_live_facts", "live_facts"} {
		if m.readSurgeLiveStatus(gid).Known {
			t.Fatalf("non-Surge prefix accepted: %s", gid)
		}
	}
	reader.setLiveStatus("live_facts", rpc.SurgeLiveStatus{})
	if m.readSurgeLiveStatus(full.GID).Known {
		t.Fatal("unknown live fell back to list")
	}
	got, list, _ := Cache.GetTask(full.GID)
	assertFullRowMetadata(t, got, full)
	if list != "waiting" || got.Status != full.Status || tracker.HasLifecycleEntry(full.GID) || historyCount() != 0 {
		t.Fatal("read had lifecycle/cache/history side effects")
	}
	pool := scheduler.NewSchedulerForTesting(nil)
	pool.Add(surgeEvents.DownloadRecord{ID: "live_facts"})
	se := rpc.NewSurgeEngineForTesting(pool)
	t.Cleanup(se.Close)
	if (&Monitor{surgeEng: se}).readSurgeLiveStatus(full.GID).Status != "waiting" {
		t.Fatal("engine fallback did not read raw ID")
	}
	if (&Monitor{surgeEng: se, surgePollReader: &mockSafeEngine{}}).readSurgeLiveStatus(full.GID).Known || (*Monitor)(nil).readSurgeLiveStatus(full.GID).Known {
		t.Fatal("missing seam/nil monitor not unknown")
	}
	pool.Cancel("live_facts")
}

func TestSurgeLiveStatus_FixtureCallbackOutsideMutex(t *testing.T) {
	reader := &mockSurgeListReader{}
	reader.liveFn = func(id string) rpc.SurgeLiveStatus {
		reader.setLiveStatus(id, rpc.SurgeLiveStatus{Status: "queued", Known: true})
		return rpc.SurgeLiveStatus{Status: "downloading", Known: true}
	}
	if live := reader.ReadLiveStatus("raw"); live.Status != "downloading" {
		t.Fatal("callback ignored")
	}
	reader.mu.Lock()
	reader.liveFn = nil
	reader.mu.Unlock()
	if live := reader.ReadLiveStatus("raw"); live.Status != "queued" {
		t.Fatal("independent live map lost")
	}
}

// newReconcileTestMonitor builds a Monitor wired with a mockSurgeListReader,
// pusher, tracker, and hub for reconciliation tests. The caller must defer
// cache cleanup.
func newReconcileTestMonitor(t *testing.T) (*Monitor, *mockSurgeListReader, *Pusher, *TaskTracker) {
	t.Helper()
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	reader := &mockSurgeListReader{}
	se := &mockSafeEngine{}
	hybrid := rpc.NewHybridEngine(nil, se)
	m := &Monitor{
		hub:               hub,
		pusher:            pusher,
		tracker:           tracker,
		engine:            hybrid,
		telemetry:         NewTelemetryCache(),
		surgePollReader:   reader,
		surgePollInterval: 10 * time.Second,
	}
	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	t.Cleanup(func() {
		State.SetWindowExists(prevWindow)
		// Drain armed namer timers so a debounced snapshot cannot leak into
		// the next test's cache writes.
		ResetDownloadGroupNamerForTest()
		resetCacheSg()
	})
	return m, reader, pusher, tracker
}

func resetCacheSg() {
	Cache.sgMu.Lock()
	Cache.sgActive = nil
	Cache.sgWaiting = nil
	Cache.sgStopped = nil
	Cache.sgMu.Unlock()
}

// pendingSnapshot drains the queued deltas and disarms the debounce timer so
// a scheduled flush cannot mutate the queue while a test asserts on it.
func (p *Pusher) pendingSnapshot() []events.TaskDelta {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	pending := append([]events.TaskDelta(nil), p.pending...)
	p.pending = nil
	p.dedupMap = make(map[string]int)
	return pending
}

// historyCount returns the current number of history entries.
func historyCount() int {
	entries := history.GetAll()
	return len(entries)
}

// resetHistoryForTest clears global history and disables file saves for the
// duration of a test so history-count assertions are independent of test order.
func resetHistoryForTest(t *testing.T) {
	t.Helper()
	prevSave := history.SaveEnabled
	history.DisableSaveForTest()
	history.Clear()
	t.Cleanup(func() {
		history.Clear()
		history.SetSaveEnabled(prevSave)
	})
}

// TestReconcileSurgeCache_MissedComplete verifies that a task the engine
// reports as complete but Cache still has active is moved to stopped,
// handleTaskComplete runs (writing history), and a complete delta is pushed
// to the frontend (matching the handleSurgeEvent event path).
func TestReconcileSurgeCache_MissedComplete(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	// Provide file metadata so handleTaskComplete can write history.
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()
	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "complete" {
				t.Errorf("Status = %s, want complete", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in stopped after reconcile")
	}

	if historyCount() != before+1 {
		t.Errorf("history count = %d, want %d (handleTaskComplete should write history)", historyCount(), before+1)
	}

	if !tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}

	foundComplete := false
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "complete" && d.GID == "sg_task1" {
			foundComplete = true
		}
	}
	if !foundComplete {
		t.Error("expected complete delta from poll (reconcile now emits deltas matching event path)")
	}
}

// TestReconcileSurgeCache_MissedComplete_AlreadyProcessed verifies that when
// the event path already processed a complete, the poll does not call
// handleTaskComplete again (history count unchanged).
func TestReconcileSurgeCache_MissedComplete_AlreadyProcessed(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "complete",
		TotalLength: "1000",
	}, "stopped")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	tracker.processedComplete["sg_task1"] = true

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()
	m.reconcileSurgeCache()

	if historyCount() != before {
		t.Errorf("history count = %d, want %d (should not double-process)", historyCount(), before)
	}
}

// TestReconcileSurgeCache_MissedComplete_EmitsDeltaWithPayload verifies that
// the complete delta emitted by reconcile carries the progress payload
// (completedLength/totalLength) when TotalLength is set.
func TestReconcileSurgeCache_MissedComplete_EmitsDeltaWithPayload(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	var delta *events.TaskDelta
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "complete" && d.GID == "sg_task1" {
			cp := d
			delta = &cp
			break
		}
	}
	if delta == nil {
		t.Fatal("expected complete delta with payload")
	}
	payload, ok := delta.Payload.(rpc.Task)
	if !ok {
		t.Fatalf("expected rpc.Task payload, got %T", delta.Payload)
	}
	if payload.CompletedLength != "1000" {
		t.Errorf("completedLength = %v, want 1000", payload.CompletedLength)
	}
	if payload.TotalLength != "1000" {
		t.Errorf("totalLength = %v, want 1000", payload.TotalLength)
	}
}

// TestReconcileSurgeCache_MissedError_StampsErrorCode verifies that a missed
// EventError recovered via poll stamps Cache ErrorCode/ErrorMessage and emits
// an error delta payload (mirrors the event path).
func TestReconcileSurgeCache_MissedError_StampsErrorCode(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_disk1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_disk1", 1000, "https://example.com/big.bin", 4, "active")

	reader.setLists(nil, nil, []rpc.Task{{
		GID:          "disk1",
		Status:       "error",
		TotalLength:  "1000",
		ErrorCode:    "9",
		ErrorMessage: surgeEvents.ErrInsufficientDiskSpace.Error(),
	}})

	m.reconcileSurgeCache()

	var stopped *rpc.Task
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_disk1" {
			t := task
			stopped = &t
			break
		}
	}
	if stopped == nil {
		t.Fatal("expected sg_disk1 in stopped after reconcile")
	}
	if stopped.Status != "error" {
		t.Fatalf("Status = %q, want error", stopped.Status)
	}
	if stopped.ErrorCode != "9" {
		t.Fatalf("ErrorCode = %q, want 9", stopped.ErrorCode)
	}
	if stopped.ErrorMessage != surgeEvents.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("ErrorMessage = %q, want sentinel", stopped.ErrorMessage)
	}

	var delta *events.TaskDelta
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "error" && d.GID == "sg_disk1" {
			cp := d
			delta = &cp
			break
		}
	}
	if delta == nil {
		t.Fatal("expected error delta from poll")
	}
	payload, ok := delta.Payload.(rpc.Task)
	if !ok {
		t.Fatalf("payload type = %T, want rpc.Task", delta.Payload)
	}
	if payload.ErrorCode != "9" {
		t.Fatalf("delta errorCode = %q, want 9", payload.ErrorCode)
	}
	if payload.ErrorMessage != surgeEvents.ErrInsufficientDiskSpace.Error() {
		t.Fatalf("delta errorMessage = %q, want sentinel", payload.ErrorMessage)
	}
}

// TestReconcileSurgeCache_MissedError_ClassifiesFromMessageOnly verifies that
// when TellStopped omits ErrorCode but carries the disk sentinel message,
// poll still classifies "9".
func TestReconcileSurgeCache_MissedError_ClassifiesFromMessageOnly(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{
		GID:    "sg_disk2",
		Status: "active",
	}, "active")

	reader.setLists(nil, nil, []rpc.Task{{
		GID:          "disk2",
		Status:       "error",
		ErrorMessage: surgeEvents.ErrInsufficientDiskSpace.Error(),
	}})

	m.reconcileSurgeCache()

	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_disk2" {
			if task.ErrorCode != "9" {
				t.Fatalf("ErrorCode = %q, want 9 (classified from message)", task.ErrorCode)
			}
			return
		}
	}
	t.Fatal("expected sg_disk2 in stopped")
}

// TestReconcileSurgeCache_MissedComplete_EmptyTotalLength verifies that when
// TotalLength is empty, the complete delta is still emitted but without a
// payload (matches handleSurgeEvent error/no-total path).
func TestReconcileSurgeCache_MissedComplete_EmptyTotalLength(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:    "sg_task1",
		Status: "active",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete"}})

	m.reconcileSurgeCache()

	foundDelta := false
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "complete" && d.GID == "sg_task1" {
			foundDelta = true
			payload, ok := d.Payload.(rpc.Task)
			if !ok || payload.Status != "complete" || payload.TotalLength != "" {
				t.Errorf("unexpected unmeasured terminal payload: %v", d.Payload)
			}
		}
	}
	if !foundDelta {
		t.Fatal("expected complete delta even with empty TotalLength")
	}
}

// TestReconcileSurgeCache_MissedComplete_HeadlessNoDelta verifies that in
// headless mode (no window), reconcile does not push a delta to the pusher,
// but still calls NotifyInternal (triggers forceTick for tray update).
func TestReconcileSurgeCache_MissedComplete_HeadlessNoDelta(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	// Override to headless after newReconcileTestMonitor set it true.
	prevWindow := State.HasWindow()
	State.SetWindowExists(false)
	t.Cleanup(func() { State.SetWindowExists(prevWindow) })

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Register a delta listener to verify NotifyInternal fires.
	var internalFired atomic.Bool
	m.hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		if delta.Type == "complete" && delta.GID == "sg_task1" {
			internalFired.Store(true)
		}
	})

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "complete" && d.GID == "sg_task1" {
			t.Error("did not expect complete delta in headless mode")
		}
	}

	if !internalFired.Load() {
		t.Error("expected NotifyInternal to fire even in headless mode")
	}
}

// TestReconcileSurgeCache_MissedComplete_DuplicateDeltaDedup verifies that
// when the event path already queued a complete delta, reconcile's delta is
// deduped by the pusher (same GID, same type → replacement, single entry).
func TestReconcileSurgeCache_MissedComplete_DuplicateDeltaDedup(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Simulate the event path already queuing a complete delta.
	pusher.Queue(events.TaskDelta{
		Type: "complete", GID: "sg_task1",
		Payload: map[string]string{
			"completedLength": "1000",
			"downloadSpeed":   "0",
			"totalLength":     "1000",
		},
	})

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	count := 0
	for _, d := range pusher.pendingSnapshot() {
		if d.GID == "sg_task1" && d.Type == "complete" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 complete delta for sg_task1 (dedup), got %d", count)
	}
}

// TestReconcileSurgeCache_MissedPause_EmitsDelta verifies that the waiting
// branch emits a pause delta and an EmitTaskMove (From: active, To: waiting).
func TestReconcileSurgeCache_MissedPause_EmitsDelta(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "active"}, "active")

	var moveFired atomic.Bool
	var moveFrom, moveTo string
	m.hub.SubscribeTaskMove(func(move events.TaskMove) {
		if move.GID == "sg_task1" {
			moveFired.Store(true)
			moveFrom = move.From
			moveTo = move.To
		}
	})

	reader.setLists(nil, []rpc.Task{{GID: "task1", Status: "paused"}}, nil)

	m.reconcileSurgeCache()

	foundPause := false
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "pause" && d.GID == "sg_task1" {
			foundPause = true
		}
	}
	if !foundPause {
		t.Error("expected pause delta from poll")
	}

	if !moveFired.Load() {
		t.Fatal("expected EmitTaskMove for missed pause")
	}
	if moveFrom != "active" {
		t.Errorf("EmitTaskMove From = %s, want active", moveFrom)
	}
	if moveTo != "waiting" {
		t.Errorf("EmitTaskMove To = %s, want waiting", moveTo)
	}
}

// TestReconcileSurgeCache_MissedResume_EmitsDelta verifies that the active
// branch emits a resume delta and an EmitTaskMove (From: waiting, To: active).
func TestReconcileSurgeCache_MissedResume_EmitsDelta(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "paused"}, "waiting")

	var moveFired atomic.Bool
	var moveFrom, moveTo string
	m.hub.SubscribeTaskMove(func(move events.TaskMove) {
		if move.GID == "sg_task1" {
			moveFired.Store(true)
			moveFrom = move.From
			moveTo = move.To
		}
	})

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading"}}, nil, nil)

	m.reconcileSurgeCache()

	foundResume := false
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "resume" && d.GID == "sg_task1" {
			foundResume = true
		}
	}
	if !foundResume {
		t.Error("expected resume delta from poll")
	}

	if !moveFired.Load() {
		t.Fatal("expected EmitTaskMove for missed resume")
	}
	if moveFrom != "waiting" {
		t.Errorf("EmitTaskMove From = %s, want waiting", moveFrom)
	}
	if moveTo != "active" {
		t.Errorf("EmitTaskMove To = %s, want active", moveTo)
	}
}

// TestReconcileSurgeCache_MissedPause verifies that a task the engine reports
// as paused but Cache has active is moved to waiting.
func TestReconcileSurgeCache_MissedPause(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "active"}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 0, "", 0, "active")

	reader.setLists(nil, []rpc.Task{{GID: "task1", Status: "paused"}}, nil)

	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "paused" {
				t.Errorf("Status = %s, want paused", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in waiting after reconcile")
	}
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in active after reconcile")
		}
	}

	if tt := tracker.tasks["sg_task1"]; tt != nil {
		if tt.Status != "paused" {
			t.Errorf("tracker status = %s, want paused", tt.Status)
		}
	} else {
		t.Fatal("expected tracker entry for sg_task1")
	}
	for _, at := range tracker.GetActiveTrackedTasks() {
		if at.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in active tracked tasks after missed pause")
		}
	}
}

// TestReconcileSurgeCache_MissedResume verifies that a task the engine reports
// as active but Cache has waiting is moved to active.
func TestReconcileSurgeCache_MissedResume(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "paused"}, "waiting")
	tracker.EnsureTrackedFromEvent("sg_task1", 0, "", 0, "paused")

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading"}}, nil, nil)

	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "active" {
				t.Errorf("Status = %s, want active", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in active after reconcile")
	}

	if tt := tracker.tasks["sg_task1"]; tt != nil {
		if tt.Status != "active" {
			t.Errorf("tracker status = %s, want active", tt.Status)
		}
	} else {
		t.Fatal("expected tracker entry for sg_task1")
	}
	foundActiveTracked := false
	for _, at := range tracker.GetActiveTrackedTasks() {
		if at.GID == "sg_task1" {
			foundActiveTracked = true
		}
	}
	if !foundActiveTracked {
		t.Fatal("expected sg_task1 in active tracked tasks after missed resume")
	}
}

// TestReconcileSurgeCache_MissedPause_FromStopped_Refused verifies engine
// waiting must not revive a terminal stopped row (Round-2 app-layer refusal).
func TestReconcileSurgeCache_MissedPause_FromStopped_Refused(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{
		GID: "sg_task1", Status: "error",
		ErrorCode: "9", ErrorMessage: "fail",
	}, "stopped")

	reader.setLists(nil, []rpc.Task{{GID: "task1", Status: "paused"}}, nil)

	m.reconcileSurgeCache()

	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_task1" {
			t.Fatal("expected reconcile to refuse stopped→waiting for terminal gid")
		}
	}
	if !Cache.IsInStopped("sg_task1") {
		t.Fatal("expected sg_task1 to remain in stopped")
	}
	stopped := Cache.GetStopped()
	for _, task := range stopped {
		if task.GID == "sg_task1" {
			if task.ErrorCode != "9" {
				t.Fatalf("expected ErrorCode preserved, got %q", task.ErrorCode)
			}
			return
		}
	}
	t.Fatal("expected sg_task1 still in stopped with error metadata")
}

// TestReconcileSurgeCache_HistoryOnlyWaiting_RefusesAdmit seeds stopped from
// durable history when cache is empty but engine reports waiting (restart /
// cache-loss + late pause leaving Surge waiting while history is terminal).
func TestReconcileSurgeCache_HistoryOnlyWaiting_RefusesAdmit(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	history.Add(history.HistoryEntry{
		GID:             "sg_task1",
		Path:            "/tmp/hist-error.bin",
		Status:          "error",
		TotalLength:     "1000",
		CompletedLength: "200",
	})

	reader.setLists(nil, []rpc.Task{{GID: "task1", Status: "paused", TotalLength: "1000"}}, nil)
	m.reconcileSurgeCache()

	for _, task := range Cache.GetWaiting() {
		if task.GID == "sg_task1" {
			t.Fatal("expected history-terminal GID not admitted to waiting")
		}
	}
	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "error" {
				t.Fatalf("Status = %q, want error", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected history-terminal GID seeded into stopped")
	}
	if _, ok := history.Get("sg_task1"); !ok {
		t.Fatal("expected history entry retained (not retired on refused waiting admit)")
	}
	if !tracker.processedComplete["sg_task1"] {
		t.Fatal("expected tracker marked terminal for seeded history row")
	}
}

// TestReconcileSurgeCache_ErrorResumeError_AllowsSecondTerminal verifies
// stopped→active reopen clears processedComplete so a later error records again.
func TestReconcileSurgeCache_ErrorResumeError_AllowsSecondTerminal(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID: "sg_task1", Status: "error",
		ErrorCode: "9", ErrorMessage: "fail",
		Files:       []rpc.File{{Path: "/tmp/a.bin"}},
		TotalLength: "1000",
	}, "stopped")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/a.bin", 0, "error")
	if completed := tracker.MarkCompleteFromEvent("sg_task1", "error"); completed == nil {
		t.Fatal("expected first MarkCompleteFromEvent")
	}
	history.Add(history.HistoryEntry{GID: "sg_task1", Path: "/tmp/a.bin", Status: "error"})
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/tmp/a.bin"},
		Dir:   "/tmp",
	}

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading", TotalLength: "1000"}}, nil, nil)
	m.reconcileSurgeCache()

	if tracker.processedComplete["sg_task1"] {
		t.Fatal("expected processedComplete cleared after stopped→active reopen")
	}
	if _, ok := history.Get("sg_task1"); ok {
		t.Fatal("expected history retired on resume")
	}

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{})
	reader.setLists(nil, nil, []rpc.Task{{
		GID: "task1", Status: "error", TotalLength: "1000",
		ErrorCode: "1", ErrorMessage: "again",
	}})
	m.reconcileSurgeCache()

	if !tracker.processedComplete["sg_task1"] {
		t.Fatal("expected second terminal to set processedComplete")
	}
	entry, ok := history.Get("sg_task1")
	if !ok {
		t.Fatal("expected history rewritten after second error")
	}
	if entry.Status != "error" {
		t.Fatalf("history Status = %q, want error", entry.Status)
	}
}

// TestReconcileSurgeCache_MissedResume_FromStopped_RetiresHistory verifies
// stopped→active reconcile removes durable history for the GID.
func TestReconcileSurgeCache_MissedResume_FromStopped_RetiresHistory(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	Cache.AddSgTask(rpc.Task{
		GID: "sg_task1", Status: "error",
		ErrorCode: "9", ErrorMessage: "fail",
		Files: []rpc.File{{Path: "/tmp/a.bin"}},
	}, "stopped")
	history.Add(history.HistoryEntry{GID: "sg_task1", Path: "/tmp/a.bin", Status: "error"})

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading"}}, nil, nil)
	m.reconcileSurgeCache()

	if _, ok := history.Get("sg_task1"); ok {
		t.Fatal("expected history retired after reconcile stopped→active")
	}
}

// TestReconcileSurgeCache_MissedResume_FromStopped_Converges verifies that a
// stopped→active reconcile emits From=stopped once and a second pass is quiet.
func TestReconcileSurgeCache_MissedResume_FromStopped_Converges(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{
		GID: "sg_task1", Status: "error",
		ErrorCode: "9", ErrorMessage: "fail",
	}, "stopped")

	var moves []events.TaskMove
	m.hub.SubscribeTaskMove(func(move events.TaskMove) {
		if move.GID == "sg_task1" {
			moves = append(moves, move)
		}
	})

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading"}}, nil, nil)

	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "active" {
				t.Errorf("Status = %s, want active", task.Status)
			}
			if task.ErrorCode != "" || task.ErrorMessage != "" {
				t.Errorf("expected cleared errors, got code=%q msg=%q", task.ErrorCode, task.ErrorMessage)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in active after reconcile from stopped")
	}
	if len(moves) != 1 {
		t.Fatalf("expected 1 move on first reconcile, got %d", len(moves))
	}
	if moves[0].From != "stopped" || moves[0].To != "active" {
		t.Errorf("move From/To = %q/%q, want stopped/active", moves[0].From, moves[0].To)
	}

	resumeCount := 0
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "resume" && d.GID == "sg_task1" {
			resumeCount++
		}
	}
	if resumeCount != 1 {
		t.Fatalf("expected 1 resume delta on first pass, got %d", resumeCount)
	}

	// Second pass: cache already active — no further move or resume delta.
	pusher.pendingSnapshot()
	moves = nil

	m.reconcileSurgeCache()

	if len(moves) != 0 {
		t.Fatalf("expected no move on second reconcile, got %d (%#v)", len(moves), moves)
	}
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "resume" && d.GID == "sg_task1" {
			t.Fatal("expected no resume delta on second reconcile")
		}
	}
}

// TestReconcileSurgeCache_MissedRemove verifies that a task in Cache but not
// in the engine is removed and a remove delta is pushed.
func TestReconcileSurgeCache_MissedRemove(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "complete"}, "stopped")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com", 4, "active")

	reader.setLists(nil, nil, nil)

	m.reconcileSurgeCache()

	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in stopped after reconcile")
		}
	}

	deltaFound := false
	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "remove" && d.GID == "sg_task1" {
			deltaFound = true
		}
	}
	if !deltaFound {
		t.Fatal("expected remove delta pushed for sg_task1")
	}

	if tracker.tasks["sg_task1"] != nil {
		t.Error("expected tracker entry removed for sg_task1")
	}
}

// TestReconcileSurgeCache_MissedAdd verifies that a task in the engine but not
// in Cache is added with its accepted active status.
func TestReconcileSurgeCache_MissedAdd(t *testing.T) {
	m, reader, pusher, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	reader.setLists([]rpc.Task{{GID: "task1", Status: "downloading", TotalLength: "5000"}}, nil, nil)

	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in active after reconcile")
	}

	for _, d := range pusher.pendingSnapshot() {
		if d.Type == "add" && d.GID == "sg_task1" {
			if task, ok := d.Payload.(rpc.Task); !ok || task.Status != "active" {
				t.Errorf("unexpected add payload: %v", d.Payload)
			}
			return
		}
	}
	t.Fatal("missing accepted add delta")
}

// TestReconcileSurgeCache_MissedAddWithComplete verifies that a task the engine
// reports as complete but is absent from Cache (both add and complete events
// dropped, or startup) is added to stopped AND has handleTaskComplete run so
// history/speed stats are not permanently lost.
func TestReconcileSurgeCache_MissedAddWithComplete(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()
	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "complete" {
				t.Errorf("Status = %s, want complete", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in stopped after missed-add-with-complete")
	}

	if got := historyCount(); got != before+1 {
		t.Errorf("history count = %d, want %d (handleTaskComplete should run for missed complete)", got, before+1)
	}
	if !tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}
}

// TestReconcileSurgeCache_RapidStateChange verifies that when a task cycles
// active→waiting→active→complete between polls, the poll reconciles to the
// final state (stopped) and records history exactly once.
func TestReconcileSurgeCache_RapidStateChange(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Simulate intermediate states the poll never observed; only the final
	// engine state matters for reconciliation.
	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()
	m.reconcileSurgeCache()

	found := false
	for _, task := range Cache.GetStopped() {
		if task.GID == "sg_task1" {
			found = true
			if task.Status != "complete" {
				t.Errorf("Status = %s, want complete", task.Status)
			}
		}
	}
	if !found {
		t.Fatal("expected sg_task1 in stopped after rapid state change")
	}
	for _, task := range Cache.GetActive() {
		if task.GID == "sg_task1" {
			t.Fatal("expected sg_task1 NOT in active after reconcile")
		}
	}

	if got := historyCount(); got != before+1 {
		t.Errorf("history count = %d, want %d (final complete processed once)", got, before+1)
	}
	if !tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}
}

// TestReconcileSurgeCache_NoDiscrepancy verifies that when Cache and engine
// agree, no corrections or deltas occur.
func TestReconcileSurgeCache_NoDiscrepancy(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "active"}, "active")
	Cache.AddSgTask(rpc.Task{GID: "sg_task2", Status: "paused"}, "waiting")
	Cache.AddSgTask(rpc.Task{GID: "sg_task3", Status: "complete"}, "stopped")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "", 4, "active")

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	reader.setLists(
		[]rpc.Task{{GID: "task1", Status: "downloading"}},
		[]rpc.Task{{GID: "task2", Status: "paused"}},
		[]rpc.Task{{GID: "task3", Status: "complete"}},
	)

	before := historyCount()
	m.reconcileSurgeCache()

	if historyCount() != before {
		t.Errorf("history count changed: %d != %d", historyCount(), before)
	}

	if pending := pusher.pendingSnapshot(); len(pending) > 0 {
		t.Errorf("expected no deltas, got %d", len(pending))
	}
}

// TestReconcileSurgeCache_NilSurgeEng verifies that reconcileSurgeCache
// returns immediately without panic when no Surge reader is available.
func TestReconcileSurgeCache_NilSurgeEng(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:               hub,
		pusher:            NewPusher(hub),
		tracker:           NewTaskTracker(),
		engine:            &mockSafeEngine{},
		surgePollInterval: 10 * time.Second,
	}

	m.reconcileSurgeCache()
}

// TestReconcileSurgeCache_TellActiveError verifies that a TellActive error
// causes the poll to skip that round without panic.
func TestReconcileSurgeCache_TellActiveError(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	reader.mu.Lock()
	reader.err = context.DeadlineExceeded
	reader.mu.Unlock()

	m.reconcileSurgeCache()
}

// TestReconcileSurgeCache_ConcurrentWithEvent verifies that concurrent
// reconciliation and event handling for the same GID does not double-process
// completion (processedComplete dedup) and is race-free.
func TestReconcileSurgeCache_ConcurrentWithEvent(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	before := historyCount()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		m.reconcileSurgeCache()
	}()

	go func() {
		defer wg.Done()
		m.handleSurgeEvent(surgeEvents.DownloadEvent{
			Type:       surgeEvents.EventComplete,
			DownloadID: "task1",
			Total:      1000,
		})
	}()

	wg.Wait()

	if !tracker.processedComplete["sg_task1"] {
		t.Error("expected processedComplete[sg_task1] = true")
	}

	// Dedup must prevent double handleTaskComplete: exactly one history write.
	if got := historyCount(); got != before+1 {
		t.Errorf("history count = %d, want %d (complete should be processed exactly once)", got, before+1)
	}
}

// TestSurgePollLoop_StartupWindow verifies that surgePollLoop's first
// reconciliation populates empty sg_ slices from the engine list.
func TestSurgePollLoop_StartupWindow(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	reader.setLists(
		[]rpc.Task{{GID: "task1", Status: "downloading"}},
		[]rpc.Task{{GID: "task2", Status: "paused"}},
		[]rpc.Task{{GID: "task3", Status: "complete"}},
	)

	m.surgePollStopChan = make(chan struct{})
	m.surgePollInterval = 50 * time.Millisecond

	done := make(chan struct{})
	go func() {
		m.surgePollLoop()
		close(done)
	}()

	// Wait for the immediate first reconcile to populate cache.
	requireCondition(t, func() bool {
		return taskInCache("sg_task1", "active") &&
			taskInCache("sg_task2", "waiting") &&
			taskInCache("sg_task3", "stopped")
	}, 2*time.Second, "startup window did not populate sg_ slices")

	close(m.surgePollStopChan)
	<-done
}

// TestSurgePollLoop_PollInterval verifies that surgePollLoop runs
// reconciliation at the configured interval.
func TestSurgePollLoop_PollInterval(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	m.surgePollStopChan = make(chan struct{})
	m.surgePollInterval = 50 * time.Millisecond

	var pollCount int64

	originalReader := m.surgePollReader
	wrapped := &countingReader{surgeListReader: originalReader, count: &pollCount}
	m.surgePollReader = wrapped

	reader.setLists(nil, nil, nil)

	done := make(chan struct{})
	go func() {
		m.surgePollLoop()
		close(done)
	}()

	requireCondition(t, func() bool {
		return atomic.LoadInt64(&pollCount) >= 3
	}, 2*time.Second, "expected at least 3 poll ticks")

	close(m.surgePollStopChan)
	<-done
}

// TestStartSurgeEventBridge_DisconnectReconnect verifies that when
// StreamEvents returns an error, surgeStreamConnected is set false, and
// after a retry interval, a successful reconnection sets it true.
func TestStartSurgeEventBridge_DisconnectReconnect(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:               hub,
		pusher:            NewPusher(hub),
		tracker:           NewTaskTracker(),
		engine:            &reconnectMockEngine{},
		surgePollInterval: 50 * time.Millisecond,
		stopChan:          make(chan struct{}),
	}
	resetCacheSg()
	t.Cleanup(func() { resetCacheSg() })

	mockEng := m.engine.(*reconnectMockEngine)
	mockEng.failCount = 1 // first call fails, second succeeds
	mockEng.streamChan = make(chan any, 1)
	mockEng.streamChan <- surgeEvents.DownloadEvent{Type: surgeEvents.EventProgress, DownloadID: "x"}

	done := make(chan struct{})
	go func() {
		m.surgeEventBridgeLoop()
		close(done)
	}()

	requireCondition(t, func() bool {
		return m.surgeStreamConnected.Load()
	}, 2*time.Second, "expected surgeStreamConnected=true after reconnect")

	close(m.stopChan)
	<-done
}

// TestStartSurgeEventBridge_ChannelClose verifies that when the event stream
// channel closes, surgeStreamConnected is set false and the loop retries.
func TestStartSurgeEventBridge_ChannelClose(t *testing.T) {
	hub := events.NewHub(nil)
	mockEng := &reconnectMockEngine{
		streamChan: make(chan any),
	}
	m := &Monitor{
		hub:               hub,
		pusher:            NewPusher(hub),
		tracker:           NewTaskTracker(),
		engine:            mockEng,
		surgePollInterval: 50 * time.Millisecond,
		stopChan:          make(chan struct{}),
	}
	resetCacheSg()
	t.Cleanup(func() { resetCacheSg() })

	done := make(chan struct{})
	go func() {
		m.surgeEventBridgeLoop()
		close(done)
	}()

	requireCondition(t, func() bool {
		return m.surgeStreamConnected.Load()
	}, 1*time.Second, "expected connected after first subscribe")

	close(mockEng.streamChan)

	requireCondition(t, func() bool {
		return !m.surgeStreamConnected.Load()
	}, 1*time.Second, "expected disconnected after channel close")

	close(m.stopChan)
	<-done
}

// TestStartSurgeEventBridge_Aria2Only verifies that startSurgeEventBridge
// does not enter a reconnect loop when surgeEng is nil (Aria2-only mode).
func TestStartSurgeEventBridge_Aria2Only(t *testing.T) {
	hub := events.NewHub(nil)
	m := &Monitor{
		hub:               hub,
		pusher:            NewPusher(hub),
		tracker:           NewTaskTracker(),
		engine:            &mockSafeEngine{},
		surgePollInterval: 50 * time.Millisecond,
		stopChan:          make(chan struct{}),
	}

	// surgeEng is nil (Aria2-only mode) — startSurgeEventBridge must return
	// immediately without launching a reconnect loop.
	m.startSurgeEventBridge()

	if m.surgeStreamConnected.Load() {
		t.Error("expected surgeStreamConnected=false in Aria2-only mode")
	}

	close(m.stopChan)
}

// TestReconcileSurgeCache_MissedComplete_ClearsPauseResumeIntention verifies
// that the reconcile stopped branch clears the pauseResumeIntentions entry for
// the completed GID, matching handleSurgeEvent's terminal cleanup.
func TestReconcileSurgeCache_MissedComplete_ClearsPauseResumeIntention(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	m.pauseResumeVersionMu.Lock()
	m.pauseResumeIntentions = map[string]pauseResumeIntention{"sg_task1": {action: "pause"}}
	m.pauseResumeVersionMu.Unlock()

	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	tracker.EnsureTrackedFromEvent("sg_task1", 1000, "https://example.com/file.zip", 4, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	m.pauseResumeVersionMu.RLock()
	_, exists := m.pauseResumeIntentions["sg_task1"]
	m.pauseResumeVersionMu.RUnlock()
	if exists {
		t.Error("expected pauseResumeIntentions[sg_task1] cleared after reconcile stopped")
	}
}

// TestReconcileSurgeCache_MissedRemove_ClearsPauseResumeIntention verifies
// that the reconcile remove path clears the pauseResumeIntentions entry,
// matching handleSurgeEvent's terminal cleanup.
func TestReconcileSurgeCache_MissedRemove_ClearsPauseResumeIntention(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	m.pauseResumeVersionMu.Lock()
	m.pauseResumeIntentions = map[string]pauseResumeIntention{"sg_task1": {action: "pause"}}
	m.pauseResumeVersionMu.Unlock()

	Cache.AddSgTask(rpc.Task{GID: "sg_task1", Status: "complete"}, "stopped")

	reader.setLists(nil, nil, nil)

	m.reconcileSurgeCache()

	m.pauseResumeVersionMu.RLock()
	_, exists := m.pauseResumeIntentions["sg_task1"]
	m.pauseResumeVersionMu.RUnlock()
	if exists {
		t.Error("expected pauseResumeIntentions[sg_task1] cleared after reconcile remove")
	}
}

// TestReconcileSurgeCache_MissedAddWithComplete_ClearsPauseResumeIntention
// verifies that the "added missing task" stopped path also clears the
// pauseResumeIntentions entry.
func TestReconcileSurgeCache_MissedAddWithComplete_ClearsPauseResumeIntention(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	m.pauseResumeVersionMu.Lock()
	m.pauseResumeIntentions = map[string]pauseResumeIntention{"sg_task1": {action: "pause"}}
	m.pauseResumeVersionMu.Unlock()

	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "1000"}})

	m.reconcileSurgeCache()

	m.pauseResumeVersionMu.RLock()
	_, exists := m.pauseResumeIntentions["sg_task1"]
	m.pauseResumeVersionMu.RUnlock()
	if exists {
		t.Error("expected pauseResumeIntentions[sg_task1] cleared after missed-add-with-complete")
	}
}

// TestReconcileSurgeCache_MissedComplete_UpdatesTrackerTotalLength verifies
// that the reconcile stopped branch calls EnsureTrackedFromEvent to update the
// tracker's TotalLength from the engine task before MarkCompleteFromEvent, so
// handleTaskComplete records accurate history. This matches handleSurgeEvent's
// complete case which calls EnsureTrackedFromEvent(completeTotal) first.
func TestReconcileSurgeCache_MissedComplete_UpdatesTrackerTotalLength(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	// Track the task with TotalLength=0 (simulates stale/zero tracker state).
	tracker.EnsureTrackedFromEvent("sg_task1", 0, "https://example.com/file.zip", 4, "active")
	Cache.AddSgTask(rpc.Task{
		GID:         "sg_task1",
		Status:      "active",
		TotalLength: "1000",
	}, "active")
	Cache.metadata["sg_task1"] = &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	}

	// Engine reports the task as complete with TotalLength=5000.
	reader.setLists(nil, nil, []rpc.Task{{GID: "task1", Status: "complete", TotalLength: "5000"}})

	m.reconcileSurgeCache()

	tt := tracker.tasks["sg_task1"]
	if tt == nil {
		t.Fatal("expected tracker entry for sg_task1")
	}
	if tt.TotalLength != 5000 {
		t.Errorf("tracker TotalLength = %d, want 5000 (EnsureTrackedFromEvent should update from engine)", tt.TotalLength)
	}
}

// --- helpers ---

type countingReader struct {
	surgeListReader
	count *int64
}

func (c *countingReader) TellActive() ([]rpc.Task, error) {
	atomic.AddInt64(c.count, 1)
	return c.surgeListReader.TellActive()
}

func (c *countingReader) TellWaiting(offset, num int) ([]rpc.Task, error) {
	atomic.AddInt64(c.count, 1)
	return c.surgeListReader.TellWaiting(offset, num)
}

func (c *countingReader) TellStopped(offset, num int) ([]rpc.Task, error) {
	atomic.AddInt64(c.count, 1)
	return c.surgeListReader.TellStopped(offset, num)
}

// reconnectMockEngine is a mockSafeEngine whose StreamEvents can be
// configured to fail N times then succeed, and whose channel can be closed
// to simulate disconnection.
type reconnectMockEngine struct {
	mockSafeEngine
	failCount  int
	failMu     sync.Mutex
	streamChan chan any
}

func (e *reconnectMockEngine) StreamEvents(ctx context.Context) (<-chan any, func(), error) {
	e.failMu.Lock()
	if e.failCount > 0 {
		e.failCount--
		e.failMu.Unlock()
		return nil, func() {}, context.Canceled
	}
	e.failMu.Unlock()
	cleanup := func() {}
	return e.streamChan, cleanup, nil
}

func (e *reconnectMockEngine) IsSurgeActive() bool {
	return true
}

func taskInCache(gid, list string) bool {
	switch list {
	case "active":
		for _, task := range Cache.GetActive() {
			if task.GID == gid {
				return true
			}
		}
	case "waiting":
		for _, task := range Cache.GetWaiting() {
			if task.GID == gid {
				return true
			}
		}
	case "stopped":
		for _, task := range Cache.GetStopped() {
			if task.GID == gid {
				return true
			}
		}
	}
	return false
}

func requireCondition(t *testing.T, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
