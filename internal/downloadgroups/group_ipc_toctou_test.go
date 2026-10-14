package downloadgroups

import (
	"errors"
	"runtime"
	"sync"
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	surgeEvents "goaria-v3/internal/surge/types"
)

func TestResumeDownloadGroup_StoppedErrorAndPartialConfirmation(t *testing.T) {
	setupDownloadGroupsTest(t)
	_, cleanup := mixedGroupTestMonitor(t)
	defer cleanup()
	group := groupReadTestGroup("dg-error-resume", 4)
	for _, gid := range []string{"sg_error_ok", "sg_error_fail", "sg_error_missing"} {
		task := groupReadTask(gid, "error", &group, "1000", "200", "0")
		monitor.Cache.AddSgTask(task, "stopped")
		monitor.RegisterTaskGroup(gid, group)
		history.Add(history.HistoryEntry{GID: gid, Status: "error", Path: task.Files[0].Path, DownloadGroup: &group})
	}
	monitor.Cache.AddSgTask(groupReadTask("sg_complete", "complete", &group, "1000", "1000", "0"), "stopped")
	monitor.Cache.UpdateFromAria2(nil, nil, monitor.Cache.GetStopped())
	original := ResumeMultiResults
	defer func() { ResumeMultiResults = original }()
	ResumeMultiResults = func(gids []string) ([]rpc.MultiCallItemResult, error) {
		if len(gids) != 3 {
			t.Fatalf("wrong eligibility: %v", gids)
		}
		return []rpc.MultiCallItemResult{{GID: "sg_error_ok", OK: true}, {GID: "sg_error_ok", Error: "duplicate"}, {GID: "sg_complete", OK: true}, {GID: "sg_error_fail", Error: "failed"}}, nil
	}
	result := ResumeDownloadGroup(group.ID)
	if result.Succeeded != 1 || result.Failed != 2 || result.Skipped != 1 {
		t.Fatalf("partial: %#v", result)
	}
	got, list, _ := monitor.Cache.GetTask("sg_error_ok")
	if list != "waiting" || got.Status != "waiting" || got.CompletedLength != "200" || got.DownloadGroup == nil {
		t.Fatalf("full confirmation: %#v", got)
	}
	if _, ok := history.Get(got.GID); ok {
		t.Fatal("H0 retained")
	}
	for _, gid := range []string{"sg_error_fail", "sg_error_missing", "sg_complete"} {
		if !taskInCacheList(gid, "stopped") {
			t.Fatal("failed/unknown moved")
		}
	}
}

func TestPauseDownloadGroup_TerminalDuringRPC_DoesNotRevive(t *testing.T) {
	setupDownloadGroupsTest(t)
	hub, cleanup := mixedGroupTestMonitor(t)
	defer cleanup()

	var moveEvents []events.TaskMove
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		moveEvents = append(moveEvents, move)
	})

	// ItemCount must be >= 2 for shouldIncludeCard; only one member is actionable.
	group := groupReadTestGroup("dg-pause-toctou", 2)
	const gid = "sg_pause_toctou"
	task := groupReadTask(gid, "active", &group, "1000", "100", "50")
	task.Files = []rpc.File{{Path: "/tmp/pause_toctou.bin"}}
	monitor.Cache.AddSgTask(task, "active")
	monitor.RegisterTaskGroup(gid, group)
	monitor.RegisterTaskGroup("sg_pause_toctou_pad", group)
	if tr := monitor.State.GetTracker(); tr != nil {
		tr.EnsureTrackedFromEvent(gid, 1000, "https://example.com/"+gid, 0, "active")
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	origPause := PauseMultiResults
	PauseMultiResults = func(gids []string) ([]rpc.MultiCallItemResult, error) {
		close(entered)
		<-release
		out := make([]rpc.MultiCallItemResult, len(gids))
		for i, g := range gids {
			out[i] = rpc.MultiCallItemResult{GID: g, OK: true}
		}
		return out, nil
	}
	defer func() { PauseMultiResults = origPause }()

	var result DownloadGroupOperationResult
	var wg sync.WaitGroup
	wg.Go(func() {
		result = PauseDownloadGroup(group.ID)
	})
	<-entered

	// Terminal lands while PauseMulti is in flight.
	monitor.Cache.MoveTaskToStoppedWithError(gid, "error", "1", "rpc-terminal")
	if tr := monitor.State.GetTracker(); tr != nil {
		_ = tr.MarkCompleteFromEvent(gid, "error")
	}
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/pause_toctou.bin", Status: "error"})

	for range 32 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	if !result.Found || result.Succeeded < 1 {
		t.Fatalf("unexpected pause result after terminal race: %#v", result)
	}
	if !taskInCacheList(gid, "stopped") {
		t.Fatal("expected task to remain stopped")
	}
	if taskInCacheList(gid, "active") || taskInCacheList(gid, "waiting") {
		t.Fatal("expected no active/waiting twin after refused group pause move")
	}
	stopped := monitor.Cache.GetStopped()
	var found bool
	for _, st := range stopped {
		if st.GID == gid {
			found = true
			if st.ErrorCode != "1" || st.ErrorMessage != "rpc-terminal" {
				t.Fatalf("expected error fields intact, got %#v", st)
			}
		}
	}
	if !found {
		t.Fatal("stopped row missing")
	}
	entry, ok := history.Get(gid)
	if !ok || entry.Status != "error" {
		t.Fatalf("expected H1 error history retained, got ok=%v entry=%#v", ok, entry)
	}
	for _, mv := range moveEvents {
		if mv.GID == gid {
			t.Fatalf("unexpected group task:move after refused transition: %#v", mv)
		}
	}
}

