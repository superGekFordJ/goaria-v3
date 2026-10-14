package monitor

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/progress"
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
	testutil.SeedMasterList(t, surgeEvents.DownloadRecord{
		ID: "pool_resume", URL: full.Files[0].Uris[0].Uri,
		DestPath: full.Files[0].Path, Filename: "a", Status: "error", TotalSize: 1000, Downloaded: 500,
	})
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
// the terminal finish runs (writing history), and a complete delta is pushed
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
	// Provide file metadata so the terminal finish can write history.
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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
		t.Errorf("history count = %d, want %d (terminal finish should write history)", historyCount(), before+1)
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
// the terminal finish again (history count unchanged).
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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/tmp/a.bin"},
		Dir:   "/tmp",
	})

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

	// Scheduler explicitly does not know the task — an absent answer, not
	// merely "no row in the bulk lists" (unknown is never removal evidence).
	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Absent: true})
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
// dropped, or startup) is added to stopped AND has the terminal finish run so
// history/speed stats are not permanently lost.
func TestReconcileSurgeCache_MissedAddWithComplete(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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
		t.Errorf("history count = %d, want %d (terminal finish should run for missed complete)", got, before+1)
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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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

	// Dedup must prevent a double terminal finish: exactly one history write.
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
	// Queued/paused admission needs scheduler truth, not only a mirror row.
	reader.setLiveStatus("task2", rpc.SurgeLiveStatus{Known: true, Status: "paused"})
	reader.setLiveStatus("task3", rpc.SurgeLiveStatus{Known: true, Status: "completed"})
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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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

	reader.setLiveStatus("task1", rpc.SurgeLiveStatus{Absent: true})
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

	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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
// the terminal finish records accurate history. This matches handleSurgeEvent's
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
	Cache.SetMetadataForTest("sg_task1", &TaskMetadata{
		GID:   "sg_task1",
		Files: []string{"/downloads/file.zip"},
		Dir:   "/downloads",
	})

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

// =========================================================================
// Recovery / projection matrix: history-loss, cache-loss, unknown-vs-absent,
// stale-read barriers, orphan sweep, bounded intention shield.
// =========================================================================

// A stopped full row with durable H0 plus a cache-lost GID with only history:
// real queued restores waiting (generation reopen, H0 retire, group re-home,
// zero activation); real running restores active. Metadata/full rows and
// typed payloads survive the moves.
func TestResumeRecovery_HistoryAndCacheLoss(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	setupTaskGroupStoreTest(t)

	full := fullRowForTest("sg_restore_full")
	full.Status = "error"
	Cache.AddSgTask(full, "stopped")
	tracker.EnsureTrackedFromEvent(full.GID, 1000, full.Files[0].Uris[0].Uri, 2, "error")
	tracker.MarkCompleteFromEvent(full.GID, "error")
	history.Add(history.HistoryEntry{GID: full.GID, Path: full.Files[0].Path, Status: "error", DownloadGroup: full.DownloadGroup})
	reader.setLiveStatus("restore_full", rpc.SurgeLiveStatus{Known: true, Status: "queued"})

	history.Add(history.HistoryEntry{GID: "sg_restore_hist", Path: "/d/h.bin", Source: "https://x/h", Status: "error"})
	reader.setLiveStatus("restore_hist", rpc.SurgeLiveStatus{Known: true, Status: "downloading", TotalLength: 400, CompletedLength: 120, TotalKnown: true, CompletedKnown: true})

	reader.setLists(
		[]rpc.Task{{GID: "restore_hist", Status: "downloading"}},
		[]rpc.Task{{GID: "restore_full", Status: "paused"}},
		nil,
	)
	m.reconcileSurgeCache()

	got, list, _ := Cache.GetTask(full.GID)
	assertFullRowMetadata(t, got, full)
	tracked, _ := tracker.GetTrackedTask(full.GID)
	if list != "waiting" || got.Status != "waiting" || !tracked.ActivatedAt.IsZero() ||
		tracker.LifecycleGeneration(full.GID) != 1 {
		t.Fatalf("queued restore must reopen to waiting once without activation: %#v gen=%d", got, tracker.LifecycleGeneration(full.GID))
	}
	if _, ok := history.Get(full.GID); ok {
		t.Fatal("H0 not retired on restore")
	}
	if group := GetStoredTaskGroup(full.GID); group == nil || group.ID != full.DownloadGroup.ID {
		t.Fatal("group not re-homed on restore")
	}

	got2, list2, ok2 := Cache.GetTask("sg_restore_hist")
	if !ok2 || list2 != "active" || got2.Status != "active" || got2.CompletedLength != "120" || got2.TotalLength != "400" {
		t.Fatalf("cache-loss running restore failed: %#v list=%s", got2, list2)
	}
	if len(got2.Files) == 0 || got2.Files[0].Path != "/d/h.bin" {
		t.Fatalf("history metadata not merged into restored row: %#v", got2)
	}
	tracked2, ok2tracked := tracker.GetTrackedTask("sg_restore_hist")
	if !ok2tracked || tracked2.ActivatedAt.IsZero() {
		t.Fatal("real running restore must activate")
	}
	if _, ok := history.Get("sg_restore_hist"); ok {
		t.Fatal("history not retired for restored row")
	}

	var sawResume, sawAdd bool
	for _, d := range pusher.pendingSnapshot() {
		if d.GID == full.GID && d.Type == "resume" {
			sawResume = true
			if task, ok := d.Payload.(rpc.Task); !ok || task.Title != full.Title || len(task.Files) != len(full.Files) {
				t.Fatalf("resume delta lost full payload: %#v", d.Payload)
			}
		}
		if d.GID == "sg_restore_hist" && d.Type == "add" {
			sawAdd = true
		}
	}
	if !sawResume || !sawAdd {
		t.Fatalf("missing accepted deltas resume=%v add=%v", sawResume, sawAdd)
	}
}

// Mirror rows, stale GetAll cfgs and nil-state workers are never live
// evidence: they neither admit waiting/active nor delete local rows, and a
// partial list failure writes nothing at all.
func TestResumeRecovery_UnknownEvidence(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	for _, gid := range []string{"sg_h_w", "sg_h_p", "sg_h_r", "sg_h_nil"} {
		history.Add(history.HistoryEntry{GID: gid, Path: "/d/" + gid + ".bin", Status: "error"})
	}
	// nil-state worker: exists in pool but GetStatus reports no state.
	reader.setLiveStatus("h_nil", rpc.SurgeLiveStatus{})
	// A live row that stays because unknown is not absence.
	Cache.AddSgTask(rpc.Task{GID: "sg_kept", Status: "active", TotalLength: "100"}, "active")
	tracker.EnsureTrackedFromEvent("sg_kept", 100, "", 1, "active")
	reader.setLiveStatus("kept", rpc.SurgeLiveStatus{})

	reader.setLists(
		[]rpc.Task{{GID: "h_r", Status: "downloading"}},
		[]rpc.Task{{GID: "h_w", Status: "waiting"}, {GID: "h_p", Status: "paused"}, {GID: "h_nil", Status: "paused"}},
		nil,
	)
	m.reconcileSurgeCache()

	// History-backed terminal rows are seeded into stopped, never waiting;
	// H0 stays durable and the generation does not reopen.
	for _, gid := range []string{"sg_h_w", "sg_h_p", "sg_h_nil"} {
		got, list, ok := Cache.GetTask(gid)
		if !ok || list != "stopped" {
			t.Fatalf("%s: expected stopped seed, got %#v list=%s ok=%v", gid, got, list, ok)
		}
		if _, ok := history.Get(gid); !ok {
			t.Fatalf("%s: H0 retired on mirror evidence", gid)
		}
		if tracker.LifecycleGeneration(gid) != 0 {
			t.Fatalf("%s: mirror row reopened the lifecycle", gid)
		}
	}
	// Mirror-only downloading is not activation evidence either.
	if _, _, ok := Cache.GetTask("sg_h_r"); ok {
		t.Fatal("mirror-only downloading admitted")
	}
	if _, ok := history.Get("sg_h_r"); !ok {
		t.Fatal("downloading mirror retired H0")
	}
	// Unknown live state is not removal evidence.
	if _, list, ok := Cache.GetTask("sg_kept"); !ok || list != "active" {
		t.Fatal("unknown live state deleted the row")
	}
	if _, ok := tracker.GetTrackedTask("sg_kept"); !ok {
		t.Fatal("tracker entry removed on unknown live state")
	}
	if len(pusher.pendingSnapshot()) != 0 {
		t.Fatal("mirror-only evidence pushed deltas")
	}

	// Partial list failure aborts the round wholesale — zero writes even for
	// candidates that would otherwise be proven.
	reader.activeFn = func() ([]rpc.Task, error) { return nil, context.DeadlineExceeded }
	reader.setLiveStatus("h_w", rpc.SurgeLiveStatus{Known: true, Status: "queued"})
	beforeHist := historyCount()
	beforeStopped := len(Cache.GetStopped())
	m.reconcileSurgeCache()
	if len(Cache.GetStopped()) != beforeStopped || historyCount() != beforeHist {
		t.Fatal("partial list failure wrote partial state")
	}
}

// Mid-read mutations must retire a captured observation: stale engine lists
// can no longer overwrite an accepted Started/Complete/Resume fact, and an
// uncaptured candidate mutated during the read is never blessed.
func TestResumeRecovery_StaleReadMatrix(t *testing.T) {
	type midFn func(m *Monitor, tracker *TaskTracker)

	seedMeta := func(gid string) {
		Cache.SetMetadataForTest(gid, &TaskMetadata{GID: gid, Files: []string{"/d/f"}, Dir: "/d"})
	}
	seedChunk := func(m *Monitor, gid string) {
		m.chunkSnapshots.Set(gid, []byte{2}, 8, 1024, nil)
	}
	intentionOf := func(m *Monitor, gid string) (pauseResumeIntention, bool) {
		m.pauseResumeVersionMu.RLock()
		defer m.pauseResumeVersionMu.RUnlock()
		in, ok := m.pauseResumeIntentions[gid]
		return in, ok
	}
	assertIntentionNone := func(t *testing.T, m *Monitor, gid string) {
		t.Helper()
		if in, ok := intentionOf(m, gid); ok && in.seq != 0 {
			t.Fatalf("unexpected live intention: %#v", in)
		}
	}
	assertChunkGone := func(t *testing.T, m *Monitor, gid string) {
		t.Helper()
		if _, ok := m.chunkSnapshots.Get(gid); ok {
			t.Fatal("chunk snapshot survived the authoritative write")
		}
	}
	assertMovesTo := func(t *testing.T, moves []events.TaskMove, gid, wantTo string) {
		t.Helper()
		for _, mv := range moves {
			if mv.GID == gid && mv.To != wantTo {
				t.Fatalf("stale round emitted a %q move: %#v", mv.To, mv)
			}
		}
	}

	for _, tc := range []struct {
		name  string
		setup func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker)
		lists func() (active, waiting, stopped []rpc.Task)
		mid   midFn
		want  func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove)
	}{
		{
			name: "old_waiting_vs_started",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
				Cache.AddSgTask(rpc.Task{GID: "sg_sm", Status: "waiting", TotalLength: "100"}, "waiting")
				tracker.EnsureTrackedFromEvent("sg_sm", 100, "", 1, "waiting")
				seedChunk(m, "sg_sm")
			},
			lists: func() (a, w, s []rpc.Task) {
				return nil, []rpc.Task{{GID: "sm", Status: "waiting"}}, nil
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventStarted, DownloadID: "sm"})
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, _ := Cache.GetTask("sg_sm")
				if list != "active" || got.Status != "active" {
					t.Fatalf("stale waiting overwrote accepted started: %#v %s", got, list)
				}
				for _, d := range pusher.pendingSnapshot() {
					if d.GID == "sg_sm" {
						if task, ok := d.Payload.(rpc.Task); ok && task.Status == "waiting" {
							t.Fatalf("stale poll emitted a waiting payload: %#v", d)
						}
					}
				}
				assertChunkGone(t, m, "sg_sm")
				assertIntentionNone(t, m, "sg_sm")
				moves := movesOf()
				assertMovesTo(t, moves, "sg_sm", "active")
				found := false
				for _, mv := range moves {
					if mv.GID == "sg_sm" {
						found = true
					}
				}
				if !found {
					t.Fatal("started accept emitted no task:move")
				}
			},
		},
		{
			name: "old_active_vs_complete",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
				Cache.AddSgTask(rpc.Task{GID: "sg_sm", Status: "active", TotalLength: "100"}, "active")
				tracker.EnsureTrackedFromEvent("sg_sm", 100, "", 1, "active")
				seedMeta("sg_sm")
				seedChunk(m, "sg_sm")
			},
			lists: func() (a, w, s []rpc.Task) {
				return []rpc.Task{{GID: "sm", Status: "downloading"}}, nil, nil
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventComplete, DownloadID: "sm", Total: 100})
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, _ := Cache.GetTask("sg_sm")
				if list != "stopped" || got.Status != "complete" {
					t.Fatalf("stale live list overwrote accepted complete: %#v %s", got, list)
				}
				if n := len(historyEntriesForGID("sg_sm")); n != 1 {
					t.Fatalf("want exactly one history write, got %d", n)
				}
				assertChunkGone(t, m, "sg_sm")
				assertIntentionNone(t, m, "sg_sm")
				assertMovesTo(t, movesOf(), "sg_sm", "stopped")
			},
		},
		{
			name: "old_active_vs_error",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
				Cache.AddSgTask(rpc.Task{GID: "sg_sm", Status: "active", TotalLength: "100"}, "active")
				tracker.EnsureTrackedFromEvent("sg_sm", 100, "", 1, "active")
				seedMeta("sg_sm")
				seedChunk(m, "sg_sm")
			},
			lists: func() (a, w, s []rpc.Task) {
				return []rpc.Task{{GID: "sm", Status: "downloading"}}, nil, nil
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventError, DownloadID: "sm", Err: errors.New("link down")})
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, _ := Cache.GetTask("sg_sm")
				if list != "stopped" || got.Status != "error" {
					t.Fatalf("stale live list overwrote accepted error: %#v %s", got, list)
				}
				if entries := historyEntriesForGID("sg_sm"); len(entries) != 1 || entries[0].Status != "error" {
					t.Fatalf("want exactly one error history write, got %#v", entries)
				}
				assertChunkGone(t, m, "sg_sm")
				assertIntentionNone(t, m, "sg_sm")
				sawError := false
				for _, d := range pusher.pendingSnapshot() {
					if d.GID == "sg_sm" && d.Type == "error" {
						sawError = true
					}
				}
				if !sawError {
					t.Fatal("accepted error emitted no error delta")
				}
				assertMovesTo(t, movesOf(), "sg_sm", "stopped")
			},
		},
		{
			name: "old_waiting_vs_complete",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
				Cache.AddSgTask(rpc.Task{GID: "sg_sm", Status: "waiting", TotalLength: "100"}, "waiting")
				tracker.EnsureTrackedFromEvent("sg_sm", 100, "", 1, "waiting")
				seedMeta("sg_sm")
				seedChunk(m, "sg_sm")
			},
			lists: func() (a, w, s []rpc.Task) {
				return nil, []rpc.Task{{GID: "sm", Status: "waiting"}}, nil
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventComplete, DownloadID: "sm", Total: 100})
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, _ := Cache.GetTask("sg_sm")
				if list != "stopped" || got.Status != "complete" || got.CompletedLength != "100" {
					t.Fatalf("stale waiting list overwrote accepted fast-complete: %#v %s", got, list)
				}
				if n := len(historyEntriesForGID("sg_sm")); n != 1 {
					t.Fatalf("want exactly one history write, got %d", n)
				}
				assertChunkGone(t, m, "sg_sm")
				assertIntentionNone(t, m, "sg_sm")
				assertMovesTo(t, movesOf(), "sg_sm", "stopped")
			},
		},
		{
			name: "old_waiting_vs_error",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
				Cache.AddSgTask(rpc.Task{GID: "sg_sm", Status: "waiting", TotalLength: "1000", CompletedLength: "500"}, "waiting")
				tracker.EnsureTrackedFromEvent("sg_sm", 1000, "", 1, "waiting")
				seedMeta("sg_sm")
				seedChunk(m, "sg_sm")
			},
			lists: func() (a, w, s []rpc.Task) {
				return nil, []rpc.Task{{GID: "sm", Status: "waiting"}}, nil
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventError, DownloadID: "sm", Err: errors.New("link down")})
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, _ := Cache.GetTask("sg_sm")
				if list != "stopped" || got.Status != "error" || got.CompletedLength != "500" {
					t.Fatalf("stale waiting list overwrote accepted error or lost the checkpoint: %#v %s", got, list)
				}
				if entries := historyEntriesForGID("sg_sm"); len(entries) != 1 || entries[0].Status != "error" {
					t.Fatalf("want exactly one error history write, got %#v", entries)
				}
				assertChunkGone(t, m, "sg_sm")
				assertIntentionNone(t, m, "sg_sm")
				assertMovesTo(t, movesOf(), "sg_sm", "stopped")
			},
		},
		{
			name: "old_stopped_vs_resume",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
				full := fullRowForTest("sg_sm")
				full.Status = "error"
				Cache.AddSgTask(full, "stopped")
				tracker.EnsureTrackedFromEvent(full.GID, 1000, "", 1, "error")
				tracker.MarkCompleteFromEvent(full.GID, "error")
				history.Add(history.HistoryEntry{GID: full.GID, Path: full.Files[0].Path, Status: "error"})
				reader.setLiveStatus("sm", rpc.SurgeLiveStatus{Known: true, Status: "queued"})
			},
			lists: func() (a, w, s []rpc.Task) {
				return nil, nil, []rpc.Task{{GID: "sm", Status: "error"}}
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				m.ConfirmPauseResumeRequest(m.BeginPauseResumeRequest("sg_sm", "resume"))
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, _ := Cache.GetTask("sg_sm")
				if list != "waiting" || got.Status != "waiting" {
					t.Fatalf("stale stopped list overwrote accepted resume: %#v %s", got, list)
				}
				if tracker.LifecycleGeneration("sg_sm") != 1 {
					t.Fatal("stale poll bumped the generation again")
				}
				in, ok := intentionOf(m, "sg_sm")
				if !ok || in.seq == 0 || !in.extended || !in.protectedUntil.After(time.Now()) {
					t.Fatalf("confirmed intention lost its bounded shield: %#v ok=%v", in, ok)
				}
				moves := movesOf()
				assertMovesTo(t, moves, "sg_sm", "waiting")
				fullMove := false
				for _, mv := range moves {
					task, _ := mv.Task.(rpc.Task)
					if mv.GID == "sg_sm" && mv.From == "stopped" && len(task.Files) > 0 {
						fullMove = true
					}
				}
				if !fullMove {
					t.Fatal("resume accept did not emit a full stopped→waiting move")
				}
			},
		},
		{
			name: "poll_queued_vs_late_ack",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
				full := fullRowForTest("sg_sm")
				full.Status = "error"
				Cache.AddSgTask(full, "stopped")
				tracker.EnsureTrackedFromEvent(full.GID, 1000, "", 1, "error")
				tracker.MarkCompleteFromEvent(full.GID, "error")
				history.Add(history.HistoryEntry{GID: full.GID, Path: full.Files[0].Path, Status: "error"})
				reader.setLiveStatus("sm", rpc.SurgeLiveStatus{Known: true, Status: "queued"})
			},
			lists: func() (a, w, s []rpc.Task) {
				// The bulk read captured the queued projection before the ack.
				return nil, []rpc.Task{{GID: "sm", Status: "waiting"}}, nil
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				m.ConfirmPauseResumeRequest(m.BeginPauseResumeRequest("sg_sm", "resume"))
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, _ := Cache.GetTask("sg_sm")
				if list != "waiting" || got.Status != "waiting" {
					t.Fatalf("stale queued row overwrote the late ack: %#v %s", got, list)
				}
				if tracker.LifecycleGeneration("sg_sm") != 1 {
					t.Fatal("stale queued row bumped the generation again")
				}
				in, ok := intentionOf(m, "sg_sm")
				if !ok || in.seq == 0 || !in.extended {
					t.Fatalf("late ack's intention was not renewed once: %#v ok=%v", in, ok)
				}
				assertMovesTo(t, movesOf(), "sg_sm", "waiting")
			},
		},
		{
			name: "uncaptured_mid_read_request",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
			},
			lists: func() (a, w, s []rpc.Task) {
				return nil, []rpc.Task{{GID: "sm", Status: "paused"}}, nil
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				// A request + row appear mid-read for a GID the poll never
				// captured: no observation means no authority to write.
				m.BeginPauseResumeRequest("sg_sm", "resume")
				Cache.AddSgTask(rpc.Task{GID: "sg_sm", Status: "waiting", TotalLength: "9"}, "waiting")
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, ok := Cache.GetTask("sg_sm")
				if !ok || got.TotalLength != "9" || list != "waiting" {
					t.Fatalf("mid-read row must not be touched by the stale round: %#v", got)
				}
				in, ok := intentionOf(m, "sg_sm")
				if !ok || in.seq == 0 || in.extended || !in.protectedUntil.After(time.Now()) {
					t.Fatalf("mid-read request lost or wrongly renewed: %#v ok=%v", in, ok)
				}
				for _, mv := range movesOf() {
					if mv.GID == "sg_sm" && mv.To != "waiting" {
						t.Fatalf("stale round emitted a %q move: %#v", mv.To, mv)
					}
				}
			},
		},
		{
			name: "complete_then_reopen_h1",
			setup: func(m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker) {
				Cache.AddSgTask(rpc.Task{GID: "sg_sm", Status: "active", TotalLength: "100"}, "active")
				tracker.EnsureTrackedFromEvent("sg_sm", 100, "", 1, "active")
				seedMeta("sg_sm")
				seedChunk(m, "sg_sm")
			},
			lists: func() (a, w, s []rpc.Task) {
				return nil, []rpc.Task{{GID: "sm", Status: "waiting"}}, nil
			},
			mid: func(m *Monitor, tracker *TaskTracker) {
				m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventComplete, DownloadID: "sm", Total: 100})
				tracker.RunUnderLifecycle("sg_sm", func() {
					retireHistoryAndReopenToStatusLocked(tracker, "sg_sm", "waiting")
				})
				Cache.AddSgTask(rpc.Task{GID: "sg_sm", Status: "waiting", TotalLength: "100"}, "waiting")
			},
			want: func(t *testing.T, m *Monitor, reader *mockSurgeListReader, tracker *TaskTracker, pusher *Pusher, movesOf func() []events.TaskMove) {
				got, list, _ := Cache.GetTask("sg_sm")
				if list != "waiting" || got.Status != "waiting" {
					t.Fatalf("stale list disturbed the reopened generation: %#v %s", got, list)
				}
				if tracker.LifecycleGeneration("sg_sm") != 1 {
					t.Fatalf("generation = %d, want exactly 1", tracker.LifecycleGeneration("sg_sm"))
				}
				if n := len(historyEntriesForGID("sg_sm")); n != 0 {
					t.Fatalf("reopen must retire the accepted history row, got %d", n)
				}
				assertChunkGone(t, m, "sg_sm")
				assertIntentionNone(t, m, "sg_sm")
				assertMovesTo(t, movesOf(), "sg_sm", "waiting")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, reader, pusher, tracker := newReconcileTestMonitor(t)
			resetCacheSg()
			resetHistoryForTest(t)
			m.chunkSnapshots = NewChunkSnapshotCache()
			var movesMu sync.Mutex
			var moves []events.TaskMove
			m.hub.SubscribeTaskMove(func(mv events.TaskMove) {
				movesMu.Lock()
				moves = append(moves, mv)
				movesMu.Unlock()
			})
			movesOf := func() []events.TaskMove {
				movesMu.Lock()
				defer movesMu.Unlock()
				return append([]events.TaskMove(nil), moves...)
			}
			tc.setup(m, reader, tracker)
			active, waiting, stopped := tc.lists()
			reader.setLists(active, waiting, stopped)

			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releasePoll := sync.OnceFunc(func() { close(release) })
			reader.activeFn = func() ([]rpc.Task, error) {
				close(entered)
				select {
				case <-release:
					return active, nil
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
			tc.mid(m, tracker)
			releasePoll()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("poll stuck")
			}
			tc.want(t, m, reader, tracker, pusher, movesOf)
		})
	}
}

