package rpc

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	surgeconfig "goaria-v3/internal/surge/config"
	"goaria-v3/internal/surge/orchestrator"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/service"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

type SurgeEngine struct {
	service   *service.LocalDownloadService
	manager   *orchestrator.LifecycleManager
	scheduler *scheduler.Scheduler
	cleanup   func()

	listCacheMu sync.Mutex
	listCache   []types.DownloadStatus
	listCacheAt time.Time

	// masterCache is an in-memory mirror of master.gob's non-active entries
	// (stopped/waiting/paused/queued). Active entries are served live from the
	// scheduler and are not stored here. Kept in sync by handleSurgeEvent on
	// status-transition events and refreshed periodically by tick().
	masterCacheMu sync.RWMutex
	masterCache   []types.DownloadRecord
}

func (e *SurgeEngine) getScheduler() *scheduler.Scheduler {
	if e.scheduler != nil {
		return e.scheduler
	}
	if e.manager != nil {
		return e.manager.GetScheduler()
	}
	return nil
}

// buildSurgeIsNameActive mirrors tip cmd.buildActiveDownloadChecker: treat
// in-flight scheduler destinations as filename conflicts within a directory.
func buildSurgeIsNameActive(pool *scheduler.Scheduler) orchestrator.IsNameActiveFunc {
	if pool == nil {
		return nil
	}
	return func(dir, name string) bool {
		dir = utils.EnsureAbsPath(strings.TrimSpace(dir))
		name = strings.TrimSpace(name)
		if dir == "" || name == "" {
			return false
		}
		for _, cfg := range pool.GetAll() {
			existingName := strings.TrimSpace(cfg.Filename)
			existingDir := strings.TrimSpace(cfg.OutputPath)
			if cfg.DestPath != "" {
				existingDir = filepath.Dir(cfg.DestPath)
				if existingName == "" {
					existingName = filepath.Base(cfg.DestPath)
				}
			}
			if ps := progress.CfgProgress(&cfg); ps != nil {
				if stateName := strings.TrimSpace(ps.GetFilename()); stateName != "" {
					existingName = stateName
				}
				if stateDestPath := strings.TrimSpace(ps.GetDestPath()); stateDestPath != "" {
					existingDir = filepath.Dir(stateDestPath)
					if existingName == "" {
						existingName = filepath.Base(stateDestPath)
					}
				}
			}
			if existingDir == "" || existingName == "" {
				continue
			}
			if utils.EnsureAbsPath(existingDir) == dir && existingName == name {
				return true
			}
		}
		return false
	}
}

func NewSurgeEngine() *SurgeEngine {
	maxDownloads := 3
	if md, err := strconv.Atoi(config.Get().MaxConcurrentDownloads); err == nil && md > 0 {
		maxDownloads = md
	}
	settings := surgeconfig.DefaultSettings()
	eventBus := orchestrator.NewEventBus()
	// Pass EventBus.InputCh as the scheduler default so a nil ProgressCh
	// fallback still reaches the live consumer (Enqueue normally sets it too).
	pool := scheduler.New(eventBus.InputCh, maxDownloads)
	mgr := orchestrator.NewLifecycleManager(pool, eventBus, settings, buildSurgeIsNameActive(pool))
	svc := service.NewLocalDownloadService(mgr)

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

	cleanup := func() {
		_ = svc.Shutdown()
		engineCancel()
		if eventCleanup != nil {
			eventCleanup()
		}
		if spawned {
			wg.Wait()
		}
	}

	var masterEntries []types.DownloadRecord
	if list, err := store.LoadMasterList(); err != nil {
		log.Printf("[Surge] Failed to load master list for cache init: %v", err)
		masterEntries = []types.DownloadRecord{}
	} else {
		masterEntries = list.Downloads
	}

	return &SurgeEngine{
		service:     svc,
		manager:     mgr,
		scheduler:   pool,
		cleanup:     cleanup,
		masterCache: masterEntries,
	}
}

func (e *SurgeEngine) IsSurgeActive() bool {
	return e.service != nil
}

// SetResumeParamsHook injects the RecomputeResumeParams callback into the
// LifecycleManager's EngineHooks. This allows GoAria to recompute Workers
// and MinChunkSize on resume using current BBR/bandwidth state.
// Uses read-modify-write to preserve any other hooks (including TightenOnPickup).
func (e *SurgeEngine) SetResumeParamsHook(fn func(cfg *types.DownloadRecord)) {
	if e.manager == nil {
		return
	}
	hooks := e.manager.GetEngineHooks()
	hooks.RecomputeResumeParams = fn
	e.manager.SetEngineHooks(hooks)
}

