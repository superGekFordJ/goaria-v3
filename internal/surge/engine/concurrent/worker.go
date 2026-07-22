package concurrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/engine"
	"goaria-v3/internal/surge/engine/events"
	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/utils"
)

// worker downloads tasks from the queue
func (d *ConcurrentDownloader) worker(ctx context.Context, id int, mirrors []string, file *os.File, queue *TaskQueue, totalSize int64, client *http.Client) error {
	// FORK-PATCH: Get pooled buffer from tiered pool
	initialTier := TierForBufferSize(d.Runtime.GetWorkerBufferSize())
	bufPtr := d.bufPool.Get(initialTier)
	defer func() {
		d.bufPool.Put(bufPtr)
	}()
	buf := *bufPtr

	utils.Debug("Worker %d started", id)
	defer utils.Debug("Worker %d finished", id)
	// FORK-PATCH: Clean up drain marker on exit .
	defer d.drainingWorkers.Delete(id)
	// FORK-PATCH: register per-worker (connection) session; cleaned up on exit.
	d.workerSessions.Store(id, &workerSession{startUnix: time.Now().UnixNano()})
	defer d.workerSessions.Delete(id)

	// Initial mirror assignment: Round Robin based on ID
	currentMirrorIdx := id % len(mirrors)
	mirrorHosts := make([]string, len(mirrors))
	for i, m := range mirrors {
		mirrorHosts[i] = engine.MirrorHost(m)
	}

	for {
		// FORK-PATCH: Check drain flag before picking up new work .
		// If draining, exit gracefully — the current chunk is already complete,
		// and the underlying TCP connection returns to the Transport idle pool.
		if _, draining := d.drainingWorkers.Load(id); draining {
			return nil
		}

		// Get next task
		task, ok := queue.Pop()

		if !ok {
			return nil // Queue closed, no more work
		}

		// FORK-PATCH: VP guard — if all bytes are already verified on disk,
		// skip this task without entering downloadTask(). This closes the
		// race window between queue.Pop() returning a task and activeTasks
		// registration: a worker that pops a task right as the completion
		// monitor hits 100% is invisible to KillWorker (not yet in
		// activeTasks) and unstoppable by queue.Close (already past Pop).
		// Without this guard the worker could hang on a tarpit server's
		// resp.Body.Read(). Returns before ActiveWorkers.Add(1), so no
		// Add(-1) pairing is needed.
		if d.State != nil && d.State.VerifiedProgress.Load() >= totalSize {
			return nil
		}

		// Update active workers
		if d.State != nil {
			d.State.ActiveWorkers.Add(1)
		}

		var lastErr error
		var lastSpeed float64 // FORK-PATCH: track speed for tier adjustment
		maxRetries := d.Runtime.GetMaxTaskRetries()
		genericAttempt := 0
		rlRetries := 0
		for {
			// #518: host-aware mirror selection
			idx, wait := d.hostLimiter.PickMirror(mirrorHosts, currentMirrorIdx, time.Now())
			currentMirrorIdx = idx
			if wait > 0 {
				if !interruptibleSleep(ctx, wait) {
					if d.State != nil {
						d.State.ActiveWorkers.Add(-1)
					}
					return ctx.Err()
				}
			}

			currentURL := mirrors[currentMirrorIdx]

			// Register active task with per-task cancellable context
			taskCtx, taskCancel := context.WithCancel(ctx)
			now := time.Now()
			activeTask := &ActiveTask{
				Task:        task,
				StartTime:   now,
				Cancel:      taskCancel,
				WindowStart: now, // Initialize sliding window
				workerID:    id,
			}
			// FORK-PATCH: Record retry attempt count for N_max fuse telemetry.
			// Uses genericAttempt (NOT rlRetries) — rate-limit retries are
			// intentionally excluded from the N_max fuse.
			activeTask.RetryCount.Store(int32(genericAttempt))
			// If the incoming Task carried a shared pointer, copy it into the active task
			if task.SharedMaxOffset != nil {
				activeTask.SharedMaxOffset = task.SharedMaxOffset
				activeTask.Hedged.Store(1)
			}
			activeTask.CurrentOffset.Store(task.Offset)
			activeTask.StopAt.Store(task.Offset + task.Length)
			activeTask.LastActivity.Store(now.UnixNano())

			d.activeMu.Lock()
			// FORK-PATCH: Final VP re-check under activeMu. Between the Pop-
			// time VP guard (above) and this registration point, another
			// worker may have pushed VerifiedProgress to 100%. The completion
			// monitor holds this same lock while cancelling active workers,
			// so this check is atomic with the kill sweep: either we register
			// and get cancelled, or we see VP>=totalSize and exit before
			// registering. Without this check the worker would enter
			// downloadTask() invisible to the kill sweep and hang on a tarpit
			// server's resp.Body.Read().
			if d.State != nil && d.State.VerifiedProgress.Load() >= totalSize {
				d.activeMu.Unlock()
				taskCancel()
				if d.State != nil {
					d.State.ActiveWorkers.Add(-1)
				}
				return nil
			}
			d.activeTasks[id] = activeTask
			d.activeMu.Unlock()

			// Update chunk status to Downloading
			if d.State != nil {
				utils.Debug("Worker %d: Setting range %d-%d to Downloading", id, task.Offset, task.Offset+task.Length)
				d.State.UpdateChunkStatus(task.Offset, task.Length, types.ChunkDownloading)
			} else {
				utils.Debug("Worker %d: d.State is nil, cannot update chunk status", id)
			}

			taskStart := time.Now()
			lastErr = d.downloadTask(taskCtx, currentURL, file, activeTask, buf, client, totalSize)
			// FORK-PATCH: Capture speed for dynamic tier adjustment
			lastSpeed = activeTask.GetSpeed()

			// CRITICAL: Capture external cancellation state BEFORE calling taskCancel()
			wasExternallyCancelled := taskCtx.Err() != nil

			taskCancel() // Clean up context resources
			utils.Debug("Worker %d: Task offset=%d length=%d took %v", id, task.Offset, task.Length, time.Since(taskStart))

			// Check for PARENT context cancellation (pause/shutdown)
			if ctx.Err() != nil {
				// DON'T delete from activeTasks - pause handler needs it
				if d.State != nil {
					d.State.ActiveWorkers.Add(-1)
				}
				return ctx.Err()
			}

			// FORK-PATCH: health-cancel path with 100% VP guard
			if wasExternallyCancelled && lastErr != nil {
				currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
				utils.Debug("Worker %d: Health check cancelled task, rotating from mirror %s to %s", id, mirrors[(currentMirrorIdx+len(mirrors)-1)%len(mirrors)], mirrors[currentMirrorIdx])

				if remaining := activeTask.RemainingTask(); remaining != nil {
					// FORK-PATCH: 100% requeue guard — skip requeue when all
					// bytes are verified on disk.
					if d.State != nil && d.State.VerifiedProgress.Load() >= totalSize {
						utils.Debug("Worker %d: skipping requeue — all bytes verified (100%% guard)", id)
					} else {
						originalEnd := task.Offset + task.Length
						if remaining.Offset+remaining.Length > originalEnd {
							remaining.Length = originalEnd - remaining.Offset
						}
						if remaining.Length > 0 {
							queue.Push(*remaining)
							utils.Debug("Worker %d: health-cancelled task requeued (remaining: %d bytes from offset %d)",
								id, remaining.Length, remaining.Offset)
						}
					}
				}
				d.activeMu.Lock()
				delete(d.activeTasks, id)
				d.activeMu.Unlock()
				lastErr = nil
				break
			}

			// Only delete from activeTasks on normal completion (not cancelled)
			d.activeMu.Lock()
			delete(d.activeTasks, id)
			d.activeMu.Unlock()

			if lastErr == nil {
				// #518: record success to host limiter
				d.hostLimiter.RecordSuccess(mirrorHosts[currentMirrorIdx])
				// Check if we stopped early due to stealing
				stopAt := activeTask.StopAt.Load()
				current := activeTask.CurrentOffset.Load()
				if current < task.Offset+task.Length && current >= stopAt {
					utils.Debug("Worker stopped early due to stealing")
				} else {
					// FORK-PATCH: Decrement conn error counter on successful chunk completion
					if d.State != nil {
						d.State.DecrConnErrors()
					}
				}
				break
			}

			// #518: error classification
			var rlErr *rateLimitError
			if errors.As(lastErr, &rlErr) {
				d.hostLimiter.Penalize(mirrorHosts[currentMirrorIdx], rlErr.retryAfter, rlErr.explicit, time.Now())
				d.ReportMirrorError(currentURL)
				rlRetries++
				if rlRetries > types.RateLimitMaxRetries {
					break
				}
				currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
				resumeOnRetryOffset(&task, activeTask)
				continue
			}

			genericAttempt++
			if genericAttempt >= maxRetries {
				break
			}
			d.ReportMirrorError(mirrors[currentMirrorIdx])
			currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
			if len(mirrors) == 1 {
				interruptibleSleep(ctx, time.Duration(1<<genericAttempt)*types.RetryBaseDelay)
			}
			resumeOnRetryOffset(&task, activeTask)
		}

		// FORK-PATCH: Dynamically adjust buffer tier based on observed speed.
		if lastSpeed > 0 {
			desiredTier := TierForSpeed(lastSpeed)
			currentTier, ok := TierForCap(cap(*bufPtr))
			if ok && desiredTier != currentTier {
				d.bufPool.Put(bufPtr)
				bufPtr = d.bufPool.Get(desiredTier)
				buf = *bufPtr
			}
		}

		// Update active workers
		if d.State != nil {
			d.State.ActiveWorkers.Add(-1)
		}

		if lastErr != nil {
			// FORK-PATCH: Increment conn error counter on server connection limit errors
			if d.State != nil && isConnLimitError(lastErr) {
				d.State.IncrConnErrors()
			}
			// Log failed task but continue with next task
			// If we modified StopAt we should probably reset it or push the remaining part?
			// TODO: Could optimize by pushing only remaining part if we track that.
			queue.Push(task)
			utils.Debug("task at offset %d failed after %d retries: %v", task.Offset, maxRetries, lastErr)
		}
	}
}

