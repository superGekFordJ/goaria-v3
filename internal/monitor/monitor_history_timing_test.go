package monitor

import (
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/testutil"
	surgeEvents "goaria-v3/internal/surge/types"
)

const timingTotal = int64(10_000_000) // below speedstats.MinFileSize

func setupTimingHistory(t *testing.T) {
	t.Helper()
	history.SetHistoryPath(filepath.Join(t.TempDir(), "history.json"))
	history.DisableSaveForTest()
	history.Clear()
	t.Cleanup(history.Clear)
}

func timingTask(gid, status string) rpc.Task {
	task := createMockTask(gid, status)
	task.TotalLength = strconv.FormatInt(timingTotal, 10)
	task.CompletedLength = task.TotalLength
	task.DownloadSpeed = "0"
	return task
}

// runArTicks drives n active ticks spaced by gap, then a terminal tick, and
// returns the completed copies emitted by the tracker.
func runArTicks(tr *TaskTracker, gid string, n int, gap time.Duration) []*TrackedTask {
	active := timingTask(gid, "active")
	for range n {
		rewindActiveClock(tr, gid, gap)
		tr.Update([]rpc.Task{active}, nil, nil)
	}
	return tr.Update(nil, nil, []rpc.Task{timingTask(gid, "complete")})
}

func newTimingMonitor(tr *TaskTracker) *Monitor {
	hub := events.NewHub(nil)
	return &Monitor{hub: hub, pusher: NewPusher(hub), tracker: tr, engine: rpc.NewHybridEngine(nil, &mockSafeEngine{})}
}

func mustHistory(t *testing.T, gid string) history.HistoryEntry {
	t.Helper()
	e, ok := history.Get(gid)
	if !ok {
		t.Fatalf("history entry %s missing", gid)
	}
	return e
}

func TestHistoryTiming_ArSessionAddedSufficientSamples(t *testing.T) {
	setupTimingHistory(t)
	tr := NewTaskTracker()
	m := newTimingMonitor(tr)
	gid := "ar_timing_ok"

	tr.MarkAdded(gid)
	// MarkAdded starts the clock, so the first gap is the add→first tick span.
	completed := runArTicks(tr, gid, 12, time.Second)
	if len(completed) != 1 {
		t.Fatalf("completed = %d, want 1", len(completed))
	}
	m.handleTaskComplete(completed[0])

	e := mustHistory(t, gid)
	if e.DurationMs < 12_000 || e.DurationMs > 13_000 {
		t.Fatalf("DurationMs = %d, want ~12000", e.DurationMs)
	}
	// DurationMs is truncated to ms while AvgSpeed uses exact elapsed; allow 0.1%.
	wantAvg := timingTotal * 1000 / e.DurationMs
	if diff := e.AvgSpeed - wantAvg; diff < -wantAvg/1000 || diff > wantAvg/1000 {
		t.Fatalf("AvgSpeed = %d, want ~%d", e.AvgSpeed, wantAvg)
	}
	if e.CompletedAt == 0 {
		t.Fatal("CompletedAt must be stamped on first add")
	}
}

func TestHistoryTiming_ArInsufficientSamplesNotWritten(t *testing.T) {
	setupTimingHistory(t)
	tr := NewTaskTracker()
	m := newTimingMonitor(tr)
	gid := "ar_timing_sparse"

	tr.MarkAdded(gid)
	completed := runArTicks(tr, gid, 3, 2*time.Second) // 6s < 10×2s
	m.handleTaskComplete(completed[0])

	if e := mustHistory(t, gid); e.DurationMs != 0 || e.AvgSpeed != 0 {
		t.Fatalf("sparse sampling wrote timing: %+v", e)
	}
}

func TestHistoryTiming_ArNotAddedThisSessionNotWritten(t *testing.T) {
	setupTimingHistory(t)
	tr := NewTaskTracker()
	m := newTimingMonitor(tr)
	gid := "ar_timing_external"

	// Seen active by tick only (external RPC add / restart resume).
	tr.Update([]rpc.Task{timingTask(gid, "active")}, nil, nil)
	completed := runArTicks(tr, gid, 20, time.Second)
	m.handleTaskComplete(completed[0])

	if e := mustHistory(t, gid); e.DurationMs != 0 {
		t.Fatalf("DurationMs = %d, want 0 without AddedAt", e.DurationMs)
	}
}

