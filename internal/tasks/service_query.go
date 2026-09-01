package tasks

import (
	"goaria-v3/internal/config"
	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/history"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
)

func (s *Service) GetActiveTasks() map[string][]rpc.Task {
	active, waiting := monitor.Cache.GetLiveTaskLists()
	monitor.HydrateTaskGroups(active)
	monitor.HydrateTaskGroups(waiting)
	return map[string][]rpc.Task{
		"active":  active,
		"waiting": waiting,
	}
}

func (s *Service) GetStoppedTasks() []rpc.Task {
	if !config.Get().ShowHistory {
		return []rpc.Task{}
	}

	active, waiting, stopped := monitor.Cache.GetTaskLists()
	return s.StoppedTasksWithHistory(active, waiting, stopped)
}

// StoppedTasksWithHistory merges cache stopped with history-only rows, excluding
// any GID that is already live in active/waiting (or present in stopped).
func (s *Service) StoppedTasksWithHistory(active, waiting, stopped []rpc.Task) []rpc.Task {
	existingGIDs := make(map[string]struct{}, len(stopped)+len(active)+len(waiting))
	for i := range active {
		if gid := active[i].GID; gid != "" {
			existingGIDs[gid] = struct{}{}
		}
	}
	for i := range waiting {
		if gid := waiting[i].GID; gid != "" {
			existingGIDs[gid] = struct{}{}
		}
	}
	for i := range stopped {
		existingGIDs[stopped[i].GID] = struct{}{}
		if stopped[i].DownloadGroup == nil {
			stopped[i].DownloadGroup = monitor.Cache.GetTaskGroup(stopped[i].GID)
			if stopped[i].DownloadGroup == nil {
				stopped[i].DownloadGroup = monitor.GetStoredTaskGroup(stopped[i].GID)
			}
		}
		if entry, ok := history.Get(stopped[i].GID); ok {
			s.backfillStoppedTaskFromHistory(&stopped[i], entry)
		}
		stopped[i].TotalLength, stopped[i].CompletedLength = history.ProjectCompletedLengths(
			stopped[i].Status,
			stopped[i].TotalLength,
			stopped[i].CompletedLength,
		)
	}

	for _, entry := range history.GetMissingByGID(existingGIDs) {
		stopped = append(stopped, history.ToStoppedTask(entry))
		if entry.DownloadGroup != nil {
			monitor.RemoveTaskGroup(entry.GID)
		}
	}
	return stopped
}

func (s *Service) backfillStoppedTaskFromHistory(task *rpc.Task, entry history.HistoryEntry) {
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

func historySourceURIs(source string) []rpc.Uri {
	if source == "" {
		return []rpc.Uri{}
	}
	return []rpc.Uri{{Uri: source}}
}

func isNonZeroLength(value string) bool {
	return value != "" && value != "0"
}

func (s *Service) GetTasks() map[string][]rpc.Task {
	active, waiting, stoppedCache := monitor.Cache.GetTaskLists()
	monitor.HydrateTaskGroups(active)
	monitor.HydrateTaskGroups(waiting)
	var stopped []rpc.Task
	if config.Get().ShowHistory {
		stopped = s.StoppedTasksWithHistory(active, waiting, stoppedCache)
	}
	return map[string][]rpc.Task{"active": active, "waiting": waiting, "stopped": stopped}
}

func (s *Service) GetTaskMetadata(gids []string) map[string]rpc.Task {
	result := make(map[string]rpc.Task)
	if len(gids) == 0 {
		return result
	}

	tasks, err := s.Engine.TellStatusMulti(gids, nil)
	if err == nil {
		for _, task := range tasks {
			if task.DownloadGroup == nil {
				task.DownloadGroup = monitor.Cache.GetTaskGroup(task.GID)
				if task.DownloadGroup == nil {
					task.DownloadGroup = monitor.GetStoredTaskGroup(task.GID)
				}
			}
			result[task.GID] = task
		}
	}
	return result
}
