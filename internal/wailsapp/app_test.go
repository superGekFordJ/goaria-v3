package wailsapp

import (
	"errors"
	"testing"
	"time"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/events"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

type controlledResumeEngine struct {
	rpc.DownloadEngine
	single func(string) error
	batch  func([]string) ([]rpc.MultiCallItemResult, error)
}

func (e *controlledResumeEngine) Resume(gid string) error { return e.single(gid) }
func (e *controlledResumeEngine) Pause(gid string) error  { return e.single(gid) }
func (e *controlledResumeEngine) ResumeMultiResults(gids []string) ([]rpc.MultiCallItemResult, error) {
	return e.batch(gids)
}

func (e *controlledResumeEngine) PauseMultiResults(gids []string) ([]rpc.MultiCallItemResult, error) {
	return e.batch(gids)
}

func requestTestMonitor(t *testing.T) *monitor.Monitor {
	t.Helper()
	oldCache, oldMon, oldTracker := monitor.Cache, monitor.State.GetMonitor(), monitor.State.GetTracker()
	monitor.Cache = monitor.NewTaskCacheForTest()
	mon := monitor.NewMonitorForTest(events.NewHub(nil))
	monitor.State.SetMonitor(mon)
	t.Cleanup(func() {
		monitor.Cache = oldCache
		monitor.State.SetMonitor(oldMon)
		monitor.State.SetTracker(oldTracker)
	})
	return mon
}

func TestPauseResumeRequests_InFlight(t *testing.T) {
	for _, change := range []string{"pause", "resume", "started", "terminal", "recreate", "failure", "old pause", "old pause failure"} {
		t.Run(change, func(t *testing.T) {
			mon := requestTestMonitor(t)
			const gid = "sg_request"
			monitor.Cache.AddSgTask(rpc.Task{GID: gid, Status: "paused", TotalLength: "100", CompletedLength: "30"}, "waiting")
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			engine := &controlledResumeEngine{single: func(string) error {
				close(entered)
				select {
				case <-release:
				case <-time.After(2 * time.Second):
					return errors.New("RPC barrier deadline")
				}
				if change == "failure" || change == "old pause failure" {
					return errors.New("old failure")
				}
				return nil
			}}
			app := &App{downloadEngine: engine}
			go func() {
				if change == "old pause" || change == "old pause failure" {
					app.PauseTask(gid)
					done <- nil
				} else {
					done <- app.ResumeTask(gid)
				}
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatal("request not entered")
			}
			var newer monitor.PauseResumeTicket
			want := "paused"
			switch change {
			case "old pause", "old pause failure":
				newer = mon.BeginPauseResumeRequest(gid, "resume")
				mon.ConfirmPauseResumeRequest(newer)
				want = "waiting"
			case "pause", "resume", "failure":
				action := change
				if change == "failure" {
					action = "pause"
				}
				newer = mon.BeginPauseResumeRequest(gid, action)
			case "started":
				monitor.Cache.MoveTaskToActive(gid, "active")
				want = "active"
			case "terminal":
				monitor.Cache.MoveTaskToStopped(gid, "error")
				monitor.State.GetTracker().MarkCompleteFromEvent(gid, "error")
				want = "error"
			case "recreate":
				monitor.State.GetTracker().RemoveTask(gid)
				monitor.Cache.AddSgTask(rpc.Task{GID: gid, Status: "paused"}, "waiting")
			}
			close(release)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("request stuck")
			}
			got, _, _ := monitor.Cache.GetTask(gid)
			if got.Status != want {
				t.Fatalf("late ack: %s want %s", got.Status, want)
			}
			if change == "pause" || change == "resume" || change == "failure" || change == "old pause" || change == "old pause failure" {
				if !mon.RunIfPauseResumeRequestCurrent(newer, func() {}) {
					t.Fatal("new request cleared")
				}
			}
		})
	}
}

func TestResumeTask_ConfirmsWaiting(t *testing.T) {
	requestTestMonitor(t)
	monitor.Cache.AddSgTask(rpc.Task{GID: "sg_ok", Status: "paused"}, "waiting")
	app := &App{downloadEngine: &controlledResumeEngine{single: func(string) error { return nil }}}
	if err := app.ResumeTask("sg_ok"); err != nil {
		t.Fatal(err)
	}
	got, list, _ := monitor.Cache.GetTask("sg_ok")
	if list != "waiting" || got.Status != "waiting" {
		t.Fatalf("ack: %#v", got)
	}
}

