package downloadgroups

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

type appTaskRPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type appTaskMulticall struct {
	MethodName string            `json:"methodName"`
	Params     []json.RawMessage `json:"params"`
}

type groupOpsRPCRecorder struct {
	requests []appTaskRPCRequest
}

func mockBatchRemove(gids []string, deleteFiles bool) {
	seen := make(map[string]struct{}, len(gids))
	uniqueGIDs := make([]string, 0, len(gids))
	for _, gid := range gids {
		gid = strings.TrimSpace(gid)
		if gid == "" {
			continue
		}
		if _, exists := seen[gid]; exists {
			continue
		}
		seen[gid] = struct{}{}
		uniqueGIDs = append(uniqueGIDs, gid)
	}
	if len(uniqueGIDs) == 0 {
		return
	}

	for _, gid := range uniqueGIDs {
		rpc.Remove(gid)
	}
	history.RemoveMany(uniqueGIDs)
	for _, gid := range uniqueGIDs {
		if tracker := monitor.State.GetTracker(); tracker != nil {
			tracker.RemoveTask(gid)
		}
		// Mirror production cleanupRemovedTask: remove from Cache before
		// InvalidateTask so reads never see removed tasks.
		monitor.Cache.RemoveTask(gid)
		if mon := monitor.State.GetMonitor(); mon != nil {
			mon.InvalidateTask(gid)
		} else {
			monitor.Cache.InvalidateMetadata(gid)
		}
	}
}

func setupGroupOpsRPCTest(t *testing.T, handler func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any) *groupOpsRPCRecorder {
	t.Helper()
	setupDownloadGroupsTest(t)
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
	return recorder
}

func appTaskSuccessResponse(result any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      "goaria",
		"result":  result,
	}
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

func findOperationItem(t *testing.T, result DownloadGroupOperationResult, gid string) DownloadGroupOperationItemResult {
	t.Helper()
	for _, item := range result.Items {
		if item.GID == gid {
			return item
		}
	}
	t.Fatalf("expected item for gid %q in %#v", gid, result.Items)
	return DownloadGroupOperationItemResult{}
}

func requireOperationWarning(t *testing.T, warnings []DownloadGroupWarning, code string) DownloadGroupWarning {
	t.Helper()
	for _, warning := range warnings {
		if warning.Code == code {
			return warning
		}
	}
	t.Fatalf("expected warning %q in %#v", code, warnings)
	return DownloadGroupWarning{}
}

func assertNoOperationResultLeak(t *testing.T, result DownloadGroupOperationResult, disallowed ...string) {
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
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		switch req.Method {
		case "system.multicall":
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{"OK"}, []any{"OK"}})
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

	result := PauseDownloadGroup(group.ID)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 3 || result.Skipped != 2 || result.Failed != 0 || result.TotalTargets != 3 {
		t.Fatalf("unexpected pause result: %#v", result)
	}
	if got := recorder.multicallGIDs(t, "aria2.pause"); strings.Join(got, ",") != "gid-active-one,gid-active-two,gid-stale" {
		t.Fatalf("unexpected pause multicall gids: %#v", got)
	}
	if recorder.count("aria2.pause") != 0 || recorder.count("aria2.saveSession") != 1 {
		t.Fatalf("expected nested pause only and one saveSession, requests=%#v", recorder.requests)
	}
	if item := findOperationItem(t, result, "gid-waiting-paused"); item.Status != DownloadGroupOperationItemSkipped || item.Code != DownloadGroupOperationCodeAlreadyPaused {
		t.Fatalf("unexpected paused skip: %#v", item)
	}
	if item := findOperationItem(t, result, "gid-stopped"); item.Code != DownloadGroupOperationCodeTerminalState {
		t.Fatalf("unexpected stopped skip: %#v", item)
	}
	if item := findOperationItem(t, result, "gid-stale"); item.Status != DownloadGroupOperationItemSucceeded || item.Code != DownloadGroupOperationCodePaused {
		t.Fatalf("unexpected pending-start stale pause: %#v", item)
	}
	requireOperationWarning(t, result.Warnings, DownloadGroupWarningStaleGroup)
}

