package tasks

import (
	"path/filepath"
	"reflect"
	"testing"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

func setupAppTaskHistoryTest(t *testing.T) {
	t.Helper()

	originalCache := monitor.Cache
	originalSaveEnabled := history.SaveEnabled
	originalConfig := config.Get()

	monitor.ResetTaskGroupStoreForTest(filepath.Join(t.TempDir(), "download_groups.json"), true)
	monitor.Cache = monitor.NewTaskCacheForTest()
	history.DisableSaveForTest()
	history.Clear()
	config.SetTestConfig(&config.AppConfig{ShowHistory: true})

	t.Cleanup(func() {
		history.Clear()
		monitor.ResetTaskGroupStoreForTest("", true)
		history.SetSaveEnabled(originalSaveEnabled)
		monitor.Cache = originalCache
		config.SetTestConfig(originalConfig)
	})
}

func stoppedTasksFromGetTasks() []rpc.Task {
	return GetTasks()["stopped"]
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

func assertHistoryBackfillBehavior(t *testing.T, fetchStopped func() []rpc.Task) {
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
			setupAppTaskHistoryTest(t)

			monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{tc.cacheTask})
			history.Add(tc.historyEntry)

			stopped := fetchStopped()
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

func assertRichCachePreservationBehavior(t *testing.T, fetchStopped func() []rpc.Task) {
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
			setupAppTaskHistoryTest(t)

			monitor.Cache.UpdateFromAria2(nil, nil, []rpc.Task{tc.cacheTask})
			history.Add(tc.historyEntry)

			stopped := fetchStopped()
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



func TestGetStoppedTasks_BackfillsHistoryMetadataAndLengths(t *testing.T) {
	assertHistoryBackfillBehavior(t, GetStoppedTasks)
}

func TestGetTasks_BackfillsHistoryMetadataAndLengths(t *testing.T) {
	assertHistoryBackfillBehavior(t, stoppedTasksFromGetTasks)
}

func TestGetStoppedTasks_RespectsShowHistoryDisabled(t *testing.T) {
	setupAppTaskHistoryTest(t)
	config.Update(func(c *config.AppConfig) { c.ShowHistory = false })

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

	if stopped := GetStoppedTasks(); len(stopped) != 0 {
		t.Fatalf("expected no stopped tasks when history is disabled, got %#v", stopped)
	}
}

func TestGetTasks_RespectsShowHistoryDisabledAndKeepsActiveWaiting(t *testing.T) {
	setupAppTaskHistoryTest(t)
	config.Update(func(c *config.AppConfig) { c.ShowHistory = false })

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

	tasks := GetTasks()
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
	assertRichCachePreservationBehavior(t, GetStoppedTasks)
}

func TestGetTasks_PreservesRichCacheData(t *testing.T) {
	assertRichCachePreservationBehavior(t, stoppedTasksFromGetTasks)
}

func TestGetStoppedTasks_AppendsHistoryOnlyStoppedTasks(t *testing.T) {
	setupAppTaskHistoryTest(t)
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

	stopped := GetStoppedTasks()
	if got := countTasksByGID(stopped, entry.GID); got != 1 {
		t.Fatalf("expected gid %q once in stopped slice, got %d", entry.GID, got)
	}

	task := mustFindTaskByGID(t, stopped, entry.GID)
	assertHistoryOnlyStoppedTask(t, task, entry)
}

func TestGetTasks_AppendsHistoryOnlyStoppedTasks(t *testing.T) {
	setupAppTaskHistoryTest(t)
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

	tasks := GetTasks()
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
	setupAppTaskHistoryTest(t)
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

	task := mustFindTaskByGID(t, GetStoppedTasks(), cacheTask.GID)
	if task.DownloadGroup == nil || task.DownloadGroup.ID != group.ID {
		t.Fatalf("expected history group backfill, got %#v", task.DownloadGroup)
	}
}

func TestGetTasks_HistoryOnlySyntheticTaskIncludesDownloadGroup(t *testing.T) {
	setupAppTaskHistoryTest(t)
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

	task := mustFindTaskByGID(t, GetTasks()["stopped"], entry.GID)
	assertHistoryOnlyStoppedTask(t, task, entry)
	if task.DownloadGroup == nil || task.DownloadGroup.ID != group.ID {
		t.Fatalf("expected synthetic task group, got %#v", task.DownloadGroup)
	}
}

func TestGetTasks_ReloadsPersistedDownloadGroupForActiveTaskAfterRestart(t *testing.T) {
	setupAppTaskHistoryTest(t)
	group := appHistoryTestDownloadGroup("dg-restart")
	monitor.RegisterTaskGroup("gid-restart", *group)

	monitor.Cache = monitor.NewTaskCacheForTest()
	monitor.LoadTaskGroups()
	monitor.Cache.UpdateFromAria2([]rpc.Task{{GID: "gid-restart", Status: "active", Dir: group.Dir}}, nil, nil)

	tasks := GetTasks()
	active := mustFindTaskByGID(t, tasks["active"], "gid-restart")
	if active.DownloadGroup == nil || active.DownloadGroup.ID != group.ID {
		t.Fatalf("expected active task to hydrate persisted group after restart, got %#v", active.DownloadGroup)
	}
}

func TestGetTasks_HistoryBackfillRemovesCompletedGroupFromDurableStore(t *testing.T) {
	setupAppTaskHistoryTest(t)
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

	_ = GetTasks()
	if got := monitor.GetStoredTaskGroup("gid-completed"); got != nil {
		t.Fatalf("expected completed history group removed from durable store, got %#v", got)
	}
}

func GetTasks() map[string][]rpc.Task {
	svc := &Service{Engine: &rpc.Aria2Engine{}}
	return svc.GetTasks()
}

func GetStoppedTasks() []rpc.Task {
	svc := &Service{Engine: &rpc.Aria2Engine{}}
	return svc.GetStoppedTasks()
}
