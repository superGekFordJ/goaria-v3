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

	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// writeAtFn is the WriteAt seam used by downloadTask. Tests may swap it to
// inject disk-full failures without filling a real volume.
var writeAtFn = func(f *os.File, b []byte, off int64) (int, error) {
	return f.WriteAt(b, off)
}

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
	// FORK-PATCH: Clean up drain marker on exit.
	defer d.drainingWorkers.Delete(id)
	// FORK-PATCH: register per-worker (connection) session; cleaned up on exit.
	d.workerSessions.Store(id, &workerSession{startUnix: time.Now().UnixNano()})
	defer d.workerSessions.Delete(id)

	currentMirrorIdx := id % len(mirrors)

	mirrorHosts := make([]string, len(mirrors))
	for i, m := range mirrors {
		mirrorHosts[i] = transport.MirrorHost(m)
	}

	for {
		// FORK-PATCH: Check drain flag before picking up new work.
		if _, draining := d.drainingWorkers.Load(id); draining {
			return nil
		}

		task, ok := queue.Pop()
		if !ok {
			return nil
		}

		// FORK-PATCH: VP guard — if all bytes are already verified on disk,
		// skip this task without entering downloadTask(). Returns before
		// ActiveWorkers.Add(1), so no Add(-1) pairing is needed.
		if d.State != nil && d.State.Bytes.VerifiedProgress.Load() >= totalSize {
			return nil
		}

		if d.State != nil {
			d.State.ActiveWorkers.Add(1)
		}

		// FORK-PATCH: Register ActiveTask once outside the retry loop so the
		// task stays visible in activeTasks during PickMirror wait / single-
		// mirror backoff (upstream #566). Residual requeue still uses this
		// pointer after map delete — do NOT adopt upstream return lastErr.
		now := time.Now()
		activeTask := &ActiveTask{
			Task:        task,
			StartTime:   now,
			WindowStart: now,
			workerID:    id,
		}
		if task.SharedMaxOffset != nil {
			activeTask.SharedMaxOffset = task.SharedMaxOffset
			activeTask.Hedged.Store(1)
		}
		activeTask.CurrentOffset.Store(task.Offset)
		activeTask.StopAt.Store(task.Offset + task.Length)
		activeTask.LastActivity.Store(now.UnixNano())

		d.activeMu.Lock()
		// FORK-PATCH: Final VP re-check under activeMu before registration.
		// Fail path never inserts; Cancel is assigned per attempt inside the loop.
		if d.State != nil && d.State.Bytes.VerifiedProgress.Load() >= totalSize {
			d.activeMu.Unlock()
			if d.State != nil {
				d.State.ActiveWorkers.Add(-1)
			}
			return nil
		}
		d.activeTasks[id] = activeTask
		d.activeMu.Unlock()

		var lastErr error
		var lastSpeed float64 // FORK-PATCH: track speed for tier adjustment
		maxRetries := d.Runtime.GetMaxTaskRetries()
		genericAttempt := 0
		rlRetries := 0

		for {
			idx, wait := d.hostLimiter.PickMirror(mirrorHosts, currentMirrorIdx, time.Now())
			currentMirrorIdx = idx
			if wait > 0 {
				activeTask.WaitingOnLimiter.Store(true)
				if !interruptibleSleep(ctx, wait) {
					activeTask.WaitingOnLimiter.Store(false)
					// Requeue before map delete so handlePause (post-worker-exit)
					// still sees remaining work after Pause cancels ctx.
					d.releaseActiveOnCancel(id, activeTask, task, queue)
					return ctx.Err()
				}
				activeTask.WaitingOnLimiter.Store(false)
			}
			currentURL := mirrors[currentMirrorIdx]

			taskCtx, taskCancel := context.WithCancel(ctx)
			now := time.Now()
			// Publish Cancel + window fields under activeMu so health (which
			// reads them under the same lock) cannot observe a torn refresh
			// on a live activeTasks entry across retries.
			d.activeMu.Lock()
			activeTask.Cancel = taskCancel
			activeTask.StartTime = now
			activeTask.WindowStart = now
			activeTask.WindowBytes.Store(0)
			activeTask.LastActivity.Store(now.UnixNano())
			d.activeMu.Unlock()
			// FORK-PATCH: Record retry attempt count for N_max fuse telemetry.
			// Uses genericAttempt (NOT rlRetries) — rate-limit retries are
			// intentionally excluded from the N_max fuse.
			activeTask.RetryCount.Store(int32(genericAttempt))

			if d.State != nil {
				utils.Debug("Worker %d: Setting range %d-%d to Downloading", id, task.Offset, task.Offset+task.Length)
				d.State.UpdateChunkStatus(task.Offset, task.Length, types.ChunkDownloading)
			} else {
				utils.Debug("Worker %d: d.State is nil, cannot update chunk status", id)
			}

			taskStart := time.Now()
			activeTask.LastHTTPStatus.Store(0)
			lastErr = d.downloadTask(taskCtx, currentURL, file, activeTask, buf, client, totalSize)
			// FORK-PATCH: Capture speed for dynamic tier adjustment
			lastSpeed = activeTask.GetSpeed()

			wasExternallyCancelled := taskCtx.Err() != nil

			taskCancel()
			// Drop Cancel while still mapped so health cannot invoke a spent
			// cancel across the inter-attempt / backoff gap.
			d.activeMu.Lock()
			activeTask.Cancel = nil
			d.activeMu.Unlock()
			utils.Debug("Worker %d: Task offset=%d length=%d took %v", id, task.Offset, task.Length, time.Since(taskStart))

			if ctx.Err() != nil {
				d.releaseActiveOnCancel(id, activeTask, task, queue)
				return ctx.Err()
			}

			// Disk-full / quota: fail immediately — no in-place retry, no mirror
			// rotate, no residual Push. Must run before health-cancel swallow so
			// a cancel race cannot clear and requeue a disk-space error.
			// Drop activeTasks + ActiveWorkers here (like the post-loop path)
			// without releaseActiveOnCancel — that helper would Push remaining.
			// Stash RemainingTask off-queue so error-path saveStateSnapshot can
			// still persist the unfinished range after peers are cancelled.
			if types.IsInsufficientDiskSpace(lastErr) {
				var stash *types.Task
				if remaining := activeTask.RemainingTask(); remaining != nil {
					originalEnd := task.Offset + task.Length
					if remaining.Offset+remaining.Length > originalEnd {
						remaining.Length = originalEnd - remaining.Offset
					}
					if remaining.Length > 0 {
						stash = remaining
					}
				}
				d.activeMu.Lock()
				if stash != nil {
					d.abandonedRemaining = append(d.abandonedRemaining, *stash)
				}
				delete(d.activeTasks, id)
				d.activeMu.Unlock()
				if d.State != nil {
					d.State.ActiveWorkers.Add(-1)
				}
				return lastErr
			}

			// FORK-PATCH: health-cancel path with 100% VP guard
			if wasExternallyCancelled && lastErr != nil {
				currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
				utils.Debug("Worker %d: Health check cancelled task, rotating from mirror %s to %s", id, mirrors[(currentMirrorIdx+len(mirrors)-1)%len(mirrors)], mirrors[currentMirrorIdx])

				if remaining := activeTask.RemainingTask(); remaining != nil {
					if d.State != nil && d.State.Bytes.VerifiedProgress.Load() >= totalSize {
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
				lastErr = nil
				break
			}

			if lastErr == nil {
				d.hostLimiter.RecordSuccess(mirrorHosts[currentMirrorIdx])
				stopAt := activeTask.StopAt.Load()
				current := activeTask.CurrentOffset.Load()
				if current < task.Offset+task.Length && current >= stopAt {
					utils.Debug("Worker stopped early due to stealing")
				} else if d.State != nil {
					// FORK-PATCH: Decrement conn error counter on successful chunk completion
					d.State.DecrConnErrors()
				}
				break
			}

			var rlErr *rateLimitError
			if errors.As(lastErr, &rlErr) {
				d.hostLimiter.Penalize(mirrorHosts[currentMirrorIdx], rlErr.retryAfter, rlErr.explicit, time.Now())
				d.ReportMirrorError(currentURL)
				rlRetries++
				if rlRetries > types.RateLimitMaxRetries {
					break
				}
				currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
				d.resumeOnRetryOffset(&task, activeTask)
				continue
			}

			genericAttempt++
			if genericAttempt >= maxRetries {
				break
			}
			d.ReportMirrorError(mirrors[currentMirrorIdx])
			currentMirrorIdx = (currentMirrorIdx + 1) % len(mirrors)
			if len(mirrors) == 1 {
				activeTask.WaitingOnLimiter.Store(true)
				if !interruptibleSleep(ctx, time.Duration(1<<genericAttempt)*types.RetryBaseDelay) {
					activeTask.WaitingOnLimiter.Store(false)
					d.releaseActiveOnCancel(id, activeTask, task, queue)
					return ctx.Err()
				}
				activeTask.WaitingOnLimiter.Store(false)
			}
			d.resumeOnRetryOffset(&task, activeTask)
		}

		d.activeMu.Lock()
		delete(d.activeTasks, id)
		d.activeMu.Unlock()

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

		if d.State != nil {
			d.State.ActiveWorkers.Add(-1)
		}

		if lastErr != nil {
			// FORK-PATCH: Increment conn error counter on server connection limit errors.
			// DEPRECATED for N_max fuse (uses genericAttempt/RetryCount); kept for
			// #22 complementary HostRateLimiter path.
			if d.State != nil && isConnLimitError(lastErr) {
				d.State.IncrConnErrors()
			}
			// FORK-PATCH: requeue residual from activeTask StopAt/CurrentOffset.
			// Transient/unknown: continue outer loop (do not escalate). Hard
			// permanent HTTP: residual requeue (originalEnd clamp) THEN return
			// lastErr. Soft-403 is decided after the existing residual path.
			// Disk-space never reaches here — early return above skips residual Push.
			if remaining := activeTask.RemainingTask(); remaining != nil {
				originalEnd := task.Offset + task.Length
				if remaining.Offset+remaining.Length > originalEnd {
					remaining.Length = originalEnd - remaining.Offset
				}
				if remaining.Length > 0 {
					queue.Push(*remaining)
				}
			}
			utils.Debug("Worker %d: task at offset %d failed after %d retries: %v", id, task.Offset, maxRetries, lastErr)
			if errors.Is(lastErr, types.ErrPermanentHTTP) {
				return lastErr
			}
			if activeTask.LastHTTPStatus.Load() == http.StatusForbidden && d.recordSoft403Exhaustion(time.Now()) {
				return fmt.Errorf("unexpected status: 403: %w", types.ErrPermanentHTTP)
			}
		}
	}
}

// releaseActiveOnCancel requeues residual work then removes the worker from
// activeTasks. Pause cancels downloadCtx and handlePause runs only after
// workers exit — without this push, cancel-path map deletes lose remaining.
func (d *ConcurrentDownloader) releaseActiveOnCancel(id int, activeTask *ActiveTask, task types.Task, queue *TaskQueue) {
	if remaining := activeTask.RemainingTask(); remaining != nil {
		originalEnd := task.Offset + task.Length
		if remaining.Offset+remaining.Length > originalEnd {
			remaining.Length = originalEnd - remaining.Offset
		}
		if remaining.Length > 0 {
			queue.Push(*remaining)
		}
	}
	d.activeMu.Lock()
	delete(d.activeTasks, id)
	d.activeMu.Unlock()
	if d.State != nil {
		d.State.ActiveWorkers.Add(-1)
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

	// Handle rate limiting explicitly: only 429/503 with Retry-After trigger
	// Penalize. Bare 429 (no Retry-After) falls through to the generic path so
	// genericAttempt feeds RetryCount and the N_max fuse can see it.
	if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) &&
		resp.Header.Get("Retry-After") != "" {
		// FORK-PATCH: Poison defense — track 4xx/5xx for hedge disabling.
		d.recordHedgeError()
		ra, ok := transport.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
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
		// Permanent 4xx (≠429) wrap after poison record so scheduler #541 can
		// skip whole-download retries once the worker escalates. Mid-chunk 403
		// is concurrent-local transient (CDN soft throttle); sticky budget
		// escalates after Soft403StickyExhaustions residual burns.
		if types.IsPermanentHTTPStatus(resp.StatusCode) && resp.StatusCode != http.StatusForbidden {
			return fmt.Errorf("unexpected status: %d: %w", resp.StatusCode, types.ErrPermanentHTTP)
		}
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// FORK-PATCH: Reset hedge poison counter on valid response.
	d.recordHedgeSuccess()

	// FORK-PATCH: send FirstByte once — first non-hedged 206 only.
	if !d.isResume.Load() && activeTask.Hedged.Load() == 0 {
		if d.ttfbSent.CompareAndSwap(false, true) {
			ttfbMs := time.Since(ttfbStart).Milliseconds()
			if d.ProgressChan != nil {
				func() {
					defer func() { _ = recover() }()
					d.ProgressChan <- types.DownloadEvent{
						Type:       types.EventFirstByte,
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
			d.State.Bytes.Downloaded.Add(pendingBytes)

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

		// FORK-PATCH: Flush pending progress before entering a potentially unbounded network block.
		// Since the batch time interval is checked synchronously during the active read/write loop,
		// already-written bytes would remain unpublished if the subsequent read blocks indefinitely.
		// Flushing here ensures that global progress observers can observe all committed bytes
		// even when the worker is stalled waiting for the next network chunk.
		flushUpdates()
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

			_, writeErr := writeAtFn(file, buf[:readSoFar], offset)
			if writeErr != nil {
				return fmt.Errorf("write error: %w", types.AnnotateInsufficientDiskSpace(writeErr))
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

			// FORK-PATCH: clamp offset and newlyWritten to the current StopAt together.
			// StealWork may reduce StopAt between the read-loop's stopAt check and this
			// count point. Clamping offset (not just newlyWritten) keeps CurrentOffset at
			// the effective completion boundary.
			clampStopAt := activeTask.StopAt.Load()
			if offset > clampStopAt {
				excess := offset - clampStopAt
				offset = clampStopAt
				if newlyWritten > excess {
					newlyWritten -= excess
				} else {
					newlyWritten = 0
				}
			}

			activeTask.CurrentOffset.Store(offset)
			activeTask.WindowBytes.Add(newlyWritten)
			// FORK-PATCH: accumulate deduplicated write bytes into the per-worker session.
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
	// silently returns nil, dropping undownloaded bytes.
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
		// FORK-PATCH: skip hedged workers — stealing one side of a hedged pair
		// with an independent SharedMaxOffset would double-count VP.
		if active.Hedged.Load() != 0 {
			continue
		}
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
	// range). Stealing creates a strictly disjoint, adjacent byte range —
	// use an independent pointer initialized to stolenStart.
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
func (d *ConcurrentDownloader) resumeOnRetryOffset(task *types.Task, activeTask *ActiveTask) {
	current := activeTask.CurrentOffset.Load()
	stopAt := activeTask.StopAt.Load()
	// FORK-PATCH: unconditionally clamp to StopAt — even when current == task.Offset
	// (retry with no progress), task.Length must shrink; otherwise the next retry
	// resets StopAt back to the original end, resurrecting the stolen range and
	// causing double-counting.
	effectiveEnd := task.Offset + task.Length
	if stopAt < effectiveEnd {
		effectiveEnd = stopAt
	}
	task.Offset = current
	task.Length = effectiveEnd - current
	if task.Length < 0 {
		task.Length = 0
	}
	activeTask.SharedMaxOffsetMu.RLock()
	task.SharedMaxOffset = activeTask.SharedMaxOffset
	activeTask.SharedMaxOffsetMu.RUnlock()
	// Publish Task under activeMu so health (which reads Task.Offset under the
	// same lock) cannot race the attempt-start snapshot used for Range/grace.
	d.activeMu.Lock()
	activeTask.Task = *task
	d.activeMu.Unlock()
}