func TestResumeDownloadGroup_SkipsNonPausedAndReportsPartialFailures(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
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

	result := ResumeDownloadGroup(group.ID)
	if result.OK || !result.Found || result.Noop || result.Succeeded != 1 || result.Skipped != 3 || result.Failed != 1 || result.TotalTargets != 2 {
		t.Fatalf("unexpected resume result: %#v", result)
	}
	if got := recorder.multicallGIDs(t, "aria2.unpause"); strings.Join(got, ",") != "gid-paused-ok,gid-paused-fail" {
		t.Fatalf("unexpected resume multicall gids: %#v", got)
	}
	if item := findOperationItem(t, result, "gid-paused-ok"); item.Status != DownloadGroupOperationItemSucceeded || item.Code != DownloadGroupOperationCodeResumed {
		t.Fatalf("unexpected success item: %#v", item)
	}
	failed := findOperationItem(t, result, "gid-paused-fail")
	if failed.Status != DownloadGroupOperationItemFailed || failed.Code != DownloadGroupOperationCodeRPCError || failed.Message != "operation failed" {
		t.Fatalf("unexpected failed item: %#v", failed)
	}
	if item := findOperationItem(t, result, "gid-active"); item.Code != DownloadGroupOperationCodeAlreadyActive {
		t.Fatalf("unexpected active skip: %#v", item)
	}
	if item := findOperationItem(t, result, "gid-queued"); item.Code != DownloadGroupOperationCodeNotPaused {
		t.Fatalf("unexpected queued skip: %#v", item)
	}
	if !result.Refresh.Tasks || !result.Refresh.Groups || !result.Refresh.Detail {
		t.Fatalf("expected refresh hints for partial resume, got %#v", result.Refresh)
	}
	assertNoOperationResultLeak(t, result, "token=secret", "d:/private")
}

func TestDownloadGroupOperation_UnknownAndEmptyGroupKeyAreNoopWarnings(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		return appTaskSuccessResponse("OK")
	})

	for _, result := range []DownloadGroupOperationResult{
		PauseDownloadGroup("   "),
		ResumeDownloadGroup("missing"),
		RemoveDownloadGroup("missing", false, mockBatchRemove),
		OpenDownloadGroupFolder("missing"),
	} {
		if !result.OK || result.Found || !result.Noop || result.TotalTargets != 0 || !result.Refresh.Groups {
			t.Fatalf("unexpected unknown/empty result: %#v", result)
		}
		requireOperationWarning(t, result.Warnings, DownloadGroupOperationCodeGroupNotFound)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("expected no RPC for unknown/empty group keys, got %#v", recorder.requests)
	}
}

func TestDownloadGroupOperation_NoActionablePauseResumeIsNoopSuccess(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
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

	resumePaused := ResumeDownloadGroup(pausedGroup.ID)
	if resumePaused.Noop || !resumePaused.OK || resumePaused.Succeeded != 2 {
		t.Fatalf("expected paused group to be actionable for resume, got %#v", resumePaused)
	}
	monitor.Cache.UpdateFromAria2(nil,
		[]rpc.Task{groupReadTask("gid-paused-one", "paused", &pausedGroup, "100", "0", "0"), groupReadTask("gid-paused-two", "paused", &pausedGroup, "100", "0", "0")},
		[]rpc.Task{groupReadTask("gid-terminal-one", "complete", &terminalGroup, "100", "100", "0"), groupReadTask("gid-terminal-two", "error", &terminalGroup, "100", "50", "0")},
	)
	pausedNoop := PauseDownloadGroup(pausedGroup.ID)
	if !pausedNoop.OK || !pausedNoop.Noop || pausedNoop.Skipped != 2 || pausedNoop.TotalTargets != 0 {
		t.Fatalf("unexpected all-paused pause noop: %#v", pausedNoop)
	}
	requireOperationWarning(t, pausedNoop.Warnings, DownloadGroupOperationCodeNoActionableMembers)
	terminalNoop := ResumeDownloadGroup(terminalGroup.ID)
	if !terminalNoop.OK || !terminalNoop.Noop || terminalNoop.Skipped != 2 || terminalNoop.TotalTargets != 0 {
		t.Fatalf("unexpected terminal resume noop: %#v", terminalNoop)
	}
	requireOperationWarning(t, terminalNoop.Warnings, DownloadGroupOperationCodeNoActionableMembers)
	if recorder.count("system.multicall") != 1 {
		t.Fatalf("expected only resume paused group to call multicall once, requests=%#v", recorder.requests)
	}
}