// Orphan/delete matrix: only explicit absent answers remove local state; the
// sweep honors fresh observations, bounded resume shields, durable history,
// and tracker-last removal ordering.
func TestResumeRecovery_OrphanMatrix(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	m.chunkSnapshots = NewChunkSnapshotCache()

	Cache.AddSgTask(rpc.Task{GID: "sg_gone", Status: "active", TotalLength: "10"}, "active")
	tracker.EnsureTrackedFromEvent("sg_gone", 10, "", 1, "active")
	m.chunkSnapshots.Set("sg_gone", []byte{1}, 4, 100, nil)
	reader.setLiveStatus("gone", rpc.SurgeLiveStatus{Absent: true})

	Cache.AddSgTask(rpc.Task{GID: "sg_unknown", Status: "waiting"}, "waiting")
	reader.setLiveStatus("unknown", rpc.SurgeLiveStatus{}) // exists, nil state

	m.chunkSnapshots.Set("sg_chunk_orphan", []byte{1}, 4, 100, nil)
	reader.setLiveStatus("chunk_orphan", rpc.SurgeLiveStatus{Absent: true})

	history.Add(history.HistoryEntry{GID: "sg_hist_orphan", Path: "/d/h.bin", Status: "complete"})
	reader.setLiveStatus("hist_orphan", rpc.SurgeLiveStatus{Absent: true})

	Cache.AddSgTask(rpc.Task{GID: "sg_shield", Status: "paused"}, "waiting")
	tracker.EnsureTrackedFromEvent("sg_shield", 10, "", 1, "paused")
	m.BeginPauseResumeRequest("sg_shield", "resume")
	reader.setLiveStatus("shield", rpc.SurgeLiveStatus{Absent: true})

	reader.setLists(nil, nil, nil)
	m.reconcileSurgeCache()

	if _, _, ok := Cache.GetTask("sg_gone"); ok {
		t.Fatal("explicit-absent row survived")
	}
	if _, ok := m.chunkSnapshots.Get("sg_gone"); ok {
		t.Fatal("chunk snapshot kept for removed gid")
	}
	if _, ok := tracker.GetTrackedTask("sg_gone"); ok {
		t.Fatal("tracker entry kept for removed gid")
	}
	if _, _, ok := Cache.GetTask("sg_unknown"); !ok {
		t.Fatal("unknown (nil-state) row deleted")
	}
	if _, ok := m.chunkSnapshots.Get("sg_chunk_orphan"); ok {
		t.Fatal("orphan snapshot not swept on absent")
	}
	if _, ok := history.Get("sg_hist_orphan"); !ok {
		t.Fatal("durable history removed as garbage")
	}
	if _, list, ok := Cache.GetTask("sg_shield"); !ok || list != "waiting" {
		t.Fatal("in-flight resume did not shield the row")
	}

	// A mid-read Begin on an uncaptured gid must not be deleted either.
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
	m.BeginPauseResumeRequest("sg_midadd", "resume")
	Cache.AddSgTask(rpc.Task{GID: "sg_midadd", Status: "waiting", TotalLength: "9"}, "waiting")
	reader.setLiveStatus("midadd", rpc.SurgeLiveStatus{Absent: true})
	releasePoll()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poll stuck")
	}
	if _, _, ok := Cache.GetTask("sg_midadd"); !ok {
		t.Fatal("mid-read add deleted by the stale round")
	}
}

