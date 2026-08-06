package main

import (
	"goaria-v3/internal/downloadgroups"
	"goaria-v3/internal/monitor"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tasks"
)
type extractorAddTaskDispatcher = tasks.ExtractorAddTaskDispatcher
type extractorAuthRuntimeSourcePlanner = tasks.ExtractorAuthRuntimeSourcePlanner


// RecordTaskSpeed 已废弃 - 后端 TaskTracker 自动采集
// 保留空实现以兼容现有前端
func (a *App) RecordTaskSpeed(gid string, speed int64, cl int64) {
	// 业务逻辑已迁移到 monitor.TaskTracker
	// 此方法保留以兼容前端，但不执行任何操作
}

func (a *App) taskService() *tasks.Service {
	return &tasks.Service{
		Dispatcher: a.extractorDispatcher,
		Runtime:    a.hostAuthRuntimeForTaskFlow(),
		Engine:     a.downloadEngine,
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

// PauseTask pauses a download task
func (a *App) PauseTask(gid string) {
	if mon := monitor.State.GetMonitor(); mon != nil {
		mon.BumpPauseResumeIntention(gid, monitor.PauseResumeIntentionPause)
	}
	_ = a.downloadEngine.Pause(gid)
}

// ResumeTask resumes a paused or stopped task. Returns an error when the engine
// rejects the resume so the frontend can avoid optimistic moves.
func (a *App) ResumeTask(gid string) error {
	mon := monitor.State.GetMonitor()
	if mon != nil {
		mon.BumpPauseResumeIntention(gid, monitor.PauseResumeIntentionResume)
	}
	err := a.downloadEngine.Resume(gid)
	if err != nil && mon != nil {
		mon.ClearPauseResumeIntention(gid)
	}
	return err
}

// BatchPause pauses multiple tasks
func (a *App) BatchPause(gids []string) {
	if mon := monitor.State.GetMonitor(); mon != nil {
		for _, gid := range gids {
			mon.BumpPauseResumeIntention(gid, monitor.PauseResumeIntentionPause)
		}
	}
	_ = a.downloadEngine.PauseMulti(gids)
}

// BatchResume resumes multiple tasks and returns per-GID results. Optimistic
// frontend moves must only apply to OK entries.
func (a *App) BatchResume(gids []string) []rpc.MultiCallItemResult {
	mon := monitor.State.GetMonitor()
	if mon != nil {
		for _, gid := range gids {
			mon.BumpPauseResumeIntention(gid, monitor.PauseResumeIntentionResume)
		}
	}

	results := a.resumeMultiResults(gids)
	if mon != nil {
		for _, item := range results {
			if !item.OK {
				mon.ClearPauseResumeIntention(item.GID)
			}
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
