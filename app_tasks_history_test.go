package main

import (
	"path/filepath"
	"reflect"
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

	monitor.Cache = &monitor.TaskCache{}
	history.DisableSaveForTest()
	history.Clear()
	config.Current = &config.AppConfig{ShowHistory: true}

	t.Cleanup(func() {
		history.Clear()
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

func TestGetStoppedTasks_BackfillsHistoryMetadataAndLengths(t *testing.T) {
	assertHistoryBackfillBehavior(t, func(app *App) []rpc.Task {
		return app.GetStoppedTasks()
	})
}

func TestGetTasks_BackfillsHistoryMetadataAndLengths(t *testing.T) {
	assertHistoryBackfillBehavior(t, stoppedTasksFromGetTasks)
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
