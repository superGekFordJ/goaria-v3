package progress

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// CfgProgress returns the *DownloadProgress associated with cfg, or
// nil if cfg.ProgressState is nil. This safely narrows the untyped State field.
func CfgProgress(cfg *types.DownloadRecord) *DownloadProgress {
	if cfg == nil || cfg.ProgressState == nil {
		return nil
	}
	dp, _ := cfg.ProgressState.(*DownloadProgress)
	return dp
}

// DownloadProgress is the facade that coordinates all trackers.
type DownloadProgress struct {
	ID string

	Bytes   ByteTracker
	Session SessionTimer
	Bitmap  BitmapTracker

	ActiveWorkers atomic.Int32
	Done          atomic.Bool
	Paused        atomic.Bool
	Pausing       atomic.Bool // Intermediate state: Pause requested but workers not yet exited
	RateLimited   atomic.Bool // Set when the downloader is backing off due to HTTP 429/rate-limit
	Error         atomic.Pointer[error]

	mu         sync.Mutex // Protects metadata only (Mirrors, limits, strings)
	cancelFunc context.CancelFunc

	destPath     string
	filename     string
	url          string
	mirrors      []types.MirrorStatus
	rateLimit    int64
	rateLimitSet bool

	// FORK-PATCH: Per-worker telemetry storage
	workerStatsMu sync.RWMutex
	workerStats   []types.WorkerSnapshot

	// FORK-PATCH: ScaleWorkers function pointer bridge
	scaleWorkersMu sync.Mutex
	scaleWorkersFn func(int) int

	// FORK-PATCH: per-worker Kill + slow-threshold override bridges
	killWorkerMu       sync.Mutex
	killWorkerFn       func(int) bool
	setSlowThresholdMu sync.Mutex
	setSlowThresholdFn func(float64)

	// FORK-PATCH: per-worker Drain bridge
	drainWorkerMu sync.Mutex
	drainWorkerFn func(int) bool

	// FORK-PATCH: Server connection error counter for N_max fuse
	// Deprecated: This is a dead-end counter, N_max fuse relies on genericAttempt
	// (reflected in WorkerSnapshot.RetryCount). Kept for fork-patch compatibility;
	// do not extend or build new logic on top of it.
	consecutiveConnErrors atomic.Int32
}

func New(id string, totalSize int64) *DownloadProgress {
	dp := &DownloadProgress{
		ID: id,
	}
	dp.Bytes.SetTotalSize(totalSize)
	// Initialize session start
	dp.Session.SyncSessionStart(0)
	return dp
}

func (ps *DownloadProgress) SetDestPath(path string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.destPath = path
}

func (ps *DownloadProgress) GetDestPath() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.destPath
}

func (ps *DownloadProgress) SetFilename(filename string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.filename = filename
}

func (ps *DownloadProgress) GetFilename() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.filename
}

func (ps *DownloadProgress) SetURL(url string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.url = url
}

func (ps *DownloadProgress) GetURL() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.url
}

func (ps *DownloadProgress) SetRateLimit(rate int64, explicit bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.rateLimit = rate
	ps.rateLimitSet = explicit
}

func (ps *DownloadProgress) GetRateLimit() (int64, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.rateLimit, ps.rateLimitSet
}

func (ps *DownloadProgress) SetTotalSize(size int64) {
	if ps.Bytes.TotalSize.Load() == size && !ps.Session.StartTime().IsZero() {
		return
	}
	ps.Bytes.SetTotalSize(size)
	ps.Session.SyncSessionStart(ps.Bytes.VerifiedProgress.Load())
}

func (ps *DownloadProgress) SyncSessionStart() {
	ps.Session.SyncSessionStart(ps.Bytes.VerifiedProgress.Load())
}

func (ps *DownloadProgress) SetError(err error) {
	ps.Error.Store(&err)
}

func (ps *DownloadProgress) GetError() error {
	if e := ps.Error.Load(); e != nil {
		return *e
	}
	return nil
}

func (ps *DownloadProgress) GetProgress() (downloaded int64, total int64, totalElapsed time.Duration, sessionElapsed time.Duration, connections int32, sessionStartBytes int64) {
	downloaded = ps.Bytes.VerifiedProgress.Load()
	total = ps.Bytes.TotalSize.Load()
	connections = ps.ActiveWorkers.Load()
	paused := ps.Paused.Load()

	sessionElapsed, totalElapsed, sessionStartBytes = ps.Session.GetElapsed(paused)

	// FORK-PATCH: Safety net — clamp downloaded to TotalSize to prevent
	// impossible progress values (>100%) from reaching the UI/speedstats.
	// Only clamp when TotalSize is known (>0); unknown-size downloads
	// (TotalSize==0) must not be clamped.
	if total > 0 && downloaded > total {
		utils.Debug("GetProgress clamp: VP=%d > Total=%d for %s", downloaded, total, ps.ID)
		downloaded = total
	}

	return
}

func (ps *DownloadProgress) Pause() {
	ps.Paused.Store(true)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.cancelFunc != nil {
		ps.cancelFunc()
	}
}

