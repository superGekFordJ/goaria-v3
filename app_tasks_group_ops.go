package main

import (
	"goaria-v3/internal/downloadgroups"
)

func (a *App) PauseDownloadGroup(groupKey string) downloadgroups.DownloadGroupOperationResult {
	return downloadgroups.PauseDownloadGroup(groupKey)
}

func (a *App) ResumeDownloadGroup(groupKey string) downloadgroups.DownloadGroupOperationResult {
	return downloadgroups.ResumeDownloadGroup(groupKey)
}

func (a *App) RemoveDownloadGroup(groupKey string, deleteFiles bool) downloadgroups.DownloadGroupOperationResult {
	return downloadgroups.RemoveDownloadGroup(groupKey, deleteFiles, a.BatchRemove)
}

func (a *App) OpenDownloadGroupFolder(groupKey string) downloadgroups.DownloadGroupOperationResult {
	return downloadgroups.OpenDownloadGroupFolder(groupKey)
}
