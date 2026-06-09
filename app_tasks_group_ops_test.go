package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

type groupOpsRPCRecorder struct {
	requests []appTaskRPCRequest
}

func setupGroupOpsRPCTest(t *testing.T, handler func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any) (*App, *groupOpsRPCRecorder) {
	t.Helper()
	app := setupAppTaskHistoryTest(t)
	recorder := &groupOpsRPCRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var req appTaskRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.requests = append(recorder.requests, req)
		response := handler(req, recorder)
		if response == nil {
			response = appTaskSuccessResponse("OK")
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	parts := strings.Split(server.URL, ":")
	rpc.Init(parts[len(parts)-1], "secret")
	t.Cleanup(server.Close)
	return app, recorder
}

func (r *groupOpsRPCRecorder) count(method string) int {
	count := 0
	for _, req := range r.requests {
		if req.Method == method {
			count++
		}
	}
	return count
}

func (r *groupOpsRPCRecorder) multicallGIDs(t *testing.T, method string) []string {
	t.Helper()
	for _, req := range r.requests {
		if req.Method != "system.multicall" {
			continue
		}
		var calls []appTaskMulticall
		if err := json.Unmarshal(req.Params[0], &calls); err != nil {
			t.Fatalf("failed to decode multicall: %v", err)
		}
		gids := make([]string, 0, len(calls))
		for _, call := range calls {
			if call.MethodName != method {
				continue
			}
			var token string
			if err := json.Unmarshal(call.Params[0], &token); err != nil || token != "token:secret" {
				t.Fatalf("unexpected nested token %q err=%v", token, err)
			}
			var gid string
			if err := json.Unmarshal(call.Params[1], &gid); err != nil {
				t.Fatalf("failed to decode gid: %v", err)
			}
			gids = append(gids, gid)
		}
		return gids
	}
	return nil
}

func findOperationItem(t *testing.T, result downloadgroups.DownloadGroupOperationResult, gid string) downloadgroups.DownloadGroupOperationItemResult {
	t.Helper()
	for _, item := range result.Items {
		if item.GID == gid {
			return item
		}
	}
	t.Fatalf("expected item for gid %q in %#v", gid, result.Items)
	return downloadgroups.DownloadGroupOperationItemResult{}
}

func requireOperationWarning(t *testing.T, warnings []downloadgroups.DownloadGroupWarning, code string) downloadgroups.DownloadGroupWarning {
	t.Helper()
	for _, warning := range warnings {
		if warning.Code == code {
			return warning
		}
	}
	t.Fatalf("expected warning %q in %#v", code, warnings)
	return downloadgroups.DownloadGroupWarning{}
}

func assertNoOperationResultLeak(t *testing.T, result downloadgroups.DownloadGroupOperationResult, disallowed ...string) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal operation result: %v", err)
	}
	lower := strings.ToLower(string(data))
	for _, value := range disallowed {
		value = strings.ToLower(value)
		if value != "" && strings.Contains(lower, value) {
			t.Fatalf("operation result leaked %q: %s", value, data)
		}
	}
}

func TestPauseDownloadGroup_ResolvesGroupKeyTargetsAndUsesMulticall(t *testing.T) {
	app, recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		switch req.Method {
		case "system.multicall":
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{"OK"}})
		case "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		default:
			return appTaskSuccessResponse("OK")
		}
	})
	group := groupReadTestGroup("dg-pause", 5)
	activeOne := groupReadTask("gid-active-one", "active", &group, "100", "10", "1")
	activeTwo := groupReadTask("gid-active-two", "active", &group, "100", "20", "1")
	waitingPaused := groupReadTask("gid-waiting-paused", "paused", &group, "100", "0", "0")
	stopped := groupReadTask("gid-stopped", "complete", &group, "100", "100", "0")
	monitor.Cache.UpdateFromAria2([]rpc.Task{activeOne, activeTwo}, []rpc.Task{waitingPaused}, []rpc.Task{stopped})
	monitor.RegisterTaskGroup("gid-stale", group)

	result := app.PauseDownloadGroup(group.ID)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 2 || result.Skipped != 3 || result.Failed != 0 || result.TotalTargets != 2 {
		t.Fatalf("unexpected pause result: %#v", result)
	}
	if got := recorder.multicallGIDs(t, "aria2.pause"); strings.Join(got, ",") != "gid-active-one,gid-active-two" {
		t.Fatalf("unexpected pause multicall gids: %#v", got)
	}
	if recorder.count("aria2.pause") != 0 || recorder.count("aria2.saveSession") != 1 {
		t.Fatalf("expected nested pause only and one saveSession, requests=%#v", recorder.requests)
	}
	if item := findOperationItem(t, result, "gid-waiting-paused"); item.Status != downloadgroups.DownloadGroupOperationItemSkipped || item.Code != downloadgroups.DownloadGroupOperationCodeAlreadyPaused {
		t.Fatalf("unexpected paused skip: %#v", item)
	}
	if item := findOperationItem(t, result, "gid-stopped"); item.Code != downloadgroups.DownloadGroupOperationCodeTerminalState {
		t.Fatalf("unexpected stopped skip: %#v", item)
	}
	if item := findOperationItem(t, result, "gid-stale"); item.Code != downloadgroups.DownloadGroupOperationCodeStaleMember {
		t.Fatalf("unexpected stale skip: %#v", item)
	}
	requireOperationWarning(t, result.Warnings, downloadgroups.DownloadGroupWarningStaleGroup)
}

