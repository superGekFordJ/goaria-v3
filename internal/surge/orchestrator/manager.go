package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/surge/config"
	probing "goaria-v3/internal/surge/probe"
	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/scheduler"
	"goaria-v3/internal/surge/store"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"

	"github.com/google/uuid"
)

// IsNameActiveFunc lets routing treat in-flight downloads as filename conflicts within a directory.
type IsNameActiveFunc func(dir, name string) bool

type LifecycleManager struct {
	settings            *config.Settings
	settingsMu          sync.RWMutex
	settingsRefreshedAt time.Time
	pool                *scheduler.Scheduler
	eventBus            *EventBus
	aggregator          *ProgressAggregator
	isNameActive        IsNameActiveFunc

	// FORK-PATCH: host EngineHooks (resume metadata prep; RMW via Get/SetEngineHooks).
	engineHooks EngineHooks
	hooksMu     sync.RWMutex

	// FORK-PATCH: probeSem caps ProbeServerWithProxy GET Range: bytes=0-0 only.
	// Trusted size+filename+explicit Range skip this slot entirely.
	probeSem     chan struct{}
	shutdownOnce sync.Once
}

const (
	maxWorkingFileReservationAttempts = 100
	// defaultMaxConcurrentProbes is the fallback probe concurrency cap used when
	// no settings value is available. The live value comes from
	// NetworkSettings.MaxConcurrentProbes.
	defaultMaxConcurrentProbes = 3
)

var reserveWorkingFile = precreateWorkingFile

// freeDiskBytes is injectable so enqueue precheck tests can simulate tight disks
// without manufacturing a full volume.
var freeDiskBytes = utils.FreeDiskBytes

