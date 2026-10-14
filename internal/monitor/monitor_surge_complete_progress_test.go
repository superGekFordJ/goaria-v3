package monitor

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/testutil"
	surgeEvents "goaria-v3/internal/surge/types"
)

// findSgStoppedTask returns the stopped task with the given GID, or nil.
func findSgStoppedTask(gid string) *rpc.Task {
	Cache.sgMu.RLock()
	defer Cache.sgMu.RUnlock()
	for i := range Cache.sgStopped {
		if Cache.sgStopped[i].GID == gid {
			return &Cache.sgStopped[i]
		}
	}
	return nil
}

// findCompleteDelta searches a drained pending snapshot for a complete delta
// with the given GID and returns the local copy element, or nil.
func findCompleteDelta(pusher *Pusher, gid string) *events.TaskDelta {
	pending := pusher.pendingSnapshot()
	for i := range pending {
		if pending[i].Type == "complete" && pending[i].GID == gid {
			return &pending[i]
		}
	}
	return nil
}

// TestComplete_SyncsProgressToFull verifies that a complete event syncs the
// cached CompletedLength to the full total. Before the fix, MoveTaskToStopped
// preserved the stale CompletedLength (e.g. "980" instead of "1000").
func TestComplete_SyncsProgressToFull(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		forceTickChan: make(chan struct{}, 1),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{
		GID:             "sg_big",
		Status:          "active",
		CompletedLength: "980",
		TotalLength:     "1000",
		DownloadSpeed:   "50",
	}}
	defer resetCacheSg()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "big",
		Total:      1000,
	})

	task := findSgStoppedTask("sg_big")
	if task == nil {
		t.Fatal("expected sg_big in stopped after complete event")
	}
	if task.CompletedLength != "1000" {
		t.Errorf("CompletedLength = %q, want 1000", task.CompletedLength)
	}
	if task.TotalLength != "1000" {
		t.Errorf("TotalLength = %q, want 1000", task.TotalLength)
	}
}

// TestComplete_SmallFileShowsFull verifies that a small file which never
// received a ProgressMsg gets CompletedLength synced to full on complete.
// Before the fix, CompletedLength stayed "0" (never patched).
func TestComplete_SmallFileShowsFull(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		forceTickChan: make(chan struct{}, 1),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{
		GID:             "sg_small",
		Status:          "active",
		CompletedLength: "0",
		TotalLength:     "500",
		DownloadSpeed:   "0",
	}}
	defer resetCacheSg()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "small",
		Total:      500,
	})

	task := findSgStoppedTask("sg_small")
	if task == nil {
		t.Fatal("expected sg_small in stopped after complete event")
	}
	if task.CompletedLength != "500" {
		t.Errorf("CompletedLength = %q, want 500", task.CompletedLength)
	}
}

// TestComplete_PayloadCarriesProgress verifies that the complete delta
// pushed to the frontend includes a payload with completedLength/totalLength.
// Before the fix, the complete delta had no payload, so the frontend kept
// the stale completedLength.
func TestComplete_PayloadCarriesProgress(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		forceTickChan: make(chan struct{}, 1),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{
		GID:             "sg_payload",
		Status:          "active",
		CompletedLength: "900",
		TotalLength:     "1000",
	}}
	defer resetCacheSg()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "payload",
		Total:      1000,
	})

	delta := findCompleteDelta(pusher, "sg_payload")
	if delta == nil {
		t.Fatal("expected complete delta in pusher pending queue")
	}
	payload, ok := delta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", delta.Payload)
	}
	if payload["completedLength"] != "1000" {
		t.Errorf("payload completedLength = %v, want 1000", payload["completedLength"])
	}
	if payload["totalLength"] != "1000" {
		t.Errorf("payload totalLength = %v, want 1000", payload["totalLength"])
	}
	if payload["downloadSpeed"] != "0" {
		t.Errorf("payload downloadSpeed = %v, want 0", payload["downloadSpeed"])
	}
}

