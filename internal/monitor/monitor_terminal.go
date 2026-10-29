// Terminal task completion flow, active duration timing, metadata recovery RPC, BBR speedstats recording, and durable history persistence.

package monitor

import (
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
)

var scopeClassifier = speedstats.NewScopeClassifier()

// scopeClassifyByURL is the classify call used by the terminal fallback;
// tests stub it once at package init so hostnames never reach OS DNS.
var scopeClassifyByURL = scopeClassifier.ClassifyByURL

// finishTerminalCopy performs the post-gate finish for a copy accepted under
// the GID gate: blocking recovery runs ungated, then speed stats and the
// history write re-enter the gate and validate the copy so a post-accept
// reopen/remove/re-add drops the stale snapshot instead of recording stats
// or history for a retired generation.
func (m *Monitor) finishTerminalCopy(task *TrackedTask) {
	if task == nil {
		return
	}
	m.recoverTerminalInfo(task)
	if m.tracker != nil {
		m.tracker.RunUnderLifecycle(task.GID, func() {
			// terminalToken == 0 passes only for hand-built test copies;
			// every production accept stamps a token.
			if task.terminalToken == 0 || m.tracker.TerminalCopyCurrent(task.GID, task) {
				m.recordTerminalSpeedStats(task)
				m.writeTerminalHistoryLocked(task)
			}
		})
		return
	}
	m.recordTerminalSpeedStats(task)
	m.writeTerminalHistoryLocked(task)
}

// recoverTerminalInfo fills missing file/dir/group fields from the metadata
// cache and, as a last resort, a blocking TellStatus call. Call it outside the
// GID lifecycle gate whenever possible.
func (m *Monitor) recoverTerminalInfo(task *TrackedTask) {
	// 清理 convergence state（无论后续路径如何，都应清理）
	if m.convergence != nil {
		m.convergence.RemoveTask(task.GID)
	}

	// 如果 FilePath 为空，尝试从元数据缓存获取
	// 场景：快速完成的任务可能在 Tracker 更新前已完成，导致 FilePath 未被填充
	if task.FilePath == "" {
		if meta := Cache.GetMetadata(task.GID); meta != nil && len(meta.Files) > 0 {
			task.FilePath = meta.Files[0]
			task.Dir = meta.Dir
			if meta.SourceURL != "" {
				task.SourceURL = meta.SourceURL
			}
			if task.DownloadGroup == nil && meta.DownloadGroup != nil {
				task.DownloadGroup = copyDownloadGroup(meta.DownloadGroup)
			}
			log.Printf("[Monitor] Recovered file info from cache for task: %s -> %s", task.GID, task.FilePath)
		}
	}
	if task.DownloadGroup == nil {
		task.DownloadGroup = Cache.GetTaskGroup(task.GID)
		if task.DownloadGroup == nil {
			task.DownloadGroup = GetStoredTaskGroup(task.GID)
		}
	}

	// 如果仍然为空，尝试直接调用 RPC 获取
	if task.FilePath == "" {
		if t, err := m.engine.TellStatus(task.GID, nil); err == nil && len(t.Files) > 0 && t.Files[0].Path != "" {
			task.FilePath = t.Files[0].Path
			task.Dir = t.Dir
			if len(t.Files[0].Uris) > 0 {
				task.SourceURL = t.Files[0].Uris[0].Uri
			}
			if task.DownloadGroup == nil && t.DownloadGroup != nil {
				task.DownloadGroup = copyDownloadGroup(t.DownloadGroup)
			}
			log.Printf("[Monitor] Recovered file info from RPC for task: %s -> %s", task.GID, task.FilePath)
		}
	}
}

