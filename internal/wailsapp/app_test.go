package wailsapp

import (
	"testing"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/rpc"
)

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
