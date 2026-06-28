package concurrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/engine"
	"goaria-v3/internal/surge/engine/events"
	"goaria-v3/internal/surge/engine/state"
	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/utils"
)

// ConcurrentDownloader handles multi-connection downloads
type ConcurrentDownloader struct {
	ProgressChan chan<- any           // Channel for events (start/complete/error)
	ID           string               // Download ID
	State        *types.ProgressState // Shared state for TUI polling
	activeTasks  map[int]*ActiveTask
	activeMu     sync.Mutex
	URL          string // For pause/resume
	DestPath     string // For pause/resume
	Runtime      *types.RuntimeConfig
	Limiter      types.ByteLimiter
	RateLimitBps int64
	RateLimitSet bool
	TotalSize    int64
	bufPool      *TieredBufferPool // FORK-PATCH: tiered buffer pool with cap filter
	Headers      map[string]string // Custom HTTP headers from browser (cookies, auth, etc.)

	// FORK-PATCH: Drain/scale infrastructure
	nextWorkerID    atomic.Int64               // dynamic worker ID allocation
	drainingWorkers sync.Map                   // workerID -> struct{}{}; checked before queue.Pop()
	workerDepsPtr   atomic.Pointer[workerDeps] // FORK-PATCH: atomized worker deps
	workerWg        sync.WaitGroup             // tracks all spawned workers (initial + scaled)
	workersActive   atomic.Bool                // FORK-PATCH: guards WaitGroup reuse
	workersMu       sync.Mutex                 // FORK-PATCH: guards Add/Done around Wait

	// FORK-PATCH: End-game hedge and poison defense.
	consecutiveHedgeErrors atomic.Int32 // consecutive 4xx/5xx counter for poison defense
	hedgeDisabled          atomic.Bool  // true when hedge is disabled due to poison detection

	// FORK-PATCH: per-worker (connection) session tracking. Survives across
	// chunks/retries, unlike ActiveTask which is rebuilt every chunk.
	workerSessions sync.Map // workerID -> *workerSession

	// FORK-PATCH: runtime override for the relative slow-worker threshold.
	// When set (slowThresholdSet=true), checkWorkerHealth uses this instead of
	// RuntimeConfig. A value of 0 disables the relative slow-speed cancel while
	// leaving stall detection armed.
	slowThresholdSet      atomic.Bool
	slowThresholdOverride atomic.Uint64 // math.Float64bits(v)
}

// FORK-PATCH: per-worker (connection) session, survives across chunks.
type workerSession struct {
	startUnix    int64
	sessionBytes atomic.Int64
}

// FORK-PATCH: workerDeps bundles worker dependencies for atomic access via ScaleWorkers
type workerDeps struct {
	ctx       context.Context
	mirrors   []string
	file      *os.File
	queue     *TaskQueue
	totalSize int64
	client    *http.Client
}

// NewConcurrentDownloader creates a new concurrent downloader with all required parameters
func NewConcurrentDownloader(id string, progressCh chan<- any, progState *types.ProgressState, runtime *types.RuntimeConfig) *ConcurrentDownloader {
	if runtime == nil {
		runtime = types.DefaultRuntimeConfig()
	}

	return &ConcurrentDownloader{
		ID:           id,
		ProgressChan: progressCh,
		State:        progState,
		activeTasks:  make(map[int]*ActiveTask),
		Runtime:      runtime,
		bufPool:      NewTieredBufferPool(), // FORK-PATCH: tiered buffer pool replaces fixed sync.Pool
	}
}