// The fact shield is bounded: an in-window resume discards a stale pause and
// blocks absence deletion; after expiry the same pause applies and a true
// absence deletes. Tickets (seq/stamp) outlive the fact TTL — a long RPC is
// never invalidated by the deadline, and old cleanups cannot clear a newer
// Begin.
func TestResumeRecovery_BoundedResumeProtection(t *testing.T) {
	m, reader, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)

	Cache.AddSgTask(rpc.Task{GID: "sg_bound", Status: "active", TotalLength: "100"}, "active")
	tracker.EnsureTrackedFromEvent("sg_bound", 100, "", 1, "active")
	m.BeginPauseResumeRequest("sg_bound", "resume")

	forceExpiry := func(gid string) {
		m.pauseResumeVersionMu.Lock()
		defer m.pauseResumeVersionMu.Unlock()
		if rec, ok := m.pauseResumeIntentions[gid]; ok {
			rec.protectedUntil = time.Now().Add(-time.Second)
			m.pauseResumeIntentions[gid] = rec
		}
	}

	// In-window: a stale paused list row is discarded while resume is armed.
	reader.setLiveStatus("bound", rpc.SurgeLiveStatus{Known: true, Status: "paused"})
	reader.setLists(nil, []rpc.Task{{GID: "bound", Status: "paused"}}, nil)
	m.reconcileSurgeCache()
	if _, list, _ := Cache.GetTask("sg_bound"); list != "active" {
		t.Fatal("in-window stale pause not discarded")
	}

	// Expiry restores normal fact handling: the same paused row now applies.
	forceExpiry("sg_bound")
	m.reconcileSurgeCache()
	if _, list, _ := Cache.GetTask("sg_bound"); list != "waiting" {
		t.Fatal("expired shield still discarded the pause")
	}

	// ...and an expired shield cannot pin a row against true absence.
	reader.setLiveStatus("bound", rpc.SurgeLiveStatus{Absent: true})
	reader.setLists(nil, nil, nil)
	m.reconcileSurgeCache()
	if _, _, ok := Cache.GetTask("sg_bound"); ok {
		t.Fatal("expired resume intention pinned an absent row")
	}

	// Long-RPC ticket freshness is seq/stamp based — fact TTL never kills it.
	ticket := m.BeginPauseResumeRequest("sg_rpc", "pause")
	forceExpiry("sg_rpc")
	ran := false
	if !m.RunIfPauseResumeRequestCurrent(ticket, func() { ran = true }) || !ran {
		t.Fatal("opaque ticket died with the fact TTL")
	}

	// Duplicate ack does not renew the protection window: the first
	// confirmation consumes the one-shot extension, the second early-returns.
	full := fullRowForTest("sg_dup")
	full.Status = "error"
	Cache.AddSgTask(full, "stopped")
	tracker.EnsureTrackedFromEvent(full.GID, 1000, "", 1, "error")
	tracker.MarkCompleteFromEvent(full.GID, "error")
	history.Add(history.HistoryEntry{GID: full.GID, Path: full.Files[0].Path, Status: "error"})
	dupTicket := m.BeginPauseResumeRequest("sg_dup", "resume")
	m.ConfirmPauseResumeRequest(dupTicket)
	m.pauseResumeVersionMu.RLock()
	first := m.pauseResumeIntentions["sg_dup"].protectedUntil
	m.pauseResumeVersionMu.RUnlock()
	m.ConfirmPauseResumeRequest(dupTicket)
	m.pauseResumeVersionMu.RLock()
	second := m.pauseResumeIntentions["sg_dup"].protectedUntil
	m.pauseResumeVersionMu.RUnlock()
	if !second.Equal(first) {
		t.Fatal("duplicate ack renewed the fact-shield deadline")
	}

	// A cleanup carrying an old ticket cannot clear a newer Begin.
	oldTicket := m.BeginPauseResumeRequest("sg_seq", "resume")
	newTicket := m.BeginPauseResumeRequest("sg_seq", "pause")
	if m.ClearPauseResumeIntentionIfCurrent(oldTicket) {
		t.Fatal("stale ticket cleared a newer Begin")
	}
	m.pauseResumeVersionMu.RLock()
	cur := m.pauseResumeIntentions["sg_seq"]
	m.pauseResumeVersionMu.RUnlock()
	if cur.seq != newTicket.seq || cur.action != PauseResumeIntentionPause {
		t.Fatal("newer Begin lost to old cleanup")
	}
}

