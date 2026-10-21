// engine_surge_cache.go manages SurgeEngine's in-memory masterCache (mirroring master.gob) and listCache (1s TTL memoization of active scheduler workers merged with static checkpoints).

package rpc

import (
	"log"
	"sync"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

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

	e.masterCacheMu.RLock()
	cache := make([]types.DownloadRecord, len(e.masterCache))
	copy(cache, e.masterCache)
	e.masterCacheMu.RUnlock()
	masterByID := make(map[string]types.DownloadRecord, len(cache))
	for _, d := range cache {
		masterByID[d.ID] = d
	}

	pool := e.getScheduler()
	if pool != nil {
		activeConfigs := pool.GetAll()
		for _, cfg := range activeConfigs {
			st := pool.GetStatus(cfg.ID)
			status := types.DownloadStatus{
				ID:           cfg.ID,
				URL:          cfg.URL,
				Filename:     cfg.Filename,
				Status:       "downloading",
				RateLimit:    cfg.RateLimit,
				RateLimitSet: cfg.RateLimitSet,
			}
			if st == nil {
				// Nil-state worker: tracked but no authoritative status. Keep
				// the row in the union as "unknown" instead of manufacturing
				// downloading — unknown is never removal or live evidence.
				status.Status = "unknown"
				fillStaticStatusCheckpoint(&status, &cfg, masterByID[cfg.ID])
				statuses = append(statuses, status)
				continue
			}
			if st.Status == "queued" {
				// Queued is queue truth: a stale ProgressState must not
				// reclassify it via Done/paused/error overlays. Checkpoint
				// backfill comes from cfg, the stale progress read, and the
				// retained master mirror — never from a run error.
				status.Status = "queued"
				fillStaticStatusCheckpoint(&status, &cfg, masterByID[cfg.ID])
				statuses = append(statuses, status)
				continue
			}
			status.Status = st.Status

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

// fillStaticStatusCheckpoint fills a list status that has no live worker
// overlay (queued / unknown rows) from cfg fields, then a stale progress read,
// then the retained master mirror — measured lengths prefer the first
// positive source.
func fillStaticStatusCheckpoint(status *types.DownloadStatus, cfg *types.DownloadRecord, master types.DownloadRecord) {
	status.TotalSize = cfg.TotalSize
	status.Downloaded = cfg.Downloaded
	status.DestPath = cfg.DestPath
	if cfg.ProgressState != nil {
		if cp := progress.CfgProgress(cfg); cp != nil {
			downloaded, totalSize, _, _, _, _ := cp.GetProgress()
			if status.TotalSize <= 0 {
				status.TotalSize = totalSize
			}
			if status.Downloaded <= 0 {
				status.Downloaded = downloaded
			}
			if status.DestPath == "" {
				if dp := cp.GetDestPath(); dp != "" {
					status.DestPath = dp
				}
			}
		}
	}
	if status.URL == "" {
		status.URL = master.URL
	}
	if status.Filename == "" {
		status.Filename = master.Filename
	}
	if status.DestPath == "" {
		status.DestPath = master.DestPath
	}
	if status.TotalSize <= 0 {
		status.TotalSize = master.TotalSize
	}
	if status.Downloaded <= 0 {
		status.Downloaded = master.Downloaded
	}
	if status.AvgSpeed == 0 {
		status.AvgSpeed = master.AvgSpeed
	}
	if status.TimeTaken == 0 {
		status.TimeTaken = master.TimeTaken
	}
	if status.TotalSize > 0 {
		status.Progress = float64(status.Downloaded) * 100 / float64(status.TotalSize)
	}
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
		out[i].HLSResume = out[i].HLSResume.Clone()
	}
	return out
}

// SetMasterCacheForTesting replaces the masterCache contents for test setup.
func (e *SurgeEngine) SetMasterCacheForTesting(entries []types.DownloadRecord) {
	e.masterCacheMu.Lock()
	e.masterCache = entries
	e.masterCacheRev++
	e.masterCacheMu.Unlock()
}

// SetMasterRefreshHookForTesting installs a hook invoked after each
// RefreshMasterCache write attempt; nil clears it.
func (e *SurgeEngine) SetMasterRefreshHookForTesting(hook func()) {
	e.masterCacheMu.Lock()
	e.refreshHook = hook
	e.masterCacheMu.Unlock()
}

// UpsertMasterCacheEntry adds or replaces an entry in masterCache by ID.
// The stored row gets private slice/resume copies — symmetric with the
// read-side clones in GetMasterCacheEntry/MasterCacheForTesting — so a
// caller mutating its own slices cannot rewrite cached state.
func (e *SurgeEngine) UpsertMasterCacheEntry(entry types.DownloadRecord) {
	if entry.Mirrors != nil {
		entry.Mirrors = append([]string(nil), entry.Mirrors...)
	}
	entry.HLSResume = entry.HLSResume.Clone()
	e.masterCacheMu.Lock()
	defer e.masterCacheMu.Unlock()
	for i, existing := range e.masterCache {
		if existing.ID == entry.ID {
			e.masterCache[i] = entry
			e.masterCacheRev++
			return
		}
	}
	e.masterCache = append(e.masterCache, entry)
	e.masterCacheRev++
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
			existing.HLSResume = existing.HLSResume.Clone()
			return existing, true
		}
	}
	return types.DownloadRecord{}, false
}

