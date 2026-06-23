package concurrent

import (
	"time"

	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/utils"
)

// checkWorkerHealth detects slow workers and cancels them
func (d *ConcurrentDownloader) checkWorkerHealth() {
	d.activeMu.Lock()
	defer d.activeMu.Unlock()

	// FORK-PATCH: Publish per-worker telemetry snapshots
	// Build copy-on-read snapshots under activeMu for concurrency safety.
	if d.State != nil {
		if len(d.activeTasks) == 0 {
			d.State.SetWorkerStats(nil)
		} else {
			snapshots := make([]types.WorkerSnapshot, 0, len(d.activeTasks))
			for workerID, active := range d.activeTasks {
				snapshots = append(snapshots, types.WorkerSnapshot{
					WorkerID:         workerID,
					EMASpeed:         active.GetSpeed(),
					LastActivityUnix: active.LastActivity.Load(),
					RetryCount:       active.RetryCount.Load(),
					ChunkStart:       active.Task.Offset,
					ChunkOffset:      active.CurrentOffset.Load(),
					ChunkLength:      max(0, active.StopAt.Load()-active.Task.Offset),
					WaitingOnLimiter: active.WaitingOnLimiter.Load(),
					Hedged:           active.Hedged.Load() != 0,
				})
			}
			d.State.SetWorkerStats(snapshots)
		}
	}

	if len(d.activeTasks) == 0 {
		return
	}

	now := time.Now()

	// First pass: calculate mean speed
	var totalSpeed float64
	var speedCount int
	for _, active := range d.activeTasks {
		if speed := active.GetSpeed(); speed > 0 {
			totalSpeed += speed
			speedCount++
		}
	}

	var meanSpeed float64
	if speedCount > 0 {
		// If we have very few workers (e.g. 1), meanSpeed is just that worker's speed,
		// so "workerSpeed < mean * threshold" will never trigger.
		// Fallback to GLOBAL session speed in this case.
		if speedCount < 2 && d.State != nil {
			downloaded, _, _, sessionElapsed, _, sessionStartBytes := d.State.GetProgress()
			elapsedSeconds := sessionElapsed.Seconds()
			if elapsedSeconds > 5.0 { // Ensure we have some history
				globalSpeed := float64(downloaded-sessionStartBytes) / elapsedSeconds
				if globalSpeed > 0 {
					meanSpeed = globalSpeed
				} else {
					// Edge case: no global progress yet? use local
					meanSpeed = totalSpeed / float64(speedCount)
				}
			} else {
				meanSpeed = totalSpeed / float64(speedCount)
			}
		} else {
			meanSpeed = totalSpeed / float64(speedCount)
		}
	}

	// Second pass: check for slow and stalled workers
	stallTimeout := d.Runtime.GetStallTimeout()
	for workerID, active := range d.activeTasks {
		// Skip workers that are intentionally blocked by the rate limiter
		if active.WaitingOnLimiter.Load() {
			continue
		}

		// timeSinceActivity := now.Sub(lastTime)
		taskDuration := now.Sub(active.StartTime)

		// Skip workers that are still in their grace period
		gracePeriod := d.Runtime.GetSlowWorkerGracePeriod()
		if taskDuration < gracePeriod {
			continue
		}

		// Check for absolute stall: no data received for StallTimeout
		// This catches dead connections that the relative speed check misses.
		// FORK-PATCH: Stall detection is NOT protected by volume grace —
		// a worker that connected but never received data must still be
		// cancelled .
		lastActivity := active.LastActivity.Load()
		if stallTimeout > 0 && lastActivity > 0 {
			timeSinceData := now.Sub(time.Unix(0, lastActivity))
			if timeSinceData >= stallTimeout {
				utils.Debug("Health: Worker %d stalled (no data for %v), cancelling",
					workerID, timeSinceData.Truncate(time.Millisecond))
				if active.Cancel != nil {
					active.Cancel()
				}
				continue // Already cancelled, skip speed check
			}
		}

		// FORK-PATCH: Volume grace — skip slow speed check if downloaded
		// less than 1MB (TCP slow start may not have ramped up yet) .
		// Only protects the slow speed check below, NOT stall detection above.
		downloadedBytes := active.CurrentOffset.Load() - active.Task.Offset
		if downloadedBytes < 0 {
			downloadedBytes = 0
		}
		if downloadedBytes < int64(types.MB) {
			continue
		}

		// Check for slow worker (relative speed)
		// Only cancel if: below threshold
		if meanSpeed > 0 {
			workerSpeed := active.GetSpeed()
			threshold := d.Runtime.GetSlowWorkerThreshold()
			isBelowThreshold := threshold > 0 && workerSpeed > 0 && workerSpeed < threshold*meanSpeed

			if isBelowThreshold {
				utils.Debug("Health: Worker %d slow (%.2f KB/s vs mean %.2f KB/s), cancelling",
					workerID, workerSpeed/float64(types.KB), meanSpeed/float64(types.KB))
				if active.Cancel != nil {
					active.Cancel()
				}
			}
		}
	}
}