func TestRemoveDownloadGroup_UsesBackendResolvedMembersAndCleansHistoryStoreCache(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
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

	result := RemoveDownloadGroup(group.ID, false, mockBatchRemove)
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
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		return appTaskSuccessResponse("OK")
	})
	group := groupReadTestGroup("dg-stale-only", 2)
	monitor.RegisterTaskGroup("gid-stale-one", group)
	monitor.RegisterTaskGroup("gid-stale-two", group)

	result := RemoveDownloadGroup(group.ID, false, mockBatchRemove)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 2 || result.TotalTargets != 2 {
		t.Fatalf("unexpected stale remove result: %#v", result)
	}
	for _, gid := range []string{"gid-stale-one", "gid-stale-two"} {
		item := findOperationItem(t, result, gid)
		if item.Code != DownloadGroupOperationCodeRemovedStale {
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
	physicalDir := t.TempDir()
	group := groupReadTestGroup("dg-open", 2)
	group.Dir = physicalDir
	group.FolderName = filepath.Base(physicalDir)
	monitor.Cache.UpdateFromAria2([]rpc.Task{groupReadTask("gid-open-one", "active", &group, "100", "10", "1")}, []rpc.Task{groupReadTask("gid-open-two", "waiting", &group, "100", "0", "0")}, nil)

	originalLauncher := OpenFolderLauncher
	var launched []string
	OpenFolderLauncher = func(dir string) error {
		launched = append(launched, dir)
		return nil
	}
	t.Cleanup(func() { OpenFolderLauncher = originalLauncher })

	result := OpenDownloadGroupFolder(group.ID)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 1 || len(launched) != 1 {
		t.Fatalf("unexpected open folder result=%#v launched=%#v", result, launched)
	}
	if launched[0] != filepath.Clean(physicalDir) {
		t.Fatalf("expected exact group dir launch, got %#v", launched[0])
	}
	assertNoOperationResultLeak(t, result, physicalDir)
}

func TestOpenDownloadGroupFolder_UsesImmutableDirWhenDisplayNameDiffersFromFolderName(t *testing.T) {
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

	originalLauncher := OpenFolderLauncher
	var launched []string
	OpenFolderLauncher = func(dir string) error {
		launched = append(launched, dir)
		return nil
	}
	t.Cleanup(func() { OpenFolderLauncher = originalLauncher })

	result := OpenDownloadGroupFolder(group.ID)
	if !result.OK || result.Succeeded != 1 || len(launched) != 1 {
		t.Fatalf("unexpected open result=%#v launched=%#v", result, launched)
	}
	if launched[0] != filepath.Clean(physicalDir) {
		t.Fatalf("expected immutable physical dir launch, got %#v", launched[0])
	}
	if group.FolderName != filepath.Base(physicalDir) || group.Dir != physicalDir || group.FolderName == group.Name {
		t.Fatalf("unexpected physical/display name coupling: group=%#v", group)
	}
	assertNoOperationResultLeak(t, result, physicalDir, "Smart Display Name")
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
		launcher   func(string) error
		disallowed []string
	}{
		{name: "unsafe uri", dir: "https://source.example.invalid/file?token=secret", wantCode: DownloadGroupOperationCodeFolderUnsafe, disallowed: []string{"source.example", "token=secret"}},
		{name: "missing", dir: "", wantCode: DownloadGroupOperationCodeFolderUnavailable},
		{name: "secret segment", dir: secretPath, wantCode: DownloadGroupOperationCodeFolderUnsafe, disallowed: []string{secretPath, "token-secret-folder"}},
		{name: "launcher error", dir: launcherErrPath, wantCode: DownloadGroupOperationCodeOpenFailed, launcher: func(dir string) error {
			return errors.New("cannot open " + launcherErrPath + "?token=secret")
		}, disallowed: []string{launcherErrPath, "token=secret"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := groupReadTestGroup("dg-open-redact-"+strings.ReplaceAll(tt.name, " ", "-"), 2)
			group.Dir = tt.dir
			monitor.Cache.UpdateFromAria2([]rpc.Task{groupReadTask("gid-redact-one", "active", &group, "100", "0", "0")}, []rpc.Task{groupReadTask("gid-redact-two", "waiting", &group, "100", "0", "0")}, nil)
			originalLauncher := OpenFolderLauncher
			launcherCalls := 0
			OpenFolderLauncher = func(dir string) error {
				launcherCalls++
				if tt.launcher != nil {
					return tt.launcher(dir)
				}
				return nil
			}
			t.Cleanup(func() { OpenFolderLauncher = originalLauncher })

			result := OpenDownloadGroupFolder(group.ID)
			if result.OK || !result.Found || result.Failed != 1 || result.Succeeded != 0 {
				t.Fatalf("unexpected open failure result: %#v", result)
			}
			if result.Items[0].Code != tt.wantCode || result.Items[0].Message == "" {
				t.Fatalf("unexpected failure item: %#v", result.Items[0])
			}
			if tt.wantCode != DownloadGroupOperationCodeOpenFailed && launcherCalls != 0 {
				t.Fatalf("expected no launcher calls for preflight failure, got %d", launcherCalls)
			}
			if tt.wantCode == DownloadGroupOperationCodeOpenFailed && launcherCalls != 1 {
				t.Fatalf("expected one launcher call, got %d", launcherCalls)
			}
			assertNoOperationResultLeak(t, result, append(tt.disallowed, tt.dir)...)
		})
	}
}

