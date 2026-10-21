package monitor

import (
	"context"
	"log"
	"maps"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
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
	m.surgeBridgeWg.Go(func() {
		m.surgeEventBridgeLoop()
	})
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
				ev, ok := rawEvt.(types.DownloadEvent)
				if !ok {
					log.Printf("[Monitor] dropping unexpected Surge stream event type %T", rawEvt)
					continue
				}
				m.handleSurgeEvent(ev)
			}
		}

		select {
		case <-m.stopChan:
			return
		case <-time.After(m.surgePollInterval):
		}
	}
}

func (m *Monitor) handleSurgeEvent(ev types.DownloadEvent) {
	if ev.Type == types.EventQueued || ev.Type == types.EventStarted || ev.Type == types.EventResumed || ev.Type == types.EventPaused {
		m.handleSurgeLifecycleEvent(ev)
		return
	}
	var deltaType string
	var gid string
	var completeTotal int64
	var completeAvgSpeed float64
	var errorMessage string
	var errorCode string

	// Reuse the cached SurgeEngine ref (set in Start); nil in Aria2-only mode.
	surgeEng := m.surgeEng

	switch ev.Type {
	case types.EventProgress:
		gid = "sg_" + ev.DownloadID
		if ev.BitmapWidth > 0 && len(ev.ChunkBitmap) > 0 && m.chunkSnapshots != nil {
			m.chunkSnapshots.Set(gid, ev.ChunkBitmap, ev.BitmapWidth, ev.ChunkSize, ev.ChunkProgress)
		}
		completedStr := strconv.FormatInt(ev.Downloaded, 10)
		speedStr := strconv.FormatInt(int64(ev.Speed), 10)
		totalStr := strconv.FormatInt(ev.Total, 10)
		Cache.PatchTaskProgress(gid, completedStr, speedStr, totalStr)
		if m.tracker != nil {
			m.tracker.UpdateProgressFromEvent(gid, ev.Total, ev.Downloaded)
			m.tracker.UpdateLiveConnections(gid, ev.Connections)
		}
		if State.HasWindow() && m.pusher != nil {
			payload := map[string]string{
				"completedLength": completedStr,
				"downloadSpeed":   speedStr,
				"totalLength":     totalStr,
				"threads":         strconv.Itoa(ev.Connections),
			}
			m.pusher.Queue(events.TaskDelta{
				Type:    "progress",
				GID:     gid,
				Payload: payload,
			})
		}
		return

	case types.EventBatchProgress:
		for _, p := range ev.BatchEvents {
			pgid := "sg_" + p.DownloadID
			// Snapshot writes are unconditional: chunk fields ride the
			// engine's slower attach throttle, so most batch frames carry
			// none and must not clear the stored snapshot.
			if p.BitmapWidth > 0 && len(p.ChunkBitmap) > 0 && m.chunkSnapshots != nil {
				m.chunkSnapshots.Set(pgid, p.ChunkBitmap, p.BitmapWidth, p.ChunkSize, p.ChunkProgress)
			}
			completedStr := strconv.FormatInt(p.Downloaded, 10)
			speedStr := strconv.FormatInt(int64(p.Speed), 10)
			totalStr := strconv.FormatInt(p.Total, 10)
			Cache.PatchTaskProgress(pgid, completedStr, speedStr, totalStr)
			if m.tracker != nil {
				m.tracker.UpdateProgressFromEvent(pgid, p.Total, p.Downloaded)
				m.tracker.UpdateLiveConnections(pgid, p.Connections)
			}
			if State.HasWindow() && m.pusher != nil {
				payload := map[string]string{
					"completedLength": completedStr,
					"downloadSpeed":   speedStr,
					"totalLength":     totalStr,
					"threads":         strconv.Itoa(p.Connections),
				}
				m.pusher.Queue(events.TaskDelta{
					Type:    "progress",
					GID:     pgid,
					Payload: payload,
				})
			}
		}
		return

	case types.EventFirstByte:
		gid = "sg_" + ev.DownloadID
		if m.tracker != nil {
			m.tracker.SetTTFB(gid, ev.TTFBMs)
		}
		return
	case types.EventComplete:
		deltaType = "complete"
		gid = "sg_" + ev.DownloadID
		cached := findTaskInCache(gid)
		completeTotal = canonicalCompleteTotal(ev.Total, cached)
		completeAvgSpeed = ev.AvgSpeed
		if completeTotal > 0 && ev.Elapsed.Seconds() > 0 {
			completeAvgSpeed = float64(completeTotal) / ev.Elapsed.Seconds()
		}
	case types.EventError:
		deltaType = "error"
		gid = "sg_" + ev.DownloadID
		if ev.Err != nil {
			errorMessage = ev.Err.Error()
			errorCode = rpc.ClassifySurgeErrorCode(errorMessage, true)
		}
	case types.EventRemoved:
		deltaType = "remove"
		gid = "sg_" + ev.DownloadID
		m.tracker.RunUnderLifecycle(gid, func() {
			if surgeEng != nil {
				surgeEng.RemoveMasterCacheEntry(ev.DownloadID)
			}
			m.removeLocalTaskLocked(gid)
			// Queue inside the removal's own gate so the remove delta's
			// enqueue order matches the commit order for this GID.
			if State.HasWindow() && m.pusher != nil {
				m.pusher.Queue(events.TaskDelta{Type: "remove", GID: gid})
			}
		})
	default:
		return
	}

	// For pause/resume, patch the cache status immediately so that
	// GetTasks() returns the correct status before the next tick runs.
	// Also queue the delta via pusher for direct frontend delivery (~50ms),
	// eliminating the gap between progress stopping (immediate from event
	// stream) and the status badge/card style changing (was waiting for tick).
	switch deltaType {
	case "complete", "error":
		if surgeEng != nil {
			surgeEng.InvalidateListCache()
		}
		errCode, errMsg := "", ""
		if deltaType == "error" {
			errCode, errMsg = errorCode, errorMessage
		}
		// Accepted terminal transaction under the GID gate: narrow master
		// mirror merge (identity/range/rate preserved) + cache move +
		// terminal field commit + acceptance + delta publish, then post-gate
		// finish. The delta queues inside the same gate section so its enqueue
		// order matches the commit order against any concurrent same-GID
		// commit (e.g. an immediate ApplyResume) — queueing after the blocking
		// finish would let a newer commit's delta land first and then be
		// displaced by this stale one.
		m.moveToStoppedAndHandleGate(gid, deltaType, errCode, errMsg, completeTotal, func() {
			mergeSurgeTerminalMaster(surgeEng, ev, deltaType, completeTotal, completeAvgSpeed, errorMessage)
		}, func(completed *TrackedTask) {
			if deltaType == "complete" {
				completed.terminalElapsed = ev.Elapsed
			}
			// AvgSpeed substitutes for PeakSpeed when no peak-time accept occurred;
			// acceptPeakSpeed refreshes PeakEnvKey to Current on this complete copy only.
			if completed.PeakSpeed == 0 && completeAvgSpeed > 0 {
				acceptPeakSpeed(completed, int64(completeAvgSpeed))
				// Speedstats only: history must not persist this as a peak.
				completed.peakFromAvgFallback = true
			}
		}, func(committedTotal int64) {
			if !State.HasWindow() || m.pusher == nil {
				return
			}
			switch {
			case deltaType == "complete" && committedTotal > 0:
				totalStr := strconv.FormatInt(committedTotal, 10)
				m.pusher.Queue(events.TaskDelta{
					Type: deltaType, GID: gid,
					Payload: map[string]string{
						"completedLength": totalStr,
						"downloadSpeed":   "0",
						"totalLength":     totalStr,
					},
				})
			case deltaType == "error" && (errorCode != "" || errorMessage != ""):
				m.pusher.Queue(events.TaskDelta{
					Type: deltaType, GID: gid,
					Payload: map[string]string{
						"errorCode":    errorCode,
						"errorMessage": errorMessage,
					},
				})
			default:
				m.pusher.Queue(events.TaskDelta{Type: deltaType, GID: gid})
			}
		})

	case "remove":
		// Group delete operations on sg_ GIDs go through RemoveDownloadGroup
		// -> BatchRemove -> cleanupRemovedTask -> InvalidateTask, which
		// already removed the task from Cache, tracker, and per-GID caches
		// (telemetry, chunk snapshots) and emitted a remove delta. The gate
		// removal and its in-gate remove delta above are idempotent no-ops
		// for those GIDs.

		if m.convergence != nil {
			m.convergence.RemoveTask(gid)
		}

	}

	m.hub.NotifyInternal(events.TaskDelta{Type: deltaType, GID: gid})

	log.Printf("[Monitor] Surge Event: %s -> %s (gid: %s)", deltaType, gid, gid)
	log.Printf("[DEBUG-EVT-MON] handleSurgeEvent done: type=%s gid=%s", deltaType, gid)

	if deltaType == "complete" || deltaType == "error" || deltaType == "remove" {
		m.ScheduleIdleMemoryReclaim()
		// A terminal/remove acceptance frees a commitment; re-evaluate admission
		// on this fact rather than waiting for a busy-release edge.
		m.KickAdmission()
	}
}

