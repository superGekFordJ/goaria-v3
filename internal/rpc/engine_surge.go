package rpc

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/surge/core"
	"goaria-v3/internal/surge/download"
	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/processing"
)

type SurgeEngine struct {
	service *core.LocalDownloadService
	manager *processing.LifecycleManager
	cleanup func()

	listCacheMu sync.Mutex
	listCache   []types.DownloadStatus
	listCacheAt time.Time
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

	engineCtx, engineCancel := context.WithCancel(context.Background())

	stream, eventCleanup, err := svc.StreamEvents(engineCtx)
	if err != nil {
		log.Printf("[Surge] Failed to stream events: %v", err)
	}

	var wg sync.WaitGroup
	var spawned bool
	if err == nil && stream != nil {
		wg.Add(1)
		spawned = true
		go func() {
			defer wg.Done()
			mgr.StartEventWorker(stream)
		}()
	}

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
		// Shutdown first: PauseAll + wait for pause events to be published,
		// then close InputCh which drains the broadcaster and closes listener
		// channels, allowing the event worker to process final pause events
		// before the stream goroutine exits.
		_ = svc.Shutdown()
		engineCancel()
		if eventCleanup != nil {
			eventCleanup()
		}
		if spawned {
			wg.Wait()
		}
	}

	return &SurgeEngine{
		service: svc,
		manager: mgr,
		cleanup: cleanup,
	}
}

func (e *SurgeEngine) IsSurgeActive() bool {
	return e.service != nil
}

// SetResumeParamsHook injects the RecomputeResumeParams callback into the
// LifecycleManager's EngineHooks. This allows GoAria to recompute Workers
// and MinChunkSize on resume using current BBR/bandwidth state.
// Uses read-modify-write to preserve any other hooks set by callers.
func (e *SurgeEngine) SetResumeParamsHook(fn func(cfg *types.DownloadConfig)) {
	if e.manager == nil {
		return
	}
	hooks := e.manager.GetEngineHooks()
	hooks.RecomputeResumeParams = fn
	e.manager.SetEngineHooks(hooks)
}

// getDownloadList returns the Surge download list with a 1s TTL request-scoped
// cache. This avoids duplicate service.List() (Gob deserialization) when
// TellWaiting and TellStopped are called concurrently within the same tick.
// This is distinct from the removed SQLite-era cachedList/cacheValid which
// cached across ticks and caused stale data bugs.
func (e *SurgeEngine) getDownloadList() ([]types.DownloadStatus, error) {
	e.listCacheMu.Lock()
	if time.Since(e.listCacheAt) < 1*time.Second && e.listCache != nil {
		cached := e.listCache
		e.listCacheMu.Unlock()
		return cached, nil
	}
	e.listCacheMu.Unlock()

	list, err := e.service.List()
	if err != nil {
		return nil, err
	}

	e.listCacheMu.Lock()
	e.listCache = list
	e.listCacheAt = time.Now()
	e.listCacheMu.Unlock()
	return list, nil
}

func mapStatus(s string) string {
	switch s {
	case "downloading", "pausing":
		return "active"
	case "paused":
		return "paused"
	case "queued":
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
	req := &processing.DownloadRequest{
		URL:          url,
		Path:         options.Dir,
		Filename:     options.Out,
		Headers:      headersMap,
		Workers:      options.Split,
		MinChunkSize: options.MinSplitSize,
	}
	gid, _, err := e.manager.Enqueue(context.Background(), req)
	return gid, err
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
	configs := e.service.Pool.GetAll()
	var res []Task
	for _, cfg := range configs {
		status := e.service.Pool.GetStatus(cfg.ID)
		if status != nil && mapStatus(status.Status) == "active" {
			res = append(res, convertTask(*status))
		}
	}
	return res, nil
}

func (e *SurgeEngine) TellActiveProgress() ([]TaskProgress, error) {
	configs := e.service.Pool.GetAll()
	var res []TaskProgress
	for _, cfg := range configs {
		status := e.service.Pool.GetStatus(cfg.ID)
		if status != nil && mapStatus(status.Status) == "active" {
			t := convertTask(*status)
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
	list, err := e.getDownloadList()
	if err != nil {
		return nil, err
	}
	var res []Task
	for _, s := range list {
		t := convertTask(s)
		if t.Status == "waiting" || t.Status == "paused" {
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
	list, err := e.getDownloadList()
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
	active, err := e.TellActive()
	if err != nil {
		return GlobalStat{}, err
	}
	var totalSpeed int64
	for _, t := range active {
		if parsed, err := strconv.ParseInt(t.DownloadSpeed, 10, 64); err == nil {
			totalSpeed += parsed
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

func (e *SurgeEngine) TellActiveLite() ([]Task, error) {
	return e.TellActive()
}

func (e *SurgeEngine) TellWaitingLite(offset, num int) ([]Task, error) {
	return e.TellWaiting(offset, num)
}

func (e *SurgeEngine) TellStoppedLite(offset, num int) ([]Task, error) {
	return e.TellStopped(offset, num)
}

func (e *SurgeEngine) StreamEvents(ctx context.Context) (<-chan any, func(), error) {
	return e.service.StreamEvents(ctx)
}

func (e *SurgeEngine) Close() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

// GetWorkerStats returns per-worker telemetry snapshots for the given download ID.
// Returns nil if the download is not active or has no telemetry data.
func (e *SurgeEngine) GetWorkerStats(gid string) []types.WorkerSnapshot {
	if e.service == nil || e.service.Pool == nil {
		return nil
	}
	return e.service.Pool.GetWorkerStats(gid)
}

// ScaleWorkers adjusts the worker count for a Surge download.
// Returns the number of workers actually added (positive) or drained (negative).
func (e *SurgeEngine) ScaleWorkers(gid string, delta int) int {
	if e.service == nil || e.service.Pool == nil {
		return 0
	}
	return e.service.Pool.ScaleWorkers(gid, delta)
}

// KillWorker hard-interrupts a specific worker of a Surge download, destroying
// its TCP socket and triggering an in-place reconnect. Returns false if the
// download is not active or the worker has no active task.
func (e *SurgeEngine) KillWorker(gid string, workerID int) bool {
	if e.service == nil || e.service.Pool == nil {
		return false
	}
	return e.service.Pool.KillWorker(gid, workerID)
}

// SetSlowWorkerThreshold applies a runtime override of the relative slow-worker
// threshold for a Surge download.
func (e *SurgeEngine) SetSlowWorkerThreshold(gid string, v float64) {
	if e.service == nil || e.service.Pool == nil {
		return
	}
	e.service.Pool.SetSlowWorkerThreshold(gid, v)
}

// GetRateLimit returns the effective per-download rate limit (bps) and whether
// an explicit rate limit is active. Returns (0, false) if no explicit limit.
func (e *SurgeEngine) GetRateLimit(gid string) (int64, bool) {
	if e.service == nil || e.service.Pool == nil {
		return 0, false
	}
	status := e.service.Pool.GetStatus(gid)
	if status == nil {
		return 0, false
	}
	if status.RateLimitSet {
		return status.RateLimit, true
	}
	return status.RateLimit, false
}

// NewSurgeEngineForTesting creates a SurgeEngine with a pre-configured pool for testing.
// This is used by monitor-side telemetry collection tests.
func NewSurgeEngineForTesting(pool *download.WorkerPool) *SurgeEngine {
	return &SurgeEngine{
		service: &core.LocalDownloadService{Pool: pool},
	}
}