func TestRemoveDownloadGroup_CleansUpFolder(t *testing.T) {
	tempBaseDir := t.TempDir()
	config.SetTestConfig(&config.AppConfig{
		DownloadDir: tempBaseDir,
	})
	defer func() {
		config.SetTestConfig(nil)
	}()

	group := groupReadTestGroup("dg-cleanup-test", 2)
	group.Dir = filepath.Join(tempBaseDir, "dg-cleanup-test-dir")
	group.FolderName = "dg-cleanup-test-dir"

	monitor.Cache.UpdateFromAria2(nil, nil, nil)
	task := groupReadTask("gid-cleanup-task", "complete", &group, "100", "100", "0")
	monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{task})
	monitor.RegisterTaskGroup(task.GID, group)

	t.Run("empty directory, deleteFiles=false", func(t *testing.T) {
		err := os.MkdirAll(group.Dir, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(group.Dir); os.IsNotExist(err) {
			t.Fatal("expected directory to exist")
		}

		result := RemoveDownloadGroup(group.ID, false, mockBatchRemove)
		if !result.OK {
			t.Fatalf("RemoveDownloadGroup failed: %#v", result)
		}

		// Wait to ensure no asynchronous task fires and deletes it
		time.Sleep(1600 * time.Millisecond)

		if _, err := os.Stat(group.Dir); os.IsNotExist(err) {
			t.Fatal("expected empty directory to NOT be cleaned up when deleteFiles=false")
		}
		_ = os.RemoveAll(group.Dir)
	})

	t.Run("empty directory, deleteFiles=true", func(t *testing.T) {
		err := os.MkdirAll(group.Dir, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(group.Dir); os.IsNotExist(err) {
			t.Fatal("expected directory to exist")
		}

		monitor.RegisterTaskGroup(task.GID, group)

		result := RemoveDownloadGroup(group.ID, true, mockBatchRemove)
		if !result.OK {
			t.Fatalf("RemoveDownloadGroup failed: %#v", result)
		}

		// Wait for goroutine cleanup
		time.Sleep(1600 * time.Millisecond)

		if _, err := os.Stat(group.Dir); !os.IsNotExist(err) {
			t.Fatal("expected empty directory to be cleaned up when deleteFiles=true")
		}
	})

	t.Run("non-empty directory, deleteFiles=true", func(t *testing.T) {
		err := os.MkdirAll(group.Dir, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		dummyFile := filepath.Join(group.Dir, "dummy.txt")
		err = os.WriteFile(dummyFile, []byte("hello"), 0o644)
		if err != nil {
			t.Fatal(err)
		}

		monitor.RegisterTaskGroup(task.GID, group)

		result := RemoveDownloadGroup(group.ID, true, mockBatchRemove)
		if !result.OK {
			t.Fatalf("RemoveDownloadGroup failed: %#v", result)
		}

		// Wait for goroutine cleanup
		time.Sleep(1600 * time.Millisecond)

		if _, err := os.Stat(group.Dir); os.IsNotExist(err) {
			t.Fatal("expected non-empty directory to be kept even when deleteFiles=true")
		}
		_ = os.RemoveAll(group.Dir)
	})
}

func TestPauseDownloadGroup_PatchesActiveSliceCounts(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		if req.Method == "system.multicall" {
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{"OK"}})
		}
		return appTaskSuccessResponse("OK")
	})
	group := groupReadTestGroup("dg-active-pause", 2)
	monitor.Cache.UpdateFromAria2(
		[]rpc.Task{
			groupReadTask("gid-active-one", "active", &group, "100", "10", "1"),
			groupReadTask("gid-active-two", "active", &group, "100", "20", "2"),
		},
		nil, nil,
	)
	monitor.RegisterTaskGroup("gid-active-one", group)
	monitor.RegisterTaskGroup("gid-active-two", group)

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.Counts.Active != 2 || card.Counts.Paused != 0 {
		t.Fatalf("pre-pause: expected 2 active 0 paused, got active=%d paused=%d", card.Counts.Active, card.Counts.Paused)
	}

	pauseResult := PauseDownloadGroup(group.ID)
	if !pauseResult.OK || pauseResult.Succeeded != 2 {
		t.Fatalf("pause failed: %#v", pauseResult)
	}
	if recorder.count("system.multicall") != 1 {
		t.Fatalf("expected 1 multicall, got %d", recorder.count("system.multicall"))
	}

	card = findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.Counts.Active != 0 || card.Counts.Paused != 2 {
		t.Fatalf("post-pause: expected 0 active 2 paused, got active=%d paused=%d", card.Counts.Active, card.Counts.Paused)
	}
}