func TestHistoryTiming_RestartReplayKeepsExistingTiming(t *testing.T) {
	setupTimingHistory(t)
	gid := "ar_timing_replay"
	history.Add(history.HistoryEntry{
		GID: gid, Path: "/tmp/a.zip", Status: "complete",
		DurationMs: 42_000, AvgSpeed: 1234, PeakSpeed: 5678,
	})
	oldCompletedAt := mustHistory(t, gid).CompletedAt
	if oldCompletedAt == 0 {
		t.Fatal("seed CompletedAt missing")
	}

	// Fresh tracker: first-seen terminal, as after an app restart.
	tr := NewTaskTracker()
	m := newTimingMonitor(tr)
	completed := tr.Update(nil, nil, []rpc.Task{timingTask(gid, "complete")})
	if len(completed) != 1 {
		t.Fatalf("completed = %d, want 1", len(completed))
	}
	m.handleTaskComplete(completed[0])

	e := mustHistory(t, gid)
	if e.CompletedAt != oldCompletedAt || e.DurationMs != 42_000 || e.AvgSpeed != 1234 || e.PeakSpeed != 5678 {
		t.Fatalf("replay erased timing: %+v (want completedAt=%d)", e, oldCompletedAt)
	}
}

func seedSgTimingTask(t *testing.T, tr *TaskTracker, downloadID string) string {
	t.Helper()
	gid := "sg_" + downloadID
	dir := t.TempDir()
	path := filepath.Join(dir, downloadID+".bin")
	Cache.sgActive = []rpc.Task{{GID: gid, Status: "active", TotalLength: "0", CompletedLength: "0", DownloadSpeed: "0", Dir: dir, Files: []rpc.File{{Path: path}}}}
	t.Cleanup(resetCacheSg)
	tr.EnsureTrackedFromEvent(gid, timingTotal, "https://example.com/"+downloadID, 4, "active")
	tr.mu.Lock()
	tr.tasks[gid].FilePath = path
	tr.tasks[gid].Dir = dir
	tr.mu.Unlock()
	return gid
}

func TestHistoryTiming_SgCompleteUsesEventElapsed(t *testing.T) {
	setupTimingHistory(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	tr := NewTaskTracker()
	m := newTimingMonitor(tr)
	gid := seedSgTimingTask(t, tr, "sg-elapsed")

	elapsed := 7*time.Second + 250*time.Millisecond
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventComplete, DownloadID: "sg-elapsed",
		Total: timingTotal, Downloaded: timingTotal, Elapsed: elapsed, AvgSpeed: 999,
	})

	e := mustHistory(t, gid)
	if e.DurationMs != elapsed.Milliseconds() {
		t.Fatalf("DurationMs = %d, want %d", e.DurationMs, elapsed.Milliseconds())
	}
	if want := int64(float64(timingTotal) / elapsed.Seconds()); e.AvgSpeed != want {
		t.Fatalf("AvgSpeed = %d, want %d", e.AvgSpeed, want)
	}
	if e.PeakSpeed != 0 {
		t.Fatalf("PeakSpeed = %d, AvgSpeed substitute must not be persisted", e.PeakSpeed)
	}
}

func TestHistoryTiming_SgRealPeakPersisted(t *testing.T) {
	setupTimingHistory(t)
	speedstats.ResetRecordsForTest()
	t.Cleanup(speedstats.ResetRecordsForTest)
	tr := NewTaskTracker()
	m := newTimingMonitor(tr)
	gid := seedSgTimingTask(t, tr, "sg-peak")
	tr.mu.Lock()
	tr.tasks[gid].PeakSpeed = 3_000_000
	tr.mu.Unlock()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventComplete, DownloadID: "sg-peak",
		Total: timingTotal, Elapsed: 5 * time.Second,
	})
	if e := mustHistory(t, gid); e.PeakSpeed != 3_000_000 {
		t.Fatalf("PeakSpeed = %d, want 3000000", e.PeakSpeed)
	}
}