func (ps *DownloadProgress) SetCancelFunc(cancel context.CancelFunc) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.cancelFunc = cancel
}

func (ps *DownloadProgress) Resume() {
	ps.Paused.Store(false)
}

func (ps *DownloadProgress) IsPaused() bool {
	return ps.Paused.Load()
}

func (ps *DownloadProgress) SetPausing(pausing bool) {
	ps.Pausing.Store(pausing)
}

func (ps *DownloadProgress) IsPausing() bool {
	return ps.Pausing.Load()
}

func (ps *DownloadProgress) SetSavedElapsed(d time.Duration) {
	ps.Session.SetSavedElapsed(d)
}

func (ps *DownloadProgress) GetSavedElapsed() time.Duration {
	return ps.Session.GetSavedElapsed()
}

func (ps *DownloadProgress) FinalizeSession(downloaded int64) (time.Duration, time.Duration) {
	if downloaded < 0 {
		downloaded = ps.Bytes.VerifiedProgress.Load()
	}

	sessionElapsed, totalElapsed := ps.Session.FinalizeSession(downloaded)

	ps.Bytes.Downloaded.Store(downloaded)
	ps.Bytes.VerifiedProgress.Store(downloaded)

	return sessionElapsed, totalElapsed
}

func (ps *DownloadProgress) SessionReset() {
	ps.Bytes.Downloaded.Store(0)
	ps.Bytes.VerifiedProgress.Store(0)
	ps.ActiveWorkers.Store(0)
	ps.Done.Store(false)
	ps.Paused.Store(false)
	ps.Pausing.Store(false)
	ps.RateLimited.Store(false)
	ps.Error.Store(nil)

	ps.Session.SessionReset()
	ps.Bitmap.Reset()

	ps.mu.Lock()
	defer ps.mu.Unlock()
	for i := range ps.mirrors {
		ps.mirrors[i].Error = false
	}

	// FORK-PATCH: Clear per-worker telemetry on session reset
	ps.workerStatsMu.Lock()
	ps.workerStats = nil
	ps.workerStatsMu.Unlock()

	// FORK-PATCH: Clear ScaleWorkers function pointer
	ps.scaleWorkersMu.Lock()
	ps.scaleWorkersFn = nil
	ps.scaleWorkersMu.Unlock()

	// FORK-PATCH: Clear per-worker kill + slow-threshold bridges
	ps.killWorkerMu.Lock()
	ps.killWorkerFn = nil
	ps.killWorkerMu.Unlock()
	ps.setSlowThresholdMu.Lock()
	ps.setSlowThresholdFn = nil
	ps.setSlowThresholdMu.Unlock()

	// FORK-PATCH: Clear per-worker drain bridge
	ps.drainWorkerMu.Lock()
	ps.drainWorkerFn = nil
	ps.drainWorkerMu.Unlock()

	// FORK-PATCH: Reset connection error counter
	ps.consecutiveConnErrors.Store(0)
}

func (ps *DownloadProgress) FinalizePauseSession(downloaded int64) time.Duration {
	_, total := ps.FinalizeSession(downloaded)
	return total
}

func (ps *DownloadProgress) SetMirrors(mirrors []types.MirrorStatus) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.mirrors = make([]types.MirrorStatus, len(mirrors))
	copy(ps.mirrors, mirrors)
}

func (ps *DownloadProgress) GetMirrors() []types.MirrorStatus {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.mirrors) == 0 {
		return nil
	}
	mirrors := make([]types.MirrorStatus, len(ps.mirrors))
	copy(mirrors, ps.mirrors)
	return mirrors
}

func (ps *DownloadProgress) InitBitmap(totalSize int64, chunkSize int64) {
	ps.Bitmap.InitBitmap(totalSize, chunkSize)
}

func (ps *DownloadProgress) RestoreBitmap(bitmap []byte, actualChunkSize int64) {
	ps.Bitmap.RestoreBitmap(ps.Bytes.TotalSize.Load(), bitmap, actualChunkSize)
}

func (ps *DownloadProgress) SetChunkProgress(progress []int64) {
	ps.Bitmap.SetChunkProgress(progress)
}

func (ps *DownloadProgress) SetChunkState(index int, status types.ChunkStatus) {
	ps.Bitmap.SetChunkState(index, status)
}

func (ps *DownloadProgress) GetChunkState(index int) types.ChunkStatus {
	return ps.Bitmap.GetChunkState(index)
}

func (ps *DownloadProgress) UpdateChunkStatus(offset, length int64, status types.ChunkStatus) {
	increment := ps.Bitmap.UpdateChunkStatus(ps.Bytes.TotalSize.Load(), offset, length, status)
	if increment > 0 {
		ps.Bytes.VerifiedProgress.Add(increment)
	}
}

func (ps *DownloadProgress) RecalculateProgress(remainingTasks []types.Task) {
	totalVerified := ps.Bitmap.RecalculateProgress(ps.Bytes.TotalSize.Load(), remainingTasks)
	ps.Bytes.VerifiedProgress.Store(totalVerified)
}

func (ps *DownloadProgress) GetBitmap() ([]byte, int, int64, int64, []int64) {
	return ps.GetBitmapSnapshot(true)
}