// RemoveMasterCacheEntry removes an entry from masterCache by ID.
func (e *SurgeEngine) RemoveMasterCacheEntry(id string) {
	e.masterCacheMu.Lock()
	defer e.masterCacheMu.Unlock()
	e.masterCacheRev++
	out := e.masterCache[:0]
	for _, existing := range e.masterCache {
		if existing.ID != id {
			out = append(out, existing)
		}
	}
	e.masterCache = out
}

// RefreshMasterCache reloads masterCache from master.gob via store.LoadMasterList.
// Entries upserted/removed between the disk read and the write are preserved:
// when the in-memory revision changed mid-read the disk rows are merged under
// the current snapshot instead of clobbering newer event writes.
func (e *SurgeEngine) RefreshMasterCache() {
	e.masterCacheMu.Lock()
	rev := e.masterCacheRev
	snapshot := make([]types.DownloadRecord, len(e.masterCache))
	copy(snapshot, e.masterCache)
	e.masterCacheMu.Unlock()

	list, err := store.LoadMasterList()
	if err != nil {
		log.Printf("[Surge] Failed to refresh master cache: %v", err)
		return
	}
	// Test-only post-read hook: runs after the disk read and before install,
	// never under masterCacheMu, so tests can inject mid-read mutations.
	e.masterCacheMu.RLock()
	hook := e.refreshHook
	e.masterCacheMu.RUnlock()
	if hook != nil {
		hook()
	}
	e.masterCacheMu.Lock()
	if e.masterCacheRev == rev {
		e.masterCache = list.Downloads
	} else {
		current := make(map[string]types.DownloadRecord, len(e.masterCache))
		for _, d := range e.masterCache {
			current[d.ID] = d
		}
		removed := make(map[string]bool, len(snapshot))
		for _, d := range snapshot {
			if _, ok := current[d.ID]; !ok {
				removed[d.ID] = true
			}
		}
		merged := make([]types.DownloadRecord, 0, len(list.Downloads)+len(current))
		onDisk := make(map[string]bool, len(list.Downloads))
		for _, d := range list.Downloads {
			if removed[d.ID] {
				continue
			}
			if cur, ok := current[d.ID]; ok {
				d = cur
			}
			onDisk[d.ID] = true
			merged = append(merged, d)
		}
		for _, d := range e.masterCache {
			if !onDisk[d.ID] {
				merged = append(merged, d)
			}
		}
		e.masterCache = merged
	}
	e.masterCacheMu.Unlock()
}