// TestReconcile_CrossListStoppedSyncsProgress verifies that the reconcile
// cross-list stopped branch syncs CompletedLength from engine data before
// moving to stopped. Before the fix, the stale CompletedLength was preserved.
func TestReconcile_CrossListStoppedSyncsProgress(t *testing.T) {
	m, reader, _, _ := newReconcileTestMonitor(t)
	resetCacheSg()

	Cache.AddSgTask(rpc.Task{
		GID:             "sg_recon",
		Status:          "active",
		CompletedLength: "800",
		TotalLength:     "1000",
	}, "active")

	reader.setLists(nil, nil, []rpc.Task{{
		GID:             "recon",
		Status:          "complete",
		CompletedLength: "1000",
		TotalLength:     "1000",
	}})

	m.reconcileSurgeCache()

	task := findSgStoppedTask("sg_recon")
	if task == nil {
		t.Fatal("expected sg_recon in stopped after reconcile")
	}
	if task.CompletedLength != "1000" {
		t.Errorf("CompletedLength = %q, want 1000", task.CompletedLength)
	}
	if task.TotalLength != "1000" {
		t.Errorf("TotalLength = %q, want 1000", task.TotalLength)
	}
}

func TestCanonicalCompleteTotal_PriorityMatrix(t *testing.T) {
	// 1. Positive event total
	if got := canonicalCompleteTotal(1000, &rpc.Task{TotalLength: "2000", CompletedLength: "2000"}); got != 1000 {
		t.Errorf("canonicalCompleteTotal positive event total = %d, want 1000", got)
	}

	// 2. Positive cached task total
	if got := canonicalCompleteTotal(0, &rpc.Task{TotalLength: "2000", CompletedLength: "500"}); got != 2000 {
		t.Errorf("canonicalCompleteTotal positive cached total = %d, want 2000", got)
	}

	// 3. 0 for non-positive / missing evidence
	if got := canonicalCompleteTotal(0, &rpc.Task{TotalLength: "0", CompletedLength: "750"}); got != 0 {
		t.Errorf("canonicalCompleteTotal zero total = %d, want 0", got)
	}
	if got := canonicalCompleteTotal(0, &rpc.Task{TotalLength: "invalid", CompletedLength: "bad"}); got != 0 {
		t.Errorf("canonicalCompleteTotal invalid strings = %d, want 0", got)
	}
	if got := canonicalCompleteTotal(0, nil); got != 0 {
		t.Errorf("canonicalCompleteTotal nil cached = %d, want 0", got)
	}
}

func TestComplete_ZeroTotalWithCachedTotal_PrefersCachedTotal(t *testing.T) {
	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		forceTickChan: make(chan struct{}, 1),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	Cache.sgActive = []rpc.Task{{
		GID:             "sg_cached_total_pref",
		Status:          "active",
		CompletedLength: "800",
		TotalLength:     "1000",
		DownloadSpeed:   "50",
	}}
	defer resetCacheSg()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "cached_total_pref",
		Total:      0,
	})

	task := findSgStoppedTask("sg_cached_total_pref")
	if task == nil {
		t.Fatal("expected sg_cached_total_pref in stopped after complete event")
	}
	if task.CompletedLength != "1000" {
		t.Errorf("CompletedLength = %q, want 1000", task.CompletedLength)
	}
	if task.TotalLength != "1000" {
		t.Errorf("TotalLength = %q, want 1000", task.TotalLength)
	}
}