func TestResumeDownloadGroup_SkipsNonPausedAndReportsPartialFailures(t *testing.T) {
	app, recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		switch req.Method {
		case "system.multicall":
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{map[string]any{"code": 1, "message": "token=secret path D:/private failed"}}})
		case "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		default:
			return appTaskSuccessResponse("OK")
		}
	})
	group := groupReadTestGroup("dg-resume", 5)
	active := groupReadTask("gid-active", "active", &group, "100", "10", "1")
	pausedOK := groupReadTask("gid-paused-ok", "paused", &group, "100", "0", "0")
	pausedFail := groupReadTask("gid-paused-fail", "paused", &group, "100", "0", "0")
	queued := groupReadTask("gid-queued", "waiting", &group, "100", "0", "0")
	stopped := groupReadTask("gid-terminal", "complete", &group, "100", "100", "0")
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, []rpc.Task{pausedOK, queued, pausedFail}, []rpc.Task{stopped})

	result := app.ResumeDownloadGroup(group.ID)
	if result.OK || !result.Found || result.Noop || result.Succeeded != 1 || result.Skipped != 3 || result.Failed != 1 || result.TotalTargets != 2 {
		t.Fatalf("unexpected resume result: %#v", result)
	}
	if got := recorder.multicallGIDs(t, "aria2.unpause"); strings.Join(got, ",") != "gid-paused-ok,gid-paused-fail" {
		t.Fatalf("unexpected resume multicall gids: %#v", got)
	}
	if item := findOperationItem(t, result, "gid-paused-ok"); item.Status != downloadgroups.DownloadGroupOperationItemSucceeded || item.Code != downloadgroups.DownloadGroupOperationCodeResumed {
		t.Fatalf("unexpected success item: %#v", item)
	}
	failed := findOperationItem(t, result, "gid-paused-fail")
	if failed.Status != downloadgroups.DownloadGroupOperationItemFailed || failed.Code != downloadgroups.DownloadGroupOperationCodeRPCError || failed.Message != "operation failed" {
		t.Fatalf("unexpected failed item: %#v", failed)
	}
	if item := findOperationItem(t, result, "gid-active"); item.Code != downloadgroups.DownloadGroupOperationCodeAlreadyActive {
		t.Fatalf("unexpected active skip: %#v", item)
	}
	if item := findOperationItem(t, result, "gid-queued"); item.Code != downloadgroups.DownloadGroupOperationCodeNotPaused {
		t.Fatalf("unexpected queued skip: %#v", item)
	}
	if !result.Refresh.Tasks || !result.Refresh.Groups || !result.Refresh.Detail {
		t.Fatalf("expected refresh hints for partial resume, got %#v", result.Refresh)
	}
	assertNoOperationResultLeak(t, result, "token=secret", "d:/private")
}

func TestDownloadGroupOperation_UnknownAndEmptyGroupKeyAreNoopWarnings(t *testing.T) {
	app, recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		return appTaskSuccessResponse("OK")
	})

	for _, result := range []downloadgroups.DownloadGroupOperationResult{app.PauseDownloadGroup("   "), app.ResumeDownloadGroup("missing"), app.RemoveDownloadGroup("missing", false), app.OpenDownloadGroupFolder("missing")} {
		if !result.OK || result.Found || !result.Noop || result.TotalTargets != 0 || !result.Refresh.Groups {
			t.Fatalf("unexpected unknown/empty result: %#v", result)
		}
		requireOperationWarning(t, result.Warnings, downloadgroups.DownloadGroupOperationCodeGroupNotFound)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expected no RPC for unknown/empty group keys, got %#v", recorder.requests)
	}
}