// mergeSurgeTerminalMaster performs the narrow master-mirror merge of an
// accepted terminal event, preserving identity/range/rate fields. Caller
// holds the GID lifecycle gate; runs only on accepted copies.
func mergeSurgeTerminalMaster(surgeEng *rpc.SurgeEngine, ev types.DownloadEvent, deltaType string, completeTotal int64, completeAvgSpeed float64, errorMessage string) {
	if surgeEng == nil {
		return
	}
	switch deltaType {
	case "complete":
		// Complete lacks URL/DestPath/Mirrors/Workers; merge onto existing.
		if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
			merged := existing
			merged.Status = "completed"
			merged.TotalSize = completeTotal
			merged.Downloaded = completeTotal
			merged.CompletedAt = time.Now().Unix()
			merged.TimeTaken = ev.Elapsed.Milliseconds()
			merged.AvgSpeed = completeAvgSpeed
			merged.RateLimit = ev.RateLimit
			merged.RateLimitSet = ev.RateLimitSet
			surgeEng.UpsertMasterCacheEntry(merged)
		} else {
			rec := types.DownloadRecord{
				ID:           ev.DownloadID,
				Filename:     ev.Filename,
				Status:       "completed",
				TotalSize:    completeTotal,
				Downloaded:   completeTotal,
				CompletedAt:  time.Now().Unix(),
				TimeTaken:    ev.Elapsed.Milliseconds(),
				AvgSpeed:     completeAvgSpeed,
				RateLimit:    ev.RateLimit,
				RateLimitSet: ev.RateLimitSet,
			}
			if ev.State != nil {
				keepRangeAcquisition(&rec, *ev.State)
			}
			surgeEng.UpsertMasterCacheEntry(rec)
		}
	case "error":
		// Error is minimal; merge onto existing to preserve URL/Mirrors/etc.
		// An early error may be the first event carrying ev.State — keep the
		// strategy/range metadata it transports instead of dropping it.
		if existing, ok := surgeEng.GetMasterCacheEntry(ev.DownloadID); ok {
			merged := existing
			merged.Status = "error"
			if ev.State != nil {
				keepRangeAcquisition(&merged, *ev.State)
			}
			if ev.DestPath != "" {
				merged.DestPath = ev.DestPath
			}
			if errorMessage != "" {
				merged.Error = errorMessage
			}
			surgeEng.UpsertMasterCacheEntry(merged)
		} else {
			rec := types.DownloadRecord{
				ID:       ev.DownloadID,
				Filename: ev.Filename,
				DestPath: ev.DestPath,
				Status:   "error",
				Error:    errorMessage,
			}
			if ev.State != nil {
				keepRangeAcquisition(&rec, *ev.State)
			}
			surgeEng.UpsertMasterCacheEntry(rec)
		}
	}
}

