package tasks

import (
	"goaria-v3/internal/config"
	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

func GetActiveTasks() map[string][]rpc.Task {
	active := monitor.Cache.GetActive()
	waiting := monitor.Cache.GetWaiting()
	monitor.HydrateTaskGroups(active)
	monitor.HydrateTaskGroups(waiting)
	return map[string][]rpc.Task{
		"active":  active,
		"waiting": waiting,
	}
}

func GetStoppedTasks() []rpc.Task {
	if !config.Current.ShowHistory {
		return []rpc.Task{}
	}

	return StoppedTasksWithHistory(monitor.Cache.GetStopped())
}

func StoppedTasksWithHistory(stopped []rpc.Task) []rpc.Task {
	existingGIDs := make(map[string]struct{}, len(stopped))
	for i := range stopped {
		existingGIDs[stopped[i].GID] = struct{}{}
		if stopped[i].DownloadGroup == nil {
			stopped[i].DownloadGroup = monitor.Cache.GetTaskGroup(stopped[i].GID)
			if stopped[i].DownloadGroup == nil {
				stopped[i].DownloadGroup = monitor.GetStoredTaskGroup(stopped[i].GID)
			}
		}
		if entry, ok := history.Get(stopped[i].GID); ok {
			backfillStoppedTaskFromHistory(&stopped[i], entry)
		}
	}

	for _, entry := range history.GetMissingByGID(existingGIDs) {
		stopped = append(stopped, historyEntryToStoppedTask(entry))
		if entry.DownloadGroup != nil {
			monitor.RemoveTaskGroup(entry.GID)
		}
	}
	return stopped
}

func backfillStoppedTaskFromHistory(task *rpc.Task, entry history.HistoryEntry) {
	if task.DownloadGroup == nil && entry.DownloadGroup != nil {
		task.DownloadGroup = downloadgroups.CopyDownloadGroup(entry.DownloadGroup)
	}
	if entry.Path != "" && (len(task.Files) == 0 || task.Files[0].Path == "") {
		var uris []rpc.Uri
		if len(task.Files) > 0 && len(task.Files[0].Uris) > 0 {
			uris = task.Files[0].Uris
		} else {
			uris = historySourceURIs(entry.Source)
		}
		task.Files = []rpc.File{{Path: entry.Path, Uris: uris}}
	}
	if len(task.Files) > 0 && len(task.Files[0].Uris) == 0 && entry.Source != "" {
		task.Files[0].Uris = []rpc.Uri{{Uri: entry.Source}}
	}

	if task.TotalLength == "0" && isNonZeroLength(entry.TotalLength) {
		task.TotalLength = entry.TotalLength
	}
	if task.CompletedLength == "0" && isNonZeroLength(entry.CompletedLength) {
		task.CompletedLength = entry.CompletedLength
	}
}

func historyEntryToStoppedTask(entry history.HistoryEntry) rpc.Task {
	return rpc.Task{
		GID:             entry.GID,
		Status:          "complete",
		TotalLength:     entry.TotalLength,
		CompletedLength: entry.CompletedLength,
		Dir:             entry.Dir,
		Files:           []rpc.File{{Path: entry.Path, Uris: historySourceURIs(entry.Source)}},
		DownloadGroup:   downloadgroups.CopyDownloadGroup(entry.DownloadGroup),
	}
}

func historySourceURIs(source string) []rpc.Uri {
	if source == "" {
		return []rpc.Uri{}
	}
	return []rpc.Uri{{Uri: source}}
}

func isNonZeroLength(value string) bool {
	return value != "" && value != "0"
}

func GetTasks() map[string][]rpc.Task {
	active := monitor.Cache.GetActive()
	waiting := monitor.Cache.GetWaiting()
	monitor.HydrateTaskGroups(active)
	monitor.HydrateTaskGroups(waiting)
	var stopped []rpc.Task
	if config.Current.ShowHistory {
		stopped = StoppedTasksWithHistory(monitor.Cache.GetStopped())
	}
	return map[string][]rpc.Task{"active": active, "waiting": waiting, "stopped": stopped}
}

func GetTaskMetadata(gids []string) map[string]rpc.Task {
	result := make(map[string]rpc.Task)
	if len(gids) == 0 {
		return result
	}

	tasks, err := rpc.TellStatusMulti(gids)
	if err == nil {
		for _, task := range tasks {
			if task != nil {
				if task.DownloadGroup == nil {
					task.DownloadGroup = monitor.Cache.GetTaskGroup(task.GID)
					if task.DownloadGroup == nil {
						task.DownloadGroup = monitor.GetStoredTaskGroup(task.GID)
					}
				}
				result[task.GID] = *task
			}
		}
	}
	return result
}
