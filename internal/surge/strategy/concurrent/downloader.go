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

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/strategy/preallocate"
	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// ConcurrentDownloader handles multi-connection downloads
type ConcurrentDownloader struct {
	ProgressChan chan<- types.DownloadEvent // Channel for events (start/complete/error)
	ID           string                     // Download ID
	State        *progress.DownloadProgress // Shared state for TUI polling
	activeTasks  map[int]*ActiveTask
	activeMu     sync.Mutex
	// abandonedRemaining holds ENOSPC in-flight residuals captured off the
	// live queue (no Push during the storm window). saveStateSnapshot unions
	// these with active+queue drains so Resume does not lose the failing range.
	abandonedRemaining []types.Task
	URL                string // For pause/resume
	DestPath           string // For pause/resume
	Runtime            *types.RuntimeConfig
	Limiter            types.ByteLimiter
	RateLimitBps       int64
	RateLimitSet       bool
	TotalSize          int64
	bufPool            *TieredBufferPool // FORK-PATCH: tiered buffer pool with cap filter
	Headers            map[string]string // Custom HTTP headers from browser (cookies, auth, etc.)
	// FORK-PATCH: payload-first Range verification. Mode is copied from the
	// download record; SkipServerProbe stays sticky after promote.
	RangeAcquisitionMode types.RangeAcquisitionMode
	SkipServerProbe      bool

	payloadFirstSession  atomic.Bool
	payloadFirstVerified atomic.Bool
	skipRangePrewarm     atomic.Bool
	pfFanout             sync.Once
	pfPlannedConns       int
	pfPlannedTasks       []types.Task
	pfChunkSize          int64
	pfCandidateMirrors   []string
	pfHelperCtx          context.Context
	pfHelperWG           *sync.WaitGroup
	pfQueue              *TaskQueue
	pfFileSize           int64

	// FORK-PATCH: Drain/scale infrastructure
	nextWorkerID    atomic.Int64               // dynamic worker ID allocation
	drainingWorkers sync.Map                   // workerID -> struct{}{}; checked before queue.Pop()
	workerDepsPtr   atomic.Pointer[workerDeps] // FORK-PATCH: atomized worker deps
	workerWg        sync.WaitGroup             // tracks all spawned workers (initial + scaled)
	workersActive   atomic.Bool                // FORK-PATCH: guards WaitGroup reuse
	workersMu       sync.Mutex                 // FORK-PATCH: guards Add/Done around Wait
	totalWorkers    atomic.Int64               // FORK-PATCH: current worker count (initial + scaled - exited)

	// FORK-PATCH: per-worker (connection) session tracking. Survives across
	// chunks/retries, unlike ActiveTask which is rebuilt every chunk.
	workerSessions sync.Map // workerID -> *workerSession

	// FORK-PATCH: runtime override for the relative slow-worker threshold.
	// When set (slowThresholdSet=true), checkWorkerHealth uses this instead of
	// RuntimeConfig. A value of 0 disables the relative slow-speed cancel while
	// leaving stall detection armed.
	slowThresholdSet      atomic.Bool
	slowThresholdOverride atomic.Uint64 // math.Float64bits(v)

	// FORK-PATCH: End-game hedge and poison defense.
	consecutiveHedgeErrors atomic.Int32
	hedgeDisabled          atomic.Bool

	// FORK-PATCH: session-local Soft-403 pressure state.
	soft403Guard soft403ProgressGuard

	// FORK-PATCH: TTFB one-shot guard — only the first non-hedged 206 sends FirstByte.
	ttfbSent atomic.Bool
	// FORK-PATCH: resume flag — suppresses FirstByte on resume (setupTasks wiring later).
	isResume atomic.Bool

	hostLimiter *transport.HostRateLimiter
}

// Soft403StickyExhaustions is the Soft-403 pressure limit.
const Soft403StickyExhaustions = 16

// Soft403NoProgressConfirmWindow is the confirmation window after pressure arms.
const Soft403NoProgressConfirmWindow = 5 * time.Second

var (
	soft403StickyExhaustions       = Soft403StickyExhaustions
	soft403NoProgressConfirmWindow = Soft403NoProgressConfirmWindow
)

type soft403ProgressGuard struct {
	mu                        sync.Mutex
	primed                    bool
	exhaustionCount           int
	observedVerifiedProgress  int64
	candidateSince            time.Time
	candidateVerifiedProgress int64
}

func (d *ConcurrentDownloader) resetSoft403Guard() {
	d.soft403Guard.reset()
}

func (d *ConcurrentDownloader) primeSoft403Guard() {
	state := d.State
	if state == nil {
		return
	}
	d.soft403Guard.prime(state.Bytes.VerifiedProgress.Load())
}

func (d *ConcurrentDownloader) recordSoft403Exhaustion(now time.Time) bool {
	state := d.State
	if state == nil {
		return d.soft403Guard.recordWithoutProgress(soft403StickyExhaustions)
	}

	return d.soft403Guard.recordWithProgress(
		now,
		state.Bytes.VerifiedProgress.Load(),
		func() int64 { return state.Bytes.VerifiedProgress.Load() },
		soft403StickyExhaustions,
		soft403NoProgressConfirmWindow,
	)
}

