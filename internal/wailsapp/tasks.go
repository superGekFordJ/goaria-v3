package wailsapp

import (
	"log"

	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tasks"
)

func (a *App) taskService() *tasks.Service {
	var adapter tasks.ExtractorAdapter
	if rt := a.getExtractorRuntime(); rt != nil {
		adapter = rt.currentTasksAdapter()
	}
	return &tasks.Service{
		Adapter: adapter,
		Engine:  a.downloadEngine,
		Deduper: a.deduper,
	}
}

// AddUri adds a new download task
// Returns "success" on success, "duplicate" if task already exists, or error message
func (a *App) AddUri(url string) string {
	return a.taskService().AddUri(url)
}

// BatchAddUri adds multiple download URLs in one batch.
func (a *App) BatchAddUri(urls []string) tasks.BatchAddResult {
	return a.taskService().BatchAddUri(urls)
}

// GetActiveTasks returns only active and waiting tasks (high-frequency channel)
func (a *App) GetActiveTasks() map[string][]rpc.Task {
	return a.taskService().GetActiveTasks()
}

func (a *App) GetActiveProgress() []rpc.TaskProgress {
	progress, err := a.downloadEngine.TellActiveProgress()
	if err != nil {
		return []rpc.TaskProgress{}
	}
	return progress
}

// GetStoppedTasks returns stopped tasks with history (low-frequency channel)
func (a *App) GetStoppedTasks() []rpc.Task {
	return a.taskService().GetStoppedTasks()
}

// GetTasks returns all tasks grouped by status
func (a *App) GetTasks() map[string][]rpc.Task {
	return a.taskService().GetTasks()
}

// GetTaskMetadata fetches detailed metadata for tasks with missing file paths
func (a *App) GetTaskMetadata(gids []string) map[string]rpc.Task {
	return a.taskService().GetTaskMetadata(gids)
}

// GetTaskDetails returns on-demand detail facts for the given GIDs from
// in-memory sources only (no engine RPC).
func (a *App) GetTaskDetails(gids []string) map[string]tasks.TaskDetailEnvelope {
	return a.taskService().GetTaskDetails(gids)
}

// PauseTask pauses a download task. The engine RPC is the only ordering
// authority: local monitor state is applied solely after a successful
// acknowledgement; a failed call leaves cache/tracker untouched.
func (a *App) PauseTask(gid string) {
	if err := a.downloadEngine.Pause(gid); err != nil {
		log.Printf("[App] Pause %s failed: %v", gid, err)
		return
	}
	monitor.State.GetMonitor().ApplyPause(gid)
}

// ResumeTask resumes a paused or stopped task. Returns an error when the engine
// rejects the resume so the frontend can avoid optimistic moves.
func (a *App) ResumeTask(gid string) error {
	err := a.downloadEngine.Resume(gid)
	if err == nil {
		monitor.State.GetMonitor().ApplyResume(gid)
	}
	return err
}

// BatchPause pauses multiple tasks. Only per-item OK results apply local
// state; failed or missing results never mutate cache/tracker.
func (a *App) BatchPause(gids []string) {
	requested := make(map[string]bool, len(gids))
	for _, gid := range gids {
		requested[gid] = true
	}
	var results []rpc.MultiCallItemResult
	var err error
	if engine, ok := a.downloadEngine.(interface {
		PauseMultiResults([]string) ([]rpc.MultiCallItemResult, error)
	}); ok {
		results, err = engine.PauseMultiResults(gids)
	} else {
		err = a.downloadEngine.PauseMulti(gids)
		if err == nil {
			for _, gid := range gids {
				results = append(results, rpc.MultiCallItemResult{GID: gid, OK: true})
			}
		}
	}
	if err != nil {
		log.Printf("[App] BatchPause failed: %v", err)
		results = nil
	}
	mon := monitor.State.GetMonitor()
	seen := make(map[string]bool, len(results))
	applied := make(map[string]bool, len(results))
	for _, item := range results {
		seen[item.GID] = true
		if item.OK && requested[item.GID] && !applied[item.GID] {
			mon.ApplyPause(item.GID)
			applied[item.GID] = true
		}
	}
	if err == nil {
		for gid := range requested {
			if !seen[gid] {
				log.Printf("[App] Pause %s failed: missing batch result", gid)
			}
		}
	}
	for _, item := range results {
		if !item.OK {
			log.Printf("[App] Pause %s failed: %s", item.GID, item.Error)
		}
	}
}