func precreateWorkingFile(destPath, filename string) error {
	if err := os.MkdirAll(destPath, 0o755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	surgePath := filepath.Join(destPath, filename) + types.IncompleteSuffix
	// Exclusive create turns the .surge file into the reservation itself, so two
	// concurrent enqueues cannot silently target the same working path.
	file, err := os.OpenFile(surgePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to pre-create working file: %w", err)
	}
	_ = file.Close()
	return nil
}

// Falls back to a no-op so enqueue callers can always consult the active-name
// hook safely, even in tests or remote contexts that do not have pool access.
func (mgr *LifecycleManager) buildIsNameActive() func(string, string) bool {
	if mgr.isNameActive != nil {
		return mgr.isNameActive
	}
	return func(string, string) bool { return false }
}

func NewLifecycleManager(pool *scheduler.Scheduler, eventBus *EventBus, settings *config.Settings, isNameActive ...IsNameActiveFunc) *LifecycleManager {
	if settings == nil {
		settings = config.DefaultSettings()
	}

	var activeCheck IsNameActiveFunc
	if len(isNameActive) > 0 {
		activeCheck = isNameActive[0]
	}

	probeCap := defaultMaxConcurrentProbes
	if settings != nil && config.Resolve[int](settings.Network.MaxConcurrentProbes) > 0 {
		probeCap = config.Resolve[int](settings.Network.MaxConcurrentProbes)
	}
	sem := make(chan struct{}, probeCap)
	for i := 0; i < probeCap; i++ {
		sem <- struct{}{}
	}

	var aggregator *ProgressAggregator
	if pool != nil && eventBus != nil {
		aggregator = NewProgressAggregator(pool, eventBus, settings)
	}

	return &LifecycleManager{
		settings:            settings,
		settingsRefreshedAt: time.Now(),
		pool:                pool,
		eventBus:            eventBus,
		aggregator:          aggregator,
		isNameActive:        activeCheck,
		probeSem:            sem,
	}
}

// SetEngineHooks injects host hooks without replacing LifecycleManager's
// direct pool/eventBus control flow.
func (mgr *LifecycleManager) SetEngineHooks(hooks EngineHooks) {
	mgr.hooksMu.Lock()
	mgr.engineHooks = hooks
	mgr.hooksMu.Unlock()
}

func (mgr *LifecycleManager) getEngineHooks() EngineHooks {
	mgr.hooksMu.RLock()
	defer mgr.hooksMu.RUnlock()
	return mgr.engineHooks
}

// GetEngineHooks returns a copy for read-modify-write updates by the host.
func (mgr *LifecycleManager) GetEngineHooks() EngineHooks {
	return mgr.getEngineHooks()
}

// GetScheduler returns the underlying scheduler
func (mgr *LifecycleManager) GetScheduler() *scheduler.Scheduler {
	return mgr.pool
}

// GetEventBus returns the event bus
func (mgr *LifecycleManager) GetEventBus() *EventBus {
	return mgr.eventBus
}

func (mgr *LifecycleManager) Shutdown() {
	mgr.shutdownOnce.Do(func() {
		if mgr.aggregator != nil {
			mgr.aggregator.Shutdown()
		}
		if mgr.pool != nil {
			mgr.pool.GracefulShutdown()
		}
		if mgr.eventBus != nil {
			mgr.eventBus.Shutdown()
		}
	})
}

func (m *LifecycleManager) GetSettings() *config.Settings {
	m.settingsMu.RLock()
	settings := m.settings
	m.settingsMu.RUnlock()

	if settings != nil {
		return settings
	}
	return config.DefaultSettings()
}

// ApplySettings swaps in a new routing snapshot for future enqueue calls.
func (m *LifecycleManager) ApplySettings(s *config.Settings) {
	if s == nil {
		return
	}
	m.settingsMu.Lock()
	m.settings = s
	m.settingsRefreshedAt = time.Now()
	m.settingsMu.Unlock()
}

// DownloadRequest carries the already-approved inputs needed to probe and reserve a file path.
type DownloadRequest struct {
	URL                string
	Filename           string
	Path               string
	Mirrors            []string
	Headers            map[string]string
	IsExplicitCategory bool
	SkipApproval       bool
	Workers            int
	MinChunkSize       int64
	// FORK-PATCH: host-supplied probe hints. Skip ProbeServer only when
	// FileSize > 0, Filename is non-empty after trim, and mode is a skip
	// mode or 286 SupportsRange is non-nil. A nil pointer + empty mode is
	// unknown Range (not optimistic true). *false means payload-first unknown.
	FileSize             int64
	SupportsRange        *bool
	RangeAcquisitionMode types.RangeAcquisitionMode
	SkipServerProbe      bool
	// FORK-PATCH: strategy discriminator + caller-owned HLS resume snapshot.
	// StrategyHLS skips the origin probe entirely (a playlist has no byte
	// range to probe) and carries no FileSize/Range requirements.
	Strategy  types.DownloadStrategy
	HLSResume *types.HLSResumeState
}

// FORK-PATCH: skip ProbeServer iff trusted size, trimmed filename, and skip mode
// (or 286 explicit Range pointer). Does not take probeSem.
func shouldSkipServerProbe(req *DownloadRequest) bool {
	if req == nil {
		return false
	}
	// FORK-PATCH: HLS has no byte-range probe target — a Range bytes=0-0
	// request against an .m3u8 would corrupt inferred TotalSize. Skip the
	// probe unconditionally, even with an empty filename/size.
	if req.Strategy == types.StrategyHLS {
		return true
	}
	if req.FileSize <= 0 || strings.TrimSpace(req.Filename) == "" {
		return false
	}
	return types.ShouldSkipServerProbe(req.RangeAcquisitionMode, req.SupportsRange)
}

func resolvedRangeMode(req *DownloadRequest) types.RangeAcquisitionMode {
	if req == nil {
		return types.RangeAcquireProbeAtEnqueue
	}
	return types.ResolveRangeAcquisitionMode(req.RangeAcquisitionMode, req.SupportsRange)
}

// FORK-PATCH: local ProbeResult from host hints; ContentType stays empty.
// SupportsRange bool is true only for proven RangeSupported.
func synthesizedProbeResult(req *DownloadRequest) *probing.ProbeResult {
	name := strings.TrimSpace(req.Filename)
	// FORK-PATCH: HLS tasks have no trusted byte size and no range decision —
	// keep ProbeAtEnqueue semantics (zero size, SupportsRange false) instead
	// of inferring RangeUnsupported from the probe-free path.
	if req.Strategy == types.StrategyHLS {
		return &probing.ProbeResult{
			FileSize:         0,
			SupportsRange:    false,
			Filename:         name,
			DetectedFilename: name,
		}
	}
	mode := resolvedRangeMode(req)
	return &probing.ProbeResult{
		FileSize:         req.FileSize,
		SupportsRange:    mode == types.RangeAcquireRangeSupported,
		Filename:         name,
		DetectedFilename: name,
	}
}

// Enqueue probes and reserves a stable destination before dispatching to the queue layer.
func (mgr *LifecycleManager) Enqueue(ctx context.Context, req *DownloadRequest) (string, string, error) {
	if mgr.pool == nil {
		return "", "", types.ErrServiceUnavailable
	}

	utils.Debug("Lifecycle: Enqueue %s (Filename: %s)", req.URL, req.Filename)
	return mgr.enqueueResolved(ctx, req, "")
}

// EnqueueWithID does the same lifecycle work as Enqueue while preserving a caller-owned id.
func (mgr *LifecycleManager) EnqueueWithID(ctx context.Context, req *DownloadRequest, requestID string) (string, string, error) {
	if mgr.pool == nil {
		return "", "", types.ErrServiceUnavailable
	}

	utils.Debug("Lifecycle: EnqueueWithID %s (%s)", req.URL, requestID)
	return mgr.enqueueResolved(ctx, req, requestID)
}

func (mgr *LifecycleManager) enqueueResolved(ctx context.Context, req *DownloadRequest, requestID string) (string, string, error) {
	if req.URL == "" {
		return "", "", types.ErrURLRequired
	}
	if req.Path == "" {
		return "", "", types.ErrDestRequired
	}

	settings := mgr.GetSettings()

	var probeResult *probing.ProbeResult
	if shouldSkipServerProbe(req) {
		// FORK-PATCH: host already has trusted size, filename, and an explicit
		// Range decision — do not take probeSem or call ProbeServerWithProxy.
		probeResult = synthesizedProbeResult(req)
	} else {
		// Throttle concurrent probes — acquire a semaphore slot before probing.
		// If the context is cancelled (e.g., shutdown) we abort immediately.
		if mgr.probeSem != nil {
			select {
			case <-mgr.probeSem:
				// acquired
			case <-ctx.Done():
				return "", "", fmt.Errorf("enqueue aborted before probe: %w", ctx.Err())
			}
			defer func() { mgr.probeSem <- struct{}{} }()
		}

		var probeErr error
		probeResult, probeErr = probing.ProbeServerWithProxy(ctx, req.URL, req.Filename, req.Headers, settings.ToRuntimeConfig())
		if probeErr != nil {
			// Distinguish between terminal client errors (invalid scheme, etc.) and
			// server-side rejections or timeouts that we can optimistically ignore.
			var isTerminal bool
			if urlErr, ok := errors.AsType[*neturl.Error](probeErr); ok {
				var opErr *net.OpError
				isTerminal = !errors.As(probeErr, &opErr) && // not a network-layer error
					strings.Contains(urlErr.Error(), "unsupported protocol scheme")
			}
			isTerminal = isTerminal || errors.Is(probeErr, probing.ErrProbeRequestCreation)

			if isTerminal {
				return "", "", probeErr
			}

			utils.Debug("Lifecycle: Probe failed: %v - enqueueing with optimistic fallback metadata\n", probeErr)
			// Probe failures are non-fatal for known server-side issues (403/405/500) or
			// network timeouts: some servers reject lightweight probe requests but still
			// serve the actual download correctly.
			probeResult = &probing.ProbeResult{SupportsRange: true}
			if req.Filename != "" {
				probeResult.Filename = req.Filename
				probeResult.DetectedFilename = req.Filename
			}
		}
	}

	isNameActive := mgr.buildIsNameActive()

	for range maxWorkingFileReservationAttempts {
		if ctx.Err() != nil {
			return "", "", fmt.Errorf("enqueue aborted: %w", ctx.Err())
		}

		finalPath, finalFilename, err := ResolveDestination(
			req.URL,
			req.Filename,
			req.Path,
			!req.IsExplicitCategory,
			settings,
			probeResult,
			isNameActive,
		)
		if err != nil {
			return "", "", fmt.Errorf("failed to resolve destination: %w", err)
		}

		// Known-size disk precheck before reservation so a reject leaves no
		// exclusive .surge orphan. Resume / ResumeBatch bypass enqueueResolved
		// by design and rely on the write-path disk sentinel + error SaveState.
		if probeResult != nil && probeResult.FileSize > 0 {
			free, freeErr := freeDiskBytes(finalPath)
			if freeErr != nil {
				utils.Debug("Lifecycle: free-space query failed for %s: %v (fail-open)\n", finalPath, freeErr)
			} else if !utils.HasSufficientDiskSpace(probeResult.FileSize, free) {
				utils.Debug("Lifecycle: insufficient disk for %s (size=%d free=%d)\n", finalPath, probeResult.FileSize, free)
				return "", "", types.ErrInsufficientDiskSpace
			}
		}

		// Reserve the working path before dispatch so a concurrent enqueue has to
		// pick a different name instead of truncating this in-flight download.
		if err := reserveWorkingFile(finalPath, finalFilename); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", "", err
		}

		surgePath := filepath.Join(finalPath, finalFilename) + types.IncompleteSuffix

		cfg, err := mgr.buildDownloadRecord(req, requestID, finalPath, finalFilename, probeResult)
		if err != nil {
			_ = os.Remove(surgePath)
			return "", "", err
		}

		queuedEvent := types.DownloadEvent{
			Type:         types.EventQueued,
			DownloadID:   cfg.ID,
			Filename:     finalFilename,
			URL:          req.URL,
			DestPath:     filepath.Join(finalPath, finalFilename),
			Mirrors:      append([]string(nil), req.Mirrors...),
			RateLimit:    cfg.RateLimit,
			RateLimitSet: cfg.RateLimitSet,
			Workers:      req.Workers,
			MinChunkSize: req.MinChunkSize,
		}
		// Reject duplicates and shutdown before touching persistence or
		// publishing. The scheduler keeps workers blocked until preparation
		// completes, so EventStarted cannot race ahead of the queued record.
		accepted, err := mgr.pool.AddWithPreparation(*cfg, func() error {
			// Persist synchronously before publishing so the download survives
			// a restart. Publication itself happens after AddWithPreparation
			// returns (see below): non-progress Publish can now wait on ingress
			// or bus cancellation with no bounded drop, so it must not run
			// under the scheduler lock.
			// FORK-PATCH: CreatedAt is stamped here once; lifecycle rewrites carry it.
			if err := store.AddToMasterList(types.DownloadRecord{
				ID:                   queuedEvent.DownloadID,
				URL:                  queuedEvent.URL,
				URLHash:              store.URLHash(queuedEvent.URL),
				DestPath:             queuedEvent.DestPath,
				Filename:             queuedEvent.Filename,
				Mirrors:              append([]string(nil), queuedEvent.Mirrors...),
				Status:               "queued",
				CreatedAt:            time.Now().Unix(),
				TotalSize:            cfg.TotalSize,
				RateLimit:            queuedEvent.RateLimit,
				RateLimitSet:         queuedEvent.RateLimitSet,
				Workers:              queuedEvent.Workers,
				MinChunkSize:         queuedEvent.MinChunkSize,
				RangeAcquisitionMode: cfg.RangeAcquisitionMode,
				SkipServerProbe:      cfg.SkipServerProbe,
				Strategy:             cfg.Strategy,
				HLSResume:            cfg.HLSResume.Clone(),
			}); err != nil {
				utils.Debug("Lifecycle: Failed to persist queued download synchronously: %v", err)
			}
			// FORK-PATCH: seed a minimal detail gob when the request carries
			// headers or mirrors — credentials must survive a kill before the
			// first worker snapshot. SkipFileHash is required: hashing the
			// precreated 0-byte .surge would plant a hash ValidateIntegrity
			// later flags as tampering, deleting the file plus master/detail
			// state. Best-effort: enqueue must not fail on it.
			// FORK-PATCH: HLS requests must also seed the detail gob even without
			// headers or mirrors — the checkpoint is authoritative detail data and
			// cold resume reads it back before any worker snapshot exists.
			if len(req.Headers) > 0 || len(req.Mirrors) > 0 || req.Strategy == types.StrategyHLS || req.HLSResume != nil {
				mirrors := queuedEvent.Mirrors
				if len(mirrors) == 0 {
					mirrors = []string{req.URL}
				}
				// FORK-PATCH: the seeded checkpoint's VerifiedBytes is the only
				// authoritative HLS progress — keep Downloaded consistent with it
				// so the first lifecycle snapshot still validates as task-backed.
				// A stray HLSResume on a non-HLS request must not leak VerifiedBytes
				// into a byte-range seed.
				var seedDownloaded int64
				if cfg.Strategy == types.StrategyHLS && cfg.HLSResume != nil {
					seedDownloaded = cfg.HLSResume.VerifiedBytes
				}
				if err := store.SaveStateWithOptions(queuedEvent.URL, queuedEvent.DestPath, &types.DownloadRecord{
					ID:                   cfg.ID,
					URL:                  queuedEvent.URL,
					URLHash:              store.URLHash(queuedEvent.URL),
					DestPath:             queuedEvent.DestPath,
					Filename:             queuedEvent.Filename,
					Status:               "queued",
					TotalSize:            cfg.TotalSize,
					Downloaded:           seedDownloaded,
					Mirrors:              append([]string(nil), mirrors...),
					RateLimit:            queuedEvent.RateLimit,
					RateLimitSet:         queuedEvent.RateLimitSet,
					Workers:              queuedEvent.Workers,
					MinChunkSize:         queuedEvent.MinChunkSize,
					RangeAcquisitionMode: cfg.RangeAcquisitionMode,
					SkipServerProbe:      cfg.SkipServerProbe,
					Headers:              maps.Clone(req.Headers),
					Strategy:             cfg.Strategy,
					HLSResume:            cfg.HLSResume.Clone(),
				}, store.SaveStateOptions{SkipFileHash: true}); err != nil {
					utils.Debug("Lifecycle: failed to persist queued detail state: %v", err)
				}
			}
			return nil
		})
		// The fork's seeding is deliberately best-effort: the closure never
		// fails today, but err stays for a future fatal-preparation variant.
		if err != nil {
			_ = os.Remove(surgePath)
			return "", "", err
		}
		if !accepted {
			_ = os.Remove(surgePath)
			return "", "", types.ErrIDExists
		}
		// FORK-PATCH: publish outside the scheduler lock. prepare runs under
		// p.mu; reliable lifecycle subscriptions make Publish wait on consumer
		// backpressure, so an in-lock send could freeze every scheduler op.
		// Durability still precedes the event: the master/detail seed above ran
		// before this Publish. A worker may legitimately publish Started first —
		// Queued arriving late is accepted rather than re-serialized under p.mu.
		// Retire once upstream moves this publication out of prepare.
		if mgr.eventBus != nil {
			if err := mgr.eventBus.Publish(queuedEvent); err != nil {
				utils.Debug("Lifecycle: queued event publish failed for %s: %v", cfg.ID, err)
			}
		}

		return cfg.ID, finalFilename, nil
	}

	return "", "", fmt.Errorf("failed to reserve unique working file for %q after %d attempts", req.URL, maxWorkingFileReservationAttempts)
}