// handleSurgeLifecycleEvent adjudicates queue/start/pause/resume events under
// the per-GID lifecycle gate. The live pool read inside the gate is the only
// authority: a known live status wins over stale cache/list echoes, an
// explicit absent refuses reopening, and unknown stays conservative —
// non-terminal rows fold normally while active/terminal rows hold.
func (m *Monitor) handleSurgeLifecycleEvent(ev types.DownloadEvent) {
	gid := "sg_" + ev.DownloadID
	m.tracker.RunUnderLifecycle(gid, func() {
		task, source, exists := Cache.GetTask(gid)
		entry, historied := history.Get(gid)
		terminal := source == "stopped" || historied || m.tracker.TerminalAcceptedInCurrentGeneration(gid)
		live := m.readSurgeLiveStatus(gid)
		status, kind := "waiting", "add"
		switch ev.Type {
		case types.EventResumed:
			kind = "resume"
			if !exists && !historied {
				return
			}
			switch {
			case live.Known:
				if live.Status != "waiting" {
					return
				}
			case live.Absent:
				return
			case terminal || (exists && (source == "active" || task.Status == "active")):
				// Unknown live never downgrades an active row or reopens a
				// terminal one.
				return
			}
		case types.EventQueued:
			switch {
			case live.Known:
				if live.Status != "waiting" {
					return
				}
			case live.Absent:
				return
			case terminal || (exists && (source == "active" || task.Status == "active" || task.Status == "paused")):
				return
			}
		case types.EventStarted:
			status = "active"
			switch {
			case live.Known:
				if live.Status != "active" {
					return
				}
			case live.Absent:
				return
			case terminal:
				return
			}
		case types.EventPaused:
			status, kind = "paused", "pause"
			if !exists || terminal || (live.Known && live.Status != "paused") {
				return
			}
		default:
			return
		}
		if !exists && historied {
			full := history.ToStoppedTask(entry)
			full.Title = entry.Title
			Cache.AddSgTask(full, "stopped")
			source = "stopped"
		}
		patch := rpc.Task{GID: gid, Status: status, DownloadSpeed: "0"}
		if ev.Total > 0 {
			patch.TotalLength = strconv.FormatInt(ev.Total, 10)
		}
		if ev.Downloaded > 0 {
			patch.CompletedLength = strconv.FormatInt(ev.Downloaded, 10)
		}
		to := engineListForStatus(status)
		Cache.AddSgTask(patch, to)
		if source == "stopped" {
			if m.tracker != nil && m.tracker.afterMoveBeforeRetire != nil {
				m.tracker.afterMoveBeforeRetire(gid)
			}
			retireHistoryAndReopenToStatusLocked(m.tracker, gid, status)
		}
		if m.tracker != nil {
			m.tracker.EnsureTrackedFromEvent(gid, ev.Total, ev.URL, ev.Workers, status)
			m.tracker.SetStatusFromEvent(gid, status)
			if ev.Downloaded > 0 {
				m.tracker.UpdateProgressFromEvent(gid, ev.Total, ev.Downloaded)
			}
		}
		task, _, _ = Cache.GetTask(gid)
		change := acceptedTaskChange{task: task, from: source, to: to, kind: kind}
		if status == "active" && m.tracker != nil {
			completed, known := live.CompletedLength, live.CompletedKnown
			if ev.State != nil {
				completed, known = ev.State.Downloaded, true
			}
			baseline, known := surgeActivationBaseline(m.tracker, gid, completed, known)
			m.tracker.ObserveActivation(gid, baseline, known)
			if ev.Type == types.EventStarted && (ev.State == nil || !ev.State.IsResume) && m.chunkSnapshots != nil {
				m.chunkSnapshots.Remove(gid)
			}
		}
		if m.surgeEng != nil {
			master, _ := m.surgeEng.GetMasterCacheEntry(ev.DownloadID)
			master.ID = ev.DownloadID
			if ev.State != nil {
				keepRangeAcquisition(&master, *ev.State)
			}
			master.Status = map[string]string{"active": "downloading", "waiting": "queued", "paused": "paused"}[status]
			if ev.URL != "" {
				master.URL, master.URLHash = ev.URL, store.URLHash(ev.URL)
			}
			if ev.DestPath != "" {
				master.DestPath = ev.DestPath
			}
			if ev.Filename != "" {
				master.Filename = ev.Filename
			}
			if len(ev.Mirrors) > 0 {
				master.Mirrors = append([]string(nil), ev.Mirrors...)
			}
			if ev.Total > 0 {
				master.TotalSize = ev.Total
			}
			if ev.Downloaded > 0 {
				master.Downloaded = ev.Downloaded
			}
			if ev.Workers > 0 {
				master.Workers = ev.Workers
			}
			if ev.MinChunkSize > 0 {
				master.MinChunkSize = ev.MinChunkSize
			}
			if ev.Type != types.EventResumed {
				master.RateLimit, master.RateLimitSet = ev.RateLimit, ev.RateLimitSet
			}
			m.surgeEng.UpsertMasterCacheEntry(master)
			m.surgeEng.InvalidateListCache()
		}
		// Publish inside the gate so the emit order of concurrent commits
		// for this GID matches the commit order.
		if change.task.GID != "" {
			Cache.PrefetchMetadata(gid)
			m.publishAcceptedTaskChange(change)
		}
	})
}