// BatchResume resumes multiple tasks and returns per-GID results. Optimistic
// frontend moves must only apply to OK entries.
func (a *App) BatchResume(gids []string) []rpc.MultiCallItemResult {
	requested := make(map[string]bool, len(gids))
	for _, gid := range gids {
		requested[gid] = true
	}
	results := a.resumeMultiResults(gids)
	mon := monitor.State.GetMonitor()
	applied := make(map[string]bool, len(results))
	for _, item := range results {
		if item.OK && requested[item.GID] && !applied[item.GID] {
			mon.ApplyResume(item.GID)
			applied[item.GID] = true
		}
	}
	return results
}

func (a *App) resumeMultiResults(gids []string) []rpc.MultiCallItemResult {
	type multiResumer interface {
		ResumeMultiResults(gids []string) ([]rpc.MultiCallItemResult, error)
	}
	if engine, ok := a.downloadEngine.(multiResumer); ok {
		results, err := engine.ResumeMultiResults(gids)
		if err != nil {
			out := make([]rpc.MultiCallItemResult, 0, len(gids))
			for _, gid := range gids {
				out = append(out, rpc.MultiCallItemResult{GID: gid, OK: false, Error: err.Error()})
			}
			return out
		}
		return results
	}

	err := a.downloadEngine.ResumeMulti(gids)
	out := make([]rpc.MultiCallItemResult, 0, len(gids))
	for _, gid := range gids {
		item := rpc.MultiCallItemResult{GID: gid, OK: err == nil}
		if err != nil {
			item.Error = err.Error()
		}
		out = append(out, item)
	}
	return out
}

// BatchRemove removes multiple tasks
func (a *App) BatchRemove(gids []string, deleteFiles bool) {
	a.taskService().BatchRemove(gids, deleteFiles)
}

// RemoveTask removes a task and optionally deletes the file
func (a *App) RemoveTask(gid string, deleteFile bool) {
	a.taskService().RemoveTask(gid, deleteFile)
}

// GetDownloadGroups returns all download groups (aggregated list)
func (a *App) GetDownloadGroups() downloadgroups.DownloadGroupListEnvelope {
	return downloadgroups.GetDownloadGroups()
}

// GetDownloadGroupDetail returns detailed split tasks and card information for a single group
func (a *App) GetDownloadGroupDetail(groupKey string) downloadgroups.DownloadGroupDetailEnvelope {
	return downloadgroups.GetDownloadGroupDetail(groupKey)
}

// PauseDownloadGroup pauses all active members of the specified group
func (a *App) PauseDownloadGroup(groupKey string) downloadgroups.DownloadGroupOperationResult {
	return downloadgroups.PauseDownloadGroup(groupKey)
}

// ResumeDownloadGroup resumes all paused members of the specified group
func (a *App) ResumeDownloadGroup(groupKey string) downloadgroups.DownloadGroupOperationResult {
	return downloadgroups.ResumeDownloadGroup(groupKey)
}

// RemoveDownloadGroup removes the group and its member tasks, and optionally deletes files
func (a *App) RemoveDownloadGroup(groupKey string, deleteFiles bool) downloadgroups.DownloadGroupOperationResult {
	return downloadgroups.RemoveDownloadGroup(groupKey, deleteFiles, a.BatchRemove)
}

// OpenDownloadGroupFolder opens the local directory of the specified group
func (a *App) OpenDownloadGroupFolder(groupKey string) downloadgroups.DownloadGroupOperationResult {
	return downloadgroups.OpenDownloadGroupFolder(groupKey)
}
