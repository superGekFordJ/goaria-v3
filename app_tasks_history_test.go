package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

func setupAppTaskHistoryTest(t *testing.T) *App {
	t.Helper()

	originalCache := monitor.Cache
	originalSaveEnabled := history.SaveEnabled
	originalConfig := config.Current

	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	monitor.Cache = &monitor.TaskCache{}
	history.DisableSaveForTest()
	history.Clear()
	config.Current = &config.AppConfig{ShowHistory: true}

	t.Cleanup(func() {
		history.Clear()
		monitor.ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		monitor.Cache = originalCache
		config.Current = originalConfig
	})

	return NewApp()
}

func stoppedTasksFromGetTasks(app *App) []rpc.Task {
	return app.GetTasks()["stopped"]
}

func mustFindTaskByGID(t *testing.T, tasks []rpc.Task, gid string) rpc.Task {
	t.Helper()

	for _, task := range tasks {
		if task.GID == gid {
			return task
		}
	}

	t.Fatalf("expected task %q in stopped slice", gid)
	return rpc.Task{}
}

func countTasksByGID(tasks []rpc.Task, gid string) int {
	count := 0
	for _, task := range tasks {
		if task.GID == gid {
			count++
		}
	}
	return count
}

func assertTaskPathAndSource(t *testing.T, task rpc.Task, wantPath string, wantSource string) {
	t.Helper()

	if len(task.Files) == 0 {
		t.Fatalf("expected task %q to include files", task.GID)
	}
	if task.Files[0].Path != wantPath {
		t.Fatalf("expected task %q path %q, got %q", task.GID, wantPath, task.Files[0].Path)
	}
	if len(task.Files[0].Uris) == 0 {
		t.Fatalf("expected task %q to include source uris", task.GID)
	}
	if task.Files[0].Uris[0].Uri != wantSource {
		t.Fatalf("expected task %q source %q, got %q", task.GID, wantSource, task.Files[0].Uris[0].Uri)
	}
}

func assertHistoryBackfillBehavior(t *testing.T, fetchStopped func(*App) []rpc.Task) {
	t.Helper()

	testCases := []struct {
		name         string
		cacheTask    rpc.Task
		historyEntry history.HistoryEntry
		wantPath     string
	}{
		{
			name: "missing files",
			cacheTask: rpc.Task{
				GID:             "gid-missing-files",
				Status:          "error",
				TotalLength:     "0",
				CompletedLength: "0",
			},
			historyEntry: history.HistoryEntry{
				GID:             "gid-missing-files",
				Dir:             filepath.Join("history", "files"),
				Path:            filepath.Join("history", "files", "restored.bin"),
				Source:          "https://example.com/restored.bin",
				TotalLength:     "1024",
				CompletedLength: "1024",
			},
			wantPath: filepath.Join("history", "files", "restored.bin"),
		},
		{
			name: "missing uris only",
			cacheTask: rpc.Task{
				GID:             "gid-missing-uris",
				Status:          "error",
				TotalLength:     "0",
				CompletedLength: "0",
				Files:           []rpc.File{{Path: filepath.Join("cache", "existing.bin")}},
			},
			historyEntry: history.HistoryEntry{
				GID:             "gid-missing-uris",
				Dir:             filepath.Join("history", "uris"),
				Path:            filepath.Join("history", "uris", "different.bin"),
				Source:          "https://example.com/source.bin",
				TotalLength:     "2048",
				CompletedLength: "2048",
			},
			wantPath: filepath.Join("cache", "existing.bin"),
		},
		{
			name: "empty path with files present",
			cacheTask: rpc.Task{
				GID:             "gid-empty-path",
				Status:          "error",
				TotalLength:     "0",
				CompletedLength: "0",
				Files:           []rpc.File{{Path: ""}},
			},
			historyEntry: history.HistoryEntry{
				GID:             "gid-empty-path",
				Dir:             filepath.Join("history", "empty-path"),
				Path:            filepath.Join("history", "empty-path", "recovered.bin"),
				Source:          "https://example.com/recovered.bin",
				TotalLength:     "3072",
				CompletedLength: "3072",
			},
			wantPath: filepath.Join("history", "empty-path", "recovered.bin"),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			app := setupAppTaskHistoryTest(t)

			monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{tc.cacheTask})
			history.Add(tc.historyEntry)

			stopped := fetchStopped(app)
			if got := countTasksByGID(stopped, tc.cacheTask.GID); got != 1 {
				t.Fatalf("expected gid %q once in stopped slice, got %d", tc.cacheTask.GID, got)
			}

			task := mustFindTaskByGID(t, stopped, tc.cacheTask.GID)
			assertTaskPathAndSource(t, task, tc.wantPath, tc.historyEntry.Source)

			if task.TotalLength != tc.historyEntry.TotalLength {
				t.Fatalf("expected total length %q, got %q", tc.historyEntry.TotalLength, task.TotalLength)
			}
			if task.CompletedLength != tc.historyEntry.CompletedLength {
				t.Fatalf("expected completed length %q, got %q", tc.historyEntry.CompletedLength, task.CompletedLength)
			}
			if task.Status != tc.cacheTask.Status {
				t.Fatalf("expected status %q to be preserved, got %q", tc.cacheTask.Status, task.Status)
			}
		})
	}
}

