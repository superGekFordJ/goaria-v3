package monitor

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

// surgeListReader abstracts the Surge engine list-read methods used by
// reconcileSurgeCache, enabling test substitution without a full SurgeEngine.
type surgeListReader interface {
	TellActive() ([]rpc.Task, error)
	TellWaiting(offset, num int) ([]rpc.Task, error)
	TellStopped(offset, num int) ([]rpc.Task, error)
}

func (m *Monitor) startSurgeEventBridge() {
	if m.surgeEng == nil {
		return
	}
	go m.surgeEventBridgeLoop()
}

// surgeEventBridgeLoop maintains a reconnect loop around the Surge event
// stream. On error or channel close it sets surgeStreamConnected=false and
// retries after surgePollInterval; the polling fallback goroutine covers
// state during the gap.
func (m *Monitor) surgeEventBridgeLoop() {
	for {
		select {
		case <-m.stopChan:
			return
		default:
		}

		ctx, cancel := context.WithCancel(context.Background())
		// Exit the stop-watcher when the iteration ends (ctx cancelled) so
		// reconnect cycles do not accumulate one goroutine per attempt.
		go func() {
			select {
			case <-m.stopChan:
				cancel()
			case <-ctx.Done():
			}
		}()

		stream, cleanup, err := m.engine.StreamEvents(ctx)
		if err != nil || stream == nil {
			m.surgeStreamConnected.Store(false)
			log.Printf("[Monitor] Surge event stream unavailable: %v, degrading to polling", err)
			cancel()
			select {
			case <-m.stopChan:
				return
			case <-time.After(m.surgePollInterval):
			}
			continue
		}

		m.surgeStreamConnected.Store(true)
		log.Printf("[Monitor] Surge event stream connected")

		streamClosed := false
		for !streamClosed {
			select {
			case <-ctx.Done():
				cleanup()
				m.surgeStreamConnected.Store(false)
				streamClosed = true
			case rawEvt, ok := <-stream:
				if !ok {
					m.surgeStreamConnected.Store(false)
					cleanup()
					cancel()
					streamClosed = true
					break
				}
				m.handleSurgeEvent(rawEvt)
			}
		}

		select {
		case <-m.stopChan:
			return
		case <-time.After(m.surgePollInterval):
		}
	}
}