// getInitialConnections returns the starting number of connections based on file size
func (d *ConcurrentDownloader) getInitialConnections(fileSize int64) int {
	maxConns := d.Runtime.GetMaxConnectionsPerDownload()
	minChunkSize := d.Runtime.GetMinChunkSize() // e.g., 1MB or 5MB

	if fileSize <= 0 {
		return 1
	}

	// If caller specified exact worker count, bypass √size heuristic.
	if workers := d.Runtime.GetWorkers(); workers > 0 {
		if workers > maxConns {
			workers = maxConns
		}
		if minChunkSize > 0 {
			maxPossibleChunks := fileSize / minChunkSize
			if maxPossibleChunks < 1 {
				maxPossibleChunks = 1
			}
			if int64(workers) > maxPossibleChunks {
				workers = int(maxPossibleChunks)
			}
		}
		return workers
	}

	// 1. Calculate ideal workers using the Square Root heuristic
	// Convert to float first to avoid integer truncation on small files
	sizeMB := float64(fileSize) / float64(types.MB)
	calculatedWorkers := int(math.Round(math.Sqrt(sizeMB)))

	// 2. Hard constraint: Don't create chunks smaller than MinChunkSize
	// If file is 20MB and MinChunk is 10MB, we strictly can't have more than 2 workers
	if minChunkSize > 0 {
		maxPossibleChunks := fileSize / minChunkSize
		if maxPossibleChunks < 1 {
			maxPossibleChunks = 1
		}
		if int64(calculatedWorkers) > maxPossibleChunks {
			calculatedWorkers = int(maxPossibleChunks)
		}
	}

	// 3. Safety Floors and Ceilings
	if calculatedWorkers < 1 {
		return 1
	}
	if calculatedWorkers > maxConns {
		return maxConns
	}

	return calculatedWorkers
}

// ReportMirrorError marks a mirror as having an error in the state
func (d *ConcurrentDownloader) ReportMirrorError(url string) {
	if d.State == nil {
		return
	}

	mirrors := d.State.GetMirrors()
	changed := false
	for i, m := range mirrors {
		if m.URL == url && !m.Error {
			mirrors[i].Error = true
			changed = true
			break
		}
	}

	if changed {
		d.State.SetMirrors(mirrors)
	}
}

// calculateChunkSize determines optimal chunk size
func (d *ConcurrentDownloader) calculateChunkSize(fileSize int64, numConns int) int64 {
	// Safety check
	if numConns <= 0 {
		return d.Runtime.GetMinChunkSize() // Fallback
	}

	chunkSize := fileSize / int64(numConns)

	// Clamp to min from config (but not max - we want large chunks)
	minChunk := d.Runtime.GetMinChunkSize()

	if chunkSize < minChunk {
		chunkSize = minChunk
	}

	// Align to 4KB
	chunkSize = (chunkSize / types.AlignSize) * types.AlignSize
	if chunkSize == 0 {
		chunkSize = types.AlignSize
	}

	return chunkSize
}

// determineChunkSize decides the strategy (Sequential vs Parallel)
func (d *ConcurrentDownloader) determineChunkSize(fileSize int64, numConns int) int64 {
	if d.Runtime.SequentialDownload {
		// Sequential mode: Use small fixed chunks (MinChunkSize) to ensure strict ordering
		chunkSize := d.Runtime.GetMinChunkSize()
		if chunkSize <= 0 {
			chunkSize = 2 * types.MB // Default 2MB if not configured
		}
		// Align to 4KB
		chunkSize = (chunkSize / types.AlignSize) * types.AlignSize
		if chunkSize == 0 {
			chunkSize = types.AlignSize
		}
		return chunkSize
	}

	// Parallel mode: Use large shards
	return d.calculateChunkSize(fileSize, numConns)
}

// createTasks generates initial task queue from file size and chunk size
func createTasks(fileSize, chunkSize int64) []types.Task {
	if chunkSize <= 0 {
		return nil
	}

	// preallocate slice capacity
	count := (fileSize + chunkSize - 1) / chunkSize
	tasks := make([]types.Task, 0, int(count))

	for offset := int64(0); offset < fileSize; offset += chunkSize {
		length := chunkSize
		if offset+length > fileSize {
			length = fileSize - offset
		}
		tasks = append(tasks, types.Task{Offset: offset, Length: length})
	}
	return tasks
}

func (d *ConcurrentDownloader) applyClientSettings(client *http.Client) {
	// Preserve headers on redirects for authenticated downloads
	// By default, Go strips sensitive headers (Cookie, Authorization) on cross-domain redirects.
	// Since these headers were explicitly provided by the browser for this download, we forward them.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return types.ErrMaxRedirects
		}
		// Copy headers from original request to redirect request
		if len(via) > 0 {
			utils.CopyRedirectHeaders(req, via[0])
		}
		// Re-apply explicit custom headers down the redirect chain
		for key, val := range d.Headers {
			if key != "Range" {
				req.Header.Set(key, val)
			}
		}
		return nil
	}
}

