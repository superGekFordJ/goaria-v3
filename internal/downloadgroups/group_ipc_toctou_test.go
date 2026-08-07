package downloadgroups

import (
	"runtime"
	"sync"
	"testing"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

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
	wg.Add(1)
	go func() {
		defer wg.Done()
		result = PauseDownloadGroup(group.ID)
	}()
	<-entered

	// Terminal lands while PauseMulti is in flight.
	monitor.Cache.MoveTaskToStoppedWithError(gid, "error", "1", "rpc-terminal")
	if tr := monitor.State.GetTracker(); tr != nil {
		_ = tr.MarkCompleteFromEvent(gid, "error")
	}
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/pause_toctou.bin", Status: "error"})

	for i := 0; i < 32; i++ {
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
	hub, cleanup := mixedGroupTestMonitor(t)
	defer cleanup()

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
	wg.Add(1)
	go func() {
		defer wg.Done()
		result = ResumeDownloadGroup(group.ID)
	}()
	<-entered

	monitor.Cache.MoveTaskToStoppedWithError(gid, "error", "1", "rpc-terminal")
	if tr := monitor.State.GetTracker(); tr != nil {
		_ = tr.MarkCompleteFromEvent(gid, "error")
	}
	history.Add(history.HistoryEntry{GID: gid, Path: "/tmp/resume_toctou.bin", Status: "error"})

	for i := 0; i < 32; i++ {
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