func TestHistoryTiming_ErrorWritesNoTiming(t *testing.T) {
	setupTimingHistory(t)
	tr := NewTaskTracker()
	m := newTimingMonitor(tr)
	gid := seedSgTimingTask(t, tr, "sg-error")
	tr.mu.Lock()
	tr.tasks[gid].PeakSpeed = 3_000_000
	tr.mu.Unlock()

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventError, DownloadID: "sg-error", Err: errTimingTest{},
		Elapsed: 5 * time.Second,
	})
	e := mustHistory(t, gid)
	if e.Status != "error" {
		t.Fatalf("Status = %q, want error", e.Status)
	}
	if e.DurationMs != 0 || e.AvgSpeed != 0 || e.PeakSpeed != 0 {
		t.Fatalf("error entry carries timing: %+v", e)
	}
}

type errTimingTest struct{}

func (errTimingTest) Error() string { return "network down" }

func TestHandleSurgeEvent_QueuedStartedPreserveMasterCreatedAt(t *testing.T) {
	testutil.SetupStateDB(t)
	hub := events.NewHub(nil)
	se := rpc.NewSurgeEngineForTesting(scheduler.NewSchedulerForTesting(nil))
	const createdAt int64 = 1_700_000_123
	se.UpsertMasterCacheEntry(surgeEvents.DownloadRecord{
		ID: "dl-created", URL: "http://x/c.bin", Status: "queued", CreatedAt: createdAt,
	})
	m := &Monitor{hub: hub, pusher: NewPusher(hub), surgeEng: se}
	t.Cleanup(resetCacheSg)

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventQueued, DownloadID: "dl-created", URL: "http://x/c.bin",
	})
	if got, _ := se.GetMasterCacheEntry("dl-created"); got.CreatedAt != createdAt {
		t.Fatalf("after Queued CreatedAt = %d, want %d", got.CreatedAt, createdAt)
	}

	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventStarted, DownloadID: "dl-created", URL: "http://x/c.bin", Total: 1000,
	})
	if got, _ := se.GetMasterCacheEntry("dl-created"); got.CreatedAt != createdAt {
		t.Fatalf("after Started CreatedAt = %d, want %d", got.CreatedAt, createdAt)
	}

	// Cache miss must not synthesize a timestamp.
	m.handleSurgeEvent(surgeEvents.DownloadEvent{
		Type: surgeEvents.EventStarted, DownloadID: "dl-fresh", URL: "http://x/f.bin", Total: 1000,
	})
	if got, _ := se.GetMasterCacheEntry("dl-fresh"); got.CreatedAt != 0 {
		t.Fatalf("fresh Started CreatedAt = %d, want 0", got.CreatedAt)
	}
}

func TestHistoryTiming_ReadersConcurrentWithWriters(t *testing.T) {
	setupTimingHistory(t)
	tr := NewTaskTracker()
	m := newTimingMonitor(tr)
	t.Cleanup(resetCacheAr)

	var wg sync.WaitGroup
	for i := range 4 {
		gid := "ar_race_" + strconv.Itoa(i)
		tr.MarkAdded(gid)
		wg.Go(func() {
			active := timingTask(gid, "active")
			for range 50 {
				tr.Update([]rpc.Task{active}, nil, nil)
			}
			for _, c := range tr.Update(nil, nil, []rpc.Task{timingTask(gid, "complete")}) {
				m.handleTaskComplete(c)
			}
		})
		wg.Go(func() {
			for range 200 {
				_, _ = tr.GetTrackedTask(gid)
				_, _, _ = Cache.GetTask(gid)
				_, _ = history.Get(gid)
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			Cache.arMu.Lock()
			Cache.arActive = []rpc.Task{timingTask("ar_race_0", "active")}
			Cache.arMu.Unlock()
		}
	})
	wg.Wait()
}