func TestResumeDownloadGroup_TerminalDuringRPC_DoesNotRevive(t *testing.T) {
	setupDownloadGroupsTest(t)
	hub := events.NewHub(nil)
	// The mid-RPC terminal is only refused when the live pool read agrees:
	// wire a real scheduler/engine so the error lands as known live state.
	prog := progress.New("resume_toctou", 0)
	pool := scheduler.NewSchedulerForTesting(map[string]surgeEvents.DownloadRecord{
		"resume_toctou": {ID: "resume_toctou", ProgressState: prog},
	})
	se := rpc.NewSurgeEngineForTesting(pool)
	t.Cleanup(se.Close)
	monitor.State.SetMonitor(monitor.NewMonitorWithSurgeEngineForTest(se))
	t.Cleanup(func() { monitor.State.SetMonitor(nil) })

	var moveEvents []events.TaskMove
	hub.SubscribeTaskMove(func(move events.TaskMove) {
		moveEvents = append(moveEvents, move)
	})

	group := groupReadTestGroup("dg-resume-toctou", 2)
	const gid = "sg_resume_toctou"
	task := groupReadTask(gid, "paused", &group, "1000", "100", "0")
	task.Files = []rpc.File{{Path: "/tmp/resume_toctou.bin"}}
	monitor.Cache.AddSgTask(task, "waiting")
	monitor.RegisterTaskGroup(gid, group)
	monitor.RegisterTaskGroup("sg_resume_toctou_pad", group)
	if tr := monitor.State.GetTracker(); tr != nil {
		tr.EnsureTrackedFromEvent(gid, 1000, "https://example.com/"+gid, 0, "paused")
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	origResume := ResumeMultiResults
	ResumeMultiResults = func(gids []string) ([]rpc.MultiCallItemResult, error) {
		close(entered)
		<-release
		out := make([]rpc.MultiCallItemResult, len(gids))
		for i, g := range gids {
			out[i] = rpc.MultiCallItemResult{GID: g, OK: true}
		}
		return out, nil
	}
	defer func() { ResumeMultiResults = origResume }()

	var result DownloadGroupOperationResult
	var wg sync.WaitGroup
	wg.Go(func() {
		result = ResumeDownloadGroup(group.ID)
	})
	<-entered

	monitor.Cache.MoveTaskToStoppedWithError(gid, "error", "1", "rpc-terminal")
	if tr := monitor.State.GetTracker(); tr != nil {
		_ = tr.MarkCompleteFromEvent(gid, "error")
	}
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/resume_toctou.bin", Status: "error"})
	prog.SetError(errors.New("rpc-terminal"))

	for range 32 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	if !result.Found || result.Succeeded < 1 {
		t.Fatalf("unexpected resume result after terminal race: %#v", result)
	}
	if !taskInCacheList(gid, "stopped") {
		t.Fatal("expected task to remain stopped")
	}
	if taskInCacheList(gid, "active") || taskInCacheList(gid, "waiting") {
		t.Fatal("expected no active/waiting twin after refused group resume move")
	}
	stopped := monitor.Cache.GetStopped()
	var found bool
	for _, st := range stopped {
		if st.GID == gid {
			found = true
			if st.ErrorCode != "1" || st.ErrorMessage != "rpc-terminal" {
				t.Fatalf("expected error fields intact, got %#v", st)
			}
		}
	}
	if !found {
		t.Fatal("stopped row missing")
	}
	entry, ok := history.Get(gid)
	if !ok || entry.Status != "error" {
		t.Fatalf("expected H1 error history retained, got ok=%v entry=%#v", ok, entry)
	}
	for _, mv := range moveEvents {
		if mv.GID == gid {
			t.Fatalf("unexpected group task:move after refused transition: %#v", mv)
		}
	}
}