func TestBatchResume_ExplicitSuccessSet(t *testing.T) {
	for _, mode := range []string{"partial", "empty", "overall"} {
		t.Run(mode, func(t *testing.T) {
			mon := requestTestMonitor(t)
			for _, gid := range []string{"sg_ok", "sg_missing", "sg_failed", "sg_unknown"} {
				monitor.Cache.AddSgTask(rpc.Task{GID: gid, Status: "paused"}, "waiting")
			}
			var newer monitor.PauseResumeTicket
			app := &App{downloadEngine: &controlledResumeEngine{batch: func(gids []string) ([]rpc.MultiCallItemResult, error) {
				if len(gids) != 4 || gids[1] != "sg_ok" {
					t.Error("original duplicates changed")
				}
				newer = mon.BeginPauseResumeRequest("sg_missing", "pause")
				if mode == "empty" {
					return nil, nil
				}
				if mode == "overall" {
					return nil, errors.New("overall")
				}
				return []rpc.MultiCallItemResult{{GID: "sg_ok", OK: true}, {GID: "sg_ok", Error: "duplicate"}, {GID: "sg_unknown", OK: true}, {GID: "sg_failed", Error: "failed"}}, nil
			}}}
			results := app.BatchResume([]string{"sg_ok", "sg_ok", "sg_missing", "sg_failed"})
			if mode == "partial" && (len(results) != 4 || results[1].Error != "duplicate") {
				t.Fatal("result contract changed")
			}
			for _, gid := range []string{"sg_ok", "sg_missing", "sg_failed", "sg_unknown"} {
				got, _, _ := monitor.Cache.GetTask(gid)
				want := "paused"
				if gid == "sg_ok" && mode == "partial" {
					want = "waiting"
				}
				if got.Status != want {
					t.Fatalf("%s: %s", gid, got.Status)
				}
			}
			if !mon.RunIfPauseResumeRequestCurrent(newer, func() {}) {
				t.Fatal("missing clobbered newer")
			}
		})
	}
}

type downloadGroupDelegateTestEngine struct {
	rpc.DownloadEngine
	pauseGIDs  []string
	resumeGIDs []string
}

func (e *downloadGroupDelegateTestEngine) PauseMultiResults(gids []string) ([]rpc.MultiCallItemResult, error) {
	e.pauseGIDs = append([]string(nil), gids...)
	return []rpc.MultiCallItemResult{{GID: gids[0], OK: true}}, nil
}

func (e *downloadGroupDelegateTestEngine) ResumeMultiResults(gids []string) ([]rpc.MultiCallItemResult, error) {
	e.resumeGIDs = append([]string(nil), gids...)
	return []rpc.MultiCallItemResult{{GID: gids[0], OK: true}}, nil
}

func TestNewAppUsesProvidedEngineForDownloadGroupDelegates(t *testing.T) {
	origOpenFolder := downloadgroups.OpenFolderLauncher
	origPause := downloadgroups.PauseMultiResults
	origResume := downloadgroups.ResumeMultiResults
	t.Cleanup(func() {
		downloadgroups.OpenFolderLauncher = origOpenFolder
		downloadgroups.PauseMultiResults = origPause
		downloadgroups.ResumeMultiResults = origResume
	})

	engine := &downloadGroupDelegateTestEngine{}
	app := NewApp(Options{DownloadEngine: engine})
	if app.downloadEngine != engine {
		t.Fatal("App did not retain the provided engine")
	}

	pauseResults, err := downloadgroups.PauseMultiResults([]string{"pause-gid"})
	if err != nil || len(pauseResults) != 1 || !pauseResults[0].OK || len(engine.pauseGIDs) != 1 || engine.pauseGIDs[0] != "pause-gid" {
		t.Fatalf("PauseMultiResults did not use the provided engine: results=%#v err=%v calls=%#v", pauseResults, err, engine.pauseGIDs)
	}

	resumeResults, err := downloadgroups.ResumeMultiResults([]string{"resume-gid"})
	if err != nil || len(resumeResults) != 1 || !resumeResults[0].OK || len(engine.resumeGIDs) != 1 || engine.resumeGIDs[0] != "resume-gid" {
		t.Fatalf("ResumeMultiResults did not use the provided engine: results=%#v err=%v calls=%#v", resumeResults, err, engine.resumeGIDs)
	}
}