func (g *soft403ProgressGuard) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.primed = false
	g.observedVerifiedProgress = 0
	g.clearPressure()
}

func (g *soft403ProgressGuard) prime(verifiedProgress int64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.primed = true
	g.observedVerifiedProgress = verifiedProgress
	g.clearPressure()
}

func (g *soft403ProgressGuard) recordWithoutProgress(limit int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.candidateSince = time.Time{}
	g.candidateVerifiedProgress = 0
	g.increment(limit)
	return g.exhaustionCount >= normalizedSoft403Limit(limit)
}

func (g *soft403ProgressGuard) recordWithProgress(now time.Time, verifiedProgress int64, finalVerifiedProgress func() int64, limit int, confirmWindow time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.primed {
		g.primed = true
		g.observedVerifiedProgress = verifiedProgress
		g.clearPressure()
	} else if verifiedProgress != g.observedVerifiedProgress {
		g.observedVerifiedProgress = verifiedProgress
		g.clearPressure()
	}

	g.increment(limit)
	limit = normalizedSoft403Limit(limit)
	if g.exhaustionCount < limit {
		return false
	}
	if g.candidateSince.IsZero() {
		g.candidateSince = now
		g.candidateVerifiedProgress = verifiedProgress
		return false
	}
	if now.Before(g.candidateSince.Add(confirmWindow)) {
		return false
	}

	finalVerified := finalVerifiedProgress()
	if finalVerified != g.candidateVerifiedProgress {
		g.observedVerifiedProgress = finalVerified
		g.clearPressure()
		g.increment(limit)
		return false
	}
	return true
}

func (g *soft403ProgressGuard) clearPressure() {
	g.exhaustionCount = 0
	g.candidateSince = time.Time{}
	g.candidateVerifiedProgress = 0
}

func (g *soft403ProgressGuard) increment(limit int) {
	limit = normalizedSoft403Limit(limit)
	if g.exhaustionCount < limit {
		g.exhaustionCount++
	}
}

func normalizedSoft403Limit(limit int) int {
	if limit < 1 {
		return 1
	}
	return limit
}

// FORK-PATCH: per-worker (connection) session, survives across chunks.
type workerSession struct {
	startUnix    int64
	sessionBytes atomic.Int64
}

// FORK-PATCH: workerDeps bundles worker dependencies for atomic access via ScaleWorkers
type workerDeps struct {
	ctx       context.Context
	cancel    context.CancelFunc
	mirrors   []string
	file      *os.File
	queue     *TaskQueue
	totalSize int64
	client    *http.Client
	errs      chan<- error
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

// recordHedgeError increments the consecutive 4xx/5xx counter and disables
// hedging when the threshold is reached.
// FORK-PATCH: Poison defense for end-game hedge.
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

// NewConcurrentDownloader creates a new concurrent downloader with all required parameters
func NewConcurrentDownloader(id string, progressCh chan<- types.DownloadEvent, progState *progress.DownloadProgress, runtime *types.RuntimeConfig) *ConcurrentDownloader {
	if runtime == nil {
		runtime = types.DefaultRuntimeConfig()
	}

	return &ConcurrentDownloader{
		ID:           id,
		ProgressChan: progressCh,
		State:        progState,
		activeTasks:  make(map[int]*ActiveTask),
		Runtime:      runtime,
		hostLimiter:  transport.DefaultHostRateLimiter,
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
	sizeMB := float64(fileSize) / float64(utils.MiB)
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
			chunkSize = 2 * utils.MiB // Default 2MB if not configured
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
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return types.ErrMaxRedirects
		}
		if len(via) > 0 {
			utils.CopyRedirectHeaders(req, via[0])
		}
		return nil
	}
}