// recordTerminalSpeedStats writes the >50MB peak-speed record without
// depending on FilePath; safe outside the GID gate.
func (m *Monitor) recordTerminalSpeedStats(task *TrackedTask) {
	// 1. 记录速度统计（仅 >50MB 文件）— 不依赖 FilePath，先于 history 执行
	if task.TotalLength > speedstats.MinFileSize && task.PeakSpeed > 0 {
		// Fallback chain: PeakThreadCount (D3) → ThreadCount (allocated/started workers) → config
		threadCount := task.PeakThreadCount
		threadSource := "peakThreadCount"
		if threadCount <= 0 {
			threadCount = task.ThreadCount
			threadSource = "threadCount"
		}
		if threadCount <= 0 {
			if v, err := strconv.Atoi(config.Get().MaxConnections); err == nil && v > 0 {
				threadCount = v
				threadSource = "config"
			}
		}
		isExploration := task.IsExploration

		// Fallback: tasks that bypassed service_add.go (external RPC, resume after restart)
		// have empty Domain/Scope. Classify from SourceURL to prevent polluting the wan pool.
		if task.Domain == "" && task.SourceURL != "" {
			scope, domain := scopeClassifyByURL(task.SourceURL)
			task.Scope = scope
			task.Domain = domain
			// PeakEnvKey stays empty — no netenv participation in fallback path.
		}

		// isExploration 直接从 tracker 记录取值，不再重新计算或减半

		// Skip recording if envKey is empty — external RPC/wake-up tasks have no
		// envKey and would pollute env-aware history buckets.
		if task.PeakEnvKey == "" {
			log.Printf("[Monitor] Skipping speed stats for task %s: no envKey (external RPC or wake-up path)", task.GID)
		} else if m.sequentialSpeedstatsSkip(task) {
			log.Printf("[Monitor] Skipping speed stats for task %s: range_unsupported", task.GID)
		} else if threadCount <= 0 {
			log.Printf("[Monitor] Skipping speed stats for task %s: no thread count", task.GID)
		} else if task.Domain == "" {
			// Skip recording if we still have no domain — a record without domain is useless
			// for BBR (GetDomainPeak/GetRTprop can't match) and would only pollute V_global_peak.
			log.Printf("[Monitor] Skipping speed stats for task %s: no domain/URL available", task.GID)
		} else if he, ok := m.engine.(*rpc.HybridEngine); ok {
			if bps, rateLimited := he.GetRateLimit(task.GID); rateLimited && bps > 0 {
				// Positive cap only: skip AddRecordV2 so capped throughput does not
				// pollute BBR. Zero/unlimited (bps<=0) records normally.
				log.Printf("[Monitor] Skipping speed stats for task %s: rate-limited (would pollute BBR)", task.GID)
			} else {
				speedstats.AddRecordV2(task.PeakSpeed, threadCount, task.TotalLength, isExploration, task.TTFBMs, task.Domain, task.Scope, task.PeakEnvKey)
				log.Printf("[Monitor] Speed stats recorded: peak=%d, threads=%d (source=%s), exploration=%v, scope=%s, envKey=%s, domain=%s, ttfb=%d",
					task.PeakSpeed, threadCount, threadSource, isExploration, task.Scope, task.PeakEnvKey, task.Domain, task.TTFBMs)
			}
		} else {
			speedstats.AddRecordV2(task.PeakSpeed, threadCount, task.TotalLength, isExploration, task.TTFBMs, task.Domain, task.Scope, task.PeakEnvKey)
			log.Printf("[Monitor] Speed stats recorded: peak=%d, threads=%d (source=%s), exploration=%v, scope=%s, envKey=%s, domain=%s, ttfb=%d",
				task.PeakSpeed, threadCount, threadSource, isExploration, task.Scope, task.PeakEnvKey, task.Domain, task.TTFBMs)
		}
	}
}