var (
	idleReclaimDelay  = 3 * time.Second
	idleReclaimAction = func() {
		// FreeOSMemory internally triggers runtime.GC() before releasing memory to OS.
		debug.FreeOSMemory()
	}
)

// ScheduleIdleMemoryReclaim debounces FreeOSMemory after Surge terminal events.
func (m *Monitor) ScheduleIdleMemoryReclaim() {
	if m == nil || m.surgeEng == nil {
		return
	}

	m.idleReclaimMu.Lock()
	defer m.idleReclaimMu.Unlock()

	if m.idleReclaimStopped {
		return
	}

	if m.idleReclaimTimer != nil {
		m.idleReclaimTimer.Stop()
	}

	m.idleReclaimTimer = time.AfterFunc(idleReclaimDelay, func() {
		if m.surgeEng == nil || m.surgeEng.ActiveCount() != 0 {
			return
		}
		idleReclaimAction()
	})
}

func keepRangeAcquisition(dst *types.DownloadRecord, src types.DownloadRecord) {
	if dst == nil {
		return
	}
	if dst.RangeAcquisitionMode == "" {
		dst.RangeAcquisitionMode = src.RangeAcquisitionMode
	}
	if !dst.SkipServerProbe {
		dst.SkipServerProbe = src.SkipServerProbe
	}
	if dst.Strategy == "" {
		dst.Strategy = src.Strategy
	}
}

