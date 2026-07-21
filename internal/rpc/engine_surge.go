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
	"goaria-v3/internal/surge/engine/state"
	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/processing"
	"goaria-v3/internal/surge/utils"
)

type SurgeEngine struct {
	service *core.LocalDownloadService
	manager *processing.LifecycleManager
	cleanup func()

	listCacheMu sync.Mutex
	listCache   []types.DownloadStatus
	listCacheAt time.Time

	// masterCache is an in-memory mirror of master.gob's non-active entries
	// (stopped/waiting/paused/queued). Active entries are served live from the
	// Pool and are not stored here. Kept in sync by handleSurgeEvent on
	// status-transition events and refreshed periodically by tick().
	masterCacheMu sync.RWMutex
	masterCache   []types.DownloadEntry
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

	// Load master list into the in-memory mirror once at startup so
	// getDownloadList reads from cache instead of gob-decoding each tick.
	// handleSurgeEvent keeps it current on status-transition events.
	var masterEntries []types.DownloadEntry
	if list, err := state.LoadMasterList(); err != nil {
		log.Printf("[Surge] Failed to load master list for cache init: %v", err)
		masterEntries = []types.DownloadEntry{}
	} else {
		masterEntries = list.Downloads
	}

	return &SurgeEngine{
		service:     svc,
		manager:     mgr,
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
// cache over the merged Pool-active + masterCache result. This avoids
// re-merging when TellWaiting and TellStopped are called concurrently within
// the same tick. The underlying masterCache is an in-memory mirror of
// master.gob kept current by handleSurgeEvent, so no gob.Decode happens here.
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

// buildDownloadList merges Pool-active entries with the masterCache mirror,
// replacing the per-tick state.ListAllDownloads gob.Decode path. The Pool
// active construction mirrors service.List (local_service.go) so output stays
// consistent with the vendored implementation.
func (e *SurgeEngine) buildDownloadList() []types.DownloadStatus {
	var statuses []types.DownloadStatus

	// 1. Active downloads from pool (mirrors service.List Pool construction).
	if e.service != nil && e.service.Pool != nil {
		activeConfigs := e.service.Pool.GetAll()
		for _, cfg := range activeConfigs {
			statusStr := "downloading"
			if st := e.service.Pool.GetStatus(cfg.ID); st != nil {
				statusStr = st.Status
			}
			status := types.DownloadStatus{
				ID:           cfg.ID,
				URL:          cfg.URL,
				Filename:     cfg.Filename,
				Status:       statusStr,
				RateLimit:    cfg.RateLimitBps,
				RateLimitSet: cfg.RateLimitSet,
			}

			if cfg.State != nil {
				downloaded, totalSize, _, sessionElapsed, connections, sessionStart := cfg.State.GetProgress()

				status.TotalSize = totalSize
				status.Downloaded = downloaded
				if dp := cfg.State.GetDestPath(); dp != "" {
					status.DestPath = dp
				}

				if status.TotalSize > 0 {
					status.Progress = float64(status.Downloaded) * 100 / float64(status.TotalSize)
				}

				status.Connections = int(connections)

				switch {
				case cfg.State.IsPausing():
					status.Status = "pausing"
				case cfg.State.IsPaused():
					status.Status = "paused"
				case cfg.State.Done.Load():
					status.Status = "completed"
				}

				if status.Status == "downloading" {
					sessionDownloaded := downloaded - sessionStart
					if sessionElapsed.Seconds() > 0 && sessionDownloaded > 0 {
						status.Speed = float64(sessionDownloaded) / sessionElapsed.Seconds() / float64(utils.MiB)

						remaining := status.TotalSize - status.Downloaded
						if remaining > 0 && status.Speed > 0 {
							speedBytes := status.Speed * float64(utils.MiB)
							status.ETA = int64(float64(remaining) / speedBytes)
						}
					}
				}
			}

			statuses = append(statuses, status)
		}
	}

	// 2. Non-active entries from the masterCache mirror (replaces
	// state.ListAllDownloads gob.Decode). Copy under RLock so concurrent
	// writers (Upsert/Remove) cannot mutate the backing array mid-iteration.
	e.masterCacheMu.RLock()
	cache := make([]types.DownloadEntry, len(e.masterCache))
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

		var progress float64
		if d.TotalSize > 0 {
			progress = float64(d.Downloaded) * 100 / float64(d.TotalSize)
		} else if d.Status == "completed" {
			progress = 100.0
		}
		statuses = append(statuses, types.DownloadStatus{
			ID:           d.ID,
			URL:          d.URL,
			Filename:     d.Filename,
			DestPath:     d.DestPath,
			Status:       d.Status,
			TotalSize:    d.TotalSize,
			Downloaded:   d.Downloaded,
			Progress:     progress,
			Speed:        completedSpeedMBps(d),
			Connections:  0,
			TimeTaken:    d.TimeTaken,
			AvgSpeed:     d.AvgSpeed,
			RateLimit:    d.RateLimit,
			RateLimitSet: d.RateLimitSet,
		})
	}
	return statuses
}

// completedSpeedMBps mirrors the vendored helper in local_service.go so
// buildDownloadList can compute completed-task speed display without calling
// the unexported core package function.
func completedSpeedMBps(entry types.DownloadEntry) float64 {
	if entry.Status != "completed" {
		return 0
	}
	if entry.AvgSpeed > 0 {
		return entry.AvgSpeed / float64(utils.MiB)
	}
	if entry.TimeTaken > 0 {
		return float64(entry.TotalSize) * 1000 / float64(entry.TimeTaken) / float64(utils.MiB)
	}
	return 0
}

// InvalidateListCache clears the 1s TTL list cache so the next getDownloadList
// call fetches fresh data. Called on status-transition events (pause/resume/
// complete/error) to prevent stale cache from causing list divergence between
// TellActive (uncached) and TellWaiting/TellStopped (cached).
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
func (e *SurgeEngine) MasterCacheForTesting() []types.DownloadEntry {
	e.masterCacheMu.RLock()
	defer e.masterCacheMu.RUnlock()
	out := make([]types.DownloadEntry, len(e.masterCache))
	copy(out, e.masterCache)
	for i := range out {
		if out[i].Mirrors != nil {
			out[i].Mirrors = append([]string(nil), out[i].Mirrors...)
		}
	}
	return out
}

// SetMasterCacheForTesting replaces the masterCache contents for test setup.
func (e *SurgeEngine) SetMasterCacheForTesting(entries []types.DownloadEntry) {
	e.masterCacheMu.Lock()
	e.masterCache = entries
	e.masterCacheMu.Unlock()
}

// UpsertMasterCacheEntry adds or replaces an entry in masterCache by ID.
// It performs a full replacement (no field-level merge), so callers handling
// events with incomplete payloads MUST first read the existing entry via
// GetMasterCacheEntry, copy it, and overwrite only the payload-provided fields
// before calling this — otherwise URL/DestPath/Mirrors/Workers etc. would be
// zeroed out.
func (e *SurgeEngine) UpsertMasterCacheEntry(entry types.DownloadEntry) {
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

// GetMasterCacheEntry returns a copy of the masterCache entry for the given ID
// and whether it was found. Used by handleSurgeEvent to read the existing
// entry before merging fields from incomplete event payloads.
func (e *SurgeEngine) GetMasterCacheEntry(id string) (types.DownloadEntry, bool) {
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
	return types.DownloadEntry{}, false
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

// RefreshMasterCache reloads masterCache from master.gob via state.LoadMasterList.
// Called on the 10s tick boundary to sync non-event-driven master list writes
// (e.g. removeDownloadsByStatus, PauseAllDownloads).
func (e *SurgeEngine) RefreshMasterCache() {
	list, err := state.LoadMasterList()
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
		DownloadSpeed:   strconv.FormatInt(int64(status.Speed*float64(utils.MiB)), 10),
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

// DrainWorker marks a specific worker of a Surge download as draining. The
// worker finishes its current chunk and exits gracefully, preserving the TCP
// connection in the Transport idle pool. Returns false if the download is not
// active.
func (e *SurgeEngine) DrainWorker(gid string, workerID int) bool {
	if e.service == nil || e.service.Pool == nil {
		return false
	}
	return e.service.Pool.DrainWorker(gid, workerID)
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
	var masterEntries []types.DownloadEntry
	if list, err := state.LoadMasterList(); err == nil {
		masterEntries = list.Downloads
	} else {
		masterEntries = []types.DownloadEntry{}
	}
	return &SurgeEngine{
		service:     &core.LocalDownloadService{Pool: pool},
		masterCache: masterEntries,
	}
}