// writeTerminalHistoryLocked writes the durable history entry. For Surge
// copies accepted under the gate, callers re-validate via finishTerminalCopy;
// direct callers (tick, tests) run it in their current context.
func (m *Monitor) writeTerminalHistoryLocked(task *TrackedTask) {
	if task.FilePath == "" {
		log.Printf("[Monitor] Task %s completed but no file path available, skipping history", task.GID)
		return
	}

	log.Printf("[Monitor] Task completed: %s, peak speed: %d B/s", task.GID, task.PeakSpeed)

	// 2. 写入历史记录
	status := task.Status
	if status == "" {
		status = "complete"
	}
	entry := history.HistoryEntry{
		GID:             task.GID,
		Title:           filepath.Base(task.FilePath),
		Dir:             task.Dir,
		Path:            task.FilePath,
		TotalLength:     strconv.FormatInt(task.TotalLength, 10),
		CompletedLength: strconv.FormatInt(task.CompletedLength, 10),
		Source:          task.SourceURL,
		Status:          status,
		DownloadGroup:   copyDownloadGroup(task.DownloadGroup),
	}
	if status == "complete" {
		entry.DurationMs, entry.AvgSpeed = terminalTiming(task)
		if task.PeakSpeed > 0 && !task.peakFromAvgFallback {
			entry.PeakSpeed = task.PeakSpeed
		}
	}
	history.Add(entry)
	if task.DownloadGroup != nil {
		RemoveTaskGroup(task.GID)
		QueueDownloadGroupName(task.DownloadGroup.ID)
	}

	log.Printf("[Monitor] History recorded: %s", task.GID)
}

// minActiveSamplesPerGap bounds the unsampled last interval to <=10% of the
// observed active time.
const minActiveSamplesPerGap = 10

// terminalTiming returns the measured active duration (ms) and average speed
// (B/s) of a completed task, or zeros when no trustworthy source exists
// (restart replay, missing events, external adds).
func terminalTiming(task *TrackedTask) (durationMs, avgSpeed int64) {
	if task == nil {
		return 0, 0
	}
	var elapsed time.Duration
	switch {
	case task.terminalElapsed > 0:
		// Surge complete event: engine-accumulated active time.
		elapsed = task.terminalElapsed
	case !IsSgGid(task.GID) &&
		!task.AddedAt.IsZero() &&
		task.activeElapsed > 0 &&
		task.activeElapsed >= minActiveSamplesPerGap*task.maxActiveGap:
		elapsed = task.activeElapsed
	}
	if elapsed <= 0 {
		return 0, 0
	}
	durationMs = elapsed.Milliseconds()
	if durationMs <= 0 {
		return 0, 0
	}
	bytes := task.TotalLength
	if bytes <= 0 {
		bytes = task.CompletedLength
	}
	if secs := elapsed.Seconds(); bytes > 0 && secs > 0 {
		avgSpeed = int64(float64(bytes) / secs)
	}
	return durationMs, avgSpeed
}

// sequentialSpeedstatsSkip is true only for proven ignore-Range sequential
// downloads. PeakThreadCount==0 is not sequential (fast concurrent / Aria2).
// Cache miss or empty mode is not sequential; persistRangeUnsupported writes
// the gob list, so that is checked when the event cache is empty or stale.
func (m *Monitor) sequentialSpeedstatsSkip(task *TrackedTask) bool {
	if task == nil || m == nil {
		return false
	}
	return m.lookupRangeAcquisitionMode(task.GID) == types.RangeAcquireRangeUnsupported
}

func (m *Monitor) lookupRangeAcquisitionMode(gid string) types.RangeAcquisitionMode {
	if !strings.HasPrefix(gid, "sg_") {
		return ""
	}
	rawID := gid[3:]
	cacheMode := m.masterCacheRangeMode(rawID)
	if cacheMode == types.RangeAcquireRangeUnsupported {
		return cacheMode
	}
	rec, err := store.GetDownload(rawID)
	if err != nil || rec == nil {
		return cacheMode
	}
	if rec.RangeAcquisitionMode == types.RangeAcquireRangeUnsupported {
		return rec.RangeAcquisitionMode
	}
	if cacheMode != "" {
		return cacheMode
	}
	return rec.RangeAcquisitionMode
}

func (m *Monitor) masterCacheRangeMode(rawID string) types.RangeAcquisitionMode {
	se := m.surgeEng
	if se == nil {
		he, ok := m.engine.(*rpc.HybridEngine)
		if !ok {
			return ""
		}
		se, ok = he.SurgeEngineRef()
		if !ok || se == nil {
			return ""
		}
	}
	entry, ok := se.GetMasterCacheEntry(rawID)
	if !ok {
		return ""
	}
	return entry.RangeAcquisitionMode
}