func assertRichCachePreservationBehavior(t *testing.T, fetchStopped func(*App) []rpc.Task) {
	t.Helper()

	testCases := []struct {
		name          string
		cacheTask     rpc.Task
		historyEntry  history.HistoryEntry
		wantPath      string
		wantSource    string
		wantTotal     string
		wantCompleted string
		wantStatus    string
	}{
		{
			name: "different history metadata and non-zero lengths",
			cacheTask: rpc.Task{
				GID:             "gid-rich-different",
				Status:          "error",
				TotalLength:     "123",
				CompletedLength: "45",
				Dir:             filepath.Join("cache", "rich"),
				Files: []rpc.File{{
					Path: filepath.Join("cache", "rich", "cache.bin"),
					Uris: []rpc.Uri{{Uri: "https://cache.example/cache.bin"}},
				}},
			},
			historyEntry: history.HistoryEntry{
				GID:             "gid-rich-different",
				Dir:             filepath.Join("history", "rich"),
				Path:            filepath.Join("history", "rich", "different.bin"),
				Source:          "https://history.example/different.bin",
				TotalLength:     "999",
				CompletedLength: "888",
			},
			wantPath:      filepath.Join("cache", "rich", "cache.bin"),
			wantSource:    "https://cache.example/cache.bin",
			wantTotal:     "123",
			wantCompleted: "45",
			wantStatus:    "error",
		},
		{
			name: "empty history metadata and blank lengths",
			cacheTask: rpc.Task{
				GID:             "gid-rich-empty",
				Status:          "removed",
				TotalLength:     "0",
				CompletedLength: "0",
				Dir:             filepath.Join("cache", "empty"),
				Files: []rpc.File{{
					Path: filepath.Join("cache", "empty", "cache.bin"),
					Uris: []rpc.Uri{{Uri: "https://cache.example/empty.bin"}},
				}},
			},
			historyEntry: history.HistoryEntry{
				GID:             "gid-rich-empty",
				Dir:             filepath.Join("history", "empty"),
				Path:            "",
				Source:          "",
				TotalLength:     "",
				CompletedLength: "",
			},
			wantPath:      filepath.Join("cache", "empty", "cache.bin"),
			wantSource:    "https://cache.example/empty.bin",
			wantTotal:     "0",
			wantCompleted: "0",
			wantStatus:    "removed",
		},
		{
			name: "zero history lengths",
			cacheTask: rpc.Task{
				GID:             "gid-rich-zero",
				Status:          "complete",
				TotalLength:     "0",
				CompletedLength: "0",
				Dir:             filepath.Join("cache", "zero"),
				Files: []rpc.File{{
					Path: filepath.Join("cache", "zero", "cache.bin"),
					Uris: []rpc.Uri{{Uri: "https://cache.example/zero.bin"}},
				}},
			},
			historyEntry: history.HistoryEntry{
				GID:             "gid-rich-zero",
				Dir:             filepath.Join("history", "zero"),
				Path:            filepath.Join("history", "zero", "zero.bin"),
				Source:          "https://history.example/zero.bin",
				TotalLength:     "0",
				CompletedLength: "0",
			},
			wantPath:      filepath.Join("cache", "zero", "cache.bin"),
			wantSource:    "https://cache.example/zero.bin",
			wantTotal:     "0",
			wantCompleted: "0",
			wantStatus:    "complete",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			app := setupAppTaskHistoryTest(t)

			monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{tc.cacheTask})
			history.Add(tc.historyEntry)

			stopped := fetchStopped(app)
			if got := countTasksByGID(stopped, tc.cacheTask.GID); got != 1 {
				t.Fatalf("expected gid %q once in stopped slice, got %d", tc.cacheTask.GID, got)
			}

			task := mustFindTaskByGID(t, stopped, tc.cacheTask.GID)
			assertTaskPathAndSource(t, task, tc.wantPath, tc.wantSource)

			if task.TotalLength != tc.wantTotal {
				t.Fatalf("expected total length %q, got %q", tc.wantTotal, task.TotalLength)
			}
			if task.CompletedLength != tc.wantCompleted {
				t.Fatalf("expected completed length %q, got %q", tc.wantCompleted, task.CompletedLength)
			}
			if task.Status != tc.wantStatus {
				t.Fatalf("expected status %q to be preserved, got %q", tc.wantStatus, task.Status)
			}
		})
	}
}

