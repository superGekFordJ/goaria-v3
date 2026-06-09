package main

import (
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tasks"
)

// RecordTaskSpeed 已废弃 - 后端 TaskTracker 自动采集
// 保留空实现以兼容现有前端
func (a *App) RecordTaskSpeed(gid string, speed int64, cl int64) {
	// 业务逻辑已迁移到 monitor.TaskTracker
	// 此方法保留以兼容前端，但不执行任何操作
}

// AddUri adds a new download task
// Returns "success" on success, "duplicate" if task already exists, or error message
func (a *App) AddUri(url string) string {
	svc := &tasks.Service{
		Dispatcher: a.extractorDispatcher,
		Runtime:    a.hostAuthRuntimeForTaskFlow(),
	}
	return svc.AddUri(url)
}

// BatchAddUri adds multiple download URLs in one batch.
func (a *App) BatchAddUri(urls []string) tasks.BatchAddResult {
	svc := &tasks.Service{
		Dispatcher: a.extractorDispatcher,
		Runtime:    a.hostAuthRuntimeForTaskFlow(),
	}
	return svc.BatchAddUri(urls)
}
}

// GetActiveTasks returns only active and waiting tasks (high-frequency channel)
func (a *App) GetActiveTasks() map[string][]rpc.Task {
	return tasks.GetActiveTasks()
}

func (a *App) GetActiveProgress() []rpc.TaskProgress {
	progress, err := rpc.TellActiveProgress()
	if err != nil {
		return []rpc.TaskProgress{}
	}
	return progress
}

// GetStoppedTasks returns stopped tasks with history (low-frequency channel)
func (a *App) GetStoppedTasks() []rpc.Task {
	return tasks.GetStoppedTasks()
}

// GetTasks returns all tasks grouped by status
func (a *App) GetTasks() map[string][]rpc.Task {
	return tasks.GetTasks()
}

// GetTaskMetadata fetches detailed metadata for tasks with missing file paths
func (a *App) GetTaskMetadata(gids []string) map[string]rpc.Task {
	return tasks.GetTaskMetadata(gids)
}

// PauseTask pauses a download task
func (a *App) PauseTask(gid string) {
	rpc.Pause(gid)
}

// ResumeTask resumes a paused task
func (a *App) ResumeTask(gid string) {
	rpc.Unpause(gid)
}

// BatchPause pauses multiple tasks
func (a *App) BatchPause(gids []string) {
	_ = rpc.PauseMulti(gids)
}

// BatchResume resumes multiple paused tasks
func (a *App) BatchResume(gids []string) {
	_ = rpc.UnpauseMulti(gids)
}

// BatchRemove removes multiple tasks
func (a *App) BatchRemove(gids []string, deleteFiles bool) {
	tasks.BatchRemove(gids, deleteFiles)
}

// RemoveTask removes a task and optionally deletes the file
func (a *App) RemoveTask(gid string, deleteFile bool) {
	tasks.RemoveTask(gid, deleteFile)
}