func (ps *DownloadProgress) GetBitmapSnapshot(includeProgress bool) ([]byte, int, int64, int64, []int64) {
	return ps.Bitmap.GetBitmapSnapshot(ps.Bytes.TotalSize.Load(), includeProgress)
}

// FORK-PATCH: SetWorkerStats stores per-worker telemetry snapshots
func (ps *DownloadProgress) SetWorkerStats(stats []types.WorkerSnapshot) {
	ps.workerStatsMu.Lock()
	defer ps.workerStatsMu.Unlock()
	ps.workerStats = stats
}

// FORK-PATCH: GetWorkerStats returns a copy of per-worker telemetry snapshots
func (ps *DownloadProgress) GetWorkerStats() []types.WorkerSnapshot {
	ps.workerStatsMu.RLock()
	defer ps.workerStatsMu.RUnlock()
	if len(ps.workerStats) == 0 {
		return nil
	}
	result := make([]types.WorkerSnapshot, len(ps.workerStats))
	copy(result, ps.workerStats)
	return result
}

// FORK-PATCH: SetScaleWorkersFn registers the ScaleWorkers method value from
// strategy/concurrent, enabling WorkerPool to call ScaleWorkers via DownloadProgress.
func (ps *DownloadProgress) SetScaleWorkersFn(fn func(int) int) {
	ps.scaleWorkersMu.Lock()
	defer ps.scaleWorkersMu.Unlock()
	ps.scaleWorkersFn = fn
}

// FORK-PATCH: ScaleWorkers calls the registered function pointer to adjust
// the worker count. Returns 0 if no function is registered.
func (ps *DownloadProgress) ScaleWorkers(delta int) int {
	ps.scaleWorkersMu.Lock()
	fn := ps.scaleWorkersFn
	ps.scaleWorkersMu.Unlock()
	if fn == nil {
		return 0
	}
	return fn(delta)
}

// FORK-PATCH: SetKillWorkerFn registers the per-worker Kill bridge.
func (ps *DownloadProgress) SetKillWorkerFn(fn func(int) bool) {
	ps.killWorkerMu.Lock()
	defer ps.killWorkerMu.Unlock()
	ps.killWorkerFn = fn
}

// FORK-PATCH: KillWorker hard-interrupts a specific worker. Returns false if no
// function is registered or the worker has no active task.
func (ps *DownloadProgress) KillWorker(workerID int) bool {
	ps.killWorkerMu.Lock()
	fn := ps.killWorkerFn
	ps.killWorkerMu.Unlock()
	if fn == nil {
		return false
	}
	return fn(workerID)
}

// FORK-PATCH: SetSetSlowThresholdFn registers the slow-threshold override bridge.
func (ps *DownloadProgress) SetSetSlowThresholdFn(fn func(float64)) {
	ps.setSlowThresholdMu.Lock()
	defer ps.setSlowThresholdMu.Unlock()
	ps.setSlowThresholdFn = fn
}

// FORK-PATCH: SetSlowWorkerThreshold applies a runtime threshold override.
func (ps *DownloadProgress) SetSlowWorkerThreshold(v float64) {
	ps.setSlowThresholdMu.Lock()
	fn := ps.setSlowThresholdFn
	ps.setSlowThresholdMu.Unlock()
	if fn != nil {
		fn(v)
	}
}

// FORK-PATCH: SetDrainWorkerFn registers the per-worker Drain bridge.
func (ps *DownloadProgress) SetDrainWorkerFn(fn func(int) bool) {
	ps.drainWorkerMu.Lock()
	defer ps.drainWorkerMu.Unlock()
	ps.drainWorkerFn = fn
}

// FORK-PATCH: DrainWorker marks a specific worker as draining. Returns false if
// no function is registered. The worker finishes its current chunk and exits,
// preserving the TCP connection in the Transport idle pool.
func (ps *DownloadProgress) DrainWorker(workerID int) bool {
	ps.drainWorkerMu.Lock()
	fn := ps.drainWorkerFn
	ps.drainWorkerMu.Unlock()
	if fn == nil {
		return false
	}
	return fn(workerID)
}

// FORK-PATCH: IncrConnErrors increments the server connection error counter.
// Deprecated: N_max fuse relies on genericAttempt (WorkerSnapshot.RetryCount).
func (ps *DownloadProgress) IncrConnErrors() {
	ps.consecutiveConnErrors.Add(1)
}

// FORK-PATCH: DecrConnErrors decrements the server connection error counter (floor 0).
// Deprecated: N_max fuse relies on genericAttempt (WorkerSnapshot.RetryCount).
func (ps *DownloadProgress) DecrConnErrors() {
	for {
		val := ps.consecutiveConnErrors.Load()
		if val <= 0 {
			return
		}
		if ps.consecutiveConnErrors.CompareAndSwap(val, val-1) {
			return
		}
	}
}

// FORK-PATCH: GetConnErrors returns the current server connection error count.
// Deprecated: N_max fuse relies on genericAttempt (WorkerSnapshot.RetryCount).
func (ps *DownloadProgress) GetConnErrors() int32 {
	return ps.consecutiveConnErrors.Load()
}