// Download downloads a file using multiple concurrent connections
// Uses pre-probed metadata (file size already known)
func (d *ConcurrentDownloader) Download(ctx context.Context, rawurl string, candidateMirrors []string, activeMirrors []string, destPath string, fileSize int64) error {
	utils.Debug("ConcurrentDownloader.Download: %s -> %s (size: %d, mirrors: %d)", rawurl, destPath, fileSize, len(activeMirrors))

	d.initMirrorStatus(rawurl, candidateMirrors, activeMirrors, destPath)

	workingPath := destPath + types.IncompleteSuffix
	downloadCtx, cancel := context.WithCancel(ctx)

	if d.State != nil {
		d.State.SetCancelFunc(cancel)
	}

	client, transport := d.setupNetwork()
	// Release transport back to the pool ONLY after all helpers and workers are joined (LIFO: runs last)
	defer engine.DefaultNetworkPool.ReleaseTransport(transport)

	// Helper synchronization for monitors and balancer
	var wgHelpers sync.WaitGroup
	// Ensure we wait for helpers to finish; run wait AFTER cancel (LIFO: Wait runs second, cancel runs first)
	defer wgHelpers.Wait()
	defer cancel()

	// Ensure we have the total file size
	if fileSize <= 0 {
		var err error
		fileSize, err = d.bootstrapMetadata(downloadCtx, client, rawurl)
		if err != nil {
			return err
		}
	}
	d.TotalSize = fileSize

	numConns := d.getInitialConnections(fileSize)
	chunkSize := d.determineChunkSize(fileSize, numConns)

	workerMirrors := d.getWorkerMirrors(activeMirrors)

	// Pre-warm connections if configured
	hedgeCount := d.Runtime.GetDialHedgeCount()
	if hedgeCount > 0 {
		d.prewarmConnections(downloadCtx, client, numConns, hedgeCount, workerMirrors)
	}

	// Open existing output file with .surge suffix (must be created by processing layer)
	outFile, err := os.OpenFile(workingPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open working file: %w", err)
	}
	defer func() {
		if outFile != nil {
			_ = outFile.Close()
		}
	}()

	// Initialize chunk visualization (must happen BEFORE setupTasks so RestoreBitmap can overwrite it)
	if d.State != nil {
		d.State.InitBitmap(fileSize, chunkSize)
	}

	tasks, err := d.setupTasks(destPath, fileSize, chunkSize, outFile)
	if err != nil {
		return err
	}

	queue := NewTaskQueue()
	queue.PushMultiple(tasks)

	// Start monitoring and balancing helpers
	d.startHelpers(downloadCtx, &wgHelpers, queue, fileSize, numConns)

	// Execute download workers
	downloadErr := d.executeWorkers(downloadCtx, client, outFile, queue, fileSize, workerMirrors, numConns)

	// Handle pause request: must return types.ErrPaused to prevent finalization
	if d.State != nil && d.State.IsPaused() {
		pauseErr := d.handlePause(destPath, fileSize, queue, candidateMirrors)
		if pauseErr == nil {
			// Pause was requested at completion boundary, so handlePause finalized it.
			return d.syncFile(outFile)
		}
		return pauseErr
	}

	// Handle cancel: context was cancelled but not via Pause()
	// Propagate cancellation so callers don't treat this as a successful completion.
	if downloadCtx.Err() == context.Canceled {
		return context.Canceled
	}
	if downloadErr != nil {
		return downloadErr
	}

	// Note: Download completion notifications are handled by the TUI via DownloadCompleteMsg
	return d.syncFile(outFile)
}

func (d *ConcurrentDownloader) initMirrorStatus(rawurl string, candidateMirrors []string, activeMirrors []string, destPath string) {
	d.URL = rawurl
	d.DestPath = destPath

	if d.State == nil {
		return
	}

	d.State.SetURL(rawurl)
	d.State.SetDestPath(destPath)

	var statuses []types.MirrorStatus
	statuses = append(statuses, types.MirrorStatus{URL: rawurl, Active: true})

	activeMap := make(map[string]bool)
	for _, m := range activeMirrors {
		activeMap[m] = true
		if m != rawurl {
			statuses = append(statuses, types.MirrorStatus{URL: m, Active: true})
		}
	}

	for _, m := range candidateMirrors {
		if !activeMap[m] && m != rawurl {
			statuses = append(statuses, types.MirrorStatus{URL: m, Active: false, Error: true})
		}
	}

	d.State.SetMirrors(statuses)
}