type baseDownloadEngineFake struct {
	*rpc.Aria2Engine
	pauseGIDs  []string
	resumeGIDs []string
	pauseErr   error
	resumeErr  error
}

func (e *baseDownloadEngineFake) PauseMulti(gids []string) error {
	e.pauseGIDs = append([]string(nil), gids...)
	return e.pauseErr
}

func (e *baseDownloadEngineFake) ResumeMulti(gids []string) error {
	e.resumeGIDs = append([]string(nil), gids...)
	return e.resumeErr
}

func TestNewAppBaseEngineFallbackRoutesThroughInjectedEngine(t *testing.T) {
	origOpenFolder := downloadgroups.OpenFolderLauncher
	origPause := downloadgroups.PauseMultiResults
	origResume := downloadgroups.ResumeMultiResults
	t.Cleanup(func() {
		downloadgroups.OpenFolderLauncher = origOpenFolder
		downloadgroups.PauseMultiResults = origPause
		downloadgroups.ResumeMultiResults = origResume
	})

	engine := &baseDownloadEngineFake{Aria2Engine: &rpc.Aria2Engine{}}
	app := NewApp(Options{DownloadEngine: engine})
	if app.downloadEngine != engine {
		t.Fatal("App did not retain the provided engine")
	}

	pauseResults, err := downloadgroups.PauseMultiResults([]string{"ar_1", "ar_2"})
	if err != nil {
		t.Fatalf("PauseMultiResults returned unexpected error: %v", err)
	}
	if len(engine.pauseGIDs) != 2 || engine.pauseGIDs[0] != "ar_1" || engine.pauseGIDs[1] != "ar_2" {
		t.Fatalf("PauseMultiResults did not route through injected engine PauseMulti: calls=%#v", engine.pauseGIDs)
	}
	if len(pauseResults) != 2 || !pauseResults[0].OK || !pauseResults[1].OK || pauseResults[0].GID != "ar_1" || pauseResults[1].GID != "ar_2" {
		t.Fatalf("PauseMultiResults produced wrong results: %#v", pauseResults)
	}

	resumeResults, err := downloadgroups.ResumeMultiResults([]string{"ar_3"})
	if err != nil {
		t.Fatalf("ResumeMultiResults returned unexpected error: %v", err)
	}
	if len(engine.resumeGIDs) != 1 || engine.resumeGIDs[0] != "ar_3" {
		t.Fatalf("ResumeMultiResults did not route through injected engine ResumeMulti: calls=%#v", engine.resumeGIDs)
	}
	if len(resumeResults) != 1 || !resumeResults[0].OK || resumeResults[0].GID != "ar_3" {
		t.Fatalf("ResumeMultiResults produced wrong results: %#v", resumeResults)
	}
}

func TestNewAppBaseEngineFallbackPropagatesErrors(t *testing.T) {
	origOpenFolder := downloadgroups.OpenFolderLauncher
	origPause := downloadgroups.PauseMultiResults
	origResume := downloadgroups.ResumeMultiResults
	t.Cleanup(func() {
		downloadgroups.OpenFolderLauncher = origOpenFolder
		downloadgroups.PauseMultiResults = origPause
		downloadgroups.ResumeMultiResults = origResume
	})

	engine := &baseDownloadEngineFake{
		Aria2Engine: &rpc.Aria2Engine{},
		pauseErr:    errors.New("boom"),
	}
	NewApp(Options{DownloadEngine: engine})

	results, err := downloadgroups.PauseMultiResults([]string{"ar_1", "ar_2"})
	if err != nil {
		t.Fatalf("wrapped fallback should not return top-level error: %v", err)
	}
	if len(results) != 2 || results[0].OK || results[1].OK || results[0].Error != "boom" || results[1].Error != "boom" {
		t.Fatalf("error results not propagated per-gid: %#v", results)
	}
}