func assertHistoryOnlyStoppedTask(t *testing.T, task rpc.Task, entry history.HistoryEntry) {
	t.Helper()

	if task.GID != entry.GID {
		t.Fatalf("expected gid %q, got %q", entry.GID, task.GID)
	}
	if task.Status != "complete" {
		t.Fatalf("expected synthetic history task status %q, got %q", "complete", task.Status)
	}
	if task.Dir != entry.Dir {
		t.Fatalf("expected dir %q, got %q", entry.Dir, task.Dir)
	}
	if task.TotalLength != entry.TotalLength {
		t.Fatalf("expected total length %q, got %q", entry.TotalLength, task.TotalLength)
	}
	if task.CompletedLength != entry.CompletedLength {
		t.Fatalf("expected completed length %q, got %q", entry.CompletedLength, task.CompletedLength)
	}
	assertTaskPathAndSource(t, task, entry.Path, entry.Source)
}

func appHistoryTestDownloadGroup(id string) *rpc.DownloadGroup {
	return &rpc.DownloadGroup{
		ID:         id,
		Kind:       "batch",
		Name:       "Batch 2026-05-07 15-04-05",
		FolderName: "Batch 2026-05-07 15-04-05 " + id,
		Dir:        filepath.Join("history", id),
		ItemCount:  5,
		CreatedAt:  1778166245,
	}
}

func snapshotRPCResponse(result any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      "goaria",
		"result":  result,
	}
}

func TestGetStoppedTasks_BackfillsHistoryMetadataAndLengths(t *testing.T) {
	assertHistoryBackfillBehavior(t, func(app *App) []rpc.Task {
		return app.GetStoppedTasks()
	})
}

func TestGetTasks_BackfillsHistoryMetadataAndLengths(t *testing.T) {
	assertHistoryBackfillBehavior(t, stoppedTasksFromGetTasks)
}