func TestComplete_UnknownLengthFastComplete_FullPipeline_TrackerHistoryMasterNN(t *testing.T) {
	tmpDir := t.TempDir()
	historyFile := filepath.Join(tmpDir, "history.json")
	history.SetHistoryPath(historyFile)
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	surgeEng := rpc.NewSurgeEngineForTesting(nil)
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		tracker:       tracker,
		surgeEng:      surgeEng,
		forceTickChan: make(chan struct{}, 1),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	gid := "sg_fast_chunked"
	filePath := filepath.Join(tmpDir, "archive.zip")

	// 1. Task starts as unknown size 0/0
	taskActive := rpc.Task{
		GID:             gid,
		Status:          "active",
		CompletedLength: "0",
		TotalLength:     "0",
		DownloadSpeed:   "0",
		Dir:             tmpDir,
		Files:           []rpc.File{{Path: filePath}},
	}
	Cache.sgActive = []rpc.Task{taskActive}
	defer resetCacheSg()

	tracker.EnsureTrackedFromEvent(gid, 0, "https://example.com/archive.zip", 1, "active")
	tracker.mu.Lock()
	if tr := tracker.tasks[gid]; tr != nil {
		tr.FilePath = filePath
		tr.Dir = tmpDir
	}
	tracker.mu.Unlock()

	// 2. Fast complete arrives with written total = 7520000, 0 interim progress ticks
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "fast_chunked",
		Total:      7520000,
		Downloaded: 7520000,
		Elapsed:    2 * time.Second,
	})

	// Assert Tracker
	tracker.mu.RLock()
	trackerTask := tracker.tasks[gid]
	if trackerTask == nil {
		tracker.mu.RUnlock()
		t.Fatal("expected tracker task exists")
	}
	if trackerTask.TotalLength != 7520000 {
		t.Errorf("tracker TotalLength = %d, want 7520000", trackerTask.TotalLength)
	}
	if trackerTask.CompletedLength != 7520000 {
		t.Errorf("tracker CompletedLength = %d, want 7520000", trackerTask.CompletedLength)
	}
	tracker.mu.RUnlock()

	// Assert History
	entry, ok := history.Get(gid)
	if !ok {
		t.Fatal("expected history entry exists")
	}
	if entry.TotalLength != "7520000" {
		t.Errorf("history TotalLength = %q, want 7520000", entry.TotalLength)
	}
	if entry.CompletedLength != "7520000" {
		t.Errorf("history CompletedLength = %q, want 7520000", entry.CompletedLength)
	}

	// Assert Master Cache
	masterEntry, ok := surgeEng.GetMasterCacheEntry("fast_chunked")
	if !ok {
		t.Fatal("expected master cache entry exists")
	}
	if masterEntry.TotalSize != 7520000 {
		t.Errorf("master TotalSize = %d, want 7520000", masterEntry.TotalSize)
	}
	if masterEntry.Downloaded != 7520000 {
		t.Errorf("master Downloaded = %d, want 7520000", masterEntry.Downloaded)
	}

	// Assert Cache Stopped
	task := findSgStoppedTask(gid)
	if task == nil {
		t.Fatal("expected task in stopped cache")
	}
	if task.TotalLength != "7520000" || task.CompletedLength != "7520000" {
		t.Errorf("cache stopped = (%q, %q), want (7520000, 7520000)", task.TotalLength, task.CompletedLength)
	}

	// Assert Delta Pusher
	delta := findCompleteDelta(pusher, gid)
	if delta == nil {
		t.Fatal("expected complete delta in pusher queue")
	}
	payload, ok := delta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string delta payload, got %T", delta.Payload)
	}
	if payload["totalLength"] != "7520000" || payload["completedLength"] != "7520000" {
		t.Errorf("delta payload = (%v, %v), want (7520000, 7520000)", payload["totalLength"], payload["completedLength"])
	}
}

