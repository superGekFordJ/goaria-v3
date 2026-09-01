package monitor

import (
	"path/filepath"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
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

// findCompleteDelta searches the pusher pending queue for a complete delta
// with the given GID and returns it, or nil.
func findCompleteDelta(pusher *Pusher, gid string) *events.TaskDelta {
	pusher.mu.Lock()
	defer pusher.mu.Unlock()
	for i := range pusher.pending {
		if pusher.pending[i].Type == "complete" && pusher.pending[i].GID == gid {
			return &pusher.pending[i]
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
	if got := canonicalCompleteTotal(1000, 500, &rpc.Task{TotalLength: "2000", CompletedLength: "2000"}); got != 1000 {
		t.Errorf("canonicalCompleteTotal positive event total = %d, want 1000", got)
	}

	// 2. Positive cached task total
	if got := canonicalCompleteTotal(0, 0, &rpc.Task{TotalLength: "2000", CompletedLength: "500"}); got != 2000 {
		t.Errorf("canonicalCompleteTotal positive cached total = %d, want 2000", got)
	}

	// 3. Positive event downloaded count
	if got := canonicalCompleteTotal(0, 800, &rpc.Task{TotalLength: "0", CompletedLength: "300"}); got != 800 {
		t.Errorf("canonicalCompleteTotal positive event downloaded = %d, want 800", got)
	}

	// 4. Positive cached task completed count
	if got := canonicalCompleteTotal(0, 0, &rpc.Task{TotalLength: "0", CompletedLength: "750"}); got != 750 {
		t.Errorf("canonicalCompleteTotal positive cached completed = %d, want 750", got)
	}

	// 5. 0 for empty/invalid
	if got := canonicalCompleteTotal(0, 0, &rpc.Task{TotalLength: "0", CompletedLength: "0"}); got != 0 {
		t.Errorf("canonicalCompleteTotal zero lengths = %d, want 0", got)
	}
	if got := canonicalCompleteTotal(0, 0, &rpc.Task{TotalLength: "invalid", CompletedLength: "bad"}); got != 0 {
		t.Errorf("canonicalCompleteTotal invalid strings = %d, want 0", got)
	}
	if got := canonicalCompleteTotal(0, 0, nil); got != 0 {
		t.Errorf("canonicalCompleteTotal nil cached = %d, want 0", got)
	}
}

func TestComplete_ZeroTotalWithCachedCompleted_SyncsToCompletedLength(t *testing.T) {
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
		GID:             "sg_chunked_zero_event",
		Status:          "active",
		CompletedLength: "7520000",
		TotalLength:     "0",
		DownloadSpeed:   "100000",
	}}
	defer resetCacheSg()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type:       surgeEvents.EventComplete,
		DownloadID: "chunked_zero_event",
		Total:      0,
	})

	task := findSgStoppedTask("sg_chunked_zero_event")
	if task == nil {
		t.Fatal("expected sg_chunked_zero_event in stopped after complete event")
	}
	if task.CompletedLength != "7520000" {
		t.Errorf("CompletedLength = %q, want 7520000", task.CompletedLength)
	}
	if task.TotalLength != "7520000" {
		t.Errorf("TotalLength = %q, want 7520000", task.TotalLength)
	}

	delta := findCompleteDelta(pusher, "sg_chunked_zero_event")
	if delta == nil {
		t.Fatal("expected complete delta in pusher pending queue")
	}
	payload, ok := delta.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", delta.Payload)
	}
	if payload["completedLength"] != "7520000" {
		t.Errorf("payload completedLength = %v, want 7520000", payload["completedLength"])
	}
	if payload["totalLength"] != "7520000" {
		t.Errorf("payload totalLength = %v, want 7520000", payload["totalLength"])
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