func TestGetStoppedTasks_RespectsShowHistoryDisabled(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	config.Current.ShowHistory = false

	cacheTask := rpc.Task{
		GID:             "gid-disabled-cache",
		Status:          "complete",
		TotalLength:     "1",
		CompletedLength: "1",
		Files: []rpc.File{{
			Path: filepath.Join("cache", "disabled.bin"),
			Uris: []rpc.Uri{{Uri: "https://example.com/cache-disabled.bin"}},
		}},
	}
	historyEntry := history.HistoryEntry{
		GID:             "gid-disabled-history",
		Dir:             filepath.Join("history", "disabled"),
		Path:            filepath.Join("history", "disabled", "history.bin"),
		Source:          "https://example.com/history-disabled.bin",
		TotalLength:     "2",
		CompletedLength: "2",
	}

	monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{cacheTask})
	history.Add(historyEntry)

	if stopped := app.GetStoppedTasks(); len(stopped) != 0 {
		t.Fatalf("expected no stopped tasks when history is disabled, got %#v", stopped)
	}
}

func TestGetTasks_RespectsShowHistoryDisabledAndKeepsActiveWaiting(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	config.Current.ShowHistory = false

	activeTask := rpc.Task{
		GID:             "gid-disabled-active",
		Status:          "active",
		TotalLength:     "10",
		CompletedLength: "1",
		Dir:             filepath.Join("active", "disabled"),
		Files: []rpc.File{{
			Path: filepath.Join("active", "disabled", "active.bin"),
			Uris: []rpc.Uri{{Uri: "https://example.com/active-disabled.bin"}},
		}},
	}
	waitingTask := rpc.Task{
		GID:             "gid-disabled-waiting",
		Status:          "waiting",
		TotalLength:     "20",
		CompletedLength: "2",
		Dir:             filepath.Join("waiting", "disabled"),
		Files: []rpc.File{{
			Path: filepath.Join("waiting", "disabled", "waiting.bin"),
			Uris: []rpc.Uri{{Uri: "https://example.com/waiting-disabled.bin"}},
		}},
	}
	stoppedTask := rpc.Task{
		GID:             "gid-disabled-stopped",
		Status:          "complete",
		TotalLength:     "30",
		CompletedLength: "30",
		Dir:             filepath.Join("stopped", "disabled"),
		Files: []rpc.File{{
			Path: filepath.Join("stopped", "disabled", "stopped.bin"),
			Uris: []rpc.Uri{{Uri: "https://example.com/stopped-disabled.bin"}},
		}},
	}
	historyEntry := history.HistoryEntry{
		GID:             "gid-disabled-history-all",
		Dir:             filepath.Join("history", "disabled-all"),
		Path:            filepath.Join("history", "disabled-all", "history.bin"),
		Source:          "https://example.com/history-disabled-all.bin",
		TotalLength:     "40",
		CompletedLength: "40",
	}

	monitor.Cache.UpdateFromAria2([]rpc.Task{activeTask}, []rpc.Task{waitingTask}, []rpc.Task{stoppedTask})
	history.Add(historyEntry)

	tasks := app.GetTasks()
	if !reflect.DeepEqual(tasks["active"], []rpc.Task{activeTask}) {
		t.Fatalf("expected active tasks unchanged, got %#v", tasks["active"])
	}
	if !reflect.DeepEqual(tasks["waiting"], []rpc.Task{waitingTask}) {
		t.Fatalf("expected waiting tasks unchanged, got %#v", tasks["waiting"])
	}
	if stopped := tasks["stopped"]; len(stopped) != 0 {
		t.Fatalf("expected no stopped tasks when history is disabled, got %#v", stopped)
	}
}

func TestGetStoppedTasks_PreservesRichCacheData(t *testing.T) {
	assertRichCachePreservationBehavior(t, func(app *App) []rpc.Task {
		return app.GetStoppedTasks()
	})
}

func TestGetTasks_PreservesRichCacheData(t *testing.T) {
	assertRichCachePreservationBehavior(t, stoppedTasksFromGetTasks)
}