// FORK-PATCH: isConnLimitError checks for server connection hard limit errors
func isConnLimitError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "503") ||
		strings.Contains(s, "429") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection reset by peer")
}

// downloadTask downloads a single byte range and writes to file at offset
func (d *ConcurrentDownloader) downloadTask(ctx context.Context, rawurl string, file *os.File, activeTask *ActiveTask, buf []byte, client *http.Client, totalSize int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return err
	}

	task := activeTask.Task

	// Apply custom headers first (from browser extension: cookies, auth, referer, etc.)
	for key, val := range d.Headers {
		// Skip Range header - we set it ourselves for parallel downloads
		if key != "Range" {
			req.Header.Set(key, val)
		}
	}

	// Set User-Agent from config only if not provided in custom headers
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", d.Runtime.GetUserAgent())
	}
	// Range header is always set for partial downloads (overrides any browser Range header)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", task.Offset, task.Offset+task.Length-1))

	// FORK-PATCH: measure TTFB at first segment GET.
	ttfbStart := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	// FORK-PATCH: record response status for poison (4xx) detection downstream.
	activeTask.LastHTTPStatus.Store(int32(resp.StatusCode))
	defer func() {
		if err := resp.Body.Close(); err != nil {
			utils.Debug("Error closing response body: %v", err)
		}
	}()

	// Handle rate limiting explicitly (429 always, 503 only with Retry-After)
	if resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Retry-After") != "") {
		// FORK-PATCH: Poison defense — track 4xx/5xx for hedge disabling.
		d.recordHedgeError()
		ra, ok := engine.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return &rateLimitError{retryAfter: ra, explicit: ok}
	}

	// Validate status code
	if resp.StatusCode == http.StatusOK {
		// Valid only if we requested the full file
		// If we wanted a partial range but got the whole file (200), that's an error because we can't handle the full stream at a non-zero offset
		if task.Offset != 0 || task.Length != totalSize {
			return fmt.Errorf("server indicated success (200) but ignored range request (expected 206)")
		}
	} else if resp.StatusCode != http.StatusPartialContent {
		// FORK-PATCH: Poison defense — track 4xx/5xx for hedge disabling.
		if resp.StatusCode >= 400 {
			d.recordHedgeError()
		}
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// FORK-PATCH: Reset hedge poison counter on valid response.
	d.recordHedgeSuccess()

	// FORK-PATCH: send FirstByteMsg once — first non-hedged 206 only.
	if !d.isResume.Load() && activeTask.Hedged.Load() == 0 {
		if d.ttfbSent.CompareAndSwap(false, true) {
			ttfbMs := time.Since(ttfbStart).Milliseconds()
			if d.ProgressChan != nil {
				func() {
					defer func() { _ = recover() }()
					d.ProgressChan <- events.FirstByteMsg{
						DownloadID: d.ID,
						TTFBMs:     ttfbMs,
					}
				}()
			}
		}
	}

	// Batching State
	var pendingBytes int64
	var pendingStart int64 = -1
	lastUpdate := time.Now()
	batchSizeThreshold := int64(types.WorkerBatchSize)
	batchTimeThreshold := types.WorkerBatchInterval

	// Helper to flush pending updates to global state
	flushUpdates := func() {
		if pendingBytes > 0 && d.State != nil {
			// Update Chunk Map (Global Lock)
			d.State.UpdateChunkStatus(pendingStart, pendingBytes, types.ChunkCompleted)

			// Update Downloaded Counter (Atomic)
			d.State.Downloaded.Add(pendingBytes)

			pendingBytes = 0
			pendingStart = -1
			lastUpdate = time.Now()
		}
	}
	// Ensure we flush whatever we have on exit
	defer flushUpdates()

	// Read and write at offset
	offset := task.Offset
	for {
		// Check if we should stop
		stopAt := activeTask.StopAt.Load()
		if offset >= stopAt {
			// Stealing happened, stop here
			return nil
		}

		// Calculate how much to read to fill buffer or hit stopAt/EOF
		// We want to fill buf as much as possible to minimize WriteAt calls

		// Limit by remaining length to stopAt
		remaining := stopAt - offset
		if remaining <= 0 {
			return nil
		}

		readSize := int64(len(buf))
		if readSize > remaining {
			readSize = remaining
		}

		readSoFar := 0
		var readErr error

		for readSoFar < int(readSize) {
			n, err := resp.Body.Read(buf[readSoFar:readSize])
			if n > 0 {
				readSoFar += n
				// CONTINUOUS HEALTH KEEPALIVE:
				// Update LastActivity directly off the TCP socket instead of waiting for the buffer
				// to completely fill and hit disk. This prevents the Health Monitor from killing
				// workers on slightly slower networks during the 500KB buffer acquisition.
				activeTask.LastActivity.Store(time.Now().UnixNano())
			}
			if err != nil {
				readErr = err
				break
			}
			if n == 0 {
				readErr = io.ErrUnexpectedEOF
				break
			}
		}

		if readSoFar > 0 {
			// check stopAt again before writing
			// truncate readSoFar
			currentStopAt := activeTask.StopAt.Load()
			if offset+int64(readSoFar) > currentStopAt {
				readSoFar = int(currentStopAt - offset)
				if readSoFar <= 0 {
					return nil // stolen completely
				}
			}

			if d.Limiter != nil {
				// Reset stall clock before the wait so the health monitor measures
				// time from when throttling begins, not from the last network read.
				activeTask.LastActivity.Store(time.Now().UnixNano())
				activeTask.WaitingOnLimiter.Store(true)
				err := d.Limiter.WaitN(ctx, int64(readSoFar))
				activeTask.WaitingOnLimiter.Store(false)
				if err != nil {
					return err
				}

				// Refresh again after the wait to keep the stall clock current.
				activeTask.LastActivity.Store(time.Now().UnixNano())
			}

			_, writeErr := file.WriteAt(buf[:readSoFar], offset)
			if writeErr != nil {
				return fmt.Errorf("write error: %w", writeErr)
			}

			now := time.Now()
			rangeStart := offset // Start of this write
			offset += int64(readSoFar)

			// Compute newly written bytes deduplicated across racing workers
			var newlyWritten int64
			// Read pointer under RLock to avoid racing with hedger initialization
			activeTask.SharedMaxOffsetMu.RLock()
			ptr := activeTask.SharedMaxOffset
			activeTask.SharedMaxOffsetMu.RUnlock()
			if ptr != nil {
				for {
					maxOff := ptr.Load()
					if offset <= maxOff {
						// This exact byte range was already reported by the racing worker!
						newlyWritten = 0
						break
					}
					if rangeStart >= maxOff {
						// Entirely new progress
						if ptr.CompareAndSwap(maxOff, offset) {
							newlyWritten = int64(readSoFar)
							break
						}
					} else {
						// Partially new progress
						if ptr.CompareAndSwap(maxOff, offset) {
							newlyWritten = offset - maxOff
							break
						}
					}
				}
			} else {
				newlyWritten = int64(readSoFar)
			}

			activeTask.CurrentOffset.Store(offset)
			activeTask.WindowBytes.Add(newlyWritten)
			// FORK-PATCH: accumulate deduplicated write bytes into the per-worker
			// session. newlyWritten is CAS-deduplicated against racing workers and
			// counts only real new bytes, so retries re-entering a chunk don't
			// double-count and chunk switches don't jump.
			if newlyWritten > 0 {
				if sess, ok := d.workerSessions.Load(activeTask.workerID); ok {
					sess.(*workerSession).sessionBytes.Add(newlyWritten)
				}
			}
			activeTask.LastActivity.Store(now.UnixNano())

			// Calculate effective contribution
			if newlyWritten > 0 {
				if pendingStart == -1 {
					pendingStart = offset - newlyWritten
				}
				pendingBytes += newlyWritten
			}

			// Check thresholds
			if pendingBytes >= batchSizeThreshold || now.Sub(lastUpdate) >= batchTimeThreshold {
				flushUpdates()
			}

			// Update EMA speed using sliding window (2 second window)
			// This relies on WindowBytes which is updated atomically above, so independent of batching
			windowElapsed := now.Sub(activeTask.WindowStart).Seconds()
			if windowElapsed >= 2.0 {
				windowBytes := activeTask.WindowBytes.Swap(0)
				recentSpeed := float64(windowBytes) / windowElapsed

				activeTask.SpeedMu.Lock()
				alpha := d.Runtime.GetSpeedEmaAlpha()
				if alpha <= 0 || activeTask.Speed == 0 {
					// Alpha 0 disables smoothing and uses the latest measured speed directly.
					activeTask.Speed = recentSpeed
				} else {
					activeTask.Speed = (1-alpha)*activeTask.Speed + alpha*recentSpeed
				}
				activeTask.SpeedMu.Unlock()

				activeTask.WindowStart = now // Reset window
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read error: %w", readErr)
		}
	}

	// FORK-PATCH: early-EOF guard. A partial-data + io.EOF (n>0) path breaks
	// out of the read loop with offset < StopAt. Without this guard the task
	// silently returns nil, dropping undownloaded bytes from both activeTasks
	// and the queue. Route to the standard error path so the worker retries
	// and requeues the residual.
	if offset < activeTask.StopAt.Load() {
		return fmt.Errorf("early EOF: read up to %d, expected %d", offset, activeTask.StopAt.Load())
	}

	return nil
}

// StealWork tries to split an active task from a busy worker
// It greedily targets the worker with the MOST remaining work.
func (d *ConcurrentDownloader) StealWork(queue *TaskQueue) bool {
	d.activeMu.Lock()
	defer d.activeMu.Unlock()

	// FORK-PATCH: Tail-end adaptive chunk degradation.
	// When total remaining < activeWorkers × defaultMinChunk, dynamically lower
	// the minimum chunk threshold to allow work stealing in the tail phase.
	defaultMinChunk := d.Runtime.GetMinChunkSize()
	dynamicMinChunk := defaultMinChunk

	activeWorkers := len(d.activeTasks)
	var totalRemaining int64
	for _, active := range d.activeTasks {
		totalRemaining += active.RemainingBytes()
	}
	if activeWorkers > 0 && totalRemaining < int64(activeWorkers)*defaultMinChunk {
		totalWorkers := int64(activeWorkers) + queue.IdleWorkers()
		if totalWorkers > 0 {
			dynamicMinChunk = totalRemaining / totalWorkers
		}
		if dynamicMinChunk < types.MinChunkDynamicFloor {
			dynamicMinChunk = types.MinChunkDynamicFloor
		}
	}

	bestID := -1
	var maxRemaining int64 = 0
	var bestActive *ActiveTask

	// Find the worker with the MOST remaining work
	for id, active := range d.activeTasks {
		remaining := active.RemainingBytes()
		if remaining > dynamicMinChunk && remaining > maxRemaining {
			maxRemaining = remaining
			bestID = id
			bestActive = active
		}
	}

	if bestID == -1 {
		return false
	}

	// Found the best candidate, now try to steal
	remaining := maxRemaining
	active := bestActive

	// Split in half, aligned to AlignSize
	splitSize := alignedSplitSizeWithMin(remaining, dynamicMinChunk)
	if splitSize == 0 {
		return false
	}

	current := active.CurrentOffset.Load()
	newStopAt := current + splitSize

	// Update the active task stop point
	active.StopAt.Store(newStopAt)

	finalCurrent := active.CurrentOffset.Load()

	// The actual start of the stolen chunk must be after where the worker effectively stops.
	stolenStart := newStopAt
	if finalCurrent > newStopAt {
		stolenStart = finalCurrent
	}

	// Double check: ensure we didn't race and lose the chunk
	currentStopAt := active.StopAt.Load()
	if stolenStart >= currentStopAt && currentStopAt != newStopAt {
		utils.Debug("StealWork race detected: stolenStart >= currentStopAt")
	}

	originalEnd := current + remaining

	if stolenStart >= originalEnd {
		return false
	}

	// FORK-PATCH: SharedMaxOffset is for HEDGING (racing on the same byte
	// range). Stealing creates a strictly disjoint, adjacent byte range. If
	// they share the pointer, the stolen worker (at a higher offset) will
	// permanently mask the original worker's progress, causing
	// VerifiedProgress to stall and preventing download completion.
	newSharedPtr := &atomic.Int64{}
	newSharedPtr.Store(stolenStart)

	stolenTask := types.Task{
		Offset:          stolenStart,
		Length:          originalEnd - stolenStart,
		SharedMaxOffset: newSharedPtr,
	}

	queue.Push(stolenTask)
	utils.Debug("Balancer: stole %s from worker %d (new range: %d-%d)",
		utils.FormatBytes(stolenTask.Length), bestID, stolenTask.Offset, stolenTask.Offset+stolenTask.Length)

	return true
}

// HedgeWork creates a duplicate task when stealing isn't possible (chunks too small).
// An idle worker picks up the duplicate and races the original on a fresh HTTP connection.
// Both workers write identical data to the same file offsets (WriteAt is idempotent),
// so the file is always correct. Whichever finishes first wins; the other exits
// naturally when the queue closes or its next read returns data already counted.
func (d *ConcurrentDownloader) HedgeWork(queue *TaskQueue) bool {
	// FORK-PATCH: Check poison defense flag before hedging.
	if d.hedgeDisabled.Load() {
		return false
	}
	d.activeMu.Lock()
	defer d.activeMu.Unlock()

	if len(d.activeTasks) == 0 {
		return false
	}

	// Find the active task with the most remaining work that hasn't been hedged yet
	var bestActive *ActiveTask
	var maxRemaining int64

	for _, active := range d.activeTasks {
		// Skip tasks already being raced
		if active.Hedged.Load() != 0 {
			continue
		}
		remaining := active.RemainingBytes()
		if remaining > 0 && remaining > maxRemaining {
			maxRemaining = remaining
			bestActive = active
		}
	}

	if bestActive == nil || maxRemaining == 0 {
		return false
	}

	// Re-check remaining bytes before CAS to avoid setting Hedged without pushing a task
	current := bestActive.CurrentOffset.Load()
	stopAt := bestActive.StopAt.Load()
	if current >= stopAt {
		return false
	}

	// Mark as hedged so we don't create multiple duplicates
	if !bestActive.Hedged.CompareAndSwap(0, 1) {
		return false // Another goroutine hedged it first
	}

	// Initialize the shared deduplication state for both tasks
	bestActive.SharedMaxOffsetMu.Lock()
	if bestActive.SharedMaxOffset == nil {
		maxOff := &atomic.Int64{}
		maxOff.Store(current)
		bestActive.SharedMaxOffset = maxOff
	}
	// Create a duplicate task for the remaining byte range
	hedgedTask := types.Task{
		Offset:          current,
		Length:          stopAt - current,
		SharedMaxOffset: bestActive.SharedMaxOffset,
	}
	bestActive.SharedMaxOffsetMu.Unlock()

	queue.Push(hedgedTask)
	utils.Debug("Balancer: hedged %s (range: %d-%d) - idle worker will race on fresh connection",
		utils.FormatBytes(hedgedTask.Length), hedgedTask.Offset, hedgedTask.Offset+hedgedTask.Length)

	return true
}

// resumeOnRetryOffset updates task to reflect remaining work after a failed
// attempt, preventing double-counting bytes on retry.
// FORK-PATCH: carries SharedMaxOffset so retried tasks share write dedup
// with hedge partners on the same byte range.
func resumeOnRetryOffset(task *types.Task, activeTask *ActiveTask) {
	current := activeTask.CurrentOffset.Load()
	if current > task.Offset {
		oldStart := task.Offset
		task.Offset = current
		task.Length = oldStart + task.Length - current
		activeTask.SharedMaxOffsetMu.RLock()
		task.SharedMaxOffset = activeTask.SharedMaxOffset
		activeTask.SharedMaxOffsetMu.RUnlock()
	}
}