// A stale Started racing an accepted live-paused row must not activate: the
// refusal is side-effect free (no activation stamp, no master mirror write,
// no history write, no emitted payload). A live-downloading Started then
// activates once and is idempotent.
func TestResumeRecovery_StartedAgainstLivePaused(t *testing.T) {
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	m.surgeEng = &rpc.SurgeEngine{}

	full := fullRowForTest("sg_stale_start")
	full.Status = "paused"
	Cache.AddSgTask(full, "waiting")
	tracker.EnsureTrackedFromEvent(full.GID, 1000, "", 1, "paused")
	reader.setLiveStatus("stale_start", rpc.SurgeLiveStatus{Known: true, Status: "paused"})

	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventStarted, DownloadID: "stale_start"})

	got, list, _ := Cache.GetTask(full.GID)
	assertFullRowMetadata(t, got, full)
	if list != "waiting" || got.Status != "paused" {
		t.Fatalf("live-paused Started mutated the row: %#v %s", got, list)
	}
	tracked, trackedOK := tracker.GetTrackedTask(full.GID)
	if !trackedOK || tracked.Status != "paused" || !tracked.ActivatedAt.IsZero() {
		t.Fatalf("stale Started activated a paused row: %#v", tracked)
	}
	if _, ok := m.surgeEng.GetMasterCacheEntry("stale_start"); ok {
		t.Fatal("refused Started wrote the master mirror")
	}
	if n := len(historyEntriesForGID(full.GID)); n != 0 {
		t.Fatalf("refused Started wrote history (%d rows)", n)
	}
	if len(pusher.pendingSnapshot()) != 0 {
		t.Fatal("refused Started pushed deltas")
	}

	// Live-downloading Started is real evidence: activates once, idempotent.
	reader.setLiveStatus("stale_start", rpc.SurgeLiveStatus{Known: true, Status: "downloading"})
	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventStarted, DownloadID: "stale_start"})
	got, list, _ = Cache.GetTask(full.GID)
	tracked, _ = tracker.GetTrackedTask(full.GID)
	if list != "active" || got.Status != "active" || tracked.ActivatedAt.IsZero() {
		t.Fatalf("real Started did not activate: %#v %s", got, list)
	}
	firstAt := tracked.ActivatedAt
	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventStarted, DownloadID: "stale_start"})
	tracked, _ = tracker.GetTrackedTask(full.GID)
	if !tracked.ActivatedAt.Equal(firstAt) {
		t.Fatal("duplicate Started rewrote activation")
	}
}