func TestDownloadGroupOperation_NoActionablePauseResumeIsNoopSuccess(t *testing.T) {
	app, recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		if req.Method == "system.multicall" {
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{"OK"}})
		}
		return appTaskSuccessResponse("OK")
	})
	pausedGroup := groupReadTestGroup("dg-all-paused", 2)
	terminalGroup := groupReadTestGroup("dg-terminal", 2)
	monitor.Cache.UpdateFromAria2(nil,
		[]rpc.Task{groupReadTask("gid-paused-one", "paused", &pausedGroup, "100", "0", "0"), groupReadTask("gid-paused-two", "paused", &pausedGroup, "100", "0", "0")},
		[]rpc.Task{groupReadTask("gid-terminal-one", "complete", &terminalGroup, "100", "100", "0"), groupReadTask("gid-terminal-two", "error", &terminalGroup, "100", "50", "0")},
	)

	resumePaused := app.ResumeDownloadGroup(pausedGroup.ID)
	if resumePaused.Noop || !resumePaused.OK || resumePaused.Succeeded != 2 {
		t.Fatalf("expected paused group to be actionable for resume, got %#v", resumePaused)
	}
	pausedNoop := app.PauseDownloadGroup(pausedGroup.ID)
	if !pausedNoop.OK || !pausedNoop.Noop || pausedNoop.Skipped != 2 || pausedNoop.TotalTargets != 0 {
		t.Fatalf("unexpected all-paused pause noop: %#v", pausedNoop)
	}
	requireOperationWarning(t, pausedNoop.Warnings, downloadgroups.DownloadGroupOperationCodeNoActionableMembers)
	terminalNoop := app.ResumeDownloadGroup(terminalGroup.ID)
	if !terminalNoop.OK || !terminalNoop.Noop || terminalNoop.Skipped != 2 || terminalNoop.TotalTargets != 0 {
		t.Fatalf("unexpected terminal resume noop: %#v", terminalNoop)
	}
	requireOperationWarning(t, terminalNoop.Warnings, downloadgroups.DownloadGroupOperationCodeNoActionableMembers)
	if recorder.count("system.multicall") != 1 {
		t.Fatalf("expected only resume paused group to call multicall once, requests=%#v", recorder.requests)
	}
}

func TestRemoveDownloadGroup_UsesBackendResolvedMembersAndCleansHistoryStoreCache(t *testing.T) {
	app, recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		return appTaskSuccessResponse("OK")
	})
	group := groupReadTestGroup("dg-remove", 3)
	unrelated := groupReadTestGroup("dg-keep", 2)
	active := groupReadTask("gid-remove-active", "active", &group, "100", "10", "1")
	waiting := groupReadTask("gid-remove-waiting", "waiting", &group, "100", "0", "0")
	stopped := groupReadTask("gid-remove-stopped", "complete", &group, "100", "100", "0")
	keep := groupReadTask("gid-keep", "active", &unrelated, "100", "0", "0")
	monitor.Cache.UpdateFromAria2([]rpc.Task{active, keep}, []rpc.Task{waiting}, []rpc.Task{stopped})
	monitor.RegisterTaskGroup(active.GID, group)
	monitor.RegisterTaskGroup(waiting.GID, group)
	monitor.RegisterTaskGroup(stopped.GID, group)
	monitor.RegisterTaskGroup(keep.GID, unrelated)
	history.Add(groupReadHistoryEntry(stopped.GID, &group, "100", "100"))
	keepHistoryDir := t.TempDir()
	history.Add(history.HistoryEntry{GID: "gid-history-keep", Dir: keepHistoryDir, Path: filepath.Join(keepHistoryDir, "keep.bin"), Source: "https://example.invalid/keep", TotalLength: "1", CompletedLength: "1"})

	result := app.RemoveDownloadGroup(group.ID, false)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 3 || result.TotalTargets != 3 {
		t.Fatalf("unexpected remove result: %#v", result)
	}
	if recorder.count("aria2.remove") != 3 || recorder.count("system.multicall") != 0 {
		t.Fatalf("expected per-gid remove and no multicall, requests=%#v", recorder.requests)
	}
	for _, gid := range []string{active.GID, waiting.GID, stopped.GID} {
		if _, ok := history.Get(gid); ok {
			t.Fatalf("expected history entry %q removed", gid)
		}
		if got := monitor.GetStoredTaskGroup(gid); got != nil {
			t.Fatalf("expected stored group for %q removed, got %#v", gid, got)
		}
	}
	if _, ok := history.Get("gid-history-keep"); !ok {
		t.Fatalf("expected unrelated history to remain")
	}
	if got := monitor.GetStoredTaskGroup(keep.GID); got == nil || got.ID != unrelated.ID {
		t.Fatalf("expected unrelated stored group to remain, got %#v", got)
	}
}