func TestGetStoppedTasks_AppendsHistoryOnlyStoppedTasks(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	entry := history.HistoryEntry{
		GID:             "gid-history-only-stopped",
		Dir:             filepath.Join("history", "only"),
		Path:            filepath.Join("history", "only", "history-only.bin"),
		Source:          "https://example.com/history-only.bin",
		TotalLength:     "4096",
		CompletedLength: "4096",
	}

	monitor.Cache.UpdateFromAria2(nil, nil, nil)
	history.Add(entry)

	stopped := app.GetStoppedTasks()
	if got := countTasksByGID(stopped, entry.GID); got != 1 {
		t.Fatalf("expected gid %q once in stopped slice, got %d", entry.GID, got)
	}

	task := mustFindTaskByGID(t, stopped, entry.GID)
	assertHistoryOnlyStoppedTask(t, task, entry)
}

func TestGetTasks_AppendsHistoryOnlyStoppedTasks(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	activeTask := rpc.Task{
		GID:             "gid-active",
		Status:          "active",
		TotalLength:     "10",
		CompletedLength: "1",
		Dir:             filepath.Join("active", "dir"),
		Files:           []rpc.File{{Path: filepath.Join("active", "dir", "active.bin")}},
	}
	waitingTask := rpc.Task{
		GID:             "gid-waiting",
		Status:          "waiting",
		TotalLength:     "20",
		CompletedLength: "2",
		Dir:             filepath.Join("waiting", "dir"),
		Files:           []rpc.File{{Path: filepath.Join("waiting", "dir", "waiting.bin")}},
	}
	entry := history.HistoryEntry{
		GID:             "gid-history-only-all",
		Dir:             filepath.Join("history", "all"),
		Path:            filepath.Join("history", "all", "history-only.bin"),
		Source:          "https://example.com/history-only-all.bin",
		TotalLength:     "8192",
		CompletedLength: "8192",
	}

	monitor.Cache.UpdateFromAria2([]rpc.Task{activeTask}, []rpc.Task{waitingTask}, nil)
	history.Add(entry)

	tasks := app.GetTasks()
	if !reflect.DeepEqual(tasks["active"], []rpc.Task{activeTask}) {
		t.Fatalf("expected active tasks unchanged, got %#v", tasks["active"])
	}
	if !reflect.DeepEqual(tasks["waiting"], []rpc.Task{waitingTask}) {
		t.Fatalf("expected waiting tasks unchanged, got %#v", tasks["waiting"])
	}
	if got := countTasksByGID(tasks["stopped"], entry.GID); got != 1 {
		t.Fatalf("expected gid %q once in stopped slice, got %d", entry.GID, got)
	}

	stoppedTask := mustFindTaskByGID(t, tasks["stopped"], entry.GID)
	assertHistoryOnlyStoppedTask(t, stoppedTask, entry)
}

func TestGetStoppedTasks_BackfillsDownloadGroupFromHistory(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	group := appHistoryTestDownloadGroup("dg-history-backfill")
	cacheTask := rpc.Task{GID: "gid-group-backfill", Status: "complete", TotalLength: "0", CompletedLength: "0"}
	entry := history.HistoryEntry{
		GID:             cacheTask.GID,
		Dir:             group.Dir,
		Path:            filepath.Join(group.Dir, "file.bin"),
		Source:          "https://example.com/group.bin",
		TotalLength:     "1024",
		CompletedLength: "1024",
		DownloadGroup:   group,
	}

	monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{cacheTask})
	history.Add(entry)

	task := mustFindTaskByGID(t, app.GetStoppedTasks(), cacheTask.GID)
	if task.DownloadGroup == nil || task.DownloadGroup.ID != group.ID {
		t.Fatalf("expected history group backfill, got %#v", task.DownloadGroup)
	}
}

