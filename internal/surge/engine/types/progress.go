package types

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/utils"
)

type ProgressState struct {
	ID            string
	Downloaded    atomic.Int64
	TotalSize     int64
	DestPath      string // Initial destination path
	Filename      string // Initial filename
	URL           string // Source URL
	StartTime     time.Time
	ActiveWorkers atomic.Int32
	Done          atomic.Bool
	Error         atomic.Pointer[error]
	Paused        atomic.Bool
	Pausing       atomic.Bool // Intermediate state: Pause requested but workers not yet exited
	cancelFunc    context.CancelFunc

	VerifiedProgress  atomic.Int64  // Verified bytes written to disk (for UI progress)
	SessionStartBytes int64         // SessionStartBytes tracks how many bytes were already downloaded when the current session started
	SavedElapsed      time.Duration // Time spent in previous sessions
	RateLimitBps      int64         // Effective per-download rate limit in bytes/sec
	RateLimitSet      bool          // Whether RateLimitBps is an explicit per-download override

	Mirrors []MirrorStatus

	ChunkBitmap     []byte
	ChunkProgress   []int64
	ActualChunkSize int64
	BitmapWidth     int

	mu sync.Mutex // Protects TotalSize, StartTime, SessionStartBytes, SavedElapsed, Mirrors

	// FORK-PATCH: Per-worker telemetry storage
	workerStatsMu sync.RWMutex
	workerStats   []WorkerSnapshot

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
	consecutiveConnErrors atomic.Int32
}

type MirrorStatus struct {
	URL    string
	Active bool
	Error  bool
}

func (ps *ProgressState) SetDestPath(path string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.DestPath = path
}

func (ps *ProgressState) GetDestPath() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.DestPath
}

func (ps *ProgressState) SetFilename(filename string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.Filename = filename
}

func (ps *ProgressState) GetFilename() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.Filename
}

func (ps *ProgressState) SetURL(url string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.URL = url
}

func (ps *ProgressState) GetURL() string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.URL
}

func (ps *ProgressState) SetRateLimit(rate int64, explicit bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.RateLimitBps = rate
	ps.RateLimitSet = explicit
}

func (ps *ProgressState) GetRateLimit() (int64, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.RateLimitBps, ps.RateLimitSet
}

func NewProgressState(id string, totalSize int64) *ProgressState {
	return &ProgressState{
		ID:        id,
		TotalSize: totalSize,
		StartTime: time.Now(),
	}
}

func (ps *ProgressState) SetTotalSize(size int64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	// If size is already set and timer is running, don't reset the clock.
	// This prevents post-download updates from erasing the session duration.
	if ps.TotalSize == size && !ps.StartTime.IsZero() {
		return
	}

	ps.TotalSize = size
	ps.SessionStartBytes = ps.VerifiedProgress.Load()
	ps.StartTime = time.Now()
}

func (ps *ProgressState) SyncSessionStart() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.SessionStartBytes = ps.VerifiedProgress.Load()
	ps.StartTime = time.Now()
}

func (ps *ProgressState) SetError(err error) {
	ps.Error.Store(&err)
}

func (ps *ProgressState) GetError() error {
	if e := ps.Error.Load(); e != nil {
		return *e
	}
	return nil
}

func (ps *ProgressState) GetProgress() (downloaded int64, total int64, totalElapsed time.Duration, sessionElapsed time.Duration, connections int32, sessionStartBytes int64) {
	downloaded = ps.VerifiedProgress.Load()
	connections = ps.ActiveWorkers.Load()
	paused := ps.Paused.Load()

	ps.mu.Lock()
	total = ps.TotalSize
	savedElapsed := ps.SavedElapsed
	startTime := ps.StartTime
	sessionStartBytes = ps.SessionStartBytes
	ps.mu.Unlock()

	// Elapsed time excludes paused duration.
	if paused {
		sessionElapsed = 0
		totalElapsed = savedElapsed
	} else {
		sessionElapsed = time.Since(startTime)
		if sessionElapsed < 0 {
			sessionElapsed = 0
		}
		totalElapsed = savedElapsed + sessionElapsed
	}
	if totalElapsed < 0 {
		totalElapsed = 0
	}

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

func (ps *ProgressState) Pause() {
	ps.Paused.Store(true)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.cancelFunc != nil {
		ps.cancelFunc()
	}
}

func (ps *ProgressState) SetCancelFunc(cancel context.CancelFunc) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.cancelFunc = cancel
}

