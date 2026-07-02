package monitor

import (
	"context"
	"log"
	"strconv"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	surgeEvents "goaria-v3/internal/surge/engine/events"
	"goaria-v3/internal/surge/engine/state"
	"goaria-v3/internal/surge/engine/types"
)

func (m *Monitor) startSurgeEventBridge() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-m.stopChan
		cancel()
	}()

	stream, cleanup, err := m.engine.StreamEvents(ctx)
	if err != nil {
		log.Printf("[Monitor] Failed to subscribe to Surge event stream: %v", err)
		return
	}
	if stream == nil {
		return
	}
	go func() {
		defer cleanup()
		for {
			select {
			case <-ctx.Done():
				return
			case rawEvt, ok := <-stream:
				if !ok {
					return
				}
				m.handleSurgeEvent(rawEvt)
			}
		}
	}()
}

func (m *Monitor) handleSurgeEvent(rawEvt any) {
	var deltaType string
	var gid string
	var completeTotal int64
	var completeAvgSpeed float64

	// Reuse the cached SurgeEngine ref (set in Start); nil in Aria2-only mode.
	surgeEng := m.surgeEng

	switch ev := rawEvt.(type) {
	case surgeEvents.ProgressMsg:
		gid = "sg_" + ev.DownloadID
		completedStr := strconv.FormatInt(ev.Downloaded, 10)
		speedStr := strconv.FormatInt(int64(ev.Speed), 10)
		totalStr := strconv.FormatInt(ev.Total, 10)
		Cache.PatchTaskProgress(gid, completedStr, speedStr, totalStr)
		if m.tracker != nil {
			m.tracker.SampleSpeedFromEvent(gid, int64(ev.Speed), ev.Total, ev.Downloaded)
		}
		if State.HasWindow() {
			m.pusher.Queue(events.TaskDelta{
				Type: "progress",
				GID:  gid,
				Payload: map[string]interface{}{
					"completedLength": completedStr,
					"downloadSpeed":   speedStr,
					"totalLength":     totalStr,
				},
			})
		}
		return

	case surgeEvents.BatchProgressMsg:
		for _, p := range ev {
			pgid := "sg_" + p.DownloadID
			completedStr := strconv.FormatInt(p.Downloaded, 10)
			speedStr := strconv.FormatInt(int64(p.Speed), 10)
			totalStr := strconv.FormatInt(p.Total, 10)
			Cache.PatchTaskProgress(pgid, completedStr, speedStr, totalStr)
			if m.tracker != nil {
				m.tracker.SampleSpeedFromEvent(pgid, int64(p.Speed), p.Total, p.Downloaded)
			}
			if State.HasWindow() {
				m.pusher.Queue(events.TaskDelta{
					Type: "progress",
					GID:  pgid,
					Payload: map[string]interface{}{
						"completedLength": completedStr,
						"downloadSpeed":   speedStr,
						"totalLength":     totalStr,
					},
				})
			}
		}
		return

	case surgeEvents.DownloadQueuedMsg:
		deltaType = "add"
		gid = "sg_" + ev.DownloadID
		if m.tracker != nil {
			m.tracker.EnsureTrackedFromEvent(gid, 0, ev.URL, ev.Workers)
		}
		if surgeEng != nil {
			surgeEng.UpsertMasterCacheEntry(types.DownloadEntry{
				ID:           ev.DownloadID,
				URL:          ev.URL,
				URLHash:      state.URLHash(ev.URL),
				DestPath:     ev.DestPath,
				Filename:     ev.Filename,
				Mirrors:      append([]string(nil), ev.Mirrors...),
				Status:       "queued",
				RateLimit:    ev.RateLimit,
				RateLimitSet: ev.RateLimitSet,
				Workers:      ev.Workers,
				MinChunkSize: ev.MinChunkSize,
			})
		}
		Cache.AddSgTask(rpc.Task{
			GID:           gid,
			Status:        "waiting",
			TotalLength:   "0",
			DownloadSpeed: "0",
		}, "waiting")
		Cache.PrefetchMetadata(gid)
	case surgeEvents.DownloadStartedMsg:
		deltaType = "add"
		gid = "sg_" + ev.DownloadID
		if m.tracker != nil {
			m.tracker.EnsureTrackedFromEvent(gid, ev.Total, ev.URL, ev.Workers)
		}
		if surgeEng != nil {
			entry := types.DownloadEntry{
				ID:           ev.DownloadID,
				URL:          ev.URL,
				URLHash:      state.URLHash(ev.URL),
				DestPath:     ev.DestPath,
				Filename:     ev.Filename,
				Status:       "downloading",
				TotalSize:    ev.Total,
				RateLimit:    ev.RateLimit,
				RateLimitSet: ev.RateLimitSet,
				Workers:      ev.Workers,
				MinChunkSize: ev.MinChunkSize,
			}
			// Preserve Mirrors/Downloaded/TimeTaken from any prior queued entry.
			if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
				entry.Mirrors = append([]string(nil), existing.Mirrors...)
				if existing.Downloaded > 0 {
					entry.Downloaded = existing.Downloaded
				}
				if existing.TimeTaken > 0 {
					entry.TimeTaken = existing.TimeTaken
				}
			}
			surgeEng.UpsertMasterCacheEntry(entry)
		}
		Cache.AddSgTask(rpc.Task{
			GID:           gid,
			Status:        "active",
			TotalLength:   strconv.FormatInt(ev.Total, 10),
			DownloadSpeed: "0",
		}, "active")
		Cache.PrefetchMetadata(gid)
	case surgeEvents.DownloadResumedMsg:
		deltaType = "resume"
		gid = "sg_" + ev.DownloadID
		// ResumedMsg payload is minimal (ID+Filename); merge onto existing
		// entry to avoid zeroing URL/DestPath/Mirrors/Workers.
		if surgeEng != nil {
			if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
				merged := existing
				merged.Status = "downloading"
				surgeEng.UpsertMasterCacheEntry(merged)
			}
		}
	case surgeEvents.DownloadPausedMsg:
		deltaType = "pause"
		gid = "sg_" + ev.DownloadID
		// PausedMsg lacks URL/DestPath/TotalSize; merge onto existing entry.
		if surgeEng != nil {
			if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
				merged := existing
				merged.Status = "paused"
				if ev.Downloaded > 0 {
					merged.Downloaded = ev.Downloaded
				}
				merged.RateLimit = ev.RateLimit
				merged.RateLimitSet = ev.RateLimitSet
				merged.Workers = ev.Workers
				merged.MinChunkSize = ev.MinChunkSize
				surgeEng.UpsertMasterCacheEntry(merged)
			} else {
				surgeEng.UpsertMasterCacheEntry(types.DownloadEntry{
					ID:           ev.DownloadID,
					Filename:     ev.Filename,
					Status:       "paused",
					Downloaded:   ev.Downloaded,
					RateLimit:    ev.RateLimit,
					RateLimitSet: ev.RateLimitSet,
					Workers:      ev.Workers,
					MinChunkSize: ev.MinChunkSize,
				})
			}
		}
	case surgeEvents.DownloadCompleteMsg:
		deltaType = "complete"
		gid = "sg_" + ev.DownloadID
		completeTotal = ev.Total
		completeAvgSpeed = ev.AvgSpeed
		// CompleteMsg lacks URL/DestPath/Mirrors/Workers; merge onto existing.
		if surgeEng != nil {
			var avgSpeed float64
			if ev.Elapsed.Seconds() > 0 {
				avgSpeed = float64(ev.Total) / ev.Elapsed.Seconds()
			}
			if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
				merged := existing
				merged.Status = "completed"
				merged.TotalSize = ev.Total
				merged.Downloaded = ev.Total
				merged.CompletedAt = time.Now().Unix()
				merged.TimeTaken = ev.Elapsed.Milliseconds()
				merged.AvgSpeed = avgSpeed
				merged.RateLimit = ev.RateLimit
				merged.RateLimitSet = ev.RateLimitSet
				surgeEng.UpsertMasterCacheEntry(merged)
			} else {
				surgeEng.UpsertMasterCacheEntry(types.DownloadEntry{
					ID:           ev.DownloadID,
					Filename:     ev.Filename,
					Status:       "completed",
					TotalSize:    ev.Total,
					Downloaded:   ev.Total,
					CompletedAt:  time.Now().Unix(),
					TimeTaken:    ev.Elapsed.Milliseconds(),
					AvgSpeed:     avgSpeed,
					RateLimit:    ev.RateLimit,
					RateLimitSet: ev.RateLimitSet,
				})
			}
		}
	case surgeEvents.DownloadErrorMsg:
		deltaType = "error"
		gid = "sg_" + ev.DownloadID
		// ErrorMsg is minimal; merge onto existing to preserve URL/Mirrors/etc.
		if surgeEng != nil {
			if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
				merged := existing
				merged.Status = "error"
				if ev.DestPath != "" {
					merged.DestPath = ev.DestPath
				}
				surgeEng.UpsertMasterCacheEntry(merged)
			} else {
				surgeEng.UpsertMasterCacheEntry(types.DownloadEntry{
					ID:       ev.DownloadID,
					Filename: ev.Filename,
					DestPath: ev.DestPath,
					Status:   "error",
				})
			}
		}
	case surgeEvents.DownloadRemovedMsg:
		deltaType = "remove"
		gid = "sg_" + ev.DownloadID
		if surgeEng != nil {
			surgeEng.RemoveMasterCacheEntry(ev.DownloadID)
		}
		Cache.RemoveTask(gid)
	default:
		return
	}

	// For pause/resume, patch the cache status immediately so that
	// GetTasks() returns the correct status before the next tick runs.
	// Also queue the delta via pusher for direct frontend delivery (~50ms),
	// eliminating the gap between progress stopping (immediate from event
	// stream) and the status badge/card style changing (was waiting for tick).
	switch deltaType {
	case "add":
		if State.HasWindow() {
			task := findTaskInCache(gid)
			if task != nil {
				enriched := []rpc.Task{*task}
				Cache.EnrichTasks(enriched)
				m.pusher.Queue(events.TaskDelta{
					Type:    "add",
					GID:     gid,
					Payload: enriched[0],
				})
			}
		}
	case "pause":
		if surgeEng != nil {
			surgeEng.InvalidateListCache()
		}
		if m.shouldDiscardStalePause(gid) {
			log.Printf("[Monitor] Discarding stale pause event for gid %s (superseded by resume)", gid)
			return
		}
		Cache.MoveTaskToWaiting(gid, "paused")
		if task := findTaskInCache(gid); task != nil {
			m.hub.EmitTaskMove(events.TaskMove{
				GID:  gid,
				From: "active",
				To:   "waiting",
				Task: task,
			})
		}
		if State.HasWindow() {
			m.pusher.Queue(events.TaskDelta{Type: "pause", GID: gid})
		}
	case "resume":
		if surgeEng != nil {
			surgeEng.InvalidateListCache()
		}
		Cache.MoveTaskToActive(gid, "active")
		if task := findTaskInCache(gid); task != nil {
			m.hub.EmitTaskMove(events.TaskMove{
				GID:  gid,
				From: "waiting",
				To:   "active",
				Task: task,
			})
		}
		if State.HasWindow() {
			m.pusher.Queue(events.TaskDelta{Type: "resume", GID: gid})
		}
	case "complete", "error":
		if surgeEng != nil {
			surgeEng.InvalidateListCache()
		}
		Cache.MoveTaskToStopped(gid, deltaType)
		if State.HasWindow() {
			m.pusher.Queue(events.TaskDelta{Type: deltaType, GID: gid})
		}
		if m.tracker != nil {
			if completeTotal > 0 {
				m.tracker.EnsureTrackedFromEvent(gid, completeTotal, "", 0)
			}
			if completed := m.tracker.MarkCompleteFromEvent(gid, deltaType); completed != nil {
				if completed.PeakSpeed == 0 && completeAvgSpeed > 0 {
					completed.PeakSpeed = int64(completeAvgSpeed)
				}
				m.handleTaskComplete(completed)
			}
		}
		// Terminal: clear intention to avoid unbounded map growth.
		m.pauseResumeVersionMu.Lock()
		if m.pauseResumeIntentions != nil {
			delete(m.pauseResumeIntentions, gid)
		}
		m.pauseResumeVersionMu.Unlock()
	case "remove":
		if m.tracker != nil {
			m.tracker.RemoveTask(gid)
		}
		if m.telemetry != nil {
			m.telemetry.Remove(gid)
		}
		if m.convergence != nil {
			m.convergence.RemoveTask(gid)
		}
		if State.HasWindow() {
			m.pusher.Queue(events.TaskDelta{Type: "remove", GID: gid})
		}
		// Terminal: clear intention to avoid unbounded map growth.
		m.pauseResumeVersionMu.Lock()
		if m.pauseResumeIntentions != nil {
			delete(m.pauseResumeIntentions, gid)
		}
		m.pauseResumeVersionMu.Unlock()
	}

	m.hub.NotifyInternal(events.TaskDelta{Type: deltaType, GID: gid})

	log.Printf("[Monitor] Surge Event: %s -> %s (gid: %s)", deltaType, gid, gid)
	log.Printf("[DEBUG-EVT-MON] handleSurgeEvent done: type=%s gid=%s", deltaType, gid)
}

// findTaskInCache searches active+waiting+stopped cache slices for a task by GID.
func findTaskInCache(gid string) *rpc.Task {
	for _, task := range Cache.GetActive() {
		if task.GID == gid {
			t := task
			return &t
		}
	}
	for _, task := range Cache.GetWaiting() {
		if task.GID == gid {
			t := task
			return &t
		}
	}
	for _, task := range Cache.GetStopped() {
		if task.GID == gid {
			t := task
			return &t
		}
	}
	return nil
}