func (d *ConcurrentDownloader) setupNetwork() (*http.Client, *http.Transport) {
	var proxyURL, customDNS string
	if d.Runtime != nil {
		proxyURL = d.Runtime.ProxyURL
		customDNS = d.Runtime.CustomDNS
	}

	transport := engine.DefaultNetworkPool.AcquireTransport(proxyURL, customDNS, types.PoolMaxConnsPerHost)
	client := &http.Client{Transport: transport}
	d.applyClientSettings(client)
	return client, transport
}

func (d *ConcurrentDownloader) getWorkerMirrors(activeMirrors []string) []string {
	mirrors := make([]string, 0, len(activeMirrors)+1)
	mirrors = append(mirrors, d.URL)
	for _, v := range activeMirrors {
		if v != d.URL {
			mirrors = append(mirrors, v)
		}
	}
	return mirrors
}

func (d *ConcurrentDownloader) setupTasks(destPath string, fileSize, chunkSize int64, outFile *os.File) ([]types.Task, error) {
	savedState, err := state.LoadState(d.URL, destPath)
	isResume := err == nil && savedState != nil && len(savedState.Tasks) > 0

	if isResume {
		if d.State != nil {
			d.State.Downloaded.Store(savedState.Downloaded)
			d.State.VerifiedProgress.Store(savedState.Downloaded)
			d.State.SetSavedElapsed(time.Duration(savedState.Elapsed))
			d.State.SyncSessionStart()

			if len(savedState.ChunkBitmap) > 0 && savedState.ActualChunkSize > 0 {
				d.State.RestoreBitmap(savedState.ChunkBitmap, savedState.ActualChunkSize)
				d.State.RecalculateProgress(savedState.Tasks)
				d.State.Downloaded.Store(d.State.VerifiedProgress.Load())
				d.State.SyncSessionStart()
				utils.Debug("Restored chunk map: size %d", savedState.ActualChunkSize)
			}
		}
		utils.Debug("Resuming from saved state: %d tasks, %d bytes downloaded", len(savedState.Tasks), savedState.Downloaded)
		return savedState.Tasks, nil
	}

	if err := outFile.Truncate(fileSize); err != nil {
		return nil, fmt.Errorf("failed to preallocate file: %w", err)
	}
	if d.State != nil {
		d.State.Downloaded.Store(0)
		d.State.SyncSessionStart()
	}
	return createTasks(fileSize, chunkSize), nil
}

func (d *ConcurrentDownloader) startHelpers(ctx context.Context, wg *sync.WaitGroup, queue *TaskQueue, fileSize int64, numConns int) {
	// Balancer for dynamic chunk splitting and work stealing
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runBalancer(ctx, queue)
	}()

	// Monitor for download completion
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runCompletionMonitor(ctx, queue, fileSize, numConns)
	}()

	// Health monitor for detecting slow workers
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runHealthMonitor(ctx)
	}()
}

// recordHedgeError increments the consecutive 4xx/5xx counter and disables
// hedging when the threshold is reached.
// FORK-PATCH: Poison defense for end-game hedge .
func (d *ConcurrentDownloader) recordHedgeError() {
	if d.hedgeDisabled.Load() {
		return
	}
	count := d.consecutiveHedgeErrors.Add(1)
	if count >= int32(types.HedgeErrorThreshold) {
		d.hedgeDisabled.Store(true)
		utils.Debug("Hedge: disabled due to %d consecutive 4xx/5xx errors", count)
	}
}

// recordHedgeSuccess decrements the consecutive error counter (decay reset)
// rather than zeroing it entirely. This requires sustained successes to clear
// the poison state, preventing a single success from masking ongoing errors
// in mixed success/failure scenarios.
// FORK-PATCH: Poison defense recovery — decay reset.
func (d *ConcurrentDownloader) recordHedgeSuccess() {
	for {
		val := d.consecutiveHedgeErrors.Load()
		if val <= 0 {
			break
		}
		if d.consecutiveHedgeErrors.CompareAndSwap(val, val-1) {
			break
		}
	}
	if d.hedgeDisabled.Load() && d.consecutiveHedgeErrors.Load() == 0 {
		d.hedgeDisabled.Store(false)
		utils.Debug("Hedge: re-enabled after sustained successful responses")
	}
}