func TestRemoveDownloadGroup_RemovesStaleStoredMembersWithoutFrontendGIDs(t *testing.T) {
	app, recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		return appTaskSuccessResponse("OK")
	})
	group := groupReadTestGroup("dg-stale-only", 2)
	monitor.RegisterTaskGroup("gid-stale-one", group)
	monitor.RegisterTaskGroup("gid-stale-two", group)

	result := app.RemoveDownloadGroup(group.ID, false)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 2 || result.TotalTargets != 2 {
		t.Fatalf("unexpected stale remove result: %#v", result)
	}
	for _, gid := range []string{"gid-stale-one", "gid-stale-two"} {
		item := findOperationItem(t, result, gid)
		if item.Code != downloadgroups.DownloadGroupOperationCodeRemovedStale {
			t.Fatalf("expected stale metadata removal for %s, got %#v", gid, item)
		}
		if got := monitor.GetStoredTaskGroup(gid); got != nil {
			t.Fatalf("expected stale stored group %s removed, got %#v", gid, got)
		}
	}
	if recorder.count("aria2.remove") != 2 {
		t.Fatalf("expected remove accepted for stale gids, requests=%#v", recorder.requests)
	}
}

func TestOpenDownloadGroupFolder_UsesSafeGroupDirAndLauncherSeam(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	safeDir := t.TempDir()
	group := groupReadTestGroup("dg-open", 2)
	group.Dir = safeDir
	group.FolderName = filepath.Base(safeDir)
	monitor.Cache.UpdateFromAria2([]rpc.Task{groupReadTask("gid-open-one", "active", &group, "100", "10", "1")}, []rpc.Task{groupReadTask("gid-open-two", "waiting", &group, "100", "0", "0")}, nil)

	originalLauncher := openFolderLauncher
	var launched []openFolderLaunchTarget
	openFolderLauncher = func(target openFolderLaunchTarget) error {
		launched = append(launched, target)
		return nil
	}
	t.Cleanup(func() { openFolderLauncher = originalLauncher })

	result := app.OpenDownloadGroupFolder(group.ID)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 1 || len(launched) != 1 {
		t.Fatalf("unexpected open folder result=%#v launched=%#v", result, launched)
	}
	if launched[0].OpenDir != filepath.Clean(safeDir) || launched[0].SelectFile != "" {
		t.Fatalf("expected exact group dir launch, got %#v", launched[0])
	}
	assertNoOperationResultLeak(t, result, safeDir)
}

func TestOpenDownloadGroupFolder_UsesImmutableDirWhenDisplayNameDiffersFromFolderName(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	physicalDir := t.TempDir()
	group := groupReadTestGroup("dg-immutable-dir", 2)
	group.Name = "Smart Display Name"
	group.NameStatus = rpc.DownloadGroupNameStatusStable
	group.FolderName = filepath.Base(physicalDir)
	group.Dir = physicalDir
	monitor.Cache.UpdateFromAria2(
		[]rpc.Task{groupReadTask("gid-immutable-one", "active", &group, "100", "10", "1")},
		[]rpc.Task{groupReadTask("gid-immutable-two", "waiting", &group, "100", "0", "0")},
		nil,
	)

	originalLauncher := openFolderLauncher
	var launched []openFolderLaunchTarget
	openFolderLauncher = func(target openFolderLaunchTarget) error {
		launched = append(launched, target)
		return nil
	}
	t.Cleanup(func() { openFolderLauncher = originalLauncher })

	result := app.OpenDownloadGroupFolder(group.ID)
	if !result.OK || result.Succeeded != 1 || len(launched) != 1 {
		t.Fatalf("unexpected open result=%#v launched=%#v", result, launched)
	}
	if launched[0].OpenDir != filepath.Clean(physicalDir) || launched[0].SelectFile != "" {
		t.Fatalf("expected immutable physical dir launch, got %#v", launched[0])
	}
	if group.FolderName != filepath.Base(physicalDir) || group.Dir != physicalDir || group.FolderName == group.Name {
		t.Fatalf("unexpected physical/display name coupling: group=%#v", group)
	}
	assertNoOperationResultLeak(t, result, physicalDir, "Smart Display Name")
}