// Download downloads a file using multiple concurrent connections
// Uses pre-probed metadata (file size already known)
func (d *ConcurrentDownloader) Download(ctx context.Context, rawurl string, candidateMirrors []string, activeMirrors []string, destPath string, fileSize int64) error {
	utils.Debug("ConcurrentDownloader.Download: %s -> %s (size: %d, mirrors: %d)", rawurl, destPath, fileSize, len(activeMirrors))

	if d.hostLimiter == nil {
		d.hostLimiter = transport.DefaultHostRateLimiter
	}

	d.resetSoft403Guard()

	payloadFirst := d.RangeAcquisitionMode.IsPayloadFirst()
	if payloadFirst {
		d.payloadFirstSession.Store(true)
		d.skipRangePrewarm.Store(true)
		if fileSize <= 0 {
			return types.ErrSourceMetadataMismatch
		}
	}

	d.initMirrorStatus(rawurl, candidateMirrors, activeMirrors, destPath)

	workingPath := destPath + types.IncompleteSuffix
	downloadCtx, cancel := context.WithCancel(ctx)

	if d.State != nil {
		d.State.SetCancelFunc(cancel)
	}

	client, httpTransport := d.setupNetwork()
	// Release transport back to the pool ONLY after all helpers and workers are joined (LIFO: runs last)
	defer transport.DefaultNetworkPool.ReleaseTransport(httpTransport)

	// Helper synchronization for monitors and balancer
	var wgHelpers sync.WaitGroup
	// Ensure we wait for helpers to finish; run wait AFTER cancel (LIFO: Wait runs second, cancel runs first)
	defer wgHelpers.Wait()
	defer cancel()

	// Ensure we have the total file size
	if fileSize <= 0 {
		if payloadFirst {
			return types.ErrSourceMetadataMismatch
		}
		var err error
		fileSize, err = d.bootstrapMetadata(downloadCtx, client, rawurl)
		if err != nil {
			return err
		}
	}
	d.TotalSize = fileSize

	// Load saved state early to determine remaining size for connection count heuristic
	savedState, err := store.LoadState(d.URL, destPath)
	isResume := err == nil && savedState != nil && len(savedState.Tasks) > 0

	effectiveSizeForWorkers := d.getEffectiveSizeForWorkers(fileSize, savedState, isResume)

	numConns := d.getInitialConnections(effectiveSizeForWorkers)
	chunkSize := d.determineChunkSize(fileSize, numConns)

	workerMirrors := d.getWorkerMirrors(activeMirrors)
	if payloadFirst {
		workerMirrors = []string{rawurl}
	}

	// Pre-warm connections if configured
	hedgeCount := d.Runtime.GetDialHedgeCount()
	if hedgeCount > 0 && !d.skipRangePrewarm.Load() {
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

	tasks, err := d.setupTasks(destPath, fileSize, chunkSize, outFile, savedState, isResume)
	if err != nil {
		return err
	}
	d.primeSoft403Guard()

	queue := NewTaskQueue()
	queue.PushMultiple(tasks)

	verifyConns := numConns
	if payloadFirst {
		d.pfPlannedConns = numConns
		d.pfPlannedTasks = append([]types.Task(nil), tasks...)
		d.pfChunkSize = chunkSize
		d.pfCandidateMirrors = append([]string(nil), candidateMirrors...)
		d.pfHelperCtx = downloadCtx
		d.pfHelperWG = &wgHelpers
		d.pfQueue = queue
		d.pfFileSize = fileSize
		verifyConns = 1
	} else {
		d.startHelpers(downloadCtx, &wgHelpers, queue, fileSize, numConns)
	}

	// Execute download workers
	downloadErr := d.executeWorkers(downloadCtx, cancel, client, outFile, queue, fileSize, workerMirrors, verifyConns)

	// Handle pause request: must return types.ErrPaused to prevent finalization
	if d.State != nil && d.State.IsPaused() {
		pauseErr := d.handlePause(destPath, fileSize, queue, candidateMirrors)
		if pauseErr == nil {
			// Pause was requested at completion boundary, so handlePause finalized it.
			return d.syncFile(outFile)
		}
		return pauseErr
	}

	if downloadErr != nil {
		// Persist pause-grade progress so whole-download retries and a later
		// EventError→Resume can continue from remaining Tasks (not byte 0).
		// Skip cancel/deadline to match scheduler isCancel (no EventError).
		// FORK-PATCH: never snapshot an unverified payload-first session —
		// Tasks at 0 bytes would cold-resume as RangeSupported.
		if d.payloadFirstSession.Load() && !d.payloadFirstVerified.Load() {
			return downloadErr
		}
		if d.State != nil &&
			!errors.Is(downloadErr, context.Canceled) &&
			!errors.Is(downloadErr, context.DeadlineExceeded) {
			_ = d.saveStateSnapshot(destPath, fileSize, queue, candidateMirrors, false)
		}
		return downloadErr
	}
	if downloadCtx.Err() != nil {
		return downloadCtx.Err()
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

	httpTransport := transport.DefaultNetworkPool.AcquireTransport(proxyURL, customDNS, types.PoolMaxConnsPerHost)
	client := &http.Client{Transport: httpTransport}
	d.applyClientSettings(client)
	return client, httpTransport
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

func (d *ConcurrentDownloader) getEffectiveSizeForWorkers(fileSize int64, savedState *types.DownloadRecord, isResume bool) int64 {
	if isResume && savedState != nil && savedState.TotalSize > 0 {
		eff := savedState.TotalSize - savedState.Downloaded
		if eff < 0 {
			return 0
		}
		return eff
	}
	return fileSize
}

func (d *ConcurrentDownloader) setupTasks(destPath string, fileSize, chunkSize int64, outFile *os.File, savedState *types.DownloadRecord, isResume bool) ([]types.Task, error) {
	d.isResume.Store(isResume) // FORK-PATCH: suppress FirstByte on resume

	if isResume {
		if d.State != nil {
			d.State.SetSavedElapsed(time.Duration(savedState.Elapsed))

			if len(savedState.ChunkBitmap) > 0 && savedState.ActualChunkSize > 0 {
				d.State.RestoreBitmap(savedState.ChunkBitmap, savedState.ActualChunkSize)
				d.State.RecalculateProgress(savedState.Tasks)
				// FORK-PATCH: Unconditionally trust bitmap-recalculated VP.
				// savedState.Downloaded may be inflated by task-loss paths;
				// overriding VP with it causes false completion. Restore
				// Downloaded to max(saved, VP) for counter consistency —
				// UI progress follows VP via GetProgress(), not this counter.
				vp := d.State.Bytes.VerifiedProgress.Load()
				if savedState.Downloaded > vp {
					d.State.Bytes.Downloaded.Store(savedState.Downloaded)
				} else {
					d.State.Bytes.Downloaded.Store(vp)
				}
				utils.Debug("Restored chunk map: size %d", savedState.ActualChunkSize)
			} else {
				// FORK-PATCH: Legacy .surge files without bitmap — keep
				// historical behavior: VP = Downloaded = savedState.Downloaded.
				d.State.Bytes.VerifiedProgress.Store(savedState.Downloaded)
				d.State.Bytes.Downloaded.Store(savedState.Downloaded)
			}
			// FORK-PATCH: Sync session start after VP is finalized in both
			// branches so SessionStartBytes captures the correct VP.
			d.State.SyncSessionStart()
		}
		utils.Debug("Resuming from saved state: %d tasks, %d bytes downloaded", len(savedState.Tasks), savedState.Downloaded)
		return savedState.Tasks, nil
	}

	// FORK-PATCH: fresh download uses shared physical preallocation.
	if err := preallocate.Preallocate(outFile, fileSize); err != nil {
		return nil, fmt.Errorf("failed to preallocate file: %w", err)
	}
	if d.State != nil {
		d.State.Bytes.Downloaded.Store(0)
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

// isEndGame returns true when all tasks have been dispatched (queue is empty)
// but some workers are still active and idle workers are available for hedging.
// FORK-PATCH: End-game detection for proactive hedge.
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
// FORK-PATCH: End-game proactive hedge.
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
			utils.FormatBytes(hedgedTask.Length),
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
			// FORK-PATCH: When all bytes are accounted for, force-exit any
			// workers still stuck in resp.Body.Read() (tarpit servers that
			// hold/trickle connections after partial data). Closing the queue
			// only unblocks queue.Pop() waiters; stuck readers need taskCtx
			// cancellation. Requeued hedged tasks may leave queue.Len() > 0,
			// so byte-count completion must NOT gate on an empty queue.
			// FORK-PATCH: use VerifiedProgress (chunk-level dedup) instead of
			// Downloaded, which can overcount when SharedMaxOffset is nil.
			if d.State != nil && d.State.Bytes.VerifiedProgress.Load() >= fileSize {
				// FORK-PATCH: Cancel all active workers under activeMu to close
				// the snapshot gap. Holding the lock during cancel is safe:
				// Cancel() is a non-blocking channel close. The worker-side VP
				// re-check under the same lock ensures workers that haven't
				// registered yet exit before entering downloadTask().
				d.activeMu.Lock()
				for _, at := range d.activeTasks {
					if at.Cancel != nil {
						at.Cancel()
					}
				}
				d.activeMu.Unlock()
				queue.Close()
				// FORK-PATCH: drain remaining queued tasks — Close() only sets
				// done=true + Broadcast; Pop() still returns already-queued tasks.
				queue.DrainRemaining()
				return
			}
			// Normal completion: queue empty AND all workers idle.
			// FORK-PATCH: Use totalWorkers (atomic) instead of stale numConns
			// to correctly handle ScaleWorkers adding/draining workers at runtime.
			// Fall back to numConns when totalWorkers is 0 (direct test usage
			// without executeWorkers initializing the counter).
			effectiveConns := int(d.totalWorkers.Load())
			if effectiveConns == 0 {
				effectiveConns = numConns
			}
			isDone := queue.Len() == 0 && int(queue.IdleWorkers()) == effectiveConns
			// FORK-PATCH: VP guard — prevent silent corruption. When tasks are
			// lost, VP stalls below fileSize but queue is empty and all workers
			// idle. Without this guard the monitor would return "success" with
			// incomplete content → zero-fill holes. Converts silent corruption
			// into a detectable hang.
			if isDone && d.State != nil && d.State.Bytes.VerifiedProgress.Load() < fileSize {
				isDone = false
			}
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

func (d *ConcurrentDownloader) executeWorkers(ctx context.Context, cancel context.CancelFunc, client *http.Client, outFile *os.File, queue *TaskQueue, fileSize int64, workerMirrors []string, numConns int) error {
	workerErrors := make(chan error, numConns)

	// FORK-PATCH: Atomically store worker deps for ScaleWorkers
	deps := &workerDeps{
		ctx:       ctx,
		cancel:    cancel,
		mirrors:   workerMirrors,
		file:      outFile,
		queue:     queue,
		totalSize: fileSize,
		client:    client,
		errs:      workerErrors,
	}
	d.workerDepsPtr.Store(deps)
	d.workersActive.Store(true)
	d.totalWorkers.Store(int64(numConns)) // FORK-PATCH: track actual worker count for completion monitor

	// Start workers
	for i := 0; i < numConns; i++ {
		// FORK-PATCH: Use nextWorkerID for dynamic ID allocation.
		workerID := int(d.nextWorkerID.Add(1)) - 1
		d.workerWg.Add(1)
		go func(wid int) {
			defer d.workerWg.Done()
			defer d.totalWorkers.Add(-1) // FORK-PATCH: track actual worker count for completion monitor
			err := d.worker(ctx, wid, workerMirrors, outFile, queue, fileSize, client)
			if err != nil && !errors.Is(err, context.Canceled) {
				workerErrors <- err
				cancel() // live: cancel whole download on fatal initial-worker error
			}
		}(workerID)
	}

	// Wait for all workers to complete
	go func() {
		// FORK-PATCH: Guard WaitGroup reuse — wait for all workers to finish,
		// THEN set workersActive=false under lock so ScaleWorkers can Add()
		// during the wait (drain) window without being blocked.
		d.workerWg.Wait()
		d.workersMu.Lock()
		d.workersActive.Store(false)
		d.workersMu.Unlock()
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
// FORK-PATCH: Lightweight drain mechanism.
func (d *ConcurrentDownloader) DrainWorker(id int) bool {
	d.drainingWorkers.Store(id, struct{}{})
	d.activeMu.Lock()
	if at, exists := d.activeTasks[id]; exists {
		at.Draining.Store(true)
	}
	d.activeMu.Unlock()
	return true
}

// ScaleWorkers adjusts the number of running workers by delta.
// Positive delta admits under workersActive, optionally prewarms once
// (bounded), then spawns all admitted workers (reusing idle connections).
// Negative delta drains the slowest |delta| workers.
// Returns the number of workers actually added (positive) or drained (negative).
// FORK-PATCH: Runtime scale up/down API (WaitGroup reuse protection + workerDeps atomization + ScaleUp prewarm)
func (d *ConcurrentDownloader) ScaleWorkers(delta int) int {
	deps := d.workerDepsPtr.Load()
	if delta == 0 || deps == nil || deps.ctx.Err() != nil {
		return 0
	}
	if delta > 0 && d.payloadFirstSession.Load() && !d.payloadFirstVerified.Load() {
		return 0
	}
	if delta > 0 {
		admitted := make([]int, 0, delta)
		for i := 0; i < delta; i++ {
			// FORK-PATCH: Guard WaitGroup reuse — check workersActive under lock before Add
			d.workersMu.Lock()
			if !d.workersActive.Load() {
				d.workersMu.Unlock()
				break
			}
			id := int(d.nextWorkerID.Add(1)) - 1
			d.workerWg.Add(1)
			d.totalWorkers.Add(1) // FORK-PATCH: track actual worker count for completion monitor
			d.workersMu.Unlock()
			admitted = append(admitted, id)
		}
		if len(admitted) == 0 {
			return 0
		}

		// FORK-PATCH: one batched ScaleUp prewarm before spawn (ignore DialHedgeCount)
		if !d.skipRangePrewarm.Load() && deps.client != nil && len(deps.mirrors) > 0 {
			need := len(admitted)
			if need > 128 {
				need = 128
			}
			d.prewarmConnectionsBounded(deps.ctx, deps.client, need, deps.mirrors, scalePrewarmBudget)
		}

		// Always spawn admitted workers (WaitGroup Done pairing); prewarm timeout is best-effort.
		for _, id := range admitted {
			go func(workerID int) {
				defer d.workerWg.Done()
				defer d.totalWorkers.Add(-1) // FORK-PATCH: track actual worker count for completion monitor
				if err := d.worker(deps.ctx, workerID, deps.mirrors, deps.file, deps.queue, deps.totalSize, deps.client); err != nil && !errors.Is(err, context.Canceled) {
					// Forward non-ctx errors like initial workers. Cancel first,
					// then non-blocking send so a full buffer cannot deadlock
					// workerWg.Wait vs channel close.
					if deps.cancel != nil {
						deps.cancel()
					}
					if deps.errs != nil {
						select {
						case deps.errs <- err:
						default:
						}
					} else {
						utils.Debug("Scaled worker %d error: %v", workerID, err)
					}
				}
			}(id)
		}
		return len(admitted)
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

// preferMaxOffsetSameSharedMaxOffset keeps, for each non-nil SharedMaxOffset
// pointer identity, the task with the highest Offset (then shortest Length on
// Offset ties; remaining ties keep the first encountered). Nil-pointer tasks
// are always kept. Same-pointer outcome is order-independent; collect still
// uses active-first for unrelated remaining visibility.
// FORK-PATCH: #568 same-pointer max-Offset before mergeOverlappingTasks.
func preferMaxOffsetSameSharedMaxOffset(tasks []types.Task) []types.Task {
	if len(tasks) <= 1 {
		return tasks
	}
	out := make([]types.Task, 0, len(tasks))
	pos := make(map[*atomic.Int64]int, len(tasks))
	for _, task := range tasks {
		if task.SharedMaxOffset == nil {
			out = append(out, task)
			continue
		}
		if idx, ok := pos[task.SharedMaxOffset]; ok {
			prev := out[idx]
			if task.Offset > prev.Offset ||
				(task.Offset == prev.Offset && task.Length < prev.Length) {
				out[idx] = task
			}
			continue
		}
		pos[task.SharedMaxOffset] = len(out)
		out = append(out, task)
	}
	return out
}

// mergeOverlappingTasks merges overlapping/adjacent tasks by union range.
// When hedge is active, activeTasks may contain both original and hedge workers
// whose RemainingTask() returns overlapping byte ranges. Without dedup,
// remainingBytes double-counts the overlap → computedDownloaded undercounts.
// FORK-PATCH: Merge-by-union for handlePause remaining-task dedup.
func mergeOverlappingTasks(tasks []types.Task) []types.Task {
	if len(tasks) <= 1 {
		return tasks
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].Offset < tasks[j].Offset
	})
	merged := make([]types.Task, 0, len(tasks))
	cur := tasks[0]
	for i := 1; i < len(tasks); i++ {
		curEnd := cur.Offset + cur.Length
		next := tasks[i]
		nextEnd := next.Offset + next.Length
		if curEnd >= next.Offset {
			// Overlap or adjacent — merge by union
			if nextEnd > curEnd {
				cur.Length = nextEnd - cur.Offset
			}
			// Keep non-nil SharedMaxOffset if either has one
			if cur.SharedMaxOffset == nil && next.SharedMaxOffset != nil {
				cur.SharedMaxOffset = next.SharedMaxOffset
			}
		} else {
			merged = append(merged, cur)
			cur = next
		}
	}
	merged = append(merged, cur)
	return merged
}

func (d *ConcurrentDownloader) handlePause(destPath string, fileSize int64, queue *TaskQueue, candidateMirrors []string) error {
	return d.saveStateSnapshot(destPath, fileSize, queue, candidateMirrors, true)
}

// persistRangeSupportedBeforeWrite writes RangeSupported + the original planned
// task layout before the first payload WriteAt. Failure must not write body.
func (d *ConcurrentDownloader) persistRangeSupportedBeforeWrite() error {
	if !d.payloadFirstSession.Load() || d.payloadFirstVerified.Load() {
		return nil
	}
	if d.DestPath == "" || d.URL == "" {
		return fmt.Errorf("payload-first persist missing dest identity")
	}

	tasks := make([]types.Task, len(d.pfPlannedTasks))
	copy(tasks, d.pfPlannedTasks)
	for i := range tasks {
		tasks[i].SharedMaxOffset = nil
	}

	var bitmap []byte
	chunkSize := d.pfChunkSize
	if d.State != nil {
		var snapChunk int64
		bitmap, _, _, snapChunk, _ = d.State.GetBitmapSnapshot(false)
		if snapChunk > 0 {
			chunkSize = snapChunk
		}
	}

	var workers int
	var minChunkSize int64
	if d.Runtime != nil {
		workers = d.Runtime.Workers
		minChunkSize = d.Runtime.MinChunkSize
	}

	s := &types.DownloadRecord{
		URL:                  d.URL,
		ID:                   d.ID,
		DestPath:             d.DestPath,
		TotalSize:            d.pfFileSize,
		Downloaded:           0,
		Tasks:                tasks,
		Filename:             filepath.Base(d.DestPath),
		Mirrors:              append([]string(nil), d.pfCandidateMirrors...),
		ChunkBitmap:          bitmap,
		ActualChunkSize:      chunkSize,
		Workers:              workers,
		MinChunkSize:         minChunkSize,
		RangeAcquisitionMode: types.RangeAcquireRangeSupported,
		SkipServerProbe:      d.SkipServerProbe,
	}
	if d.State != nil {
		s.RateLimit, s.RateLimitSet = d.State.GetRateLimit()
	}

	if err := store.SaveStateWithOptions(d.URL, d.DestPath, s, store.SaveStateOptions{SkipFileHash: true}); err != nil {
		return fmt.Errorf("payload-first persist RangeSupported: %w", err)
	}

	d.RangeAcquisitionMode = types.RangeAcquireRangeSupported
	d.payloadFirstVerified.Store(true)
	d.fanoutAfterRangeVerified()
	return nil
}

func (d *ConcurrentDownloader) fanoutAfterRangeVerified() {
	d.pfFanout.Do(func() {
		if d.pfHelperWG != nil && d.pfHelperCtx != nil && d.pfQueue != nil {
			d.startHelpers(d.pfHelperCtx, d.pfHelperWG, d.pfQueue, d.pfFileSize, d.pfPlannedConns)
		}
		remaining := d.pfPlannedConns - 1
		if remaining > 0 {
			d.ScaleWorkers(remaining)
		}
	})
}

func (d *ConcurrentDownloader) sendFirstByteOnce(ttfbStart time.Time, activeTask *ActiveTask) {
	if d.isResume.Load() || activeTask == nil || activeTask.Hedged.Load() != 0 {
		return
	}
	if !d.ttfbSent.CompareAndSwap(false, true) {
		return
	}
	ttfbMs := time.Since(ttfbStart).Milliseconds()
	if d.ProgressChan == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		d.ProgressChan <- types.DownloadEvent{
			Type:       types.EventFirstByte,
			DownloadID: d.ID,
			TTFBMs:     ttfbMs,
		}
	}()
}

// saveStateSnapshot builds a pause-grade DownloadRecord from active tasks +
// queue remaining work. emitPauseEvent=true mirrors historical handlePause
// (EventPaused + ErrPaused). emitPauseEvent=false best-effort persists via
// SaveStateWithOptions and stashes the record for EventError.State.
func (d *ConcurrentDownloader) saveStateSnapshot(destPath string, fileSize int64, queue *TaskQueue, candidateMirrors []string, emitPauseEvent bool) error {
	// 1. Collect active RemainingTask copies, drain abandoned ENOSPC stash
	// (off-queue residuals), then drain the live queue (active-first).
	// Same-pointer prefer is max-Offset (order-independent); active-first
	// collect is retained for remaining visibility when the map is non-empty.
	var activeRemaining []types.Task
	d.activeMu.Lock()
	for _, active := range d.activeTasks {
		if remaining := active.RemainingTask(); remaining != nil {
			activeRemaining = append(activeRemaining, *remaining)
		}
	}
	abandoned := d.abandonedRemaining
	d.abandonedRemaining = nil
	d.activeMu.Unlock()

	allTasks := append(append(append([]types.Task(nil), activeRemaining...), abandoned...), queue.DrainRemaining()...)

	// FORK-PATCH: #568 max-Offset same SharedMaxOffset pointer, then
	// merge-by-union for general range overlap/adjacency (SPEC-200). Prefer
	// must run before merge — merge alone unions queued-stale 500/500 with
	// advanced 600/400 into 500/500. Max-Offset also covers empty-active
	// production pause (requeue-then-delete) where FIFO would see stale first.
	remainingTasks := mergeOverlappingTasks(preferMaxOffsetSameSharedMaxOffset(allTasks))

	// Calculate Downloaded from remaining tasks (ensures consistency)
	var remainingBytes int64
	for _, task := range remainingTasks {
		remainingBytes += task.Length
	}
	if remainingBytes == 0 {
		if d.State == nil || d.State.Bytes.VerifiedProgress.Load() >= fileSize {
			utils.Debug("Download pause requested at completion boundary; finalizing as completed")
			if d.State != nil {
				d.State.Resume()
				_, _ = d.State.FinalizeSession(fileSize)
			}
			return nil
		}
		// VP < fileSize but remainingBytes == 0: tasks were lost or VP
		// undercounted. Fall through to the standard pause path to save
		// state for resume instead of finalizing an incomplete file.
		utils.Debug("Download pause at remainingBytes=0 but VP=%d < fileSize=%d; saving state for resume",
			d.State.Bytes.VerifiedProgress.Load(), fileSize)
	}
	computedDownloaded := fileSize - remainingBytes
	// FORK-PATCH: Trust the chunk-level dedup counter over the recompute.
	// remainingBytes can still overstate work when hedged partners share a
	// pointer (queued-stale duplicates / bitmap already counted bytes), so
	// the recompute can undercount even though RemainingTask carries
	// SharedMaxOffset for write-dedup. Prefer + max(VP, …) close the gap.
	if d.State != nil {
		if vp := d.State.Bytes.VerifiedProgress.Load(); vp > computedDownloaded {
			computedDownloaded = vp
		}
	} else {
		// Call sites normally gate State; keep the helper nil-safe for reuse.
		utils.Debug("saveStateSnapshot: nil State with remainingBytes=%d; skipping persist", remainingBytes)
		if emitPauseEvent {
			return types.ErrPaused
		}
		return nil
	}

	// Calculate total elapsed time
	totalElapsed := d.State.FinalizePauseSession(computedDownloaded)

	// Get persisted bitmap data
	bitmap, _, _, chunkSize, _ := d.State.GetBitmapSnapshot(false)

	var rateLimit int64
	var rateLimitSet bool
	rateLimit, rateLimitSet = d.State.GetRateLimit()

	var workers int
	var minChunkSize int64
	if d.Runtime != nil {
		workers = d.Runtime.Workers
		minChunkSize = d.Runtime.MinChunkSize
	}

	// Clear SharedMaxOffset on pause-snapshot copies only so hot resume does
	// not pin ranges as hedged. Live ActiveTask pointers stay untouched.
	for i := range remainingTasks {
		remainingTasks[i].SharedMaxOffset = nil
	}

	// Save state for resume (use computed value for consistency)
	s := &types.DownloadRecord{
		URL:                  d.URL,
		ID:                   d.ID,
		DestPath:             destPath,
		TotalSize:            fileSize,
		Downloaded:           computedDownloaded,
		Tasks:                remainingTasks,
		Filename:             filepath.Base(destPath),
		Elapsed:              totalElapsed.Nanoseconds(),
		Mirrors:              candidateMirrors,
		ChunkBitmap:          bitmap,
		ActualChunkSize:      chunkSize,
		RateLimit:            rateLimit,
		RateLimitSet:         rateLimitSet,
		Workers:              workers,
		MinChunkSize:         minChunkSize,
		RangeAcquisitionMode: d.RangeAcquisitionMode,
		SkipServerProbe:      d.SkipServerProbe,
	}

	if emitPauseEvent {
		if d.ProgressChan != nil {
			d.ProgressChan <- types.DownloadEvent{
				Type:         types.EventPaused,
				DownloadID:   d.ID,
				Filename:     filepath.Base(destPath),
				Downloaded:   computedDownloaded,
				State:        s,
				RateLimit:    rateLimit,
				RateLimitSet: rateLimitSet,
				Workers:      workers,
				MinChunkSize: minChunkSize,
			}
		}

		utils.Debug("Download paused, state saved (Downloaded=%d, RemainingTasks=%d, RemainingBytes=%d)",
			computedDownloaded, len(remainingTasks), remainingBytes)
		return types.ErrPaused
	}

	// Error / retry path: stash for EventError + best-effort direct persist.
	// Do not set Pausing/Paused — scheduler isPaused must stay false.
	d.State.SetPendingResumeState(s)
	if saveErr := store.SaveStateWithOptions(d.URL, destPath, s, store.SaveStateOptions{SkipFileHash: true}); saveErr != nil {
		utils.Debug("Failed to save state snapshot: %v", saveErr)
	} else {
		utils.Debug("Saved progress state snapshot (Downloaded=%d, RemainingTasks=%d)", s.Downloaded, len(s.Tasks))
	}
	return nil
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
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32*utils.KiB))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusPartialContent {
		if types.IsPermanentHTTPStatus(resp.StatusCode) {
			return 0, fmt.Errorf("concurrent bootstrap requires 206 response, got %d: %w", resp.StatusCode, types.ErrPermanentHTTP)
		}
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

// FORK-PATCH: ScaleUp prewarm wait budget (hard cap). Not DialTimeout.
// Bounds Convergence tick stall; timeout degrades to today's worker dial.
const scalePrewarmBudget = 300 * time.Millisecond

// prewarmConnections fires off concurrent pings to populate the idle pool.
// Initial Download path: wait budget is DialTimeout; total pings = numRequired+hedgeCount.
func (d *ConcurrentDownloader) prewarmConnections(ctx context.Context, client *http.Client, numRequired, hedgeCount int, mirrors []string) {
	_ = d.prewarmConnectionsWithBudget(ctx, client, numRequired, numRequired+hedgeCount, mirrors, types.DialTimeout)
}

// FORK-PATCH: ScaleUp bounded prewarm — count capped at 128, no DialHedge, caller-supplied budget.
func (d *ConcurrentDownloader) prewarmConnectionsBounded(ctx context.Context, client *http.Client, count int, mirrors []string, budget time.Duration) int {
	if count <= 0 {
		return 0
	}
	if count > 128 {
		count = 128
	}
	return d.prewarmConnectionsWithBudget(ctx, client, count, count, mirrors, budget)
}

// prewarmConnectionsWithBudget starts totalToStart Range 0-0 pings and waits until
// numRequired are ready, budget elapses, or ctx is done. Leftover pings are cancelled.
func (d *ConcurrentDownloader) prewarmConnectionsWithBudget(ctx context.Context, client *http.Client, numRequired, totalToStart int, mirrors []string, budget time.Duration) int {
	if totalToStart > 128 { // Safety cap
		totalToStart = 128
	}
	if totalToStart <= 0 || numRequired <= 0 {
		return 0
	}
	if numRequired > totalToStart {
		numRequired = totalToStart
	}

	// Channel to signal when a connection is ready (handshake complete)
	ready := make(chan struct{}, totalToStart)

	// Create a sub-context for the pings so we can stop them once we have enough
	pingCtx, cancelPings := context.WithCancel(ctx)
	defer cancelPings()

	for i := 0; i < totalToStart; i++ {
		go func(idx int) {
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

			if req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", d.Runtime.GetUserAgent())
			}
			req.Header.Set("Range", "bytes=0-0")

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

	completed := 0
	timeout := time.After(budget)

	for completed < numRequired {
		select {
		case <-ready:
			completed++
		case <-timeout:
			utils.Debug("Pre-warming timed out after %d/%d connections", completed, numRequired)
			return completed
		case <-ctx.Done():
			return completed
		}
	}

	utils.Debug("Pre-warming complete: %d connections hot", completed)
	return completed
}