// isEndGame returns true when all tasks have been dispatched (queue is empty)
// but some workers are still active and idle workers are available for hedging.
// FORK-PATCH: End-game detection for proactive hedge .
func (d *ConcurrentDownloader) isEndGame(queue *TaskQueue) bool {
	if queue.Len() > 0 {
		return false
	}
	if queue.IdleWorkers() == 0 {
		return false
	}
	d.activeMu.Lock()
	count := len(d.activeTasks)
	d.activeMu.Unlock()
	return count > 0
}

// HedgeAll creates duplicate tasks for ALL un-hedged active tasks that still
// have remaining work. Intended for the end-game phase where all original tasks
// have been dispatched and idle workers should race every remaining chunk.
// Returns the number of tasks hedged.
// FORK-PATCH: End-game proactive hedge .
func (d *ConcurrentDownloader) HedgeAll(queue *TaskQueue) int {
	if d.hedgeDisabled.Load() {
		return 0
	}
	d.activeMu.Lock()
	defer d.activeMu.Unlock()

	if len(d.activeTasks) == 0 {
		return 0
	}

	hedged := 0
	for _, active := range d.activeTasks {
		if active.Hedged.Load() != 0 {
			continue
		}
		current := active.CurrentOffset.Load()
		stopAt := active.StopAt.Load()
		if current >= stopAt {
			continue
		}
		if !active.Hedged.CompareAndSwap(0, 1) {
			continue
		}
		active.SharedMaxOffsetMu.Lock()
		if active.SharedMaxOffset == nil {
			maxOff := &atomic.Int64{}
			maxOff.Store(current)
			active.SharedMaxOffset = maxOff
		}
		hedgedTask := types.Task{
			Offset:          current,
			Length:          stopAt - current,
			SharedMaxOffset: active.SharedMaxOffset,
		}
		active.SharedMaxOffsetMu.Unlock()
		queue.Push(hedgedTask)
		hedged++
		utils.Debug("EndGame: hedged %s (range: %d-%d)",
			utils.ConvertBytesToHumanReadable(hedgedTask.Length),
			hedgedTask.Offset, hedgedTask.Offset+hedgedTask.Length)
	}
	return hedged
}

func (d *ConcurrentDownloader) runBalancer(ctx context.Context, queue *TaskQueue) {
	// FORK-PATCH: Variable tick interval — fast in end-game, normal otherwise.
	ticker := time.NewTicker(types.BalancerTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// FORK-PATCH: End-game detection and proactive hedge.
			if d.isEndGame(queue) {
				ticker.Reset(types.EndGameTickInterval)
				d.HedgeAll(queue)
			} else {
				ticker.Reset(types.BalancerTickInterval)
			}

			for queue.IdleWorkers() > 0 {
				didWork := false
				if queue.Len() == 0 {
					if d.StealWork(queue) {
						didWork = true
					}
				}
				if !didWork && queue.Len() == 0 {
					if d.HedgeWork(queue) {
						didWork = true
					}
				}
				if !didWork {
					break
				}
			}
		}
	}
}

func (d *ConcurrentDownloader) runCompletionMonitor(ctx context.Context, queue *TaskQueue, fileSize int64, numConns int) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			queue.Close()
			return
		case <-ticker.C:
			// Completion condition:
			// 1. Queue is empty (no pending retries)
			// AND
			// 2. All workers are idle OR we've accounted for all bytes
			// Ensure queue is empty (no pending retries) before considering byte count.
			// This protects against cutting off active retries even if byte count seems high (due to overlaps etc).
			isDone := queue.Len() == 0 && (int(queue.IdleWorkers()) == numConns || (d.State != nil && d.State.Downloaded.Load() >= fileSize))
			if isDone {
				queue.Close()
				return
			}
		}
	}
}

func (d *ConcurrentDownloader) runHealthMonitor(ctx context.Context) {
	ticker := time.NewTicker(types.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkWorkerHealth()
		}
	}
}