func TestOpenFolderCommandSpec_WindowsExplorerStartsWithoutWaiting(t *testing.T) {
	spec, ok := openFolderCommandSpecForGOOS("windows", openFolderLaunchTarget{OpenDir: `C:\Downloads\Batch 1`})
	if !ok {
		t.Fatalf("expected Windows folder command spec")
	}
	if spec.Name != "explorer.exe" || spec.Wait {
		t.Fatalf("expected explorer start semantics without wait, got %#v", spec)
	}
	if len(spec.Args) != 1 || spec.Args[0] != `C:\Downloads\Batch 1` {
		t.Fatalf("unexpected explorer args: %#v", spec.Args)
	}

	selectSpec, ok := openFolderCommandSpecForGOOS("windows", openFolderLaunchTarget{SelectFile: `C:\Downloads\Batch 1\file.bin`})
	if !ok || selectSpec.Name != "explorer.exe" || selectSpec.Wait {
		t.Fatalf("expected Windows select command to start without waiting, got ok=%v spec=%#v", ok, selectSpec)
	}
	if strings.Join(selectSpec.Args, "|") != `/select,|C:\Downloads\Batch 1\file.bin` {
		t.Fatalf("unexpected select args: %#v", selectSpec.Args)
	}
}

func TestOpenDownloadGroupFolder_RedactsUnsafeMissingAndLauncherErrors(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "token-secret-folder")
	if err := os.MkdirAll(secretPath, 0o755); err != nil {
		t.Fatalf("failed to create secret path fixture: %v", err)
	}
	launcherErrPath := t.TempDir()
	tests := []struct {
		name       string
		dir        string
		wantCode   string
		launcher   func(openFolderLaunchTarget) error
		disallowed []string
	}{
		{name: "unsafe uri", dir: "https://source.example.invalid/file?token=secret", wantCode: downloadgroups.DownloadGroupOperationCodeFolderUnsafe, disallowed: []string{"source.example", "token=secret"}},
		{name: "missing", dir: "", wantCode: downloadgroups.DownloadGroupOperationCodeFolderUnavailable},
		{name: "secret segment", dir: secretPath, wantCode: downloadgroups.DownloadGroupOperationCodeFolderUnsafe, disallowed: []string{secretPath, "token-secret-folder"}},
		{name: "launcher error", dir: launcherErrPath, wantCode: downloadgroups.DownloadGroupOperationCodeOpenFailed, launcher: func(openFolderLaunchTarget) error {
			return errors.New("cannot open " + launcherErrPath + "?token=secret")
		}, disallowed: []string{launcherErrPath, "token=secret"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := setupAppTaskHistoryTest(t)
			group := groupReadTestGroup("dg-open-redact-"+strings.ReplaceAll(tt.name, " ", "-"), 2)
			group.Dir = tt.dir
			monitor.Cache.UpdateFromAria2([]rpc.Task{groupReadTask("gid-redact-one", "active", &group, "100", "0", "0")}, []rpc.Task{groupReadTask("gid-redact-two", "waiting", &group, "100", "0", "0")}, nil)
			originalLauncher := openFolderLauncher
			launcherCalls := 0
			openFolderLauncher = func(target openFolderLaunchTarget) error {
				launcherCalls++
				if tt.launcher != nil {
					return tt.launcher(target)
				}
				return nil
			}
			t.Cleanup(func() { openFolderLauncher = originalLauncher })

			result := app.OpenDownloadGroupFolder(group.ID)
			if result.OK || !result.Found || result.Failed != 1 || result.Succeeded != 0 {
				t.Fatalf("unexpected open failure result: %#v", result)
			}
			if result.Items[0].Code != tt.wantCode || result.Items[0].Message == "" {
				t.Fatalf("unexpected failure item: %#v", result.Items[0])
			}
			if tt.wantCode != downloadgroups.DownloadGroupOperationCodeOpenFailed && launcherCalls != 0 {
				t.Fatalf("expected no launcher calls for preflight failure, got %d", launcherCalls)
			}
			if tt.wantCode == downloadgroups.DownloadGroupOperationCodeOpenFailed && launcherCalls != 1 {
				t.Fatalf("expected one launcher call, got %d", launcherCalls)
			}
			assertNoOperationResultLeak(t, result, append(tt.disallowed, tt.dir)...)
		})
	}
}