func TestResumeDownloadGroup_PatchesWaitingSliceCounts(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		if req.Method == "system.multicall" {
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{"OK"}})
		}
		return appTaskSuccessResponse("OK")
	})
	group := groupReadTestGroup("dg-paused-resume", 2)
	monitor.Cache.UpdateFromAria2(nil,
		[]rpc.Task{
			groupReadTask("gid-paused-one", "paused", &group, "100", "10", "0"),
			groupReadTask("gid-paused-two", "paused", &group, "100", "20", "0"),
		},
		nil,
	)
	monitor.RegisterTaskGroup("gid-paused-one", group)
	monitor.RegisterTaskGroup("gid-paused-two", group)

	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.Counts.Paused != 2 || card.Counts.Active != 0 {
		t.Fatalf("pre-resume: expected 0 active 2 paused, got active=%d paused=%d", card.Counts.Active, card.Counts.Paused)
	}

	resumeResult := ResumeDownloadGroup(group.ID)
	if !resumeResult.OK || resumeResult.Succeeded != 2 {
		t.Fatalf("resume failed: %#v", resumeResult)
	}
	if recorder.count("system.multicall") != 1 {
		t.Fatalf("expected 1 multicall, got %d", recorder.count("system.multicall"))
	}

	card = findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.Counts.Active != 2 || card.Counts.Paused != 0 {
		t.Fatalf("post-resume: expected 2 active 0 paused, got active=%d paused=%d", card.Counts.Active, card.Counts.Paused)
	}
}