func (d *ConcurrentDownloader) executeWorkers(ctx context.Context, client *http.Client, outFile *os.File, queue *TaskQueue, fileSize int64, workerMirrors []string, numConns int) error {
	// FORK-PATCH: Atomically store worker deps for ScaleWorkers
	deps := &workerDeps{
		ctx:       ctx,
		mirrors:   workerMirrors,
		file:      outFile,
		queue:     queue,
		totalSize: fileSize,
		client:    client,
	}
	d.workerDepsPtr.Store(deps)
	d.workersActive.Store(true)

	workerErrors := make(chan error, numConns)

	// Start workers
	for i := 0; i < numConns; i++ {
		// FORK-PATCH: Use nextWorkerID for dynamic ID allocation .
		workerID := int(d.nextWorkerID.Add(1)) - 1
		d.workerWg.Add(1)
		go func(wid int) {
			defer d.workerWg.Done()
			err := d.worker(ctx, wid, workerMirrors, outFile, queue, fileSize, client)
			if err != nil && err != context.Canceled {
				workerErrors <- err
			}
		}(workerID)
	}

	// Wait for all workers to complete
	go func() {
		// FORK-PATCH: Guard WaitGroup reuse — set workersActive=false under lock
		// before Wait(), so ScaleWorkers cannot Add() after Wait() begins
		d.workersMu.Lock()
		d.workersActive.Store(false)
		d.workersMu.Unlock()
		d.workerWg.Wait()
		close(workerErrors)
		queue.Close()
	}()

	// Check for errors or pause
	var downloadErr error
	seenErrors := make(map[string]bool)
	for err := range workerErrors {
		if err != nil {
			errStr := err.Error()
			if !seenErrors[errStr] {
				downloadErr = errors.Join(downloadErr, err)
				seenErrors[errStr] = true
			}
		}
	}
	return downloadErr
}

// DrainWorker marks a worker as draining. The worker will finish its current
// chunk and exit without picking up new work, preserving the TCP connection
// in the Transport idle pool.
// FORK-PATCH: Lightweight drain mechanism .
func (d *ConcurrentDownloader) DrainWorker(id int) bool {
	d.drainingWorkers.Store(id, struct{}{})
	d.activeMu.Lock()
	if at, exists := d.activeTasks[id]; exists {
		at.Draining.Store(true)
	}
	d.activeMu.Unlock()
	return true
}

// activeWorkerIDs returns IDs of all workers currently in activeTasks.
// FORK-PATCH: Used by ScaleWorkers to find drain candidates .
func (d *ConcurrentDownloader) activeWorkerIDs() []int {
	d.activeMu.Lock()
	defer d.activeMu.Unlock()
	ids := make([]int, 0, len(d.activeTasks))
	for id := range d.activeTasks {
		ids = append(ids, id)
	}
	return ids
}

// ScaleWorkers adjusts the number of running workers by delta.
// Positive delta spawns new workers (reusing idle connections from the
// Transport pool). Negative delta drains the slowest |delta| workers.
// Returns the number of workers actually added (positive) or drained (negative).
// FORK-PATCH: Runtime scale up/down API (WaitGroup reuse protection + workerDeps atomization)
func (d *ConcurrentDownloader) ScaleWorkers(delta int) int {
	deps := d.workerDepsPtr.Load()
	if delta == 0 || deps == nil || deps.ctx.Err() != nil {
		return 0
	}
	if delta > 0 {
		added := 0
		for i := 0; i < delta; i++ {
			// FORK-PATCH: Guard WaitGroup reuse — check workersActive under lock before Add
			d.workersMu.Lock()
			if !d.workersActive.Load() {
				d.workersMu.Unlock()
				break
			}
			id := int(d.nextWorkerID.Add(1)) - 1
			d.workerWg.Add(1)
			d.workersMu.Unlock()

			go func(workerID int) {
				defer d.workerWg.Done()
				if err := d.worker(deps.ctx, workerID, deps.mirrors, deps.file, deps.queue, deps.totalSize, deps.client); err != nil && err != context.Canceled {
					utils.Debug("Scaled worker %d error: %v", workerID, err)
				}
			}(id)
			added++
		}
		return added
	}

	toDrain := -delta
	type workerSpeed struct {
		id    int
		speed float64
	}
	candidates := make([]workerSpeed, 0, len(d.activeTasks))
	d.activeMu.Lock()
	for id, at := range d.activeTasks {
		if at.Draining.Load() {
			continue
		}
		candidates = append(candidates, workerSpeed{id, at.GetSpeed()})
	}
	d.activeMu.Unlock()

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].speed < candidates[j].speed
	})

	drained := 0
	for i := 0; i < toDrain && i < len(candidates); i++ {
		d.DrainWorker(candidates[i].id)
		drained++
	}
	return -drained
}