func TestGetTasks_HistoryOnlySyntheticTaskIncludesDownloadGroup(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	group := appHistoryTestDownloadGroup("dg-history-only")
	entry := history.HistoryEntry{
		GID:             "gid-history-only-group",
		Dir:             group.Dir,
		Path:            filepath.Join(group.Dir, "file.bin"),
		Source:          "https://example.com/history-only-group.bin",
		TotalLength:     "2048",
		CompletedLength: "2048",
		DownloadGroup:   group,
	}
	history.Add(entry)

	task := mustFindTaskByGID(t, app.GetTasks()["stopped"], entry.GID)
	assertHistoryOnlyStoppedTask(t, task, entry)
	if task.DownloadGroup == nil || task.DownloadGroup.ID != group.ID {
		t.Fatalf("expected synthetic task group, got %#v", task.DownloadGroup)
	}
}

func TestGetTasks_ReloadsPersistedDownloadGroupForActiveTaskAfterRestart(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	group := appHistoryTestDownloadGroup("dg-restart")
	monitor.RegisterTaskGroup("gid-restart", *group)

	monitor.Cache = &monitor.TaskCache{}
	monitor.LoadTaskGroups()
	monitor.Cache.UpdateFromAria2([]rpc.Task{{GID: "gid-restart", Status: "active", Dir: group.Dir}}, nil, nil)

	tasks := app.GetTasks()
	active := mustFindTaskByGID(t, tasks["active"], "gid-restart")
	if active.DownloadGroup == nil || active.DownloadGroup.ID != group.ID {
		t.Fatalf("expected active task to hydrate persisted group after restart, got %#v", active.DownloadGroup)
	}
}