// Waiting source: a resume-acknowledged waiting row never passed through
// active/progress, yet the same unknown-length fast-complete must run the
// full tracker/history/master/payload pipeline.
func TestComplete_UnknownLengthFastComplete_WaitingSource(t *testing.T) {
	tmpDir := t.TempDir()
	historyFile := filepath.Join(tmpDir, "history.json")
	history.SetHistoryPath(historyFile)
	history.DisableSaveForTest()
	history.Clear()
	defer history.Clear()

	hub := events.NewHub(nil)
	pusher := NewPusher(hub)
	tracker := NewTaskTracker()
	surgeEng := rpc.NewSurgeEngineForTesting(nil)
	m := &Monitor{
		hub:           hub,
		pusher:        pusher,
		tracker:       tracker,
		surgeEng:      surgeEng,
		forceTickChan: make(chan struct{}, 1),
	}

	prevWindow := State.HasWindow()
	State.SetWindowExists(true)
	defer State.SetWindowExists(prevWindow)

	gid := "sg_fast_waiting"
	filePath := filepath.Join(tmpDir, "archive.zip")

	// Resume ack lands in waiting; no active snapshot or progress ever arrives.
	taskWaiting := rpc.Task{
		GID:             gid,
		Status:          "paused",
		CompletedLength: "0",
		TotalLength:     "0",
		DownloadSpeed:   "0",
		Dir:             tmpDir,
		Files:           []rpc.File{{Path: filePath}},
	}
	Cache.sgWaiting = []rpc.Task{taskWaiting}
	defer resetCacheSg()

	tracker.EnsureTrackedFromEvent(gid, 0, "https://example.com/archive.zip", 1, "paused")
	tracker.mu.Lock()
	if tr := tracker.tasks[gid]; tr != nil {
		tr.FilePath = filePath
		tr.Dir = tmpDir
	}
	tracker.mu.Unlock()

	m.ConfirmPauseResumeRequest(m.BeginPauseResumeRequest(gid, "resume"))
	if task, list, _ := Cache.GetTask(gid); list != "waiting" || task.Status != "waiting" {
		t.Fatalf("resume ack did not land in waiting: %#v %s", task, list)
	}

	// Fast complete arrives straight onto the waiting row.
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "fast_waiting",
		Total:      7520000,
		Downloaded: 7520000,
		Elapsed:    2 * time.Second,
	})

	trackerTask, _ := tracker.GetTrackedTask(gid)
	if trackerTask.TotalLength != 7520000 || trackerTask.CompletedLength != 7520000 {
		t.Errorf("tracker = (%d, %d), want (7520000, 7520000)", trackerTask.TotalLength, trackerTask.CompletedLength)
	}
	entry, ok := history.Get(gid)
	if !ok || entry.TotalLength != "7520000" || entry.CompletedLength != "7520000" {
		t.Fatalf("history = %#v", entry)
	}
	masterEntry, ok := surgeEng.GetMasterCacheEntry("fast_waiting")
	if !ok || masterEntry.TotalSize != 7520000 || masterEntry.Downloaded != 7520000 {
		t.Fatalf("master = %#v ok=%v", masterEntry, ok)
	}
	task := findSgStoppedTask(gid)
	if task == nil || task.TotalLength != "7520000" || task.CompletedLength != "7520000" || task.DownloadSpeed != "0" {
		t.Fatalf("cache stopped = %#v", task)
	}
	delta := findCompleteDelta(pusher, gid)
	if delta == nil {
		t.Fatal("expected complete delta in pusher queue")
	}
	payload, ok := delta.Payload.(map[string]string)
	if !ok || payload["totalLength"] != "7520000" || payload["completedLength"] != "7520000" {
		t.Fatalf("delta payload = %#v", delta.Payload)
	}
}