// findTaskInCache searches active+waiting+stopped cache slices for a task by GID
// using a coherent snapshot.
func findTaskInCache(gid string) *rpc.Task {
	active, waiting, stopped := Cache.GetTaskLists()
	for i := range active {
		if active[i].GID == gid {
			t := active[i]
			return &t
		}
	}
	for i := range waiting {
		if waiting[i].GID == gid {
			t := waiting[i]
			return &t
		}
	}
	for i := range stopped {
		if stopped[i].GID == gid {
			t := stopped[i]
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
	maps.Copy(engineAll, engineActiveMap)
	maps.Copy(engineAll, engineWaitingMap)
	maps.Copy(engineAll, engineStoppedMap)

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

	// Deletion requires an explicit absent answer from the scheduler.
	// Unknown (nil-state) is not absence and never removes anything; a known
	// live status is real evidence, so the candidate re-enters the union and
	// the main loop repairs it from live state.
	deleteCandidates := make(map[string]string, len(cacheAll))
	maps.Copy(deleteCandidates, cacheAll)
	if m.tracker != nil {
		for _, gid := range m.tracker.SurgeGIDs() {
			if _, ok := deleteCandidates[gid]; !ok {
				deleteCandidates[gid] = ""
			}
		}
	}
	for gid, cacheList := range deleteCandidates {
		if _, exists := engineAll[gid]; exists {
			continue
		}
		removed := false
		m.tracker.RunUnderLifecycle(gid, func() {
			live := m.readSurgeLiveStatus(gid)
			if live.Known {
				liveTask, _, _ := Cache.GetTask(gid)
				liveTask.GID = gid
				engineAll[gid] = liveTask
				return
			}
			if !live.Absent {
				return
			}
			if m.surgeEng != nil {
				id := strings.TrimPrefix(gid, "sg_")
				if rec, ok := m.surgeEng.GetMasterCacheEntry(id); ok {
					switch rec.Status {
					case "queued", "downloading", "pausing", "paused", "completed", "error":
						// A projectable mirror row is engine-side evidence the
						// bulk read raced an event write; defer to it.
						return
					default:
						m.surgeEng.RemoveMasterCacheEntry(id)
					}
				}
			}
			m.removeLocalTaskLocked(gid)
			removed = true
			// Same commit-order rule as the event path: the remove delta
			// queues inside this GID's gate, not after it.
			if cacheList != "" && State.HasWindow() && m.pusher != nil {
				m.pusher.Queue(events.TaskDelta{Type: "remove", GID: gid})
			}
		})
		if !removed {
			continue
		}
		// Accepted removal frees a commitment — notify outside the GID gate.
		m.KickAdmission()
		if m.convergence != nil {
			m.convergence.RemoveTask(gid)
		}
		if cacheList != "" {
			log.Printf("[Monitor] Surge poll: removed stale task %s (was in %s)", gid, cacheList)
		}
	}

	// Snapshot keys absent from every engine list are orphans: their
	// start/remove events were both lost, so nothing else will ever
	// visit them. Runs only on a full three-list read (early returns
	// above protect partial reads) and only on an explicit absent answer —
	// keys are enumerated after the successful read, never pre-sampled.
	if m.chunkSnapshots != nil {
		for _, gid := range m.chunkSnapshots.GIDs() {
			if _, ok := engineAll[gid]; !ok {
				m.tracker.RunUnderLifecycle(gid, func() {
					if m.readSurgeLiveStatus(gid).Absent {
						m.chunkSnapshots.Remove(gid)
					}
				})
			}
		}
	}

	for gid, engineTask := range engineAll {
		var accepted *TrackedTask
		terminalCommitted := false
		m.tracker.RunUnderLifecycle(gid, func() {
			task, source, exists := Cache.GetTask(gid)
			live := m.readSurgeLiveStatus(gid)
			status := engineTask.Status
			if live.Known {
				status = live.Status
				engineTask.TotalLength, engineTask.CompletedLength = "", ""
				if live.TotalKnown {
					engineTask.TotalLength = strconv.FormatInt(live.TotalLength, 10)
				}
				if live.CompletedKnown {
					engineTask.CompletedLength = strconv.FormatInt(live.CompletedLength, 10)
				}
			}
			switch status {
			case "active", "waiting", "paused", "complete", "error":
			default:
				return
			}
			list := engineListForStatus(status)
			if !live.Known && (list == "active" ||
				status == "waiting" && source == "active" || status == "paused" && task.Status == "waiting") {
				// Unknown live state must not manufacture activation, demote
				// an active row to waiting, or overwrite a waiting row with
				// a mirror-only pause.
				return
			}
			histEntry, historied := history.Get(gid)
			realQueued := live.Known && live.Status == "waiting"
			if list == "waiting" {
				switch {
				case !exists && historied && !realQueued:
					// A durable terminal row must never rejoin waiting on
					// mirror evidence alone; seed stopped and mark it terminal.
					stoppedTask := history.ToStoppedTask(histEntry)
					stoppedTask.Title = histEntry.Title
					Cache.AddSgTask(stoppedTask, "stopped")
					if m.chunkSnapshots != nil {
						m.chunkSnapshots.Remove(gid)
					}
					if m.tracker != nil {
						m.tracker.EnsureTrackedFromEvent(gid, parseInt64(stoppedTask.TotalLength), sourceURLFromTask(stoppedTask), 0, stoppedTask.Status)
						m.tracker.MarkCompleteFromEvent(gid, stoppedTask.Status)
					}
					terminalCommitted = true
					return
				case !exists && !live.Known:
					// A mirror-only waiting/paused row is not enqueue evidence.
					return
				case source == "stopped" && !realQueued:
					// Stale waiting/paused projections must not revive a
					// terminal stopped row.
					return
				}
			}
			if !exists && historied {
				// Cache-loss restore: carry the durable projection's metadata
				// under the engine identity/checkpoint.
				stoppedTask := history.ToStoppedTask(histEntry)
				stoppedTask.Title = histEntry.Title
				fillTaskMetadata(&engineTask, stoppedTask)
			}
			if exists {
				fillTaskMetadata(&task, engineTask)
				engineTask.Title, engineTask.Dir = task.Title, task.Dir
				engineTask.Files, engineTask.DownloadGroup = task.Files, task.DownloadGroup
			}
			engineTask.Status = status
			if list != "active" {
				engineTask.DownloadSpeed = "0"
			}
			if list == "stopped" {
				if source == "stopped" {
					return
				}
				if status == "error" && engineTask.ErrorCode == "" {
					engineTask.ErrorCode = rpc.ClassifySurgeErrorCode(engineTask.ErrorMessage, true)
				}
				if exists {
					total := parseInt64(engineTask.TotalLength)
					if status == "complete" && total <= 0 {
						// Same in-gate canonical fallback as the event path: a
						// stopped row with empty TotalLength must still commit
						// the cached checkpoint's total=completed.
						if task, _, ok := Cache.GetTask(gid); ok {
							total = parsePositiveLength(task.TotalLength)
						}
					}
					// Terminal move commits the canonical total in the same
					// critical section — waiting-source rows included.
					Cache.MoveTaskToStoppedTerminal(gid, status, engineTask.ErrorCode, engineTask.ErrorMessage, total)
				} else {
					if status == "complete" && engineTask.TotalLength != "" {
						engineTask.CompletedLength = engineTask.TotalLength
						engineTask.DownloadSpeed = "0"
					}
					Cache.AddSgTask(engineTask, list)
				}
				if m.tracker != nil {
					m.tracker.EnsureTrackedFromEvent(gid, parseInt64(engineTask.TotalLength), sourceURLFromTask(engineTask), 0, status)
				}
				accepted = m.markCompleteAndHandleLocked(gid, status, nil)
			} else {
				Cache.AddSgTask(engineTask, list)
				if source == "stopped" || !exists && (list == "active" || historied) {
					if m.tracker != nil && m.tracker.afterMoveBeforeRetire != nil {
						m.tracker.afterMoveBeforeRetire(gid)
					}
					retireHistoryAndReopenToStatusLocked(m.tracker, gid, status)
				}
				if m.tracker != nil {
					m.tracker.EnsureTrackedFromEvent(gid, parseInt64(engineTask.TotalLength), sourceURLFromTask(engineTask), 0, status)
					m.tracker.SetStatusFromEvent(gid, status)
					completed := parseInt64(engineTask.CompletedLength)
					if status == "active" {
						baseline, known := surgeActivationBaseline(m.tracker, gid, completed, live.CompletedKnown)
						m.tracker.ObserveActivation(gid, baseline, known)
					}
					if completed > 0 {
						m.tracker.UpdateProgressFromEvent(gid, parseInt64(engineTask.TotalLength), completed)
					}
				}
			}
			prefetch := !exists
			var change acceptedTaskChange
			if !exists || source != list || task.Status != status {
				kind := "resume"
				if !exists {
					kind = "add"
				}
				if status == "paused" {
					kind = "pause"
				}
				if list == "stopped" {
					kind = status
				}
				task, _, _ = Cache.GetTask(gid)
				change = acceptedTaskChange{task: task, from: source, to: list, kind: kind}
			}
			// Prefetch + publish inside the gate so per-GID emits serialize
			// in commit order.
			if prefetch {
				Cache.PrefetchMetadata(gid)
			}
			m.publishAcceptedTaskChange(change)
		})
		if accepted != nil {
			m.finishTerminalCopy(accepted)
		}
		if terminalCommitted {
			// A durable terminal seed committed inside the gate without an
			// emitted change; still an admission-relevant fact.
			m.KickAdmission()
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

// surgeActivationBaseline picks the run baseline for a real Surge start or
// authoritative engine-active poll row: prefer the engine-supplied completed
// value when it is a reliable observation, then the tracker's stored
// checkpoint — a sparse engine zero must not erase an already-known resume
// amount. known=false when no trustworthy checkpoint exists; the first
// reliable completed observation then becomes the baseline without counting
// as growth.
func surgeActivationBaseline(tracker *TaskTracker, gid string, engineCompleted int64, engineKnown bool) (int64, bool) {
	baseline, known := engineCompleted, engineKnown
	if tracker != nil {
		if tt, ok := tracker.GetTrackedTask(gid); ok && tt.CompletedLength > baseline {
			baseline = tt.CompletedLength
			known = true
		}
	}
	return baseline, known
}

func sourceURLFromTask(t rpc.Task) string {
	if len(t.Files) > 0 && len(t.Files[0].Uris) > 0 {
		return t.Files[0].Uris[0].Uri
	}
	return ""
}

func parsePositiveLength(s string) int64 {
	val, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err == nil && val > 0 {
		return val
	}
	return 0
}

// canonicalCompleteTotal derives a definitive total length for completion events.
// Priority:
// 1. Positive event total (authoritative from engine completion event)
// 2. Positive cached task total (preserves prior known total if event had non-positive total)
// 3. 0 (legal empty file or unknown)
func canonicalCompleteTotal(eventTotal int64, cached *rpc.Task) int64 {
	if eventTotal > 0 {
		return eventTotal
	}
	if cached != nil {
		if total := parsePositiveLength(cached.TotalLength); total > 0 {
			return total
		}
	}
	return 0
}