// SetTightenOnPickupHook injects the TightenOnPickup callback into EngineHooks.
// SetEngineHooks syncs the callback onto the scheduler worker path (nil clears).
// Tighten-only: may lower Runtime.Workers before RunDownload; must never raise
// Workers/MinChunkSize. RMW preserves Resume hook.
func (e *SurgeEngine) SetTightenOnPickupHook(fn func(cfg *types.DownloadRecord)) {
	if e.manager == nil {
		return
	}
	hooks := e.manager.GetEngineHooks()
	hooks.TightenOnPickup = fn
	e.manager.SetEngineHooks(hooks)
}

// getDownloadList returns the Surge download list with a 1s TTL request-scoped
// cache over the merged scheduler-active + masterCache result.
func (e *SurgeEngine) getDownloadList() ([]types.DownloadStatus, error) {
	e.listCacheMu.Lock()
	if time.Since(e.listCacheAt) < 1*time.Second && e.listCache != nil {
		cached := e.listCache
		e.listCacheMu.Unlock()
		return cached, nil
	}
	e.listCacheMu.Unlock()

	list := e.buildDownloadList()

	e.listCacheMu.Lock()
	e.listCache = list
	e.listCacheAt = time.Now()
	e.listCacheMu.Unlock()
	return list, nil
}

// buildDownloadList merges scheduler-active entries with the masterCache mirror.
// Speed fields are B/s (aligned with tip scheduler / local_service).
func (e *SurgeEngine) buildDownloadList() []types.DownloadStatus {
	var statuses []types.DownloadStatus

	pool := e.getScheduler()
	if pool != nil {
		activeConfigs := pool.GetAll()
		for _, cfg := range activeConfigs {
			statusStr := "downloading"
			if st := pool.GetStatus(cfg.ID); st != nil {
				statusStr = st.Status
			}
			status := types.DownloadStatus{
				ID:           cfg.ID,
				URL:          cfg.URL,
				Filename:     cfg.Filename,
				Status:       statusStr,
				RateLimit:    cfg.RateLimit,
				RateLimitSet: cfg.RateLimitSet,
			}

			if cfg.ProgressState != nil {
				cp := progress.CfgProgress(&cfg)
				if cp != nil {
					downloaded, totalSize, _, sessionElapsed, connections, sessionStart := cp.GetProgress()

					status.TotalSize = totalSize
					status.Downloaded = downloaded
					if dp := cp.GetDestPath(); dp != "" {
						status.DestPath = dp
					}

					if status.TotalSize > 0 {
						status.Progress = float64(status.Downloaded) * 100 / float64(status.TotalSize)
					}

					status.Connections = int(connections)

					switch {
					case cp.IsPausing():
						status.Status = "pausing"
					case cp.IsPaused():
						status.Status = "paused"
					case cp.Done.Load():
						status.Status = "completed"
					}
					// GetStatus and metric reads are separate snapshots; recheck errors
					// so a worker failure is not reported as completed or pausing.
					if err := cp.GetError(); err != nil {
						status.Status = "error"
						status.Error = err.Error()
					}

					if status.Status == "downloading" {
						sessionDownloaded := downloaded - sessionStart
						if sessionElapsed.Seconds() > 0 && sessionDownloaded > 0 {
							status.Speed = float64(sessionDownloaded) / sessionElapsed.Seconds()

							remaining := status.TotalSize - status.Downloaded
							if remaining > 0 && status.Speed > 0 {
								status.ETA = int64(float64(remaining) / status.Speed)
							}
						}
					}
				}
			}

			statuses = append(statuses, status)
		}
	}

	e.masterCacheMu.RLock()
	cache := make([]types.DownloadRecord, len(e.masterCache))
	copy(cache, e.masterCache)
	e.masterCacheMu.RUnlock()

	existingIDs := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		existingIDs[s.ID] = true
	}

	for _, d := range cache {
		if existingIDs[d.ID] {
			continue
		}

		var progressPct float64
		if d.TotalSize > 0 {
			progressPct = float64(d.Downloaded) * 100 / float64(d.TotalSize)
		} else if d.Status == "completed" {
			progressPct = 100.0
		}
		statuses = append(statuses, types.DownloadStatus{
			ID:           d.ID,
			URL:          d.URL,
			Filename:     d.Filename,
			DestPath:     d.DestPath,
			Status:       d.Status,
			TotalSize:    d.TotalSize,
			Downloaded:   d.Downloaded,
			Progress:     progressPct,
			Speed:        completedSpeedBps(d),
			Connections:  0,
			TimeTaken:    d.TimeTaken,
			AvgSpeed:     d.AvgSpeed,
			RateLimit:    d.RateLimit,
			RateLimitSet: d.RateLimitSet,
			Error:        d.Error,
		})
	}
	return statuses
}