// FORK-PATCH: hard-interrupt a specific worker's current chunk. Cancels the
// task-level context (not the parent), which forces http.Transport to close
// the TCP socket and triggers the existing external-cancel requeue path: the
// worker goroutine stays alive, requeues remaining bytes, and reconnects on a
// fresh socket. Used for both dead and CDN-throttled workers. currentWorkers
// is unchanged (in-place reconnect, no headcount loss).
func (d *ConcurrentDownloader) KillWorker(id int) bool {
	d.activeMu.Lock()
	defer d.activeMu.Unlock()
	if at, ok := d.activeTasks[id]; ok && at.Cancel != nil {
		at.Cancel()
		return true
	}
	return false
}

// FORK-PATCH: runtime override of the relative slow-worker threshold.
func (d *ConcurrentDownloader) SetSlowWorkerThreshold(v float64) {
	d.slowThresholdOverride.Store(math.Float64bits(v))
	d.slowThresholdSet.Store(true)
}

// FORK-PATCH: slowThresholdOrDefault returns the runtime override when set,
// otherwise the supplied default (e.g. RuntimeConfig.GetSlowWorkerThreshold()).
func (d *ConcurrentDownloader) slowThresholdOrDefault(def float64) float64 {
	if !d.slowThresholdSet.Load() {
		return def
	}
	return math.Float64frombits(d.slowThresholdOverride.Load())
}

func (d *ConcurrentDownloader) handlePause(destPath string, fileSize int64, queue *TaskQueue, candidateMirrors []string) error {
	// 1. Collect active tasks as remaining work FIRST
	var activeRemaining []types.Task
	d.activeMu.Lock()
	for _, active := range d.activeTasks {
		if remaining := active.RemainingTask(); remaining != nil {
			activeRemaining = append(activeRemaining, *remaining)
		}
	}
	d.activeMu.Unlock()

	// 2. Collect remaining tasks from queue
	remainingTasks := queue.DrainRemaining()
	remainingTasks = append(remainingTasks, activeRemaining...)

	// Calculate Downloaded from remaining tasks (ensures consistency)
	var remainingBytes int64
	for _, task := range remainingTasks {
		remainingBytes += task.Length
	}
	if remainingBytes == 0 {
		utils.Debug("Download pause requested at completion boundary; finalizing as completed")
		d.State.Resume()
		_, _ = d.State.FinalizeSession(fileSize)
		return nil
	}
	computedDownloaded := fileSize - remainingBytes

	// Calculate total elapsed time
	totalElapsed := d.State.FinalizePauseSession(computedDownloaded)

	// Get persisted bitmap data
	bitmap, _, _, chunkSize, _ := d.State.GetBitmapSnapshot(false)

	var rateLimit int64
	var rateLimitSet bool
	if d.State != nil {
		rateLimit, rateLimitSet = d.State.GetRateLimit()
	} else {
		rateLimit, rateLimitSet = d.RateLimitBps, d.RateLimitSet
	}

	// Save state for resume (use computed value for consistency)
	s := &types.DownloadState{
		URL:             d.URL,
		ID:              d.ID,
		DestPath:        destPath,
		TotalSize:       fileSize,
		Downloaded:      computedDownloaded,
		Tasks:           remainingTasks,
		Filename:        filepath.Base(destPath),
		Elapsed:         totalElapsed.Nanoseconds(),
		Mirrors:         candidateMirrors,
		ChunkBitmap:     bitmap,
		ActualChunkSize: chunkSize,
		RateLimit:       rateLimit,
		RateLimitSet:    rateLimitSet,
		Workers:         d.Runtime.Workers,
		MinChunkSize:    d.Runtime.MinChunkSize,
	}
	if d.ProgressChan != nil {
		d.ProgressChan <- events.DownloadPausedMsg{
			DownloadID:   d.ID,
			Filename:     filepath.Base(destPath),
			Downloaded:   computedDownloaded,
			State:        s,
			RateLimit:    rateLimit,
			RateLimitSet: rateLimitSet,
			Workers:      d.Runtime.Workers,
			MinChunkSize: d.Runtime.MinChunkSize,
		}
	}

	utils.Debug("Download paused, state saved (Downloaded=%d, RemainingTasks=%d, RemainingBytes=%d)",
		computedDownloaded, len(remainingTasks), remainingBytes)
	return types.ErrPaused
}