func (ps *ProgressState) Resume() {
	ps.Paused.Store(false)
}

func (ps *ProgressState) IsPaused() bool {
	return ps.Paused.Load()
}

func (ps *ProgressState) SetPausing(pausing bool) {
	ps.Pausing.Store(pausing)
}

func (ps *ProgressState) IsPausing() bool {
	return ps.Pausing.Load()
}

func (ps *ProgressState) SetSavedElapsed(d time.Duration) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.SavedElapsed = d
}

func (ps *ProgressState) GetSavedElapsed() time.Duration {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.SavedElapsed
}

// FinalizeSession closes the current session and accumulates its elapsed time into total elapsed.
// It returns (sessionElapsed, totalElapsedAfterFinalize).
func (ps *ProgressState) FinalizeSession(downloaded int64) (time.Duration, time.Duration) {
	if downloaded < 0 {
		downloaded = ps.VerifiedProgress.Load()
	}

	now := time.Now()
	ps.mu.Lock()
	sessionElapsed := now.Sub(ps.StartTime)
	if sessionElapsed < 0 {
		sessionElapsed = 0
	}
	ps.SavedElapsed += sessionElapsed
	if ps.SavedElapsed < 0 {
		ps.SavedElapsed = 0
	}
	ps.SessionStartBytes = downloaded
	ps.StartTime = now
	totalElapsed := ps.SavedElapsed
	ps.mu.Unlock()

	ps.Downloaded.Store(downloaded)
	ps.VerifiedProgress.Store(downloaded)

	return sessionElapsed, totalElapsed
}