func TestGetFullSnapshot_UsesFreshRPCStateAndHydratesDownloadGroupsForWindowRestore(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	activeGroup := appHistoryTestDownloadGroup("dg-snapshot-fresh-active")
	waitingGroup := appHistoryTestDownloadGroup("dg-snapshot-fresh-waiting")
	stoppedGroup := appHistoryTestDownloadGroup("dg-snapshot-fresh-stopped")

	monitor.RegisterTaskGroup("gid-fresh-active", *activeGroup)
	monitor.RegisterTaskGroup("gid-fresh-waiting", *waitingGroup)
	monitor.Cache.UpdateFromAria2(
		[]rpc.Task{{
			GID:             "gid-stale-active",
			Status:          "active",
			TotalLength:     "999",
			CompletedLength: "111",
			DownloadSpeed:   "333",
			Dir:             activeGroup.Dir,
		}},
		[]rpc.Task{{
			GID:             "gid-stale-waiting",
			Status:          "waiting",
			TotalLength:     "888",
			CompletedLength: "222",
			DownloadSpeed:   "444",
			Dir:             waitingGroup.Dir,
		}},
		[]rpc.Task{{
			GID:             "gid-fresh-stopped",
			Status:          "complete",
			TotalLength:     "0",
			CompletedLength: "0",
		}},
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "aria2.tellActive":
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]map[string]any{{
				"gid":             "gid-fresh-active",
				"status":          "active",
				"totalLength":     "100",
				"completedLength": "25",
				"downloadSpeed":   "555",
				"errorCode":       "",
				"errorMessage":    "",
				"dir":             activeGroup.Dir,
				"files": []map[string]any{{
					"path": filepath.ToSlash(filepath.Join(activeGroup.Dir, "active.bin")),
					"uris": []map[string]any{{"uri": "https://example.com/fresh-active.bin"}},
				}},
			}}))
		case "aria2.tellWaiting":
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]map[string]any{{
				"gid":             "gid-fresh-waiting",
				"status":          "waiting",
				"totalLength":     "200",
				"completedLength": "40",
				"downloadSpeed":   "0",
				"errorCode":       "",
				"errorMessage":    "",
				"dir":             waitingGroup.Dir,
				"files": []map[string]any{{
					"path": filepath.ToSlash(filepath.Join(waitingGroup.Dir, "waiting.bin")),
					"uris": []map[string]any{{"uri": "https://example.com/fresh-waiting.bin"}},
				}},
			}}))
		case "aria2.tellStopped":
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]map[string]any{{
				"gid":             "gid-fresh-stopped",
				"status":          "complete",
				"totalLength":     "0",
				"completedLength": "0",
				"downloadSpeed":   "0",
				"errorCode":       "",
				"errorMessage":    "",
				"dir":             stoppedGroup.Dir,
				"files":           []map[string]any{},
			}}))
		default:
			_ = json.NewEncoder(w).Encode(snapshotRPCResponse([]any{}))
		}
	}))
	defer server.Close()

	parts := strings.Split(server.URL, ":")
	rpc.Init(parts[len(parts)-1], "secret")

	history.Add(history.HistoryEntry{
		GID:             "gid-fresh-stopped",
		Dir:             stoppedGroup.Dir,
		Path:            filepath.Join(stoppedGroup.Dir, "stopped.bin"),
		Source:          "https://example.com/snapshot-stopped.bin",
		TotalLength:     "300",
		CompletedLength: "300",
		DownloadGroup:   stoppedGroup,
	})

	snapshot := app.GetFullSnapshot()

	active := mustFindTaskByGID(t, snapshot.Tasks.Active, "gid-fresh-active")
	if active.DownloadGroup == nil || active.DownloadGroup.ID != activeGroup.ID {
		t.Fatalf("expected active snapshot task to hydrate group, got %#v", active.DownloadGroup)
	}
	if active.CompletedLength != "25" || active.DownloadSpeed != "555" {
		t.Fatalf("expected active snapshot to prefer fresh RPC values, got completed=%q speed=%q", active.CompletedLength, active.DownloadSpeed)
	}
	if countTasksByGID(snapshot.Tasks.Active, "gid-stale-active") != 0 {
		t.Fatalf("expected stale active cache task excluded from snapshot, got %#v", snapshot.Tasks.Active)
	}

	waiting := mustFindTaskByGID(t, snapshot.Tasks.Waiting, "gid-fresh-waiting")
	if waiting.DownloadGroup == nil || waiting.DownloadGroup.ID != waitingGroup.ID {
		t.Fatalf("expected waiting snapshot task to hydrate group, got %#v", waiting.DownloadGroup)
	}
	if waiting.CompletedLength != "40" {
		t.Fatalf("expected waiting snapshot to prefer fresh RPC values, got completed=%q", waiting.CompletedLength)
	}
	if countTasksByGID(snapshot.Tasks.Waiting, "gid-stale-waiting") != 0 {
		t.Fatalf("expected stale waiting cache task excluded from snapshot, got %#v", snapshot.Tasks.Waiting)
	}

	stopped := mustFindTaskByGID(t, snapshot.Tasks.Stopped, "gid-fresh-stopped")
	if stopped.DownloadGroup == nil || stopped.DownloadGroup.ID != stoppedGroup.ID {
		t.Fatalf("expected stopped snapshot task to backfill group, got %#v", stopped.DownloadGroup)
	}
	assertTaskPathAndSource(t, stopped, filepath.Join(stoppedGroup.Dir, "stopped.bin"), "https://example.com/snapshot-stopped.bin")
}

func TestGetTasks_HistoryBackfillRemovesCompletedGroupFromDurableStore(t *testing.T) {
	app := setupAppTaskHistoryTest(t)
	group := appHistoryTestDownloadGroup("dg-history-cleanup")
	monitor.RegisterTaskGroup("gid-completed", *group)
	history.Add(history.HistoryEntry{
		GID:             "gid-completed",
		Dir:             group.Dir,
		Path:            filepath.Join(group.Dir, "file.bin"),
		Source:          "https://example.com/completed.bin",
		TotalLength:     "1",
		CompletedLength: "1",
		DownloadGroup:   group,
	})

	_ = app.GetTasks()
	if got := monitor.GetStoredTaskGroup("gid-completed"); got != nil {
		t.Fatalf("expected completed history group removed from durable store, got %#v", got)
	}
}