func (d *ConcurrentDownloader) syncFile(outFile *os.File) error {
	if outFile == nil {
		return nil
	}
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync file: %w", err)
	}
	return nil
}

func (d *ConcurrentDownloader) bootstrapMetadata(ctx context.Context, client *http.Client, rawurl string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create concurrent bootstrap request: %w", err)
	}

	// Preserve auth/session cookies from the browser across the bootstrap request;
	// the server may reject unauthenticated probes with 401/403.
	for key, val := range d.Headers {
		if key != "Range" {
			req.Header.Set(key, val)
		}
	}
	// Range must come after custom headers so a caller-supplied Range can't override the probe byte
	req.Header.Set("User-Agent", d.Runtime.GetUserAgent())
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to bootstrap concurrent download: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*types.KB))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("concurrent bootstrap requires 206 response, got %d", resp.StatusCode)
	}

	contentRange := resp.Header.Get("Content-Range")
	if contentRange == "" {
		return 0, fmt.Errorf("concurrent bootstrap missing Content-Range header")
	}
	idx := strings.LastIndex(contentRange, "/")
	if idx == -1 || idx+1 >= len(contentRange) {
		return 0, fmt.Errorf("concurrent bootstrap invalid Content-Range header: %q", contentRange)
	}

	sizeStr := contentRange[idx+1:]
	if sizeStr == "*" {
		return 0, fmt.Errorf("concurrent bootstrap returned unknown size")
	}

	fileSize, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || fileSize <= 0 {
		return 0, fmt.Errorf("concurrent bootstrap invalid file size %q", sizeStr)
	}

	if d.State != nil {
		d.State.SetTotalSize(fileSize)
	}

	return fileSize, nil
}

// prewarmConnections fires off concurrent pings to the mirrors to populate the connection pool
func (d *ConcurrentDownloader) prewarmConnections(ctx context.Context, client *http.Client, numRequired, hedgeCount int, mirrors []string) {
	totalToStart := numRequired + hedgeCount
	if totalToStart > 128 { // Safety cap
		totalToStart = 128
	}

	// Channel to signal when a connection is ready (handshake complete)
	ready := make(chan struct{}, totalToStart)

	// Create a sub-context for the pings so we can stop them once we have enough
	pingCtx, cancelPings := context.WithCancel(ctx)
	defer cancelPings()

	for i := 0; i < totalToStart; i++ {
		go func(idx int) {
			// Round-robin mirrors
			mirror := mirrors[idx%len(mirrors)]

			// Use a fast Range request to ensure the handshake completes
			req, err := http.NewRequestWithContext(pingCtx, http.MethodGet, mirror, nil)
			if err != nil {
				return
			}

			// Forward custom headers (essential for authenticated mirrors)
			for key, val := range d.Headers {
				if key != "Range" {
					req.Header.Set(key, val)
				}
			}

			// Ensure User-Agent and Range are set
			if req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", d.Runtime.GetUserAgent())
			}
			req.Header.Set("Range", "bytes=0-0")

			// Perform dial + request
			resp, err := client.Do(req)
			if err != nil {
				return
			}

			// Drain body and close to return connection to idle pool, then signal readiness.
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			ready <- struct{}{}
		}(i)
	}

	// Wait until we have enough ready connections OR we hit a timeout
	completed := 0
	timeout := time.After(types.DialTimeout) // Use standard dial timeout for the whole batch

	for completed < numRequired {
		select {
		case <-ready:
			completed++
		case <-timeout:
			utils.Debug("Pre-warming timed out after %d/%d connections", completed, numRequired)
			return
		case <-ctx.Done():
			return
		}
	}

	utils.Debug("Pre-warming complete: %d connections hot", completed)
	// Remaining pings will be cancelled by defer cancelPings()
}
