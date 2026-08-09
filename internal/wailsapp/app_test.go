package wailsapp

import (
	"fmt"
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
		pauseErr:    fmt.Errorf("boom"),
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