// Real scheduler/pool: after a confirmed resume, Resumed/Queued events lost —
// a poll round restores the row into waiting from real queued evidence
// (single lifecycle reopen, H0 retire, group re-home, zero activation). A
// master/history row that never saw a real Add is never admitted.
func TestReconcileSurgeCache_RealPoolLostEvents(t *testing.T) {
	testutil.SetupStateDB(t)
	m, _, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	setupTaskGroupStoreTest(t)

	full := fullRowForTest("sg_lost")
	full.Files[0].Uris[0].Status = "used"
	Cache.AddSgTask(full, "stopped")
	tracker.EnsureTrackedFromEvent(full.GID, 1000, full.Files[0].Uris[0].Uri, 4, "error")
	tracker.MarkCompleteFromEvent(full.GID, "error")
	history.Add(history.HistoryEntry{GID: full.GID, Path: full.Files[0].Path, Status: "error", DownloadGroup: full.DownloadGroup})
	testutil.SeedMasterList(t, surgeEvents.DownloadRecord{
		ID: "lost", URL: full.Files[0].Uris[0].Uri, DestPath: full.Files[0].Path,
		Filename: full.Files[0].Path, Status: "error", TotalSize: 1000, Downloaded: 500,
	})
	// A mirror row that never reached the real pool stays a stopped seed.
	testutil.SeedMasterList(t, surgeEvents.DownloadRecord{ID: "noadd", Status: "queued"})
	history.Add(history.HistoryEntry{GID: "sg_noadd", Path: "/d/n.bin", Status: "error"})
	// A configured-but-nil-state pool member stays history-only forever.
	history.Add(history.HistoryEntry{GID: "sg_nilst", Path: "/d/nil.bin", Status: "error"})
	testutil.SeedMasterList(t, surgeEvents.DownloadRecord{ID: "nilst", Status: "paused"})

	pool := scheduler.NewSchedulerForTesting(map[string]surgeEvents.DownloadRecord{"nilst": {ID: "nilst"}})
	se := rpc.NewSurgeEngineForTesting(pool)
	t.Cleanup(se.Close)
	m.surgeEng, m.surgePollReader = se, se
	m.engine = rpc.NewHybridEngine(nil, se)

	if err := se.Resume("lost"); err != nil {
		t.Fatalf("real pool resume: %v", err)
	}
	if live := se.ReadLiveStatus("lost"); !live.Known || live.Status != "queued" {
		t.Fatalf("real pool does not report queued: %#v", live)
	}
	// No ConfirmPauseResumeRequest and no queued/resumed events reach the
	// monitor: the poll alone must recover the confirmed resume.
	for range 2 {
		m.reconcileSurgeCache()
	}
	got, list, _ := Cache.GetTask(full.GID)
	assertFullRowMetadata(t, got, full)
	tracked, _ := tracker.GetTrackedTask(full.GID)
	if list != "waiting" || got.Status != "waiting" || !tracked.ActivatedAt.IsZero() ||
		tracker.LifecycleGeneration(full.GID) != 1 {
		t.Fatalf("poll did not restore the confirmed resume once: %#v gen=%d", got, tracker.LifecycleGeneration(full.GID))
	}
	if _, ok := history.Get(full.GID); ok {
		t.Fatal("H0 not retired by the real-pool restore")
	}
	if group := GetStoredTaskGroup(full.GID); group == nil || group.ID != full.DownloadGroup.ID {
		t.Fatal("group not re-homed by the real-pool restore")
	}
	got3, list3, ok3 := Cache.GetTask("sg_noadd")
	if !ok3 || list3 != "stopped" {
		t.Fatalf("no-Add master row must stay a stopped seed: %#v %s", got3, list3)
	}
	if _, _, ok := Cache.GetTask("sg_nilst"); ok {
		t.Fatal("nil-state pool member admitted")
	}
	if _, ok := history.Get("sg_nilst"); !ok {
		t.Fatal("nil-state pool member lost its history")
	}
}