func TestPauseThenResume_ActiveSliceRoundTrip(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		if req.Method == "system.multicall" {
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{"OK"}})
		}
		return appTaskSuccessResponse("OK")
	})
	group := groupReadTestGroup("dg-roundtrip", 2)
	monitor.Cache.UpdateFromAria2(
		[]rpc.Task{
			groupReadTask("gid-rt-one", "active", &group, "100", "10", "1"),
			groupReadTask("gid-rt-two", "active", &group, "100", "20", "2"),
		},
		nil, nil,
	)
	monitor.RegisterTaskGroup("gid-rt-one", group)
	monitor.RegisterTaskGroup("gid-rt-two", group)

	pauseResult := PauseDownloadGroup(group.ID)
	if !pauseResult.OK || pauseResult.Succeeded != 2 {
		t.Fatalf("pause failed: %#v", pauseResult)
	}
	card := findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.Counts.Active != 0 || card.Counts.Paused != 2 {
		t.Fatalf("post-pause: expected 0 active 2 paused, got active=%d paused=%d", card.Counts.Active, card.Counts.Paused)
	}

	resumeResult := ResumeDownloadGroup(group.ID)
	if !resumeResult.OK || resumeResult.Succeeded != 2 {
		t.Fatalf("resume after pause should succeed, not be skipped: %#v", resumeResult)
	}
	if recorder.count("system.multicall") != 2 {
		t.Fatalf("expected 2 multicalls (pause+resume), got %d", recorder.count("system.multicall"))
	}
	card = findDownloadGroupCard(t, GetDownloadGroups().Groups, group.ID)
	if card.Counts.Active != 2 || card.Counts.Paused != 0 {
		t.Fatalf("post-resume: expected 2 active 0 paused, got active=%d paused=%d", card.Counts.Active, card.Counts.Paused)
	}
}

func TestPauseDownloadGroup_IncludesPendingStartStaleMembers(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		switch req.Method {
		case "system.multicall":
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{"OK"}, []any{"OK"}})
		case "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		default:
			return appTaskSuccessResponse("OK")
		}
	})
	group := groupReadTestGroup("dg-pending-stale", 5)
	active := groupReadTask("gid-pending-active", "active", &group, "100", "10", "1")
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, nil, nil)
	monitor.RegisterTaskGroup("gid-pending-stale-one", group)
	monitor.RegisterTaskGroup("gid-pending-stale-two", group)

	result := PauseDownloadGroup(group.ID)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 3 || result.Failed != 0 || result.TotalTargets != 3 {
		t.Fatalf("unexpected pause result: %#v", result)
	}
	pausedGIDs := recorder.multicallGIDs(t, "aria2.pause")
	expected := map[string]bool{"gid-pending-active": false, "gid-pending-stale-one": false, "gid-pending-stale-two": false}
	for _, gid := range pausedGIDs {
		expected[gid] = true
	}
	for gid, found := range expected {
		if !found {
			t.Fatalf("expected gid %q in pause multicall, got %#v", gid, pausedGIDs)
		}
	}
}