// completedSpeedBps mirrors the vendored helper in local_service.go.
// Returns bytes/sec for completed tasks.
func completedSpeedBps(entry types.DownloadRecord) float64 {
	if entry.Status != "completed" {
		return 0
	}
	if entry.AvgSpeed > 0 {
		return entry.AvgSpeed
	}
	if entry.TimeTaken > 0 {
		return float64(entry.TotalSize) * 1000 / float64(entry.TimeTaken)
	}
	return 0
}

// InvalidateListCache clears the 1s TTL list cache so the next getDownloadList
// call fetches fresh data.
func (e *SurgeEngine) InvalidateListCache() {
	e.listCacheMu.Lock()
	e.listCache = nil
	e.listCacheAt = time.Time{}
	e.listCacheMu.Unlock()
}

// ListCacheMuForTesting returns the list cache mutex for test inspection.
func (e *SurgeEngine) ListCacheMuForTesting() *sync.Mutex {
	return &e.listCacheMu
}

// ListCacheAtForTesting returns the list cache timestamp for test inspection.
func (e *SurgeEngine) ListCacheAtForTesting() time.Time {
	return e.listCacheAt
}

// MasterCacheForTesting returns a copy of the masterCache slice for test inspection.
func (e *SurgeEngine) MasterCacheForTesting() []types.DownloadRecord {
	e.masterCacheMu.RLock()
	defer e.masterCacheMu.RUnlock()
	out := make([]types.DownloadRecord, len(e.masterCache))
	copy(out, e.masterCache)
	for i := range out {
		if out[i].Mirrors != nil {
			out[i].Mirrors = append([]string(nil), out[i].Mirrors...)
		}
	}
	return out
}

// SetMasterCacheForTesting replaces the masterCache contents for test setup.
func (e *SurgeEngine) SetMasterCacheForTesting(entries []types.DownloadRecord) {
	e.masterCacheMu.Lock()
	e.masterCache = entries
	e.masterCacheMu.Unlock()
}

// UpsertMasterCacheEntry adds or replaces an entry in masterCache by ID.
func (e *SurgeEngine) UpsertMasterCacheEntry(entry types.DownloadRecord) {
	e.masterCacheMu.Lock()
	defer e.masterCacheMu.Unlock()
	for i, existing := range e.masterCache {
		if existing.ID == entry.ID {
			e.masterCache[i] = entry
			return
		}
	}
	e.masterCache = append(e.masterCache, entry)
}

// GetMasterCacheEntry returns a copy of the masterCache entry for the given ID.
func (e *SurgeEngine) GetMasterCacheEntry(id string) (types.DownloadRecord, bool) {
	e.masterCacheMu.RLock()
	defer e.masterCacheMu.RUnlock()
	for _, existing := range e.masterCache {
		if existing.ID == id {
			if existing.Mirrors != nil {
				existing.Mirrors = append([]string(nil), existing.Mirrors...)
			}
			return existing, true
		}
	}
	return types.DownloadRecord{}, false
}

// RemoveMasterCacheEntry removes an entry from masterCache by ID.
func (e *SurgeEngine) RemoveMasterCacheEntry(id string) {
	e.masterCacheMu.Lock()
	defer e.masterCacheMu.Unlock()
	out := e.masterCache[:0]
	for _, existing := range e.masterCache {
		if existing.ID != id {
			out = append(out, existing)
		}
	}
	e.masterCache = out
}

// RefreshMasterCache reloads masterCache from master.gob via store.LoadMasterList.
func (e *SurgeEngine) RefreshMasterCache() {
	list, err := store.LoadMasterList()
	if err != nil {
		log.Printf("[Surge] Failed to refresh master cache: %v", err)
		return
	}
	e.masterCacheMu.Lock()
	e.masterCache = list.Downloads
	e.masterCacheMu.Unlock()
}