func (m *Monitor) handleSurgeEvent(rawEvt any) {
	var deltaType string
	var gid string
	var completeTotal int64
	var completeAvgSpeed float64

	// Reuse the cached SurgeEngine ref (set in Start); nil in Aria2-only mode.
	surgeEng := m.surgeEng

	ev, ok := rawEvt.(types.DownloadEvent)
	if !ok {
		return
	}

	switch ev.Type {
	case types.EventProgress:
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

	case types.EventBatchProgress:
		for _, p := range ev.BatchEvents {
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

	case types.EventQueued:
		deltaType = "add"
		gid = "sg_" + ev.DownloadID
		if m.tracker != nil {
			m.tracker.EnsureTrackedFromEvent(gid, 0, ev.URL, ev.Workers, "waiting")
		}
		if surgeEng != nil {
			surgeEng.UpsertMasterCacheEntry(types.DownloadRecord{
				ID:           ev.DownloadID,
				URL:          ev.URL,
				URLHash:      store.URLHash(ev.URL),
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
	case types.EventStarted:
		deltaType = "add"
		gid = "sg_" + ev.DownloadID
		if m.tracker != nil {
			m.tracker.EnsureTrackedFromEvent(gid, ev.Total, ev.URL, ev.Workers, "active")
		}
		if surgeEng != nil {
			entry := types.DownloadRecord{
				ID:           ev.DownloadID,
				URL:          ev.URL,
				URLHash:      store.URLHash(ev.URL),
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
	case types.EventFirstByte:
		gid = "sg_" + ev.DownloadID
		if m.tracker != nil {
			m.tracker.SetTTFB(gid, ev.TTFBMs)
		}
		return
	case types.EventResumed:
		deltaType = "resume"
		gid = "sg_" + ev.DownloadID
		// Resumed payload is minimal (ID+Filename); merge onto existing
		// entry to avoid zeroing URL/DestPath/Mirrors/Workers.
		if surgeEng != nil {
			if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
				merged := existing
				merged.Status = "downloading"
				surgeEng.UpsertMasterCacheEntry(merged)
			}
		}
	case types.EventPaused:
		deltaType = "pause"
		gid = "sg_" + ev.DownloadID
		// Paused lacks URL/DestPath/TotalSize; merge onto existing entry.
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
				surgeEng.UpsertMasterCacheEntry(types.DownloadRecord{
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
	case types.EventComplete:
		deltaType = "complete"
		gid = "sg_" + ev.DownloadID
		completeTotal = ev.Total
		completeAvgSpeed = ev.AvgSpeed
		// Complete lacks URL/DestPath/Mirrors/Workers; merge onto existing.
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
				surgeEng.UpsertMasterCacheEntry(types.DownloadRecord{
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
	case types.EventError:
		deltaType = "error"
		gid = "sg_" + ev.DownloadID
		// Error is minimal; merge onto existing to preserve URL/Mirrors/etc.
		if surgeEng != nil {
			if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
				merged := existing
				merged.Status = "error"
				if ev.DestPath != "" {
					merged.DestPath = ev.DestPath
				}
				surgeEng.UpsertMasterCacheEntry(merged)
			} else {
				surgeEng.UpsertMasterCacheEntry(types.DownloadRecord{
					ID:       ev.DownloadID,
					Filename: ev.Filename,
					DestPath: ev.DestPath,
					Status:   "error",
				})
			}
		}
	case types.EventRemoved:
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
		if m.tracker != nil {
			m.tracker.SetStatusFromEvent(gid, "paused")
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
		if m.tracker != nil {
			m.tracker.SetStatusFromEvent(gid, "active")
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
		// Sync progress before MoveTaskToStopped: PatchTaskProgress only
		// scans sgActive, so it must run while the task is still active.
		if deltaType == "complete" && completeTotal > 0 {
			totalStr := strconv.FormatInt(completeTotal, 10)
			Cache.PatchTaskProgress(gid, totalStr, "0", totalStr)
		}
		Cache.MoveTaskToStopped(gid, deltaType)
		if State.HasWindow() {
			if deltaType == "complete" && completeTotal > 0 {
				totalStr := strconv.FormatInt(completeTotal, 10)
				m.pusher.Queue(events.TaskDelta{
					Type: deltaType, GID: gid,
					Payload: map[string]interface{}{
						"completedLength": totalStr,
						"downloadSpeed":   "0",
						"totalLength":     totalStr,
					},
				})
			} else {
				m.pusher.Queue(events.TaskDelta{Type: deltaType, GID: gid})
			}
		}
		if m.tracker != nil {
			if completeTotal > 0 {
				m.tracker.EnsureTrackedFromEvent(gid, completeTotal, "", 0, "")
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
		// Group delete operations on sg_ GIDs go through RemoveDownloadGroup
		// -> BatchRemove -> cleanupRemovedTask, which already removed the task
		// from Cache, tracker, and emitted a remove delta. The cleanup and
		// pusher.Queue below are therefore idempotent no-ops for those GIDs.
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

// surgePollLoop runs the periodic reconciliation loop at surgePollInterval.
// It executes reconcileSurgeCache immediately at startup (covering the
// startup window before the first event arrives) and then on each tick.
func (m *Monitor) surgePollLoop() {
	ticker := time.NewTicker(m.surgePollInterval)
	defer ticker.Stop()

	m.reconcileSurgeCache()

	for {
		select {
		case <-m.surgePollStopChan:
			return
		case <-ticker.C:
			m.reconcileSurgeCache()
		}
	}
}

// reconcileSurgeCache compares the Surge engine's actual task lists against
// the Cache sg_ slices and corrects discrepancies caused by dropped events.
// Reads engine state via non-Lite TellActive/TellWaiting/TellStopped (which
// read the masterCache mirror + Pool active — μs-level, no gob.Decode).
// All corrections reuse the same Cache methods as handleSurgeEvent, and
// processedComplete dedup prevents double-processing of completes.
func (m *Monitor) reconcileSurgeCache() {
	reader := m.surgePollReader
	if reader == nil && m.surgeEng != nil {
		reader = m.surgeEng
	}
	if reader == nil {
		return
	}

	// Sync the in-memory masterCache mirror from master.gob and clear the
	// 1s TTL list cache before reading, so a dropped complete/error event
	// (which leaves masterCache stale until the next 10s tick refresh) does
	// not cause the task to vanish from all three engine lists and get
	// removed instead of moved to stopped.
	if m.surgeEng != nil {
		m.surgeEng.RefreshMasterCache()
		m.surgeEng.InvalidateListCache()
	}

	engineActive, err := reader.TellActive()
	if err != nil {
		log.Printf("[Monitor] Surge poll: TellActive error: %v", err)
		return
	}
	engineWaiting, err := reader.TellWaiting(0, 10000)
	if err != nil {
		log.Printf("[Monitor] Surge poll: TellWaiting error: %v", err)
		return
	}
	engineStopped, err := reader.TellStopped(0, 10000)
	if err != nil {
		log.Printf("[Monitor] Surge poll: TellStopped error: %v", err)
		return
	}

	if !m.surgeRecovered.Swap(true) {
		log.Printf("[Monitor] Surge engine first recovery successful")
	}
	m.maybeLogRecoveryComplete()

	engineActive = prefixSgTasks(engineActive)
	engineWaiting = prefixSgTasks(engineWaiting)
	engineStopped = prefixSgTasks(engineStopped)

	engineActiveMap := taskMapByGid(engineActive)
	engineWaitingMap := taskMapByGid(engineWaiting)
	engineStoppedMap := taskMapByGid(engineStopped)
	engineAll := make(map[string]rpc.Task, len(engineActive)+len(engineWaiting)+len(engineStopped))
	for gid, t := range engineActiveMap {
		engineAll[gid] = t
	}
	for gid, t := range engineWaitingMap {
		engineAll[gid] = t
	}
	for gid, t := range engineStoppedMap {
		engineAll[gid] = t
	}

	cacheActive := filterSgOnly(Cache.GetActive())
	cacheWaiting := filterSgOnly(Cache.GetWaiting())
	cacheStopped := filterSgOnly(Cache.GetStopped())

	cacheAll := make(map[string]string, len(cacheActive)+len(cacheWaiting)+len(cacheStopped))
	for _, t := range cacheActive {
		cacheAll[t.GID] = "active"
	}
	for _, t := range cacheWaiting {
		cacheAll[t.GID] = "waiting"
	}
	for _, t := range cacheStopped {
		cacheAll[t.GID] = "stopped"
	}

	for gid, cacheList := range cacheAll {
		if _, exists := engineAll[gid]; exists {
			continue
		}
		Cache.RemoveTask(gid)
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
		log.Printf("[Monitor] Surge poll: removed stale task %s (was in %s)", gid, cacheList)
	}

	for gid, engineTask := range engineAll {
		cacheList, inCache := cacheAll[gid]
		if !inCache {
			list := engineListForStatus(engineTask.Status)
			Cache.AddSgTask(engineTask, list)
			if m.tracker != nil {
				m.tracker.EnsureTrackedFromEvent(gid, parseInt64(engineTask.TotalLength), sourceURLFromTask(engineTask), 0, engineStatusForTask(engineTask.Status))
			}
			Cache.PrefetchMetadata(gid)
			if list == "stopped" {
				if m.tracker != nil {
					status := "complete"
					if engineTask.Status == "error" {
						status = "error"
					}
					if completed := m.tracker.MarkCompleteFromEvent(gid, status); completed != nil {
						m.handleTaskComplete(completed)
					}
				}
				// Terminal: clear intention to avoid unbounded map growth.
				m.pauseResumeVersionMu.Lock()
				if m.pauseResumeIntentions != nil {
					delete(m.pauseResumeIntentions, gid)
				}
				m.pauseResumeVersionMu.Unlock()
			}
			log.Printf("[Monitor] Surge poll: added missing task %s to %s", gid, list)
			continue
		}

		engineList := engineListForStatus(engineTask.Status)
		if cacheList == engineList {
			continue
		}

		switch engineList {
		case "stopped":
			status := "complete"
			if engineTask.Status == "error" {
				status = "error"
			}
			// Sync progress from engine data before moving to stopped.
			if status == "complete" && engineTask.TotalLength != "" {
				Cache.PatchTaskProgress(gid, engineTask.TotalLength, "0", engineTask.TotalLength)
			}
			Cache.MoveTaskToStopped(gid, status)
			// Emit frontend delta (matches handleSurgeEvent complete/error path).
			if State.HasWindow() {
				if status == "complete" && engineTask.TotalLength != "" {
					m.pusher.Queue(events.TaskDelta{
						Type: status, GID: gid,
						Payload: map[string]interface{}{
							"completedLength": engineTask.TotalLength,
							"downloadSpeed":   "0",
							"totalLength":     engineTask.TotalLength,
						},
					})
				} else {
					m.pusher.Queue(events.TaskDelta{Type: status, GID: gid})
				}
			}
			m.hub.NotifyInternal(events.TaskDelta{Type: status, GID: gid})
			if m.tracker != nil {
				if total := parseInt64(engineTask.TotalLength); total > 0 {
					m.tracker.EnsureTrackedFromEvent(gid, total, "", 0, "")
				}
				if completed := m.tracker.MarkCompleteFromEvent(gid, status); completed != nil {
					m.handleTaskComplete(completed)
				}
			}
			// Terminal: clear intention to avoid unbounded map growth.
			m.pauseResumeVersionMu.Lock()
			if m.pauseResumeIntentions != nil {
				delete(m.pauseResumeIntentions, gid)
			}
			m.pauseResumeVersionMu.Unlock()
			log.Printf("[Monitor] Surge poll: moved task %s from %s to stopped (missed %s)", gid, cacheList, status)
		case "waiting":
			if m.tracker != nil {
				m.tracker.SetStatusFromEvent(gid, engineStatusForTask(engineTask.Status))
			}
			Cache.MoveTaskToWaiting(gid, "paused")
			if task := findTaskInCache(gid); task != nil {
				m.hub.EmitTaskMove(events.TaskMove{
					GID: gid, From: cacheList, To: "waiting", Task: task,
				})
			}
			if State.HasWindow() {
				m.pusher.Queue(events.TaskDelta{Type: "pause", GID: gid})
			}
			m.hub.NotifyInternal(events.TaskDelta{Type: "pause", GID: gid})
			log.Printf("[Monitor] Surge poll: moved task %s from %s to waiting (missed pause)", gid, cacheList)
		case "active":
			if m.tracker != nil {
				m.tracker.SetStatusFromEvent(gid, engineStatusForTask(engineTask.Status))
			}
			Cache.MoveTaskToActive(gid, "active")
			if task := findTaskInCache(gid); task != nil {
				m.hub.EmitTaskMove(events.TaskMove{
					GID: gid, From: cacheList, To: "active", Task: task,
				})
			}
			if State.HasWindow() {
				m.pusher.Queue(events.TaskDelta{Type: "resume", GID: gid})
			}
			m.hub.NotifyInternal(events.TaskDelta{Type: "resume", GID: gid})
			log.Printf("[Monitor] Surge poll: moved task %s from %s to active (missed resume)", gid, cacheList)
		}
	}
}

func prefixSgTasks(tasks []rpc.Task) []rpc.Task {
	out := make([]rpc.Task, len(tasks))
	for i, t := range tasks {
		out[i] = t
		if !strings.HasPrefix(t.GID, "sg_") {
			out[i].GID = "sg_" + t.GID
		}
	}
	return out
}

func filterSgOnly(tasks []rpc.Task) []rpc.Task {
	out := make([]rpc.Task, 0, len(tasks))
	for _, t := range tasks {
		if strings.HasPrefix(t.GID, "sg_") {
			out = append(out, t)
		}
	}
	return out
}

func taskMapByGid(tasks []rpc.Task) map[string]rpc.Task {
	m := make(map[string]rpc.Task, len(tasks))
	for _, t := range tasks {
		m[t.GID] = t
	}
	return m
}

func engineListForStatus(status string) string {
	switch status {
	case "active", "downloading":
		return "active"
	case "waiting", "paused":
		return "waiting"
	case "complete", "error":
		return "stopped"
	default:
		return "stopped"
	}
}

// engineStatusForTask maps an engine-side status string to the tracker status
// string. Distinct from engineListForStatus (which maps to cache list names)
// because tracker status preserves paused/complete/error granularity.
func engineStatusForTask(status string) string {
	switch status {
	case "active", "downloading":
		return "active"
	case "waiting":
		return "waiting"
	case "paused":
		return "paused"
	case "complete":
		return "complete"
	case "error":
		return "error"
	default:
		return "active"
	}
}

func sourceURLFromTask(t rpc.Task) string {
	if len(t.Files) > 0 && len(t.Files[0].Uris) > 0 {
		return t.Files[0].Uris[0].Uri
	}
	return ""
}