func TestPauseDownloadGroup_IncludesPendingStartStoppedMembers(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		switch req.Method {
		case "system.multicall":
			return appTaskSuccessResponse([]any{[]any{"OK"}, []any{"OK"}})
		case "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		default:
			return appTaskSuccessResponse("OK")
		}
	})
	group := groupReadTestGroup("dg-pending-stopped", 3)
	active := groupReadTask("gid-ps-active", "active", &group, "100", "10", "1")
	stopped := groupReadTask("gid-ps-stopped", "complete", &group, "100", "100", "0")
	monitor.Cache.UpdateFromAria2([]rpc.Task{active}, nil, []rpc.Task{stopped})
	monitor.RegisterTaskGroup("gid-ps-stopped", group)

	result := PauseDownloadGroup(group.ID)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 2 || result.Failed != 0 || result.TotalTargets != 2 {
		t.Fatalf("unexpected pause result: %#v", result)
	}
	pausedGIDs := recorder.multicallGIDs(t, "aria2.pause")
	expected := map[string]bool{"gid-ps-active": false, "gid-ps-stopped": false}
	for _, gid := range pausedGIDs {
		expected[gid] = true
	}
	for gid, found := range expected {
		if !found {
			t.Fatalf("expected gid %q in pause multicall, got %#v", gid, pausedGIDs)
		}
	}
}

func TestPauseDownloadGroup_PendingStartClearedAfterCacheUpdate(t *testing.T) {
	setupDownloadGroupsTest(t)
	group := groupReadTestGroup("dg-pending-clear", 2)
	monitor.RegisterTaskGroup("gid-pending-clear", group)
	if !monitor.Cache.IsPendingStart("gid-pending-clear") {
		t.Fatalf("expected gid to be pending-start after RegisterTaskGroup")
	}
	monitor.Cache.UpdateFromAria2(nil,
		[]rpc.Task{groupReadTask("gid-pending-clear", "waiting", &group, "100", "0", "0")},
		nil,
	)
	if monitor.Cache.IsPendingStart("gid-pending-clear") {
		t.Fatalf("expected gid to no longer be pending-start after cache update")
	}
}

// TestResumeDownloadGroup_SkipsPendingStartStoppedMembers verifies that
// pending-start stopped members are skipped on resume (terminal-state), unlike
// pause where they are included. Only the paused member should be resumed.
func TestResumeDownloadGroup_SkipsPendingStartStoppedMembers(t *testing.T) {
	recorder := setupGroupOpsRPCTest(t, func(req appTaskRPCRequest, recorder *groupOpsRPCRecorder) map[string]any {
		switch req.Method {
		case "system.multicall":
			return appTaskSuccessResponse([]any{[]any{"OK"}})
		case "aria2.saveSession":
			return appTaskSuccessResponse("OK")
		default:
			return appTaskSuccessResponse("OK")
		}
	})
	group := groupReadTestGroup("dg-resume-pending-stopped", 3)
	paused := groupReadTask("gid-rps-paused", "paused", &group, "100", "0", "0")
	stopped := groupReadTask("gid-rps-stopped", "complete", &group, "100", "100", "0")
	monitor.Cache.UpdateFromAria2(nil, []rpc.Task{paused}, []rpc.Task{stopped})
	monitor.RegisterTaskGroup("gid-rps-stopped", group)

	result := ResumeDownloadGroup(group.ID)
	if !result.OK || !result.Found || result.Noop || result.Succeeded != 1 || result.Failed != 0 || result.TotalTargets != 1 {
		t.Fatalf("unexpected resume result: %#v", result)
	}
	resumedGIDs := recorder.multicallGIDs(t, "aria2.unpause")
	if len(resumedGIDs) != 1 || resumedGIDs[0] != "gid-rps-paused" {
		t.Fatalf("expected only gid-rps-paused in resume multicall, got %#v", resumedGIDs)
	}
	if item := findOperationItem(t, result, "gid-rps-stopped"); item.Code != DownloadGroupOperationCodeTerminalState {
		t.Fatalf("expected terminal_state skip for pending-start stopped member on resume, got %#v", item)
	}
}