// A resume-acknowledged waiting row may finish without ever reaching active
// or emitting progress. Both terminal paths must still commit the canonical
// total (a zero measurement falls back to the positive checkpoint), keep the
// full multi-File/Uri/Title/group payload, and land exactly one tracker /
// history / master / delta terminal — errors keep their checkpoint and
// duplicate terminals never rewrite history.
func TestResumeTerminal_WaitingFastComplete(t *testing.T) {
	testutil.SetupStateDB(t)
	m, reader, pusher, tracker := newReconcileTestMonitor(t)
	resetCacheSg()
	resetHistoryForTest(t)
	m.surgeEng = rpc.NewSurgeEngineForTesting(nil)

	seedWaitingRow := func(gid string) rpc.Task {
		full := fullRowForTest(gid)
		full.Status = "paused"
		full.ErrorCode, full.ErrorMessage = "", ""
		full.DownloadSpeed = "0"
		Cache.AddSgTask(full, "waiting")
		tracker.EnsureTrackedFromEvent(gid, 1000, full.Files[0].Uris[0].Uri, 2, "paused")
		Cache.SetMetadataForTest(gid, &TaskMetadata{GID: gid, Files: []string{full.Files[0].Path}, Dir: full.Dir})
		m.ConfirmPauseResumeRequest(m.BeginPauseResumeRequest(gid, "resume"))
		task, list, _ := Cache.GetTask(gid)
		if list != "waiting" || task.Status != "waiting" {
			t.Fatalf("resume ack did not land in waiting: %#v %s", task, list)
		}
		return full
	}

	// --- Event path: unknown-length fast-complete straight onto waiting ---
	eventFull := seedWaitingRow("sg_wf_event")
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventComplete, DownloadID: "wf_event",
		Total: 0, Elapsed: time.Second,
	})
	task, list, _ := Cache.GetTask("sg_wf_event")
	if list != "stopped" || task.Status != "complete" ||
		task.TotalLength != "1000" || task.CompletedLength != "1000" || task.DownloadSpeed != "0" {
		t.Fatalf("waiting fast-complete did not commit canonical total: %#v %s", task, list)
	}
	assertFullRowMetadata(t, task, eventFull)
	tracked, _ := tracker.GetTrackedTask("sg_wf_event")
	if !tracker.TerminalAcceptedInCurrentGeneration("sg_wf_event") || tracked.Status != "complete" {
		t.Fatal("terminal not accepted on the tracker")
	}
	if entries := historyEntriesForGID("sg_wf_event"); len(entries) != 1 || entries[0].Status != "complete" {
		t.Fatalf("want one complete history row, got %#v", entries)
	}
	if master, ok := m.surgeEng.GetMasterCacheEntry("wf_event"); !ok || master.Status != "completed" {
		t.Fatalf("master mirror not committed: %#v ok=%v", master, ok)
	}
	// Duplicate terminal events are accepted-dedup'd: no second history write.
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventComplete, DownloadID: "wf_event", Total: 0,
	})
	if entries := historyEntriesForGID("sg_wf_event"); len(entries) != 1 {
		t.Fatalf("duplicate terminal rewrote history: %#v", entries)
	}

	// --- Poll path: the same waiting row completes via a missed-event poll ---
	pollFull := seedWaitingRow("sg_wf_poll")
	reader.setLists(nil, nil, []rpc.Task{{
		GID: "wf_poll", Status: "complete",
		TotalLength: "1000", CompletedLength: "1000",
	}})
	m.reconcileSurgeCache()
	task, list, _ = Cache.GetTask("sg_wf_poll")
	if list != "stopped" || task.Status != "complete" ||
		task.TotalLength != "1000" || task.CompletedLength != "1000" || task.DownloadSpeed != "0" {
		t.Fatalf("poll terminal lost canonical total: %#v %s", task, list)
	}
	assertFullRowMetadata(t, task, pollFull)
	if entries := historyEntriesForGID("sg_wf_poll"); len(entries) != 1 || entries[0].Status != "complete" {
		t.Fatalf("poll terminal wrote %#v", entries)
	}
	// The poll emits the accepted full row as the delta payload.
	foundPollDelta := false
	for _, d := range pusher.pendingSnapshot() {
		if d.GID == "sg_wf_poll" && d.Type == "complete" {
			foundPollDelta = true
			payload, ok := d.Payload.(rpc.Task)
			if !ok || payload.TotalLength != "1000" || len(payload.Files) != len(pollFull.Files) || payload.Title != pollFull.Title {
				t.Fatalf("poll complete payload lost the full row: %#v", d.Payload)
			}
		}
	}
	if !foundPollDelta {
		t.Fatal("poll terminal emitted no complete delta")
	}

	// --- Error keeps the checkpoint instead of synthesizing a total ---
	errFull := fullRowForTest("sg_wf_err")
	errFull.Status = "paused"
	errFull.ErrorCode, errFull.ErrorMessage = "", ""
	errFull.DownloadSpeed = "0"
	Cache.AddSgTask(errFull, "waiting")
	tracker.EnsureTrackedFromEvent("sg_wf_err", 1000, errFull.Files[0].Uris[0].Uri, 2, "paused")
	Cache.SetMetadataForTest("sg_wf_err", &TaskMetadata{GID: "sg_wf_err", Files: []string{errFull.Files[0].Path}, Dir: errFull.Dir})
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventError, DownloadID: "wf_err",
		Err: errors.New("connection reset"),
	})
	task, list, _ = Cache.GetTask("sg_wf_err")
	if list != "stopped" || task.Status != "error" ||
		task.TotalLength != "1000" || task.CompletedLength != "500" || task.ErrorCode == "" || task.ErrorMessage == "" {
		t.Fatalf("error terminal dropped the checkpoint: %#v %s", task, list)
	}
	assertFullRowMetadata(t, task, errFull)
	if entries := historyEntriesForGID("sg_wf_err"); len(entries) != 1 || entries[0].Status != "error" {
		t.Fatalf("error terminal history = %#v", entries)
	}
	if master, ok := m.surgeEng.GetMasterCacheEntry("wf_err"); !ok || master.Status != "error" {
		t.Fatalf("error master mirror not committed: %#v ok=%v", master, ok)
	}
}

