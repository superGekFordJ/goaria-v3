package main

import (
	"goaria-v3/internal/downloadgroups"
)

func (a *App) GetDownloadGroups() downloadgroups.DownloadGroupListEnvelope {
	return downloadgroups.GetDownloadGroups()
}

func (a *App) GetDownloadGroupDetail(groupKey string) downloadgroups.DownloadGroupDetailEnvelope {
	return downloadgroups.GetDownloadGroupDetail(groupKey)
}
