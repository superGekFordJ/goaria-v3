package tasks

import (
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	surgetypes "goaria-v3/internal/surge/types"
)

// panicAria2Engine embeds a nil interface: any engine method call panics,
// proving GetTaskDetails issues no aria2 RPC.
type panicAria2Engine struct {
	rpc.DownloadEngine
}

type taskDetailFixture struct {
	svc     *Service
	tracker *monitor.TaskTracker
	surge   *rpc.SurgeEngine
}

func setupTaskDetailTest(t *testing.T) taskDetailFixture {
	t.Helper()
	setupAppTaskHistoryTest(t)
	tr := monitor.NewTaskTracker()
	origTr := monitor.State.GetTracker()
	monitor.State.SetTracker(tr)
	t.Cleanup(func() { monitor.State.SetTracker(origTr) })

	se := rpc.NewSurgeEngineForTesting(nil)
	se.SetMasterCacheForTesting(nil)
	return taskDetailFixture{
		svc:     &Service{Engine: rpc.NewHybridEngine(panicAria2Engine{}, se)},
		tracker: tr,
		surge:   se,
	}
}

func detailFor(t *testing.T, svc *Service, gid string) *TaskDetail {
	t.Helper()
	env, ok := svc.GetTaskDetails([]string{gid})[gid]
	if !ok {
		t.Fatalf("result missing key %q", gid)
	}
	if !env.Found || env.Detail == nil {
		t.Fatalf("%s: found=%v detail=%v, want found detail", gid, env.Found, env.Detail)
	}
	if env.Detail.GID != gid {
		t.Fatalf("detail.GID = %q, want %q", env.Detail.GID, gid)
	}
	return env.Detail
}

func arTask(gid, status string, uris ...string) rpc.Task {
	u := make([]rpc.Uri, 0, len(uris))
	for _, s := range uris {
		u = append(u, rpc.Uri{Uri: s})
	}
	return rpc.Task{GID: gid, Status: status, Files: []rpc.File{{Path: "/d/" + gid, Uris: u}}}
}

func TestTaskDetail_NotFoundWhenAllSourcesMiss(t *testing.T) {
	f := setupTaskDetailTest(t)
	res := f.svc.GetTaskDetails([]string{"ar_ghost", "sg_ghost"})
	for _, gid := range []string{"ar_ghost", "sg_ghost"} {
		env, ok := res[gid]
		if !ok || env.Found || env.Detail != nil {
			t.Fatalf("%s: %+v ok=%v, want found:false", gid, env, ok)
		}
	}
}

func TestTaskDetail_TrimsDedupesAndSkipsEmpty(t *testing.T) {
	f := setupTaskDetailTest(t)
	res := f.svc.GetTaskDetails([]string{" ar_a ", "ar_a", "", "   "})
	if len(res) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(res), res)
	}
	if _, ok := res["ar_a"]; !ok {
		t.Fatal("expected trimmed key ar_a")
	}
	if got := f.svc.GetTaskDetails(nil); len(got) != 0 {
		t.Fatalf("nil input = %+v, want empty", got)
	}
}

func TestTaskDetail_ArLiveWithTracker(t *testing.T) {
	f := setupTaskDetailTest(t)
	gid := "ar_live"
	monitor.Cache.UpdateFromAria2([]rpc.Task{arTask(gid, "active", "http://a/x", "http://mirror/x", "http://a/x", "")}, nil, nil)
	f.tracker.MarkAdded(gid)
	f.tracker.RecordPeakEfficiency(gid, 4_000_000, 4)

	d := detailFor(t, f.svc, gid)
	if d.AddedAt == 0 || d.AddedAt > time.Now().Unix() {
		t.Fatalf("AddedAt = %d", d.AddedAt)
	}
	if d.PeakSpeed != 4_000_000 {
		t.Fatalf("PeakSpeed = %d, want 4000000", d.PeakSpeed)
	}
	if want := []string{"http://a/x", "http://mirror/x"}; !reflect.DeepEqual(d.URIs, want) {
		t.Fatalf("URIs = %v, want %v", d.URIs, want)
	}
	if d.CompletedAt != 0 || d.TimeTakenMs != 0 || d.AvgSpeed != 0 {
		t.Fatalf("live task grew terminal fields: %+v", d)
	}
}