func mapStatus(s string) string {
	switch s {
	case "downloading":
		return "active"
	case "pausing", "paused":
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

// ClassifySurgeErrorCode maps a Surge error string (and/or error status) to
// Aria2-compatible codes: disk-space sentinel → "9"; other errors → "1"; none → "".
func ClassifySurgeErrorCode(errMsg string, statusIsError bool) string {
	if errMsg == "" && !statusIsError {
		return ""
	}
	if errMsg != "" && strings.Contains(errMsg, types.ErrInsufficientDiskSpace.Error()) {
		return "9"
	}
	return "1"
}

func errorCodeForSurgeStatus(status types.DownloadStatus) string {
	return ClassifySurgeErrorCode(status.Error, status.Status == "error" || status.Error != "")
}

func convertTask(status types.DownloadStatus) Task {
	dir := filepath.Dir(status.DestPath)
	// status.Speed is already B/s; Aria2 DownloadSpeed is also B/s.
	return Task{
		GID:             status.ID,
		Title:           status.Filename,
		Status:          mapStatus(status.Status),
		TotalLength:     strconv.FormatInt(status.TotalSize, 10),
		CompletedLength: strconv.FormatInt(status.Downloaded, 10),
		DownloadSpeed:   strconv.FormatInt(int64(status.Speed), 10),
		ErrorCode:       errorCodeForSurgeStatus(status),
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
	req := &orchestrator.DownloadRequest{
		URL:                  url,
		Path:                 options.Dir,
		Filename:             options.Out,
		Headers:              headersMap,
		Workers:              options.Split,
		MinChunkSize:         options.MinSplitSize,
		FileSize:             options.FileSize,
		SupportsRange:        options.SupportsRange,
		RangeAcquisitionMode: options.RangeAcquisitionMode,
		SkipServerProbe:      options.SkipServerProbe,
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
		return Task{}, errors.New("task not found")
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
	pool := e.getScheduler()
	if pool == nil {
		return nil, nil
	}
	configs := pool.GetAll()
	var res []Task
	for _, cfg := range configs {
		status := pool.GetStatus(cfg.ID)
		if status != nil && mapStatus(status.Status) == "active" {
			res = append(res, convertTask(*status))
		}
	}
	return res, nil
}

func (e *SurgeEngine) TellActiveProgress() ([]TaskProgress, error) {
	pool := e.getScheduler()
	if pool == nil {
		return nil, nil
	}
	configs := pool.GetAll()
	var res []TaskProgress
	for _, cfg := range configs {
		status := pool.GetStatus(cfg.ID)
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
	typed, cleanup, err := e.service.StreamEvents(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan any, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-typed:
				if !ok {
					return
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, cleanup, nil
}

func (e *SurgeEngine) Close() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

// GetWorkerStats returns per-worker telemetry snapshots for the given download ID.
func (e *SurgeEngine) GetWorkerStats(gid string) []types.WorkerSnapshot {
	pool := e.getScheduler()
	if pool == nil {
		return nil
	}
	return pool.GetWorkerStats(gid)
}

// ScaleWorkers adjusts the worker count for a Surge download.
func (e *SurgeEngine) ScaleWorkers(gid string, delta int) int {
	pool := e.getScheduler()
	if pool == nil {
		return 0
	}
	return pool.ScaleWorkers(gid, delta)
}

// KillWorker hard-interrupts a specific worker of a Surge download.
func (e *SurgeEngine) KillWorker(gid string, workerID int) bool {
	pool := e.getScheduler()
	if pool == nil {
		return false
	}
	return pool.KillWorker(gid, workerID)
}

// SetSlowWorkerThreshold applies a runtime override of the relative slow-worker
// threshold for a Surge download.
func (e *SurgeEngine) SetSlowWorkerThreshold(gid string, v float64) {
	pool := e.getScheduler()
	if pool == nil {
		return
	}
	pool.SetSlowWorkerThreshold(gid, v)
}

// DrainWorker marks a specific worker of a Surge download as draining.
func (e *SurgeEngine) DrainWorker(gid string, workerID int) bool {
	pool := e.getScheduler()
	if pool == nil {
		return false
	}
	return pool.DrainWorker(gid, workerID)
}

// GetRateLimit returns the effective per-download rate limit (bps) and whether
// a positive bandwidth cap is active. bps == 0 (Surge "0"/unlimited, including
// RateLimitSet=true explicit unlimited) is never limited.
func (e *SurgeEngine) GetRateLimit(gid string) (int64, bool) {
	pool := e.getScheduler()
	if pool == nil {
		return 0, false
	}
	status := pool.GetStatus(gid)
	if status == nil {
		return 0, false
	}
	if status.RateLimit > 0 {
		return status.RateLimit, true
	}
	return 0, false
}

// NewSurgeEngineForTesting creates a SurgeEngine with a pre-configured scheduler for testing.
func NewSurgeEngineForTesting(pool *scheduler.Scheduler) *SurgeEngine {
	var masterEntries []types.DownloadRecord
	if list, err := store.LoadMasterList(); err == nil {
		masterEntries = list.Downloads
	} else {
		masterEntries = []types.DownloadRecord{}
	}
	eventBus := orchestrator.NewEventBus()
	mgr := orchestrator.NewLifecycleManager(pool, eventBus, surgeconfig.DefaultSettings(), buildSurgeIsNameActive(pool))
	return &SurgeEngine{
		service:     service.NewLocalDownloadService(mgr),
		manager:     mgr,
		scheduler:   pool,
		masterCache: masterEntries,
	}
}