// SessionReset wipes the current progress and session state, allowing for a fresh start (e.g. fallback).
func (ps *ProgressState) SessionReset() {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.Downloaded.Store(0)
	ps.VerifiedProgress.Store(0)
	ps.SessionStartBytes = 0
	ps.StartTime = time.Now()
	ps.SavedElapsed = 0
	ps.Done.Store(false)
	ps.Paused.Store(false)
	ps.Pausing.Store(false)
	ps.Error.Store(nil)

	// Clear mirrors error status
	for i := range ps.Mirrors {
		ps.Mirrors[i].Error = false
	}

	// Reset chunk tracking if initialized
	if ps.BitmapWidth > 0 {
		ps.ChunkBitmap = make([]byte, len(ps.ChunkBitmap))
		ps.ChunkProgress = make([]int64, ps.BitmapWidth)
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

// FinalizePauseSession finalizes the current session for a pause transition.
// It keeps timing/data frozen while paused and returns total elapsed after finalize.
func (ps *ProgressState) FinalizePauseSession(downloaded int64) time.Duration {
	_, total := ps.FinalizeSession(downloaded)
	return total
}

func (ps *ProgressState) SetMirrors(mirrors []MirrorStatus) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	// Deep copy to prevent race conditions if caller modifies the slice
	ps.Mirrors = make([]MirrorStatus, len(mirrors))
	copy(ps.Mirrors, mirrors)
}

// FORK-PATCH: SetWorkerStats stores per-worker telemetry snapshots
func (ps *ProgressState) SetWorkerStats(stats []WorkerSnapshot) {
	ps.workerStatsMu.Lock()
	defer ps.workerStatsMu.Unlock()
	ps.workerStats = stats
}

// FORK-PATCH: GetWorkerStats returns a copy of per-worker telemetry snapshots
func (ps *ProgressState) GetWorkerStats() []WorkerSnapshot {
	ps.workerStatsMu.RLock()
	defer ps.workerStatsMu.RUnlock()
	if len(ps.workerStats) == 0 {
		return nil
	}
	result := make([]WorkerSnapshot, len(ps.workerStats))
	copy(result, ps.workerStats)
	return result
}

// FORK-PATCH: SetScaleWorkersFn registers the ScaleWorkers method value from
// ConcurrentDownloader, enabling WorkerPool to call ScaleWorkers via ProgressState.
func (ps *ProgressState) SetScaleWorkersFn(fn func(int) int) {
	ps.scaleWorkersMu.Lock()
	defer ps.scaleWorkersMu.Unlock()
	ps.scaleWorkersFn = fn
}

// FORK-PATCH: ScaleWorkers calls the registered function pointer to adjust
// the worker count. Returns 0 if no function is registered.
func (ps *ProgressState) ScaleWorkers(delta int) int {
	ps.scaleWorkersMu.Lock()
	fn := ps.scaleWorkersFn
	ps.scaleWorkersMu.Unlock()
	if fn == nil {
		return 0
	}
	return fn(delta)
}

// FORK-PATCH: SetKillWorkerFn registers the per-worker Kill bridge.
func (ps *ProgressState) SetKillWorkerFn(fn func(int) bool) {
	ps.killWorkerMu.Lock()
	defer ps.killWorkerMu.Unlock()
	ps.killWorkerFn = fn
}

// FORK-PATCH: KillWorker hard-interrupts a specific worker. Returns false if no
// function is registered or the worker has no active task.
func (ps *ProgressState) KillWorker(workerID int) bool {
	ps.killWorkerMu.Lock()
	fn := ps.killWorkerFn
	ps.killWorkerMu.Unlock()
	if fn == nil {
		return false
	}
	return fn(workerID)
}

// FORK-PATCH: SetSetSlowThresholdFn registers the slow-threshold override bridge.
func (ps *ProgressState) SetSetSlowThresholdFn(fn func(float64)) {
	ps.setSlowThresholdMu.Lock()
	defer ps.setSlowThresholdMu.Unlock()
	ps.setSlowThresholdFn = fn
}

// FORK-PATCH: SetSlowWorkerThreshold applies a runtime threshold override.
func (ps *ProgressState) SetSlowWorkerThreshold(v float64) {
	ps.setSlowThresholdMu.Lock()
	fn := ps.setSlowThresholdFn
	ps.setSlowThresholdMu.Unlock()
	if fn != nil {
		fn(v)
	}
}

// FORK-PATCH: SetDrainWorkerFn registers the per-worker Drain bridge.
func (ps *ProgressState) SetDrainWorkerFn(fn func(int) bool) {
	ps.drainWorkerMu.Lock()
	defer ps.drainWorkerMu.Unlock()
	ps.drainWorkerFn = fn
}

// FORK-PATCH: DrainWorker marks a specific worker as draining. Returns false if
// no function is registered. The worker finishes its current chunk and exits,
// preserving the TCP connection in the Transport idle pool.
func (ps *ProgressState) DrainWorker(workerID int) bool {
	ps.drainWorkerMu.Lock()
	fn := ps.drainWorkerFn
	ps.drainWorkerMu.Unlock()
	if fn == nil {
		return false
	}
	return fn(workerID)
}

// FORK-PATCH: IncrConnErrors increments the server connection error counter.
func (ps *ProgressState) IncrConnErrors() {
	ps.consecutiveConnErrors.Add(1)
}

// FORK-PATCH: DecrConnErrors decrements the server connection error counter (floor 0).
func (ps *ProgressState) DecrConnErrors() {
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
func (ps *ProgressState) GetConnErrors() int32 {
	return ps.consecutiveConnErrors.Load()
}

func (ps *ProgressState) GetMirrors() []MirrorStatus {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	// Return a copy
	if len(ps.Mirrors) == 0 {
		return nil
	}
	mirrors := make([]MirrorStatus, len(ps.Mirrors))
	copy(mirrors, ps.Mirrors)
	return mirrors
}

// ChunkStatus represents the status of a visualization chunk
type ChunkStatus int

const (
	ChunkPending     ChunkStatus = 0 // 00
	ChunkDownloading ChunkStatus = 1 // 01
	ChunkCompleted   ChunkStatus = 2 // 10 (Bit 2 set)
)

// bitmapLayout returns the number of tracked chunks and backing bytes for a
// 2-bit-per-chunk bitmap.
func bitmapLayout(totalSize, chunkSize int64) (numChunks int, bytesNeeded int, ok bool) {
	if totalSize <= 0 || chunkSize <= 0 {
		return 0, 0, false
	}

	numChunks = int((totalSize + chunkSize - 1) / chunkSize)
	if numChunks <= 0 {
		return 0, 0, false
	}

	bytesNeeded = (numChunks + 3) / 4
	return numChunks, bytesNeeded, true
}

// InitBitmap initializes the chunk bitmap
func (ps *ProgressState) InitBitmap(totalSize int64, chunkSize int64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(ps.ChunkBitmap) > 0 && ps.TotalSize == totalSize && ps.ActualChunkSize == chunkSize {
		return
	}

	utils.Debug("InitBitmap: Total=%d, ChunkSize=%d", totalSize, chunkSize)

	if chunkSize <= 0 {
		return
	}

	numChunks, bytesNeeded, ok := bitmapLayout(totalSize, chunkSize)
	if !ok {
		return
	}

	ps.ActualChunkSize = chunkSize
	ps.BitmapWidth = numChunks
	ps.ChunkBitmap = make([]byte, bytesNeeded)
	ps.ChunkProgress = make([]int64, numChunks)
}

// RestoreBitmap restores the chunk bitmap from saved state
func (ps *ProgressState) RestoreBitmap(bitmap []byte, actualChunkSize int64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(bitmap) == 0 || actualChunkSize <= 0 || ps.TotalSize <= 0 {
		return
	}

	numChunks, bytesNeeded, ok := bitmapLayout(ps.TotalSize, actualChunkSize)
	if !ok {
		return
	}

	// Deep copy to prevent mutation hazard of caller's backing array
	ps.ChunkBitmap = make([]byte, bytesNeeded)
	copy(ps.ChunkBitmap, bitmap)
	ps.ActualChunkSize = actualChunkSize
	ps.BitmapWidth = numChunks

	if len(ps.ChunkProgress) != numChunks {
		ps.ChunkProgress = make([]int64, numChunks)
	}
}

// SetChunkProgress updates chunk progress array from external sources (e.g. remote events).
func (ps *ProgressState) SetChunkProgress(progress []int64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(progress) == 0 {
		return
	}
	if len(ps.ChunkProgress) != len(progress) {
		ps.ChunkProgress = make([]int64, len(progress))
	}
	copy(ps.ChunkProgress, progress)
}

// SetChunkState sets the 2-bit state for a specific chunk index (thread-safe)
func (ps *ProgressState) SetChunkState(index int, status ChunkStatus) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.setChunkState(index, status)
}

// setChunkState sets the 2-bit state (internal, expects lock)
func (ps *ProgressState) setChunkState(index int, status ChunkStatus) {
	if index < 0 || index >= ps.BitmapWidth {
		return
	}

	byteIndex := index / 4
	if byteIndex >= len(ps.ChunkBitmap) {
		return
	}
	bitOffset := (index % 4) * 2

	mask := byte(3 << bitOffset)
	ps.ChunkBitmap[byteIndex] &= ^mask

	val := byte(status) << bitOffset
	ps.ChunkBitmap[byteIndex] |= val
}

// GetChunkState gets the 2-bit state for a specific chunk index (thread-safe)
func (ps *ProgressState) GetChunkState(index int) ChunkStatus {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.getChunkState(index)
}

// getChunkState gets the 2-bit state (internal, expects lock)
func (ps *ProgressState) getChunkState(index int) ChunkStatus {
	if index < 0 || index >= ps.BitmapWidth {
		return ChunkPending
	}

	byteIndex := index / 4
	if byteIndex >= len(ps.ChunkBitmap) {
		return ChunkPending
	}
	bitOffset := (index % 4) * 2

	val := (ps.ChunkBitmap[byteIndex] >> bitOffset) & 3
	return ChunkStatus(val)
}

// UpdateChunkStatus updates the bitmap based on byte range
func (ps *ProgressState) UpdateChunkStatus(offset, length int64, status ChunkStatus) {
	ps.mu.Lock()

	if ps.ActualChunkSize == 0 || len(ps.ChunkBitmap) == 0 {
		utils.Debug("UpdateChunkStatus skipped: ActualChunkSize=%d, BitmapLen=%d", ps.ActualChunkSize, len(ps.ChunkBitmap))
		ps.mu.Unlock()
		return
	}

	if len(ps.ChunkProgress) != ps.BitmapWidth {
		utils.Debug("UpdateChunkStatus: Initializing ChunkProgress array (width=%d)", ps.BitmapWidth)
		ps.ChunkProgress = make([]int64, ps.BitmapWidth)
	}

	startIdx := int(offset / ps.ActualChunkSize)
	endIdx := int((offset + length - 1) / ps.ActualChunkSize)

	if startIdx < 0 {
		startIdx = 0
	}
	if endIdx >= ps.BitmapWidth {
		endIdx = ps.BitmapWidth - 1
	}

	var totalIncrement int64

	for i := startIdx; i <= endIdx; i++ {
		// Calculate precise overlap with this chunk
		chunkStart := int64(i) * ps.ActualChunkSize
		chunkEnd := chunkStart + ps.ActualChunkSize
		if chunkEnd > ps.TotalSize {
			chunkEnd = ps.TotalSize
		}

		updateStart := offset
		if updateStart < chunkStart {
			updateStart = chunkStart
		}

		updateEnd := offset + length
		if updateEnd > chunkEnd {
			updateEnd = chunkEnd
		}

		overlap := updateEnd - updateStart
		if overlap < 0 {
			overlap = 0
		}

		switch status {
		case ChunkCompleted:
			increment := overlap
			remainingSpace := (chunkEnd - chunkStart) - ps.ChunkProgress[i]

			if increment > remainingSpace {
				increment = remainingSpace
			}

			if increment > 0 {
				ps.ChunkProgress[i] += increment
				totalIncrement += increment
			}

			if ps.ChunkProgress[i] >= (chunkEnd - chunkStart) {
				ps.ChunkProgress[i] = chunkEnd - chunkStart
				ps.setChunkState(i, ChunkCompleted)
			} else {
				if ps.getChunkState(i) != ChunkCompleted {
					ps.setChunkState(i, ChunkDownloading)
				}
			}
		case ChunkDownloading:
			current := ps.getChunkState(i)
			if current != ChunkCompleted {
				ps.setChunkState(i, ChunkDownloading)
			}
		}
	}

	ps.mu.Unlock()

	if totalIncrement > 0 {
		ps.VerifiedProgress.Add(totalIncrement)
	}
}

// RecalculateProgress reconstructs ChunkProgress from remaining tasks (for resume)
func (ps *ProgressState) RecalculateProgress(remainingTasks []Task) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.ActualChunkSize == 0 || ps.BitmapWidth == 0 {
		return
	}

	ps.ChunkProgress = make([]int64, ps.BitmapWidth)
	var totalVerified int64
	for i := 0; i < ps.BitmapWidth; i++ {
		chunkStart := int64(i) * ps.ActualChunkSize
		chunkEnd := chunkStart + ps.ActualChunkSize
		if chunkEnd > ps.TotalSize {
			chunkEnd = ps.TotalSize
		}
		ps.ChunkProgress[i] = chunkEnd - chunkStart
		totalVerified += ps.ChunkProgress[i]
	}

	for _, task := range remainingTasks {
		offset := task.Offset
		length := task.Length

		startIdx := int(offset / ps.ActualChunkSize)
		endIdx := int((offset + length - 1) / ps.ActualChunkSize)

		if startIdx < 0 {
			startIdx = 0
		}
		if endIdx >= ps.BitmapWidth {
			endIdx = ps.BitmapWidth - 1
		}

		for i := startIdx; i <= endIdx; i++ {
			chunkStart := int64(i) * ps.ActualChunkSize
			chunkEnd := chunkStart + ps.ActualChunkSize
			if chunkEnd > ps.TotalSize {
				chunkEnd = ps.TotalSize
			}

			taskStart := offset
			if taskStart < chunkStart {
				taskStart = chunkStart
			}

			taskEnd := offset + length
			if taskEnd > chunkEnd {
				taskEnd = chunkEnd
			}

			overlap := taskEnd - taskStart
			if overlap > 0 {
				ps.ChunkProgress[i] -= overlap
				totalVerified -= overlap
			}
		}
	}

	// FORK-PATCH: Trust the restored bitmap — chunks marked ChunkCompleted
	// have their bytes fully on disk even if remainingTasks still cover them
	// (hedged bytes re-queued by KillWorker). Restore ChunkProgress to full
	// and add back to totalVerified so VP stays in sync with sum(ChunkProgress).
	for i := 0; i < ps.BitmapWidth; i++ {
		if ps.getChunkState(i) == ChunkCompleted {
			chunkStart := int64(i) * ps.ActualChunkSize
			chunkEnd := chunkStart + ps.ActualChunkSize
			if chunkEnd > ps.TotalSize {
				chunkEnd = ps.TotalSize
			}
			chunkSize := chunkEnd - chunkStart
			if ps.ChunkProgress[i] < chunkSize {
				totalVerified += chunkSize - ps.ChunkProgress[i]
				ps.ChunkProgress[i] = chunkSize
			}
		}
	}

	ps.VerifiedProgress.Store(totalVerified)

	for i := 0; i < ps.BitmapWidth; i++ {
		chunkStart := int64(i) * ps.ActualChunkSize
		chunkEnd := chunkStart + ps.ActualChunkSize
		if chunkEnd > ps.TotalSize {
			chunkEnd = ps.TotalSize
		}
		chunkSize := chunkEnd - chunkStart

		if ps.ChunkProgress[i] >= chunkSize {
			ps.ChunkProgress[i] = chunkSize
			ps.setChunkState(i, ChunkCompleted)
		} else if ps.ChunkProgress[i] > 0 {
			ps.setChunkState(i, ChunkDownloading)
		} else {
			ps.ChunkProgress[i] = 0
			ps.setChunkState(i, ChunkPending)
		}
	}
}

// GetBitmap returns a copy of the bitmap and metadata
func (ps *ProgressState) GetBitmap() ([]byte, int, int64, int64, []int64) {
	return ps.GetBitmapSnapshot(true)
}

// GetBitmapSnapshot returns a copy of bitmap metadata and optionally chunk progress.
func (ps *ProgressState) GetBitmapSnapshot(includeProgress bool) ([]byte, int, int64, int64, []int64) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(ps.ChunkBitmap) == 0 {
		return nil, 0, 0, 0, nil
	}

	result := make([]byte, len(ps.ChunkBitmap))
	copy(result, ps.ChunkBitmap)

	var progressResult []int64
	if includeProgress {
		progressResult = make([]int64, len(ps.ChunkProgress))
		copy(progressResult, ps.ChunkProgress)
	}

	return result, ps.BitmapWidth, ps.TotalSize, ps.ActualChunkSize, progressResult
}