// terminalCopyBlockingEngine behaves like mockStoppedEngine but parks the
// first TellStatus call so a test can interleave a lifecycle mutation while
// a real tick is inside recoverTerminalInfo, outside the GID gate.
type terminalCopyBlockingEngine struct {
	mockStoppedEngine
	entered chan struct{}
	release chan struct{}
	task    rpc.Task
}

func (e *terminalCopyBlockingEngine) TellStatus(gid string, keys []string) (rpc.Task, error) {
	select {
	case e.entered <- struct{}{}:
		select {
		case <-e.release:
		case <-time.After(3 * time.Second):
		}
	default:
	}
	task := e.task
	task.GID = gid
	return task, nil
}

// TestResumeTerminal_TickCopyGeneration proves tick terminal consumption
// honors the stamped lifecycle entry+generation on every accepted copy: a
// copy retired by a reopen or by a remove+same-ID rebuild writes no
// history/stats and emits no delta even when the generation number collides;
// a duplicate stopped row that only advances the request revision never
// blocks the first copy. The emitted delta carries the accepted full cache
// row, not a single-file rebuild from the sparse tracked copy.
func TestResumeTerminal_TickCopyGeneration(t *testing.T) {
	setup := func(t *testing.T, gid string, stopped []rpc.Task) (*Monitor, *TaskTracker, *Pusher, *terminalCopyBlockingEngine) {
		m, _, pusher, tracker := newReconcileTestMonitor(t)
		eng := &terminalCopyBlockingEngine{
			stopped: stopped,
			entered: make(chan struct{}, 1),
			release: make(chan struct{}),
			task: rpc.Task{
				Dir:   "/dl",
				Files: []rpc.File{{Path: "/dl/f.bin", Uris: []rpc.Uri{{Uri: "https://x/f.bin"}}}},
			},
		}
		m.engine = eng
		Cache.engine = eng
		t.Cleanup(func() {
			Cache.engine = nil
			Cache.arMu.Lock()
			Cache.arActive, Cache.arWaiting, Cache.arStopped = nil, nil, nil
			Cache.arMu.Unlock()
			Cache.DeleteMetadataForTest(gid)
		})
		return m, tracker, pusher, eng
	}

	// blockedTick runs one real tick far enough to mint the terminal copy and
	// park its finish inside the blocking TellStatus; mutate runs mid-consume
	// while the stale copy is still unvalidated.
	blockedTick := func(t *testing.T, m *Monitor, eng *terminalCopyBlockingEngine, mutate func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { m.tick(); close(done) }()
		select {
		case <-eng.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("tick never reached blocking TellStatus")
		}
		mutate()
		close(eng.release)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("tick did not finish after release")
		}
	}

	countComplete := func(pusher *Pusher, gid string) int {
		n := 0
		for _, d := range pusher.pendingSnapshot() {
			if d.GID == gid && d.Type == "complete" {
				n++
			}
		}
		return n
	}

	// H0 copy parked mid-consume, then reopened to waiting and completed again
	// as H1: the stale H0 copy must write no history and emit no delta; only
	// the H1 copy may finish.
	t.Run("reopen_retires_stale_copy", func(t *testing.T) {
		gid := "ar_tg_reopen"
		sparse := rpc.Task{GID: gid, Status: "complete", TotalLength: "100", CompletedLength: "100"}
		m, tracker, pusher, eng := setup(t, gid, []rpc.Task{sparse})
		resetHistoryForTest(t)

		blockedTick(t, m, eng, func() {
			tracker.RunUnderLifecycle(gid, func() {
				retireHistoryAndReopenToStatusLocked(tracker, gid, "waiting")
			})
		})
		if got := tracker.LifecycleGeneration(gid); got != 1 {
			t.Fatalf("reopen did not advance generation: %d", got)
		}
		if n := len(historyEntriesForGID(gid)); n != 0 {
			t.Fatalf("retired generation wrote history: %d", n)
		}

		m.tick()
		if entries := historyEntriesForGID(gid); len(entries) != 1 || entries[0].Status != "complete" {
			t.Fatalf("H1 copy did not finish: %#v", entries)
		}
		if got := countComplete(pusher, gid); got != 1 {
			t.Fatalf("stale copy emitted or fresh copy lost: complete deltas=%d", got)
		}
	})

	// Same-ID rebuild: the new lifecycle entry may restart at generation 0 —
	// the same number the stale copy was stamped with. Entry identity, not
	// the numeric generation, must reject it.
	t.Run("remove_readd_rejects_old_entry", func(t *testing.T) {
		gid := "ar_tg_readd"
		sparse := rpc.Task{GID: gid, Status: "complete", TotalLength: "100", CompletedLength: "100"}
		m, tracker, pusher, eng := setup(t, gid, []rpc.Task{sparse})
		resetHistoryForTest(t)

		blockedTick(t, m, eng, func() {
			tracker.RemoveTask(gid)
			tracker.EnsureTrackedFromEvent(gid, 100, "https://x/f.bin", 1, "active")
		})
		if !tracker.HasLifecycleEntry(gid) {
			t.Fatal("re-add did not rebuild the lifecycle entry")
		}
		if got := tracker.LifecycleGeneration(gid); got != 0 {
			t.Fatalf("rebuilt entry should collide at generation 0, got %d", got)
		}
		if n := len(historyEntriesForGID(gid)); n != 0 {
			t.Fatalf("rebuilt entry accepted the retired copy: %d", n)
		}

		m.tick()
		if entries := historyEntriesForGID(gid); len(entries) != 1 || entries[0].Status != "complete" {
			t.Fatalf("rebuilt generation did not finish its own copy: %#v", entries)
		}
		if got := countComplete(pusher, gid); got != 1 {
			t.Fatalf("old entry emitted or new copy lost: %d", got)
		}
	})

	// A repeated stopped row only advances the request revision — it mints no
	// second copy and must not block the first copy's finish or emit.
	t.Run("dedup_revision_keeps_first_copy", func(t *testing.T) {
		gid := "ar_tg_dedup"
		sparse := rpc.Task{GID: gid, Status: "complete", TotalLength: "100", CompletedLength: "100"}
		m, tracker, pusher, eng := setup(t, gid, []rpc.Task{sparse})
		resetHistoryForTest(t)

		blockedTick(t, m, eng, func() {
			if got := tracker.Update(nil, nil, []rpc.Task{sparse}); len(got) != 0 {
				t.Fatalf("deduped terminal minted a second copy: %d", len(got))
			}
		})
		if entries := historyEntriesForGID(gid); len(entries) != 1 || entries[0].Status != "complete" {
			t.Fatalf("first copy lost after revision advance: %#v", entries)
		}
		m.tick()
		if n := len(historyEntriesForGID(gid)); n != 1 {
			t.Fatalf("repeat tick rewrote history: %d", n)
		}
		if got := countComplete(pusher, gid); got != 1 {
			t.Fatalf("first copy emitted %d times, want 1", got)
		}
	})

	// The accepted full row reaches the wire: multi-Files/Uris, Title, and
	// the download group survive instead of a single-file rebuild.
	t.Run("emitted_payload_is_full_row", func(t *testing.T) {
		gid := "ar_tg_payload"
		full := fullRowForTest(gid)
		full.Status = "complete"
		full.CompletedLength = "1000"
		full.ErrorCode, full.ErrorMessage = "", ""
		m, _, pusher, _ := setup(t, gid, []rpc.Task{full})

		m.tick()
		var payload rpc.Task
		found := false
		for _, d := range pusher.pendingSnapshot() {
			if d.GID == gid && d.Type == "complete" {
				found = true
				p, ok := d.Payload.(rpc.Task)
				if !ok {
					t.Fatalf("complete payload = %T, want rpc.Task", d.Payload)
				}
				payload = p
			}
		}
		if !found {
			t.Fatal("no complete delta emitted")
		}
		assertFullRowMetadata(t, payload, full)
		if payload.Status != "complete" || payload.DownloadSpeed != "0" {
			t.Fatalf("payload not terminal-normalized: %#v", payload)
		}
	})
}
