package rpc

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"goaria-v3/internal/config"
	"goaria-v3/internal/surge/core"
	"goaria-v3/internal/surge/download"
	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/processing"
)

type SurgeEngine struct {
	service *core.LocalDownloadService
	cleanup func()
}

func NewSurgeEngine() *SurgeEngine {
	progressCh := make(chan any, 256)
	maxDownloads := 3
	if config.Current != nil {
		if md, err := strconv.Atoi(config.Current.MaxConcurrentDownloads); err == nil && md > 0 {
			maxDownloads = md
		}
	}
	pool := download.NewWorkerPool(progressCh, maxDownloads)
	svc := core.NewLocalDownloadServiceWithInput(pool, progressCh)

	mgr := processing.NewLifecycleManager(svc.Add, svc.AddWithID)
	stream, eventCleanup, err := svc.StreamEvents(context.Background())
	if err != nil {
		log.Printf("[Surge] Failed to stream events: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		mgr.StartEventWorker(stream)
	}()

	svc.SetLifecycleHooks(core.LifecycleHooks{
		Pause:       mgr.Pause,
		Resume:      mgr.Resume,
		ResumeBatch: mgr.ResumeBatch,
		Cancel:      mgr.Cancel,
		UpdateURL:   mgr.UpdateURL,
	})

	mgr.SetEngineHooks(processing.EngineHooks{
		Pause:               svc.Pool.Pause,
		ExtractPausedConfig: svc.Pool.ExtractPausedConfig,
		GetStatus:           svc.Pool.GetStatus,
		AddConfig:           svc.Pool.Add,
		Cancel:              svc.Pool.Cancel,
		UpdateURL:           svc.Pool.UpdateURL,
		PublishEvent:        svc.Publish,
	})

	cleanup := func() {
		eventCleanup()
		_ = svc.Shutdown()
		wg.Wait()
	}

	return &SurgeEngine{
		service: svc,
		cleanup: cleanup,
	}
}

func mapStatus(s string) string {
	switch s {
	case "downloading", "pausing":
		return "active"
	case "queued", "paused":
		return "waiting"
	case "completed":
		return "complete"
	case "error":
		return "error"
	default:
		return s
	}
}

func convertTask(status types.DownloadStatus) Task {
	dir := filepath.Dir(status.DestPath)
	var errCode string
	if status.Error != "" || status.Status == "error" {
		errCode = "1"
	}
	return Task{
		GID:             status.ID,
		Title:           status.Filename,
		Status:          mapStatus(status.Status),
		TotalLength:     strconv.FormatInt(status.TotalSize, 10),
		CompletedLength: strconv.FormatInt(status.Downloaded, 10),
		DownloadSpeed:   strconv.FormatInt(int64(status.Speed*float64(types.MB)), 10),
		ErrorCode:       errCode,
		ErrorMessage:    status.Error,
		Dir:             dir,
		Files: []File{
			{
				Path: status.DestPath,
				Uris: []Uri{{Uri: status.URL, Status: "used"}},
			},
		},
	}
}

func (e *SurgeEngine) AddUri(url string, options AddURIOptions) (string, error) {
	headersMap := make(map[string]string)
	for _, h := range options.Headers {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) == 2 {
			headersMap[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return e.service.Add(url, options.Dir, options.Out, nil, headersMap, false, 0, false)
}

func (e *SurgeEngine) Pause(gid string) error {
	return e.service.Pause(gid)
}

func (e *SurgeEngine) Resume(gid string) error {
	return e.service.Resume(gid)
}

func (e *SurgeEngine) PauseMulti(gids []string) error {
	for _, gid := range gids {
		if err := e.service.Pause(gid); err != nil {
			return err
		}
	}
	return nil
}

func (e *SurgeEngine) ResumeMulti(gids []string) error {
	for _, gid := range gids {
		if err := e.service.Resume(gid); err != nil {
			return err
		}
	}
	return nil
}

func (e *SurgeEngine) Remove(gid string, deleteFile bool) error {
	if deleteFile {
		return e.service.Purge(gid)
	}
	return e.service.Delete(gid)
}

func (e *SurgeEngine) TellStatus(gid string, keys []string) (Task, error) {
	status, err := e.service.GetStatus(gid)
	if err != nil {
		return Task{}, err
	}
	if status == nil {
		return Task{}, fmt.Errorf("task not found")
	}
	return convertTask(*status), nil
}

func (e *SurgeEngine) TellStatusMulti(gids []string, keys []string) ([]Task, error) {
	res := make([]Task, 0, len(gids))
	for _, gid := range gids {
		status, err := e.service.GetStatus(gid)
		if err == nil && status != nil {
			res = append(res, convertTask(*status))
		}
	}
	return res, nil
}

func (e *SurgeEngine) TellActive() ([]Task, error) {
	list, err := e.service.List()
	if err != nil {
		return nil, err
	}
	var res []Task
	for _, s := range list {
		t := convertTask(s)
		if t.Status == "active" {
			res = append(res, t)
		}
	}
	return res, nil
}

func (e *SurgeEngine) TellActiveProgress() ([]TaskProgress, error) {
	list, err := e.service.List()
	if err != nil {
		return nil, err
	}
	var res []TaskProgress
	for _, s := range list {
		t := convertTask(s)
		if t.Status == "active" {
			res = append(res, TaskProgress{
				GID:             t.GID,
				CompletedLength: t.CompletedLength,
				DownloadSpeed:   t.DownloadSpeed,
			})
		}
	}
	return res, nil
}

func (e *SurgeEngine) TellWaiting(offset, num int) ([]Task, error) {
	list, err := e.service.List()
	if err != nil {
		return nil, err
	}
	var res []Task
	for _, s := range list {
		t := convertTask(s)
		if t.Status == "waiting" {
			res = append(res, t)
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(res) {
		return []Task{}, nil
	}
	end := offset + num
	if end > len(res) || num <= 0 {
		end = len(res)
	}
	return res[offset:end], nil
}

func (e *SurgeEngine) TellStopped(offset, num int) ([]Task, error) {
	list, err := e.service.List()
	if err != nil {
		return nil, err
	}
	var res []Task
	for _, s := range list {
		t := convertTask(s)
		if t.Status == "complete" || t.Status == "error" {
			res = append(res, t)
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(res) {
		return []Task{}, nil
	}
	end := offset + num
	if end > len(res) || num <= 0 {
		end = len(res)
	}
	return res[offset:end], nil
}

func (e *SurgeEngine) GetGlobalStat() (GlobalStat, error) {
	list, err := e.service.List()
	if err != nil {
		return GlobalStat{}, err
	}
	var totalSpeed int64
	for _, s := range list {
		t := convertTask(s)
		if t.Status == "active" {
			var sp int64
			if parsed, err := strconv.ParseInt(t.DownloadSpeed, 10, 64); err == nil {
				sp = parsed
			}
			totalSpeed += sp
		}
	}
	return GlobalStat{
		DownloadSpeed: strconv.FormatInt(totalSpeed, 10),
	}, nil
}

func (e *SurgeEngine) SaveSession() error {
	return nil
}

func (e *SurgeEngine) ChangeGlobalOption(options map[string]string) error {
	return nil
}

func (e *SurgeEngine) Close() {
	if e.cleanup != nil {
		e.cleanup()
	}
}