func TestTaskDetail_ArLiveWithoutTrackerHidesAddedAt(t *testing.T) {
	f := setupTaskDetailTest(t)
	gid := "ar_external"
	monitor.Cache.UpdateFromAria2(nil, []rpc.Task{arTask(gid, "paused", "http://ext/x")}, nil)

	d := detailFor(t, f.svc, gid)
	if d.AddedAt != 0 || d.PeakSpeed != 0 {
		t.Fatalf("fabricated fields without tracker: %+v", d)
	}
	if !reflect.DeepEqual(d.URIs, []string{"http://ext/x"}) {
		t.Fatalf("URIs = %v", d.URIs)
	}
}

func TestTaskDetail_ArStoppedWithHistory(t *testing.T) {
	f := setupTaskDetailTest(t)
	gid := "ar_done"
	monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{arTask(gid, "complete")})
	history.Add(history.HistoryEntry{GID: gid, Path: "/d/x", Source: "http://hist/x", DurationMs: 9000, AvgSpeed: 111, PeakSpeed: 222})

	d := detailFor(t, f.svc, gid)
	if d.CompletedAt == 0 || d.TimeTakenMs != 9000 || d.AvgSpeed != 111 || d.PeakSpeed != 222 {
		t.Fatalf("history timing not surfaced: %+v", d)
	}
	// Cache task has no URIs, so the chain falls through to history Source.
	if !reflect.DeepEqual(d.URIs, []string{"http://hist/x"}) {
		t.Fatalf("URIs = %v", d.URIs)
	}
}

func TestTaskDetail_HistoryOnlyRow(t *testing.T) {
	f := setupTaskDetailTest(t)
	for _, gid := range []string{"ar_hist", "sg_hist"} {
		history.Add(history.HistoryEntry{GID: gid, Path: "/d/" + gid, Source: "http://h/" + gid, DurationMs: 1500})
		d := detailFor(t, f.svc, gid)
		if d.TimeTakenMs != 1500 || d.CompletedAt == 0 || d.AddedAt != 0 {
			t.Fatalf("%s: %+v", gid, d)
		}
		if !reflect.DeepEqual(d.URIs, []string{"http://h/" + gid}) {
			t.Fatalf("%s URIs = %v", gid, d.URIs)
		}
	}
}

func TestTaskDetail_FastCompleteTrackerOnlyIsSparseFound(t *testing.T) {
	f := setupTaskDetailTest(t)
	gid := "ar_fast"
	// No cache row, no history (handleTaskComplete skipped for missing path).
	f.tracker.MarkAdded(gid)

	d := detailFor(t, f.svc, gid)
	if d.AddedAt == 0 {
		t.Fatal("expected AddedAt from tracker")
	}
	if d.CompletedAt != 0 || d.TimeTakenMs != 0 || len(d.URIs) != 0 {
		t.Fatalf("sparse detail grew fields: %+v", d)
	}
}

func TestTaskDetail_SgCompletedMaster(t *testing.T) {
	f := setupTaskDetailTest(t)
	gid := "sg_m1"
	f.surge.SetMasterCacheForTesting([]surgetypes.DownloadRecord{{
		ID: "m1", Status: "completed", URL: "http://p/x", Mirrors: []string{"http://m/x", "http://p/x", " "},
		CreatedAt: 1_700_000_000, CompletedAt: 1_700_000_100, TimeTaken: 12_345, AvgSpeed: 2048.9,
	}})
	history.Add(history.HistoryEntry{GID: gid, Path: "/d/x", DurationMs: 1, AvgSpeed: 1})
	monitor.Cache.AddSgTask(rpc.Task{GID: gid, Status: "complete"}, "stopped")
	f.tracker.MarkAdded(gid)

	d := detailFor(t, f.svc, gid)
	if d.AddedAt != 1_700_000_000 {
		t.Fatalf("AddedAt = %d, master CreatedAt must win", d.AddedAt)
	}
	if d.CompletedAt != 1_700_000_100 {
		t.Fatalf("CompletedAt = %d", d.CompletedAt)
	}
	// master TimeTaken is already ms: pass through untouched.
	if d.TimeTakenMs != 12_345 || d.AvgSpeed != 2048 {
		t.Fatalf("TimeTakenMs=%d AvgSpeed=%d", d.TimeTakenMs, d.AvgSpeed)
	}
	if want := []string{"http://p/x", "http://m/x"}; !reflect.DeepEqual(d.URIs, want) {
		t.Fatalf("URIs = %v, want %v", d.URIs, want)
	}
}