// IsNameActive reports whether the configured active-download callback would
// treat the given directory/name pair as an in-flight conflict.
func (mgr *LifecycleManager) IsNameActive(dir, name string) bool {
	return mgr.buildIsNameActive()(dir, name)
}

func (mgr *LifecycleManager) buildDownloadRecord(req *DownloadRequest, requestID string, finalPath string, finalFilename string, probeResult *probing.ProbeResult) (*types.DownloadRecord, error) {
	if mgr.pool == nil {
		return nil, types.ErrPoolNotInit
	}

	settings := mgr.GetSettings()
	id := strings.TrimSpace(requestID)
	if id == "" {
		id = uuid.New().String()
	}

	if st := mgr.pool.GetStatus(id); st != nil {
		return nil, types.ErrIDExists
	}

	state := progress.New(id, 0)
	state.SetDestPath(filepath.Join(finalPath, finalFilename))

	runtime := settings.ToRuntimeConfig()
	if req.Workers > 0 {
		maxConns := runtime.GetMaxConnectionsPerDownload()
		if req.Workers > maxConns {
			req.Workers = maxConns
		}
		runtime.Workers = req.Workers
	}
	if req.MinChunkSize > 0 {
		runtime.MinChunkSize = req.MinChunkSize
	}

	var rateLimit int64
	var rateLimitSet bool
	if settings.Network.DefaultDownloadRateLimit != nil {
		if parsed, err := utils.ParseRateLimitValue(settings.Network.DefaultDownloadRateLimit.Value); err == nil {
			rateLimit = parsed
			// FORK-PATCH: Only a positive default is an explicit override. "0"/unlimited must
			// keep RateLimitSet=false so inherit semantics stay intact.
			if parsed > 0 {
				rateLimitSet = true
			}
		}
	}

	mode := resolvedRangeMode(req)
	if req.Strategy == types.StrategyHLS {
		// FORK-PATCH: HLS carries no byte-range decision — a caller-supplied
		// mode or 286 *bool hint must not stamp a range policy onto a
		// playlist task.
		mode = types.RangeAcquireProbeAtEnqueue
	} else if mode == types.RangeAcquireProbeAtEnqueue && probeResult != nil && !probeResult.SupportsRange {
		// FORK-PATCH: ProbeServer proved ignore-Range while mode is still
		// empty. Persist range_unsupported so sequential speedstats can
		// skip. Probe-fail optimistic SupportsRange=true is unchanged.
		mode = types.RangeAcquireRangeUnsupported
	}
	skipOrigin := req.SkipServerProbe || shouldSkipServerProbe(req)

	cfg := types.DownloadRecord{
		URL:                  req.URL,
		Mirrors:              req.Mirrors,
		OutputPath:           finalPath,
		ID:                   id,
		Filename:             finalFilename,
		ProgressState:        state,
		Runtime:              runtime,
		Headers:              req.Headers,
		IsExplicitCategory:   req.IsExplicitCategory,
		TotalSize:            probeResult.FileSize,
		SupportsRange:        probeResult.SupportsRange,
		RangeAcquisitionMode: mode,
		SkipServerProbe:      skipOrigin,
		RateLimit:            rateLimit,
		RateLimitSet:         rateLimitSet,
		Strategy:             req.Strategy,
		HLSResume:            req.HLSResume.Clone(),
	}

	if mgr.eventBus != nil {
		cfg.ProgressCh = mgr.eventBus.InputCh
	}

	return &cfg, nil
}