// Real worker + local HTTP: one blocking body occupies the only worker while
// a second download queues behind it. The Queued/Started/Complete events are
// captured but never fed to the monitor — the poll alone restores waiting,
// repairs the lost Started into active (idempotent), and the worker's own
// EventComplete (the only event delivered) drives the real terminal path.
// A late Resumed cannot downgrade the finished row.
func TestReconcileSurgeCache_RealWorkerLostStarted(t *testing.T) {
	testutil.SetupStateDB(t)
	m, _, _, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	setupTaskGroupStoreTest(t)

	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	var onceA, onceB sync.Once
	t.Cleanup(func() {
		onceA.Do(func() { close(releaseA) })
		onceB.Do(func() { close(releaseB) })
	})
	body := strings.Repeat("x", 4096)
	server := testutil.NewHTTPServerT(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gate := releaseB
		if strings.HasSuffix(r.URL.Path, "a.bin") {
			gate = releaseA
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	progressCh := make(chan surgeEvents.DownloadEvent, 32)
	var evMu sync.Mutex
	evs := map[surgeEvents.EventType][]surgeEvents.DownloadEvent{}
	drainStop, drainDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case ev := <-progressCh:
				evMu.Lock()
				evs[ev.Type] = append(evs[ev.Type], ev)
				evMu.Unlock()
			case <-drainStop:
				return
			}
		}
	}()
	t.Cleanup(func() { close(drainStop); <-drainDone })
	findEvent := func(typ surgeEvents.EventType, id string) (surgeEvents.DownloadEvent, bool) {
		evMu.Lock()
		defer evMu.Unlock()
		for _, ev := range evs[typ] {
			if ev.DownloadID == id {
				return ev, true
			}
		}
		return surgeEvents.DownloadEvent{}, false
	}

	pool := scheduler.New(progressCh, 1)
	t.Cleanup(pool.GracefulShutdown)
	se := rpc.NewSurgeEngineForTesting(pool)
	t.Cleanup(se.Close)
	m.surgeEng, m.surgePollReader = se, se
	m.engine = rpc.NewHybridEngine(nil, se)

	tmpDir := t.TempDir()
	addDownload := func(id, name string) {
		destPath := filepath.Join(tmpDir, name)
		// The worker owns the .surge working file; reserve it like Enqueue does.
		if err := os.WriteFile(destPath+surgeEvents.IncompleteSuffix, nil, 0o644); err != nil {
			t.Fatalf("reserve %s: %v", name, err)
		}
		if !pool.Add(surgeEvents.DownloadRecord{
			ID: id, URL: server.URL + "/" + name,
			OutputPath: tmpDir, Filename: name, DestPath: destPath,
			ProgressState: progress.New(id, 0),
			Runtime:       &surgeEvents.RuntimeConfig{SequentialDownload: true},
		}) {
			t.Fatalf("pool.Add %s refused", id)
		}
	}
	addDownload("busy", "a.bin")
	addDownload("late", "b.bin")

	requireCondition(t, func() bool {
		st := pool.GetStatus("busy")
		return st != nil && st.Status == "downloading"
	}, 5*time.Second, "first download never occupied the single worker")
	if st := pool.GetStatus("late"); st == nil || st.Status != "queued" {
		t.Fatalf("second download not queued behind the busy worker: %#v", st)
	}

	// Round 1 — every lifecycle event so far is lost. The poll alone restores
	// the queued row to waiting (no activation) and the running row to active.
	m.reconcileSurgeCache()
	gotB, listB, okB := Cache.GetTask("sg_late")
	trackedB, _ := tracker.GetTrackedTask("sg_late")
	if !okB || listB != "waiting" || gotB.Status != "waiting" || !trackedB.ActivatedAt.IsZero() {
		t.Fatalf("queued row not restored to waiting: %#v %s", gotB, listB)
	}
	gotA, listA, _ := Cache.GetTask("sg_busy")
	trackedA, _ := tracker.GetTrackedTask("sg_busy")
	if listA != "active" || gotA.Status != "active" || trackedA.ActivatedAt.IsZero() {
		t.Fatalf("real running row not restored active: %#v %s", gotA, listA)
	}

	// Release the first body: it completes, the worker claims the second and
	// emits a real EventStarted — captured but never fed to the monitor.
	onceA.Do(func() { close(releaseA) })
	requireCondition(t, func() bool {
		_, ok := findEvent(surgeEvents.EventStarted, "late")
		return ok
	}, 10*time.Second, "worker never emitted a real EventStarted for the second download")
	requireCondition(t, func() bool {
		st := pool.GetStatus("late")
		return st != nil && st.Status == "downloading"
	}, 5*time.Second, "second download never reached the running state")

	// Round 2 — body still blocked; the real running fact repairs the lost
	// Started into active exactly once.
	m.reconcileSurgeCache()
	gotB, listB, _ = Cache.GetTask("sg_late")
	trackedB, _ = tracker.GetTrackedTask("sg_late")
	if listB != "active" || gotB.Status != "active" || trackedB.ActivatedAt.IsZero() {
		t.Fatalf("poll did not repair the lost Started: %#v %s", gotB, listB)
	}
	firstActivation := trackedB.ActivatedAt
	m.reconcileSurgeCache()
	trackedB, _ = tracker.GetTrackedTask("sg_late")
	if !trackedB.ActivatedAt.Equal(firstActivation) {
		t.Fatal("repeat poll renewed the activation")
	}

	// File metadata lets the terminal path write history for the completed row.
	bDestPath := filepath.Join(tmpDir, "b.bin")
	Cache.SetMetadataForTest("sg_late", &TaskMetadata{GID: "sg_late", Files: []string{bDestPath}, Dir: tmpDir})

	// Release the second body; the worker emits a real EventComplete — the one
	// real event delivered to the monitor drives the full terminal pipeline.
	onceB.Do(func() { close(releaseB) })
	requireCondition(t, func() bool {
		_, ok := findEvent(surgeEvents.EventComplete, "late")
		return ok
	}, 10*time.Second, "worker never completed the second download")
	complete, _ := findEvent(surgeEvents.EventComplete, "late")
	m.handleSurgeEvent(complete)
	gotB, listB, _ = Cache.GetTask("sg_late")
	if listB != "stopped" || gotB.Status != "complete" {
		t.Fatalf("real complete did not land in stopped: %#v %s", gotB, listB)
	}
	if entries := historyEntriesForGID("sg_late"); len(entries) != 1 {
		t.Fatalf("real complete wrote %d history rows, want 1", len(entries))
	}
	m.handleSurgeEvent(surgeEvents.DownloadEvent{Type: surgeEvents.EventResumed, DownloadID: "late"})
	gotB, listB, _ = Cache.GetTask("sg_late")
	if listB != "stopped" || gotB.Status != "complete" {
		t.Fatalf("late Resumed downgraded the terminal row: %#v %s", gotB, listB)
	}
}