func TestTaskDetail_SgNonCompletedMasterSkipsTerminalFields(t *testing.T) {
	f := setupTaskDetailTest(t)
	gid := "sg_m2"
	f.surge.SetMasterCacheForTesting([]surgetypes.DownloadRecord{{
		ID: "m2", Status: "paused", URL: "http://p/y",
		CompletedAt: 1_700_000_100, TimeTaken: 5_000, AvgSpeed: 99,
	}})
	monitor.Cache.AddSgTask(rpc.Task{GID: gid, Status: "paused"}, "waiting")

	d := detailFor(t, f.svc, gid)
	if d.CompletedAt != 0 || d.TimeTakenMs != 0 || d.AvgSpeed != 0 {
		t.Fatalf("paused master leaked terminal fields: %+v", d)
	}
	if d.AddedAt != 0 {
		t.Fatalf("AddedAt = %d, want hidden (no CreatedAt, no tracker)", d.AddedAt)
	}
	if !reflect.DeepEqual(d.URIs, []string{"http://p/y"}) {
		t.Fatalf("URIs = %v", d.URIs)
	}
}

func TestTaskDetail_SgMasterMissingFallsBack(t *testing.T) {
	f := setupTaskDetailTest(t)
	gid := "sg_nomaster"
	f.tracker.EnsureTrackedFromEvent(gid, 100, "http://tracker/z", 4, "active")
	f.tracker.MarkAdded(gid)
	monitor.Cache.AddSgTask(rpc.Task{GID: gid, Status: "active"}, "active")

	d := detailFor(t, f.svc, gid)
	if d.AddedAt == 0 {
		t.Fatal("expected tracker AddedAt fallback")
	}
	if !reflect.DeepEqual(d.URIs, []string{"http://tracker/z"}) {
		t.Fatalf("URIs = %v", d.URIs)
	}
}

func TestTaskDetail_NoHybridEngineStillReadsMemory(t *testing.T) {
	setupTaskDetailTest(t)
	history.Add(history.HistoryEntry{GID: "sg_plain", Path: "/d/p", DurationMs: 10})
	svc := &Service{Engine: panicAria2Engine{}}
	if d := detailFor(t, svc, "sg_plain"); d.TimeTakenMs != 10 {
		t.Fatalf("%+v", d)
	}
	if d := detailFor(t, &Service{}, "sg_plain"); d.TimeTakenMs != 10 {
		t.Fatalf("%+v", d)
	}
}

func TestTaskDetail_ConcurrentWithWriters(t *testing.T) {
	f := setupTaskDetailTest(t)
	var wg sync.WaitGroup
	for i := range 4 {
		gid := "ar_c" + strconv.Itoa(i)
		wg.Go(func() {
			for range 100 {
				monitor.Cache.UpdateFromAria2([]rpc.Task{arTask(gid, "active", "http://c/"+gid)}, nil, nil)
				f.tracker.MarkAdded(gid)
				history.Add(history.HistoryEntry{GID: gid, Path: "/d/" + gid})
			}
		})
		wg.Go(func() {
			for range 100 {
				_ = f.svc.GetTaskDetails([]string{gid, "sg_x"})
			}
		})
	}
	wg.Wait()
}
