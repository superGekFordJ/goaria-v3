package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"goaria-v3/internal/surge/progress"
	"goaria-v3/internal/surge/transport"
	"goaria-v3/internal/surge/types"
	"goaria-v3/internal/surge/utils"
)

// activeDownload tracks a download that's currently running
type activeDownload struct {
	config types.DownloadRecord
	cancel context.CancelFunc
	// running is true while the worker goroutine is executing RunDownload for this config.
	running atomic.Bool
	// done is closed by the worker when it stops (running becomes false).
	// Callers waiting for the worker to exit should select on this channel.
	done chan struct{}
}

// queuedTask wraps a download config with SQS-style visibility and retry state
type queuedTask struct {
	cfg      types.DownloadRecord
	retries  int
	inFlight bool
	retryAt  time.Time // task not eligible for pickup until this time
	// token is the pool-issued opportunity identity for this queueing round:
	// allocated at Add and re-minted at each retry requeue, monotonic per pool.
	token uint64
	// admitted marks that an external admission decision authorized this
	// opportunity (managed pools only); legacy pools leave it false and
	// workers pick up tasks directly.
	admitted bool
}

// AdmissionKey identifies one queueing opportunity inside a live pool: the
// raw download ID plus the token the pool issued at Add or retry requeue.
// A token is never reused within a pool's lifetime, so a stale or forged key
// cannot alias a newer opportunity for the same ID.
type AdmissionKey struct {
	ID    string
	Token uint64
}

// AdmissionCandidate is a scalar snapshot of one queued opportunity in FIFO
// order. TotalSize/Downloaded are config/checkpoint hints, not live measures.
type AdmissionCandidate struct {
	Key            AdmissionKey
	RetryAt        time.Time
	Admitted       bool
	InFlight       bool
	RuntimeReady   bool // Runtime present with a positive Workers ceiling
	Workers        int
	MinChunkSize   int64
	TotalSize      int64
	Downloaded     int64
	Strategy       types.DownloadStrategy
	RecordEligible bool // qualifies for host speedstats recording
}

// BusySlot is one physical claim held by a worker: from waitForTask claim
// until the worker finishes the claim's bookkeeping. Running flips true once
// the task registers as an activeDownload and false when RunDownload returns.
// Strategy/RecordEligible are cached at claim mint so the claim-before-
// register window (ad == nil) still reports accurate telemetry metadata.
type BusySlot struct {
	Key            AdmissionKey
	Running        bool
	Strategy       types.DownloadStrategy
	RecordEligible bool
}

// AdmissionSnapshot is a consistent view of the pool's admission state.
// Candidates is the complete FIFO queue (including admitted and in-flight
// entries); Busy lists unsettled physical claims in no particular order.
// CommittedSlots = len(Busy) + count(admitted && !inFlight) and stays within
// [0, Capacity] on managed pools.
type AdmissionSnapshot struct {
	Candidates     []AdmissionCandidate
	Busy           []BusySlot
	Capacity       int
	CommittedSlots int
	Managed        bool
	Stopping       bool
}

// AdmitResult reports the outcome of an Admit call.
type AdmitResult uint8

const (
	// AdmitStale: the key does not match a live opportunity (wrong/ended token).
	AdmitStale AdmitResult = iota
	// AdmitAccepted: this call authorized the opportunity.
	AdmitAccepted
	// AdmitAlreadyAdmitted: the same opportunity is already authorized or
	// physically claimed; a repeat call must not re-account or roll back.
	AdmitAlreadyAdmitted
	// AdmitUnavailable: mode off, stopping, not yet runnable, capacity full,
	// or an invalid tighten request — no side effects.
	AdmitUnavailable
)

// busySlot is the pool-internal physical claim record keyed by AdmissionKey.
// ad is set once the claimed task registers as an activeDownload; Running in
// snapshots is derived from ad.running so claim-before-register windows are
// visible without extra bookkeeping.
type busySlot struct {
	ad *activeDownload
	// Cached at claim mint so snapshots stay accurate while ad is nil.
	strategy       types.DownloadStrategy
	recordEligible bool
}

// shouldRetryFailedDownload reports whether a whole-download failure should be
// re-enqueued (≤10 retries). Permanent HTTP and insufficient disk space are
// terminal at this layer.
func shouldRetryFailedDownload(err error, shuttingDown bool, retries int) bool {
	return !shuttingDown &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!types.IsPermanentHTTPError(err) &&
		!types.IsInsufficientDiskSpace(err) &&
		!errors.Is(err, types.ErrRangeUnsupported) &&
		!errors.Is(err, types.ErrSourceMetadataMismatch) &&
		!errors.Is(err, types.ErrPayloadFirstPersist) &&
		// FORK-PATCH: every permanent HLS sentinel (including the
		// not-implemented dispatch stub) is terminal — never retry.
		!types.IsPermanentHLSError(err) &&
		retries < 10
}

// Scheduler manages the download workers and tasks.
//
// Lock Ordering:
// If a code path must acquire both the pool's main mutex (p.mu) and a limiter's internal
// mutex (limiter.mu, via limiter.SetRate() or limiter.WaitN()), it MUST acquire p.mu first.
// Examples: Add, SetDownloadRateLimit, SetDefaultDownloadRateLimit.
// Never acquire p.mu while holding a limiter's internal mutex to prevent deadlocks.
type Scheduler struct {
	taskCond       *sync.Cond
	queueOrder     []string
	progressCh     chan<- types.DownloadEvent
	progressDone   chan struct{}              // closed when progressCh must no longer be sent to
	downloads      map[string]*activeDownload // Track active downloads for pause/resume
	queued         map[string]*queuedTask     // Track queued downloads
	mu             sync.RWMutex
	wg             sync.WaitGroup // We use this to wait for all active downloads to pause before exiting the program
	maxDownloads   int
	isShuttingDown bool

	// Non-blocking retry backoff: a single timer wakes waitForTask when the
	// earliest pending retry becomes eligible.
	retryTimer           *time.Timer
	retryWakeAt          time.Time
	retryTimerGeneration uint64

	globalLimiter               *transport.RateLimiter
	downloadLimiters            map[string]*transport.RateLimiter
	defaultDownloadRateLimitBps int64
	shutdownOnce                sync.Once

	// FORK-PATCH: managed admission mode. When true, workers only claim queued
	// tasks whose current opportunity was authorized via Admit. busy tracks
	// physical claims from waitForTask handout until end-of-claim bookkeeping;
	// lastToken mints monotonic per-pool opportunity identities (never reused).
	managed   bool
	lastToken uint64
	busy      map[AdmissionKey]*busySlot

	// FORK-PATCH: optional slot-release notification. Copied under p.mu and
	// invoked after Unlock so the callback may re-enter AdmissionSnapshot.
	admissionWake atomic.Pointer[func()]

	// FORK-PATCH: test-only seams for pinning worker windows. workerClaimedGate
	// fires after waitForTask hands a claimed task to a worker, before it
	// re-locks p.mu to register it. workerDoneGate fires after RunDownload
	// returns and ad.done is closed, before terminal bookkeeping re-locks
	// p.mu — it must sit after close(ad.done) so Cancel never waits on a
	// parked worker. Per-pool atomics keep the seams race-free and confined:
	// workers only ever consult their own pool's gates, so a leaked worker
	// from another pool can neither observe nor consume them. A gate is a
	// single-entry contract: only one worker may enter an armed instance —
	// the Load+Store(nil) self-clear in gate fns is not atomic. nil = no-op.
	workerClaimedGate atomic.Pointer[func()]
	workerDoneGate    atomic.Pointer[func()]
}

// PauseResult describes the download state that Pause changed.
type PauseResult struct {
	Found        bool
	QueuedConfig *types.DownloadRecord
}

var (
	// gracefulShutdownPauseSoftTimeout controls when we emit a warning that
	// pausing is taking longer than expected. It is intentionally soft; shutdown
	// continues waiting for durable pause persistence.
	gracefulShutdownPauseSoftTimeout = 10 * time.Second
	// gracefulShutdownPausePollInterval controls how often shutdown rechecks pause state.
	gracefulShutdownPausePollInterval = 100 * time.Millisecond
	// gracefulShutdownPauseHardTimeout prevents indefinite shutdown hangs if a worker is stuck.
	gracefulShutdownPauseHardTimeout = 30 * time.Second
	// The timeout/poll interval vars below are kept for graceful-shutdown use only.
	// cancelStopWaitTimeout bounds how long Cancel waits for an active worker to exit.
	cancelStopWaitTimeout = 3 * time.Second
)

func New(progressCh chan<- types.DownloadEvent, maxDownloads int) *Scheduler {
	if maxDownloads < 1 {
		maxDownloads = 3 // Default to 3 if invalid
	}
	return newScheduler(progressCh, maxDownloads, false)
}

// NewManaged creates a Scheduler whose workers only claim opportunities that
// were explicitly authorized by Admit; queued tasks never self-promote.
// capacity must be positive — it is the physical slot ceiling the
// CommittedSlots invariant is checked against.
func NewManaged(progressCh chan<- types.DownloadEvent, capacity int) (*Scheduler, error) {
	if capacity < 1 {
		return nil, fmt.Errorf("scheduler: managed pool requires capacity > 0, got %d", capacity)
	}
	return newScheduler(progressCh, capacity, true), nil
}

func newScheduler(progressCh chan<- types.DownloadEvent, maxDownloads int, managed bool) *Scheduler {
	pool := &Scheduler{
		progressCh:       progressCh,
		progressDone:     make(chan struct{}),
		downloads:        make(map[string]*activeDownload),
		queued:           make(map[string]*queuedTask),
		maxDownloads:     maxDownloads,
		managed:          managed,
		busy:             make(map[AdmissionKey]*busySlot),
		globalLimiter:    transport.NewRateLimiter(0, 0),
		downloadLimiters: make(map[string]*transport.RateLimiter),
	}
	pool.taskCond = sync.NewCond(&pool.mu)
	for i := 0; i < maxDownloads; i++ {
		go pool.worker()
	}
	return pool
}

// allocTokenLocked mints the next opportunity token. Tokens are monotonic per
// pool and never reused; exhaustion refuses the call instead of wrapping.
// Caller holds p.mu.
func (p *Scheduler) allocTokenLocked() (uint64, bool) {
	if p.lastToken == math.MaxUint64 {
		return 0, false
	}
	p.lastToken++
	return p.lastToken, true
}

// committedSlotsLocked = len(Busy) + count(queued.admitted && !queued.inFlight).
// A claimed task moves from the admitted term into Busy atomically at claim,
// so the sum never double-counts. Caller holds p.mu.
func (p *Scheduler) committedSlotsLocked() int {
	n := len(p.busy)
	for _, qt := range p.queued {
		if qt.admitted && !qt.inFlight {
			n++
		}
	}
	return n
}

// releaseBusyLocked drops the physical claim identity for key. It runs once
// per claim at the end of the worker's bookkeeping; the wake callback (if
// installed) is copied here and must be invoked after p.mu is released so it
// can re-enter AdmissionSnapshot without deadlock. Caller holds p.mu.
func (p *Scheduler) releaseBusyLocked(key AdmissionKey) func() {
	if _, held := p.busy[key]; !held {
		return nil
	}
	delete(p.busy, key)
	p.taskCond.Broadcast()
	if fn := p.admissionWake.Load(); fn != nil {
		return *fn
	}
	return nil
}

// SetAdmissionWake installs a notification invoked outside the pool lock each
// time a physical claim is released and the freed slot is already visible to
// AdmissionSnapshot. Nil-safe; nil clears the hook. Clearing does not
// synchronize with an in-flight release: a callback already loaded before the
// clear may still fire once — acceptable for a notification edge.
func (p *Scheduler) SetAdmissionWake(fn func()) {
	if p == nil {
		return
	}
	if fn == nil {
		p.admissionWake.Store(nil)
		return
	}
	p.admissionWake.Store(&fn)
}

// AdmissionSnapshot returns the complete FIFO queue plus unsettled physical
// claims. Nil receivers yield a stopping empty snapshot.
func (p *Scheduler) AdmissionSnapshot() AdmissionSnapshot {
	if p == nil {
		return AdmissionSnapshot{Stopping: true}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	snap := AdmissionSnapshot{
		Capacity: p.maxDownloads,
		Managed:  p.managed,
		Stopping: p.isShuttingDown,
	}
	snap.Candidates = make([]AdmissionCandidate, 0, len(p.queueOrder))
	for _, id := range p.queueOrder {
		qt, ok := p.queued[id]
		if !ok {
			continue
		}
		cand := AdmissionCandidate{
			Key:      AdmissionKey{ID: id, Token: qt.token},
			RetryAt:  qt.retryAt,
			Admitted: qt.admitted,
			InFlight: qt.inFlight,
			Strategy: qt.cfg.Strategy,
		}
		if rt := qt.cfg.Runtime; rt != nil {
			cand.RuntimeReady = rt.Workers > 0
			cand.Workers = rt.Workers
			cand.MinChunkSize = rt.MinChunkSize
		}
		cand.Downloaded, cand.TotalSize = cfgEffectiveProgress(&qt.cfg)
		cand.RecordEligible = recordEligibleForConfig(&qt.cfg, cand.TotalSize)
		snap.Candidates = append(snap.Candidates, cand)
	}
	snap.Busy = make([]BusySlot, 0, len(p.busy))
	for key, slot := range p.busy {
		bs := BusySlot{
			Key:            key,
			Running:        slot.ad != nil && slot.ad.running.Load(),
			Strategy:       slot.strategy,
			RecordEligible: slot.recordEligible,
		}
		if slot.ad != nil {
			// Once registered, live config wins over the claim-time cache.
			bs.Strategy = slot.ad.config.Strategy
			bs.RecordEligible = recordEligibleForConfig(&slot.ad.config, effectiveConfigTotalSize(&slot.ad.config))
		}
		snap.Busy = append(snap.Busy, bs)
	}
	snap.CommittedSlots = p.committedSlotsLocked()
	return snap
}

// Admit authorizes one queued opportunity for dispatch on a managed pool and
// tightens its runtime parameters in place. Only keys minted by this pool and
// still current are honored; stale or replayed keys never alias a newer
// opportunity for the same ID.
func (p *Scheduler) Admit(key AdmissionKey, workers int, minChunkSize int64) AdmitResult {
	if p == nil {
		return AdmitUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.managed || p.isShuttingDown {
		return AdmitUnavailable
	}
	if key.ID == "" || key.Token == 0 {
		return AdmitStale
	}
	qt, queued := p.queued[key.ID]
	current := queued && qt.token == key.Token
	if slot, held := p.busy[key]; held {
		// A claim counts as authorization only while its opportunity is still
		// live: registered and active under its own ad, or claimed-in-flight
		// on the current queue entry. A residual claim settling after Cancel,
		// completion, or token rotation must not authorize the old key.
		if slot.ad != nil && p.downloads[key.ID] == slot.ad {
			return AdmitAlreadyAdmitted
		}
		if slot.ad == nil && current && qt.inFlight {
			return AdmitAlreadyAdmitted
		}
		return AdmitStale
	}
	if !current {
		return AdmitStale
	}
	if qt.admitted {
		return AdmitAlreadyAdmitted
	}
	if qt.inFlight {
		// A retry-published task between runs is not yet claimable.
		return AdmitUnavailable
	}
	if !qt.retryAt.IsZero() && time.Now().Before(qt.retryAt) {
		return AdmitUnavailable
	}
	rt := qt.cfg.Runtime
	if rt == nil || rt.Workers <= 0 {
		return AdmitUnavailable
	}
	if workers < 1 || workers > rt.Workers {
		return AdmitUnavailable
	}
	if rt.MinChunkSize > 0 && (minChunkSize <= 0 || minChunkSize > rt.MinChunkSize) {
		return AdmitUnavailable
	}
	if p.committedSlotsLocked() >= p.maxDownloads {
		return AdmitUnavailable
	}
	// Copy-on-write: only Workers/MinChunkSize may tighten; every other field
	// (headers, range/payload-first mode, rate limits, resume state) is kept.
	patched := *rt
	patched.Workers = workers
	if rt.MinChunkSize > 0 {
		patched.MinChunkSize = minChunkSize
	}
	qt.cfg.Runtime = &patched
	qt.admitted = true
	p.taskCond.Broadcast()
	return AdmitAccepted
}

// managedRuntimeCeiling returns a Runtime copy whose Workers ceiling is always
// positive so managed Admit never starves on a nil/auto runtime. Sequential and
// proven range-unsupported downloads stay single-threaded; positive caller
// ceilings are preserved (never widened) and every other field is kept.
// FORK-PATCH: managed Add materialization — runs before the queue insert so
// AdmissionSnapshot/Admit always observe a positive ceiling.
func managedRuntimeCeiling(cfg types.DownloadRecord) *types.RuntimeConfig {
	var rt types.RuntimeConfig
	if cfg.Runtime != nil {
		rt = *cfg.Runtime
	} else {
		rt = *types.DefaultRuntimeConfig()
	}
	// FORK-PATCH: HLS is exempt only from the range_unsupported single-thread
	// lock — its segments fan out on their own scheduler. SequentialDownload
	// still forces one worker and a positive caller ceiling stays the bound.
	if rt.SequentialDownload || (cfg.Strategy != types.StrategyHLS && cfg.RangeAcquisitionMode == types.RangeAcquireRangeUnsupported) {
		rt.Workers = 1
	} else if rt.Workers <= 0 {
		rt.Workers = rt.GetMaxConnectionsPerDownload()
	}
	return &rt
}

// cfgEffectiveProgress resolves the best-known downloaded/total pair for a
// config — progress state, when present, is more current than the persisted
// hints.
func cfgEffectiveProgress(cfg *types.DownloadRecord) (downloaded, effectiveTotal int64) {
	if cfg == nil {
		return 0, 0
	}
	downloaded = cfg.Downloaded
	effectiveTotal = cfg.TotalSize
	if cfg.ProgressState != nil {
		d, total, _, _, _, _ := progress.CfgProgress(cfg).GetProgress()
		downloaded = d
		if total > 0 {
			effectiveTotal = total
		}
	}
	return downloaded, effectiveTotal
}

// effectiveConfigTotalSize resolves the best-known total for a config —
// progress state, when present, is more current than the persisted hint.
func effectiveConfigTotalSize(cfg *types.DownloadRecord) int64 {
	_, total := cfgEffectiveProgress(cfg)
	return total
}

// recordEligibleForConfig reports whether a config qualifies for host
// speedstats recording. HLS tasks carry their own eligibility flag inside
// the resume checkpoint (nil resume = eligible); byte-range tasks qualify
// strictly above the size threshold.
func recordEligibleForConfig(cfg *types.DownloadRecord, effectiveTotal int64) bool {
	if cfg == nil {
		return false
	}
	if cfg.Strategy == types.StrategyHLS {
		return cfg.HLSResume == nil || cfg.HLSResume.RecordEligible
	}
	return effectiveTotal > types.RecordEligibleMinFileSize
}

// removeQueueOrderLocked removes an ID from queueOrder preserving the order of other elements.
func (p *Scheduler) removeQueueOrderLocked(id string) {
	for i, qid := range p.queueOrder {
		if qid == id {
			p.queueOrder = append(p.queueOrder[:i], p.queueOrder[i+1:]...)
			return
		}
	}
}

// syncConfigFromState syncs Filename, DestPath, and Mirrors from the associated state.
func syncConfigFromState(cfg *types.DownloadRecord) {
	if cfg.ProgressState == nil {
		return
	}
	if fn := progress.CfgProgress(cfg).GetFilename(); fn != "" {
		cfg.Filename = fn
	}
	if dp := progress.CfgProgress(cfg).GetDestPath(); dp != "" {
		cfg.DestPath = dp
	}
	if ms := progress.CfgProgress(cfg).GetMirrors(); len(ms) > 0 {
		var urls []string
		for _, m := range ms {
			urls = append(urls, m.URL)
		}
		cfg.Mirrors = urls
	}
	if _, totalSize, _, _, _, _ := progress.CfgProgress(cfg).GetProgress(); totalSize > 0 {
		cfg.TotalSize = totalSize
	}
}

// resolveDestPath resolves the destination path consistently from config, state, and output bounds.
func resolveDestPath(cfg *types.DownloadRecord) string {
	destPath := cfg.DestPath
	if destPath == "" && cfg.ProgressState != nil {
		destPath = progress.CfgProgress(cfg).GetDestPath()
	}
	if destPath == "" && cfg.OutputPath != "" && cfg.Filename != "" {
		destPath = filepath.Join(cfg.OutputPath, cfg.Filename)
	}
	if destPath == "" {
		destPath = cfg.OutputPath // default fallback
	}
	return destPath
}

// Add adds a new download task to the pool and reports whether it was
// accepted. An ID is owned by at most one queued or active entry at a time:
// a same-ID add, or one racing the shutdown sweep, is refused without
// touching queue state, limiters, or the wait group.
// The caller (LifecycleManager) is responsible for emitting any lifecycle
// events (e.g. DownloadQueuedMsg).
func (p *Scheduler) Add(cfg types.DownloadRecord) bool {
	accepted, _ := p.AddWithPreparation(cfg, nil)
	return accepted
}

// AddWithPreparation checks admission before preparing persistence and events,
// then makes the task runnable. prepare runs under the scheduler lock — it
// must be bounded, it blocks all scheduler operations, and it must not call
// scheduler methods. If it fails, no task is queued and no admission token is
// minted.
func (p *Scheduler) AddWithPreparation(cfg types.DownloadRecord, prepare func() error) (bool, error) {
	if cfg.ProgressCh == nil {
		cfg.ProgressCh = p.progressCh
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isShuttingDown {
		utils.Debug("Scheduler: Add refused for %s: shutting down", cfg.ID)
		return false, nil
	}
	// A queued entry owns the ID even while inFlight — the claiming worker
	// settles its wait-group count itself.
	if _, exists := p.queued[cfg.ID]; exists {
		utils.Debug("Scheduler: Add refused for %s: ID already queued", cfg.ID)
		return false, nil
	}
	if _, exists := p.downloads[cfg.ID]; exists {
		utils.Debug("Scheduler: Add refused for %s: ID already active", cfg.ID)
		return false, nil
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			return false, err
		}
	}
	// prepare precedes token minting so a failed enqueue cannot burn a
	// monotonic token; a refusal below may still follow its side effects.
	token, ok := p.allocTokenLocked()
	if !ok {
		utils.Debug("Scheduler: Add refused for %s: admission token space exhausted", cfg.ID)
		return false, nil
	}
	if p.managed {
		cfg.Runtime = managedRuntimeCeiling(cfg)
	}
	// FORK-PATCH: the queue must own a physically isolated checkpoint —
	// caller-side mutations must never leak into queued state.
	cfg.HLSResume = cfg.HLSResume.Clone()
	p.ensureLimiterForConfigLocked(&cfg)
	qt := &queuedTask{cfg: cfg, token: token}
	p.queued[cfg.ID] = qt
	p.queueOrder = append(p.queueOrder, cfg.ID)
	p.wg.Add(1)
	p.taskCond.Signal()
	return true, nil
}

func (p *Scheduler) ensureLimiterForConfigLocked(cfg *types.DownloadRecord) {
	if cfg == nil || cfg.ID == "" {
		return
	}

	if p.globalLimiter == nil {
		p.globalLimiter = transport.NewRateLimiter(0, 0)
	}
	if p.downloadLimiters == nil {
		p.downloadLimiters = make(map[string]*transport.RateLimiter)
	}

	// If state already carries an explicit rate, prefer it over cfg default.
	if cfg.ProgressState != nil {
		if stateRate, stateSet := progress.CfgProgress(cfg).GetRateLimit(); stateSet {
			cfg.RateLimit = stateRate
			cfg.RateLimitSet = true
		}
	}

	rate := cfg.RateLimit
	if !cfg.RateLimitSet {
		rate = p.defaultDownloadRateLimitBps
		cfg.RateLimit = rate
	}
	if cfg.ProgressState != nil {
		progress.CfgProgress(cfg).SetRateLimit(rate, cfg.RateLimitSet)
	}

	limiter := p.downloadLimiters[cfg.ID]
	if limiter == nil {
		limiter = transport.NewRateLimiter(rate, rateLimiterBurst(rate))
		p.downloadLimiters[cfg.ID] = limiter
	} else {
		limiter.SetRate(rate, rateLimiterBurst(rate))
	}

	if cfg.Limiter == nil {
		cfg.Limiter = transport.NewMultiLimiter(p.globalLimiter, limiter)
	}
}

func rateLimiterBurst(rate int64) int64 {
	if rate <= 0 {
		return 0
	}
	return rate
}

// HasDownload reports whether a download with the given URL is currently active or queued in the pool.
func (p *Scheduler) HasDownload(url string) bool {
	p.mu.RLock()
	for _, ad := range p.downloads {
		if ad.config.URL == url {
			p.mu.RUnlock()
			return true
		}
	}
	for _, qd := range p.queued {
		if qd.cfg.URL == url {
			p.mu.RUnlock()
			return true
		}
	}
	p.mu.RUnlock()

	return false
}

// ActiveCount returns the number of currently active (downloading/pausing) downloads
func (p *Scheduler) ActiveCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, ad := range p.downloads {
		// Count if not completed and not fully paused
		if ad.config.ProgressState != nil && !progress.CfgProgress(&ad.config).Done.Load() && !progress.CfgProgress(&ad.config).IsPaused() {
			count++
		}
	}
	// Also count queued
	count += len(p.queued)
	return count
}

// GetAll returns all active download configs (for listing)
func (p *Scheduler) GetAll() []types.DownloadRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()

	configs := make([]types.DownloadRecord, 0, len(p.downloads)+len(p.queued))
	for _, ad := range p.downloads {
		cfg := ad.config
		cfg.Limiter = nil
		syncConfigFromState(&cfg)
		cfg.HLSResume = cfg.HLSResume.Clone()
		configs = append(configs, cfg)
	}
	for _, id := range p.queueOrder {
		if qd, ok := p.queued[id]; ok {
			cfg := qd.cfg
			cfg.Limiter = nil
			cfg.HLSResume = cfg.HLSResume.Clone()
			configs = append(configs, cfg)
		}
	}
	return configs
}

// Pause pauses a specific download by ID. A queued download is removed before a
// worker can start it; an in-flight worker will observe its removal and balance
// the wait group itself. No lifecycle events are emitted by the scheduler.
func (p *Scheduler) Pause(downloadID string) PauseResult {
	p.mu.Lock()
	if queued, exists := p.queued[downloadID]; exists {
		queuedConfig := queued.cfg
		if queuedConfig.ProgressState != nil {
			state := progress.CfgProgress(&queuedConfig)
			queuedConfig.Downloaded, _, _, _, _, _ = state.GetProgress()
			queuedConfig.RateLimit, queuedConfig.RateLimitSet = state.GetRateLimit()
		}
		queuedConfig.Limiter = nil
		queuedConfig.HLSResume = queuedConfig.HLSResume.Clone()

		delete(p.queued, downloadID)
		p.removeQueueOrderLocked(downloadID)
		delete(p.downloadLimiters, downloadID)
		inFlight := queued.inFlight
		p.mu.Unlock()

		if !inFlight {
			p.wg.Done()
		}
		return PauseResult{Found: true, QueuedConfig: &queuedConfig}
	}

	ad, exists := p.downloads[downloadID]
	var configTotal int64
	if exists && ad != nil {
		configTotal = ad.config.TotalSize
	}
	p.mu.Unlock()

	if !exists || ad == nil {
		return PauseResult{}
	}

	// Set paused flag and cancel context
	if ad.config.ProgressState != nil {
		prog := progress.CfgProgress(&ad.config)

		// Completion boundary guard: if done or verified progress reached positive total, no-op return true.
		if prog.Done.Load() {
			return PauseResult{Found: true}
		}
		total := prog.Bytes.TotalSize.Load()
		if total <= 0 {
			total = configTotal
		}
		if total > 0 && prog.Bytes.VerifiedProgress.Load() >= total {
			return PauseResult{Found: true}
		}

		// Idempotency: If already paused, do nothing.
		if prog.IsPaused() {
			return PauseResult{Found: true}
		}
		// If transition is already in progress, still ensure worker context is canceled.
		if prog.IsPausing() {
			if ad.cancel != nil {
				ad.cancel()
			}
			return PauseResult{Found: true}
		}
		prog.SetPausing(true) // Mark as transitioning to pause
		prog.Pause()
	}
	// Always cancel worker context as a safety net (single downloader does not set state cancel itself).
	if ad.cancel != nil {
		ad.cancel()
	}

	// Send pause message is now exclusively handled by worker return paths
	// to ensure fully synchronized byte counts.
	return PauseResult{Found: true}
}

// SetGlobalRateLimit updates the global rate limiter (bytes/sec). Use 0 to disable.
func (p *Scheduler) SetGlobalRateLimit(rate int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.globalLimiter == nil {
		p.globalLimiter = transport.NewRateLimiter(0, 0)
	}
	// All per-download MultiLimiters hold a pointer to this globalLimiter,
	// so updating the rate here propagates to all active downloads instantly.
	p.globalLimiter.SetRate(rate, rateLimiterBurst(rate))
}

// SetDefaultDownloadRateLimit updates the default per-download rate limit (bytes/sec).
func (p *Scheduler) SetDefaultDownloadRateLimit(rate int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.defaultDownloadRateLimitBps = rate
	if p.downloadLimiters == nil {
		p.downloadLimiters = make(map[string]*transport.RateLimiter)
	}

	for _, id := range p.queueOrder {
		if qd, ok := p.queued[id]; ok {
			cfg := qd.cfg
			if cfg.RateLimitSet {
				continue
			}
			cfg.RateLimit = rate
			qd.cfg = cfg
			if cfg.ProgressState != nil {
				progress.CfgProgress(&cfg).SetRateLimit(rate, false)
			}
			limiter := p.downloadLimiters[id]
			if limiter == nil {
				p.downloadLimiters[id] = transport.NewRateLimiter(rate, rateLimiterBurst(rate))
			} else {
				limiter.SetRate(rate, rateLimiterBurst(rate))
			}
		}
	}
	for id, ad := range p.downloads {
		if ad.config.RateLimitSet {
			continue
		}
		ad.config.RateLimit = rate
		if ad.config.ProgressState != nil {
			progress.CfgProgress(&ad.config).SetRateLimit(rate, false)
		}
		limiter := p.downloadLimiters[id]
		if limiter == nil {
			// Note: ensureLimiterForConfigLocked guarantees all active/queued downloads have a limiter.
			// This nil branch is defensive and should be unreachable in practice.
			p.downloadLimiters[id] = transport.NewRateLimiter(rate, rateLimiterBurst(rate))
		} else {
			limiter.SetRate(rate, rateLimiterBurst(rate))
		}
	}
}

// SetDownloadRateLimit updates a specific download's rate limit (bytes/sec).
func (p *Scheduler) SetDownloadRateLimit(downloadID string, rate int64) bool {
	if downloadID == "" {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	found := false
	if ad, ok := p.downloads[downloadID]; ok {
		ad.config.RateLimit = rate
		ad.config.RateLimitSet = true
		if ad.config.ProgressState != nil {
			progress.CfgProgress(&ad.config).SetRateLimit(rate, true)
		}
		found = true
	}
	if qt, ok := p.queued[downloadID]; ok {
		qt.cfg.RateLimit = rate
		qt.cfg.RateLimitSet = true
		if qt.cfg.ProgressState != nil {
			progress.CfgProgress(&qt.cfg).SetRateLimit(rate, true)
		}
		found = true
	}

	if !found {
		return false
	}

	if p.downloadLimiters == nil {
		p.downloadLimiters = make(map[string]*transport.RateLimiter)
	}
	limiter := p.downloadLimiters[downloadID]
	if limiter == nil {
		p.downloadLimiters[downloadID] = transport.NewRateLimiter(rate, rateLimiterBurst(rate))
	} else {
		limiter.SetRate(rate, rateLimiterBurst(rate))
	}
	return true
}

// ClearDownloadRateLimit removes a specific download's override so it inherits the current default.
func (p *Scheduler) ClearDownloadRateLimit(downloadID string) bool {
	if downloadID == "" {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	defaultRate := p.defaultDownloadRateLimitBps

	found := false
	if ad, ok := p.downloads[downloadID]; ok {
		ad.config.RateLimit = defaultRate
		ad.config.RateLimitSet = false
		if ad.config.ProgressState != nil {
			progress.CfgProgress(&ad.config).SetRateLimit(defaultRate, false)
		}
		found = true
	}
	if qt, ok := p.queued[downloadID]; ok {
		qt.cfg.RateLimit = defaultRate
		qt.cfg.RateLimitSet = false
		if qt.cfg.ProgressState != nil {
			progress.CfgProgress(&qt.cfg).SetRateLimit(defaultRate, false)
		}
		found = true
	}

	if !found {
		return false
	}

	if p.downloadLimiters == nil {
		p.downloadLimiters = make(map[string]*transport.RateLimiter)
	}
	limiter := p.downloadLimiters[downloadID]
	if limiter == nil {
		p.downloadLimiters[downloadID] = transport.NewRateLimiter(defaultRate, rateLimiterBurst(defaultRate))
	} else {
		limiter.SetRate(defaultRate, rateLimiterBurst(defaultRate))
	}
	return true
}

// PauseAll pauses all active downloads (for graceful shutdown)
func (p *Scheduler) PauseAll() {
	p.mu.RLock()
	ids := make([]string, 0, len(p.downloads)) // This stores the uuids of the downloads to be paused
	for id, ad := range p.downloads {
		// Only pause downloads that are actually active (not already paused or done or pausing)
		if ad != nil && ad.config.ProgressState != nil && !progress.CfgProgress(&ad.config).IsPaused() && !progress.CfgProgress(&ad.config).Done.Load() && !progress.CfgProgress(&ad.config).IsPausing() {
			ids = append(ids, id)
		}
	}
	p.mu.RUnlock()

	for _, id := range ids {
		p.Pause(id)
	}
}

// Cancel cancels and removes a download by ID. Returns metadata about what was
// removed so the caller (LifecycleManager) can emit events and handle cleanup.
// No events are emitted by the pool itself.
func (p *Scheduler) Cancel(downloadID string) types.CancelResult {
	p.mu.Lock()
	ad, activeExists := p.downloads[downloadID]
	qCfg, queuedExists := p.queued[downloadID]
	var queuedCfg types.DownloadRecord
	if queuedExists {
		queuedCfg = qCfg.cfg
	}
	if activeExists {
		delete(p.downloads, downloadID)
	}

	var droppedQueued bool
	if queuedExists {
		delete(p.queued, downloadID)
		p.removeQueueOrderLocked(downloadID)
		if !qCfg.inFlight {
			droppedQueued = true
		}
	}
	if activeExists || queuedExists {
		delete(p.downloadLimiters, downloadID)
	}
	p.mu.Unlock()

	if droppedQueued {
		p.wg.Done()
	}

	if !activeExists && !queuedExists {
		return types.CancelResult{}
	}

	result := types.CancelResult{Found: true, WasQueued: queuedExists && !activeExists}

	if activeExists && ad != nil {
		result.Filename = ad.config.Filename
		result.DestPath = resolveDestPath(&ad.config)
		result.Completed = ad.config.ProgressState != nil && progress.CfgProgress(&ad.config).Done.Load()

		// Cancel the context to stop workers
		if ad.cancel != nil {
			ad.cancel()
		}

		// Best effort: wait for worker to exit so delete cleanup doesn't race with
		// downloader startup that can recreate the .surge file after removal.
		if ad.running.Load() {
			deadline := time.NewTimer(cancelStopWaitTimeout)
			select {
			case <-ad.done:
			case <-deadline.C:
			}
			deadline.Stop()
		}

		// Cancelled is not completed: leave Done false so cleanup follows the
		// error path rather than treating cancel as a successful finish.
	} else if queuedExists {
		result.Filename = queuedCfg.Filename
		result.DestPath = resolveDestPath(&queuedCfg)
	}

	return result
}

// ExtractPausedConfig atomically removes a paused download from the pool and returns
// its config (with state cleared for re-enqueue) so the LifecycleManager can resume it.
// Returns nil if the download is not found, not paused, or still transitioning (pausing).
func (p *Scheduler) ExtractPausedConfig(downloadID string) *types.DownloadRecord {
	p.mu.Lock()
	ad, exists := p.downloads[downloadID]
	if !exists || ad == nil {
		p.mu.Unlock()
		return nil
	}

	// Cannot extract if still pausing or not actually paused
	if ad.config.ProgressState == nil || !progress.CfgProgress(&ad.config).IsPaused() || progress.CfgProgress(&ad.config).IsPausing() {
		p.mu.Unlock()
		return nil
	}

	// Sync latest filename/path/mirrors from live state before handing off
	syncConfigFromState(&ad.config)

	cfg := ad.config
	delete(p.downloads, downloadID)
	delete(p.downloadLimiters, downloadID)
	p.mu.Unlock()

	cfg.Limiter = nil
	cfg.HLSResume = cfg.HLSResume.Clone()
	if cfg.ProgressState != nil {
		progress.CfgProgress(&cfg).Resume()
	}
	return &cfg
}

// UpdateURL updates the in-memory URL of a download by ID.
// The caller (LifecycleManager) is responsible for persisting the change to the DB.
// It fails if the download is actively downloading (not paused or errored).
func (p *Scheduler) UpdateURL(downloadID string, newURL string) error {
	p.mu.Lock()
	ad, exists := p.downloads[downloadID]
	_, qExists := p.queued[downloadID]

	if qExists {
		p.mu.Unlock()
		return types.ErrQueuedUpdate
	}

	if exists && ad != nil {
		if ad.config.ProgressState != nil && !progress.CfgProgress(&ad.config).IsPaused() {
			if ad.running.Load() {
				p.mu.Unlock()
				return types.ErrActiveUpdate
			}
		}
		ad.config.URL = newURL
		if ad.config.ProgressState != nil {
			progress.CfgProgress(&ad.config).SetURL(newURL)
		}
	}
	p.mu.Unlock()

	return nil
}

// armRetryTimerLocked schedules a Broadcast for when the earliest pending
// retry becomes eligible. Caller must hold p.mu.
// retryWakeAt gives idempotent early-out for a stale post-disarm callback;
// retryTimerGeneration discriminates callbacks from an older re-armed timer.
func (p *Scheduler) armRetryTimerLocked(earliest time.Time) {
	if earliest.IsZero() {
		if p.retryTimer != nil {
			p.retryTimer.Stop()
			p.retryTimer = nil
		}
		p.retryWakeAt = time.Time{}
		return
	}

	if !p.retryWakeAt.IsZero() && p.retryWakeAt.Equal(earliest) {
		return
	}

	if p.retryTimer != nil {
		p.retryTimer.Stop()
	}

	p.retryTimerGeneration++
	gen := p.retryTimerGeneration
	p.retryWakeAt = earliest

	delay := time.Until(earliest)
	if delay < 0 {
		delay = 0
	}

	p.retryTimer = time.AfterFunc(delay, func() {
		p.mu.Lock()
		defer p.mu.Unlock()

		if p.isShuttingDown || gen != p.retryTimerGeneration || !p.retryWakeAt.Equal(earliest) {
			return
		}

		p.retryWakeAt = time.Time{}
		p.retryTimer = nil
		p.taskCond.Broadcast()
	})
}

func (p *Scheduler) waitForTask() (*queuedTask, AdmissionKey) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for {
		if p.isShuttingDown {
			return nil, AdmissionKey{}
		}
		now := time.Now()
		var earliestPending time.Time

		// Skip tasks whose retryAt is still in the future; arm the wake timer
		// for the earliest pending one. Managed pools additionally require an
		// admitted opportunity — the timer only re-scans; Admit wakes waiters.
		for _, id := range p.queueOrder {
			qt, ok := p.queued[id]
			if !ok || qt.inFlight {
				continue
			}
			if !qt.retryAt.IsZero() && now.Before(qt.retryAt) {
				if earliestPending.IsZero() || qt.retryAt.Before(earliestPending) {
					earliestPending = qt.retryAt
				}
				continue
			}
			if p.managed && !qt.admitted {
				continue
			}
			qt.inFlight = true
			key := AdmissionKey{ID: id, Token: qt.token}
			if p.busy == nil {
				p.busy = make(map[AdmissionKey]*busySlot)
			}
			// FORK-PATCH: cache telemetry metadata at claim mint — the
			// claim-before-register window has slot.ad == nil, so snapshots
			// must read strategy/eligibility from the slot, not the config.
			p.busy[key] = &busySlot{
				strategy:       qt.cfg.Strategy,
				recordEligible: recordEligibleForConfig(&qt.cfg, effectiveConfigTotalSize(&qt.cfg)),
			}
			return qt, key
		}

		p.armRetryTimerLocked(earliestPending)
		p.taskCond.Wait()
	}
}

func (p *Scheduler) worker() {
	for {
		qt, claimKey := p.waitForTask()
		if qt == nil {
			return
		}

		if g := p.workerClaimedGate.Load(); g != nil {
			(*g)()
		}

		// Create cancellable context
		ctx, cancel := context.WithCancel(context.Background())

		p.mu.Lock()
		// qt.cfg is mutated under p.mu elsewhere (e.g. SetDefaultDownloadRateLimit),
		// so the ID is read inside this critical section.
		id := qt.cfg.ID
		// The claim is tied to this exact queuedTask: a Pause/Cancel/shutdown
		// removal plus a same-ID re-add must not be adopted by this worker.
		if qtNow, stillQueued := p.queued[id]; !stillQueued || qtNow != qt {
			wake := p.releaseBusyLocked(claimKey)
			p.mu.Unlock()
			cancel()
			p.wg.Done()
			if wake != nil {
				wake()
			}
			continue
		}

		// Last line of defense against two RunDownloads sharing one
		// destination — a same-ID active entry registered during the claim
		// window already owns the ID, so this claim is voided. The queued entry
		// is ours; drop it so it cannot linger as a ghost, then settle the
		// wait-group count like the refusal above.
		if _, occupied := p.downloads[id]; occupied {
			delete(p.queued, id)
			p.removeQueueOrderLocked(id)
			wake := p.releaseBusyLocked(claimKey)
			p.mu.Unlock()
			cancel()
			p.wg.Done()
			if wake != nil {
				wake()
			}
			utils.Debug("Scheduler: claim for %s dropped: ID already has an active download", id)
			continue
		}

		// Ensure Runtime is initialized before exposing to GetAll
		if qt.cfg.Runtime == nil {
			qt.cfg.Runtime = types.DefaultRuntimeConfig()
		}

		// Register active download
		ad := &activeDownload{
			config: qt.cfg,
			cancel: cancel,
			done:   make(chan struct{}),
		}
		if ad.config.ProgressState != nil {
			progress.CfgProgress(&ad.config).SetCancelFunc(cancel)
		}
		ad.running.Store(true)

		ad.config = qt.cfg
		delete(p.queued, id)
		p.removeQueueOrderLocked(id)
		p.downloads[id] = ad
		if slot, held := p.busy[claimKey]; held {
			slot.ad = ad
		}

		// Make a local copy for RunDownload to mutate safely; the checkpoint
		// must be detached too or worker mutation would alias ad.config.
		localCfg := ad.config
		localCfg.HLSResume = localCfg.HLSResume.Clone()
		p.mu.Unlock()

		err := RunDownload(ctx, &localCfg)
		ad.running.Store(false)
		close(ad.done) // unblock any Cancel caller waiting for this worker to exit

		if g := p.workerDoneGate.Load(); g != nil {
			(*g)()
		}

		// Sync back mutated fields cleanly under lock — only when the slot is
		// still ours; the write target is this ad's own config.
		p.mu.Lock()
		if p.downloads[localCfg.ID] == ad {
			ad.config.TotalSize = localCfg.TotalSize
			ad.config.SupportsRange = localCfg.SupportsRange
			ad.config.RangeAcquisitionMode = localCfg.RangeAcquisitionMode
			ad.config.SkipServerProbe = localCfg.SkipServerProbe
			ad.config.Runtime = localCfg.Runtime
			ad.config.Strategy = localCfg.Strategy
			ad.config.HLSResume = localCfg.HLSResume.Clone()
		}
		p.mu.Unlock()

		// Logic:
		// 1. If err == nil (successful download): Clear stale pause, mark Done, remove from downloads.
		// 2. If paused (ErrPaused or pause requested): Keep in downloads for potential resume.
		// 3. If other error: Retry or emit EventError.

		isPaused := localCfg.ProgressState != nil && progress.CfgProgress(&localCfg).IsPaused()

		// Clear "Pausing" transition state now that worker has exited
		if localCfg.ProgressState != nil {
			progress.CfgProgress(&localCfg).SetPausing(false)
		}

		isPauseResult := errors.Is(err, types.ErrPaused) || (isPaused && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)))

		if err == nil {
			// Physical success takes precedence over late-arriving pause signals.
			// Clear any stale paused flag, mark Done, and remove from tracking maps.
			if localCfg.ProgressState != nil {
				prog := progress.CfgProgress(&localCfg)
				prog.Paused.Store(false)
				prog.Done.Store(true)
			}
			p.mu.Lock()
			// Delete only the slot this worker still owns — a same-ID
			// replacement registered after our claim keeps its own entry and
			// its limiter.
			if p.downloads[localCfg.ID] == ad {
				delete(p.downloads, localCfg.ID)
				delete(p.downloadLimiters, localCfg.ID)
			}
			p.mu.Unlock()
		} else if isPauseResult {
			utils.Debug("Scheduler: Download %s paused cleanly", localCfg.ID)
			// The concurrent downloader sends DownloadPausedMsg itself via handlePause().
			// When a single-threaded download is paused, RunDownload returns an error,
			// and the pool fills the gap.
			if !errors.Is(err, types.ErrPaused) && localCfg.ProgressCh != nil {
				var downloaded int64
				var rateLimit int64
				var rateLimitSet bool
				var workers int
				var minChunkSize int64
				if localCfg.ProgressState != nil {
					downloaded = progress.CfgProgress(&localCfg).Bytes.Downloaded.Load()
				}
				if localCfg.Runtime != nil {
					workers = localCfg.Runtime.Workers
					minChunkSize = localCfg.Runtime.MinChunkSize
				}
				rateLimit, rateLimitSet = localCfg.RateLimit, localCfg.RateLimitSet
				safeSendProgress(localCfg.ProgressCh, types.DownloadEvent{
					Type:         types.EventPaused,
					DownloadID:   localCfg.ID,
					Filename:     localCfg.Filename,
					Downloaded:   downloaded,
					RateLimit:    rateLimit,
					RateLimitSet: rateLimitSet,
					Workers:      workers,
					MinChunkSize: minChunkSize,
				}, p.progressDone)
			}
		} else {
			p.mu.Lock()
			// Delete the slot only when this worker still owns it; a same-ID
			// replacement registered during the run keeps its entry.
			ownedSlot := p.downloads[localCfg.ID] == ad
			if ownedSlot {
				delete(p.downloads, localCfg.ID)
			}
			// Any surviving same-ID entry belongs to a replacement that
			// arrived while we were running — our stale task must neither
			// requeue over a queued replacement nor emit events for a foreign
			// active owner.
			_, slotTaken := p.queued[localCfg.ID]
			foreignActive := p.downloads[localCfg.ID] != nil

			// A requeue mints a fresh opportunity token: the old admission must
			// not carry over, and stale keys for the finished claim must never
			// alias the new queueing round. On token exhaustion the task takes
			// the terminal path below — no opportunity, no leak.
			retryable := shouldRetryFailedDownload(err, p.isShuttingDown, qt.retries) && !slotTaken && !foreignActive
			var newToken uint64
			tokenOK := true
			if retryable {
				newToken, tokenOK = p.allocTokenLocked()
				if !tokenOK {
					utils.Debug("Scheduler: retry for %s cannot mint a new opportunity: admission token space exhausted", localCfg.ID)
				}
			}
			if retryable && tokenOK {
				qt.retries++
				// Defer eligibility instead of blocking this worker; the
				// retry timer wakes waitForTask at retryAt.
				retryDelay := time.Second * time.Duration(qt.retries)
				qt.retryAt = time.Now().Add(retryDelay)
				qt.inFlight = true // Keep in-flight while sending progress outside lock
				// Detach the checkpoint so the requeued entry cannot alias
				// the worker-local slices the next run may still mutate.
				localCfg.HLSResume = localCfg.HLSResume.Clone()
				qt.cfg = localCfg
				qt.token = newToken
				qt.admitted = false
				p.queued[localCfg.ID] = qt

				p.removeQueueOrderLocked(localCfg.ID)
				p.queueOrder = append(p.queueOrder, localCfg.ID)

				p.mu.Unlock()

				if localCfg.ProgressCh != nil {
					var workers int
					var minChunkSize int64
					var downloaded int64
					if localCfg.ProgressState != nil {
						downloaded = progress.CfgProgress(&localCfg).Bytes.Downloaded.Load()
					}
					if localCfg.Runtime != nil {
						workers = localCfg.Runtime.Workers
						minChunkSize = localCfg.Runtime.MinChunkSize
					}
					safeSendProgress(localCfg.ProgressCh, types.DownloadEvent{
						Type:         types.EventQueued,
						DownloadID:   localCfg.ID,
						Filename:     localCfg.Filename,
						URL:          localCfg.URL,
						DestPath:     localCfg.DestPath,
						Mirrors:      localCfg.Mirrors,
						Downloaded:   downloaded,
						Total:        localCfg.TotalSize,
						RateLimit:    localCfg.RateLimit,
						RateLimitSet: localCfg.RateLimitSet,
						Workers:      workers,
						MinChunkSize: minChunkSize,
					}, p.progressDone)
				}

				p.mu.Lock()
				// Identity check: the requeued entry must still be the task this
				// worker owns. A same-ID replacement belongs to a different claim.
				if current, ok := p.queued[localCfg.ID]; ok && current == qt {
					qt.inFlight = false

					now := time.Now()
					var earliestPending time.Time
					for _, qid := range p.queueOrder {
						if t, ok := p.queued[qid]; ok && !t.inFlight && !t.retryAt.IsZero() {
							if t.retryAt.After(now) {
								if earliestPending.IsZero() || t.retryAt.Before(earliestPending) {
									earliestPending = t.retryAt
								}
							}
						}
					}
					p.armRetryTimerLocked(earliestPending)
				} else {
					// GracefulShutdown, Cancel, or Pause removed it while we were
					// unlocked. Since we were in-flight, they didn't decrement
					// wg, so we must; never overwrite a same-ID replacement.
					p.wg.Done()
				}
				wake := p.releaseBusyLocked(claimKey)
				p.taskCond.Broadcast()
				p.mu.Unlock()
				if wake != nil {
					wake()
				}
				continue
			}

			if slotTaken || foreignActive {
				// The same-ID replacement owns this ID and its event stream;
				// settle our own wait-group count silently — no EventQueued or
				// EventError from a stale task may leak into it. The limiter
				// entry also belongs to the replacement, so it is kept.
				wake := p.releaseBusyLocked(claimKey)
				p.wg.Done()
				p.taskCond.Broadcast()
				p.mu.Unlock()
				if wake != nil {
					wake()
				}
				utils.Debug("Scheduler: worker for %s settles stale claim: ID taken by same-ID replacement", localCfg.ID)
				continue
			}

			// All retries exhausted (or error is permanent/canceled): collect event
			// metadata under the lock, then send outside it so a full progress
			// channel cannot block the scheduler mutex.
			//
			// FORK-PATCH: User cancel / deadline must clean up without EventError —
			// LifecycleManager.Cancel already publishes EventRemoved; treating
			// context.Canceled as a failure would race notify + error DB writes.
			isCancel := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
			var errEvent *types.DownloadEvent
			if !isCancel && localCfg.ProgressCh != nil {
				var finalDestPath string
				var finalFilename string
				if localCfg.ProgressState != nil {
					p := progress.CfgProgress(&localCfg)
					finalDestPath = p.GetDestPath()
					finalFilename = p.GetFilename()
				}
				if finalDestPath == "" {
					finalDestPath = resolveDestPath(&localCfg)
				}
				if finalFilename == "" {
					finalFilename = localCfg.Filename
				}
				errEvent = &types.DownloadEvent{
					Type:       types.EventError,
					DownloadID: localCfg.ID,
					Filename:   finalFilename,
					DestPath:   finalDestPath,
					Err:        err,
				}
				if localCfg.ProgressState != nil {
					if pending := progress.CfgProgress(&localCfg).TakePendingResumeState(); pending != nil {
						errEvent.State = pending
						if pending.Downloaded > 0 {
							errEvent.Downloaded = pending.Downloaded
						}
					}
				}
			}

			if !isCancel && localCfg.ProgressState != nil {
				progress.CfgProgress(&localCfg).SetError(err)
			}
			if ownedSlot {
				delete(p.downloadLimiters, localCfg.ID)
			}
			p.mu.Unlock()

			// Send outside the lock: safeSendProgress may block on a full
			// channel and must not hold p.mu while doing so.
			if errEvent != nil {
				safeSendProgress(localCfg.ProgressCh, *errEvent, p.progressDone)
			}
		}
		// If paused, we keep it in downloads map for potential resume.
		// Shared tail for success/pause/terminal-error: the physical claim is
		// settled exactly once, after all event emission and sync-back.
		p.mu.Lock()
		wake := p.releaseBusyLocked(claimKey)
		p.mu.Unlock()
		p.wg.Done()
		if wake != nil {
			wake()
		}
	}
}

// GetStatus returns the status of an active download
func (p *Scheduler) GetStatus(id string) *types.DownloadStatus {
	var adURL, adFilename, adDestPath string
	var adRateLimitBps int64
	var adRateLimitSet bool
	var adState *progress.DownloadProgress

	p.mu.RLock()
	ad, exists := p.downloads[id]
	qCfg, qExists := p.queued[id]
	var queuedCfg types.DownloadRecord
	if qExists {
		queuedCfg = qCfg.cfg
	}
	if exists {
		adURL = ad.config.URL
		adFilename = ad.config.Filename
		adDestPath = ad.config.DestPath
		adRateLimitBps = ad.config.RateLimit
		adRateLimitSet = ad.config.RateLimitSet
		adState = progress.CfgProgress(&ad.config)
	}
	p.mu.RUnlock()

	if !exists && !qExists {
		return nil
	}

	if qExists {
		return &types.DownloadStatus{
			ID:           id,
			URL:          queuedCfg.URL,
			Filename:     queuedCfg.Filename,
			DestPath:     resolveDestPath(&queuedCfg),
			Status:       "queued",
			Downloaded:   0,
			TotalSize:    0, // Metadata not yet fetched
			RateLimit:    queuedCfg.RateLimit,
			RateLimitSet: queuedCfg.RateLimitSet,
		}
	}

	state := adState
	if state == nil {
		return nil
	}

	// Use state filename/destpath if available (thread-safe)
	filename := adFilename
	if str := state.GetFilename(); str != "" {
		filename = str
	}

	// Calculate progress and speed (thread-safe)
	downloaded, totalSize, _, sessionElapsed, _, sessionStart := state.GetProgress()

	status := &types.DownloadStatus{
		ID:           id,
		URL:          adURL,
		Filename:     filename,
		TotalSize:    totalSize,
		Downloaded:   downloaded,
		Status:       "downloading",
		RateLimit:    adRateLimitBps,
		RateLimitSet: adRateLimitSet,
	}
	if dp := state.GetDestPath(); dp != "" {
		status.DestPath = dp
	} else {
		status.DestPath = adDestPath
	}

	if state.IsPausing() {
		status.Status = "pausing"
	} else if state.IsPaused() {
		status.Status = "paused"
	} else if state.Done.Load() {
		status.Status = "completed"
	}

	if err := state.GetError(); err != nil {
		status.Status = "error"
		status.Error = err.Error()
	}

	// Calculate progress
	if status.TotalSize > 0 {
		status.Progress = float64(status.Downloaded) * 100 / float64(status.TotalSize)
	}

	// Calculate speed (bytes/s) only for active downloads.
	if status.Status == "downloading" {
		sessionDownloaded := downloaded - sessionStart
		if sessionElapsed.Seconds() > 0 && sessionDownloaded > 0 {
			bytesPerSec := float64(sessionDownloaded) / sessionElapsed.Seconds()
			status.Speed = bytesPerSec
		}
	}

	return status
}

// FORK-PATCH: GetWorkerStats returns per-worker telemetry for a download.
func (p *Scheduler) GetWorkerStats(id string) []types.WorkerSnapshot {
	p.mu.RLock()
	ad, exists := p.downloads[id]
	p.mu.RUnlock()

	if !exists || ad == nil || ad.config.ProgressState == nil {
		return nil
	}

	return progress.CfgProgress(&ad.config).GetWorkerStats()
}

// FORK-PATCH: ScaleWorkers adjusts the worker count for a download.
func (p *Scheduler) ScaleWorkers(id string, delta int) int {
	p.mu.RLock()
	ad, exists := p.downloads[id]
	p.mu.RUnlock()
	if !exists || ad == nil || ad.config.ProgressState == nil {
		return 0
	}
	return progress.CfgProgress(&ad.config).ScaleWorkers(delta)
}

// FORK-PATCH: KillWorker hard-interrupts a specific worker of a download.
func (p *Scheduler) KillWorker(id string, workerID int) bool {
	p.mu.RLock()
	ad, exists := p.downloads[id]
	p.mu.RUnlock()
	if !exists || ad == nil || ad.config.ProgressState == nil {
		return false
	}
	return progress.CfgProgress(&ad.config).KillWorker(workerID)
}

// FORK-PATCH: SetSlowWorkerThreshold applies a runtime threshold override.
func (p *Scheduler) SetSlowWorkerThreshold(id string, v float64) {
	p.mu.RLock()
	ad, exists := p.downloads[id]
	p.mu.RUnlock()
	if !exists || ad == nil || ad.config.ProgressState == nil {
		return
	}
	progress.CfgProgress(&ad.config).SetSlowWorkerThreshold(v)
}

// FORK-PATCH: DrainWorker marks a specific worker of a download as draining.
func (p *Scheduler) DrainWorker(id string, workerID int) bool {
	p.mu.RLock()
	ad, exists := p.downloads[id]
	p.mu.RUnlock()
	if !exists || ad == nil || ad.config.ProgressState == nil {
		return false
	}
	return progress.CfgProgress(&ad.config).DrainWorker(workerID)
}

// FORK-PATCH: Test helper for monitor-side telemetry collection tests.
// NewSchedulerForTesting creates a Scheduler with a pre-populated downloads map.
// Queued/cond fields are initialized so Add after ExtractPausedConfig is safe
// without starting worker goroutines.
func NewSchedulerForTesting(configs map[string]types.DownloadRecord) *Scheduler {
	downloads := make(map[string]*activeDownload, len(configs))
	for id, cfg := range configs {
		downloads[id] = &activeDownload{config: cfg}
	}
	p := &Scheduler{
		downloads:        downloads,
		queued:           make(map[string]*queuedTask),
		busy:             make(map[AdmissionKey]*busySlot),
		downloadLimiters: make(map[string]*transport.RateLimiter),
		progressDone:     make(chan struct{}),
		progressCh:       make(chan types.DownloadEvent, 8),
	}
	p.taskCond = sync.NewCond(&p.mu)
	return p
}

// GracefulShutdown pauses all downloads and waits for them to save state
func (p *Scheduler) GracefulShutdown() {
	p.shutdownOnce.Do(func() {
		p.PauseAll()

		// Discard all queued-but-not-yet-started downloads so that idle workers
		// do not pick them up and begin downloading after shutdown is initiated.
		// Workers already guard against this with the p.queued check at loop entry,
		// so clearing the map here is sufficient; draining taskChan is belt-and-suspenders.
		p.mu.Lock()
		p.armRetryTimerLocked(time.Time{})
		for id, qt := range p.queued {
			delete(p.queued, id)
			if !qt.inFlight {
				p.wg.Done()
			}
		}
		p.queueOrder = nil
		p.isShuttingDown = true
		p.taskCond.Broadcast()
		p.mu.Unlock()

		// Wait for any downloads in "Pausing" state to finish transitioning
		// This ensures we don't exit while a database write is pending/active
		ticker := time.NewTicker(gracefulShutdownPausePollInterval)
		defer ticker.Stop()
		start := time.Now()
		warned := false

		for {
			p.mu.Lock()
			stillPausing := false
			for _, ad := range p.downloads {
				if ad.config.ProgressState != nil && progress.CfgProgress(&ad.config).IsPausing() {
					// If no worker is running this download anymore, pausing is stale.
					// Normalize it so shutdown can proceed.
					if !ad.running.Load() {
						progress.CfgProgress(&ad.config).SetPausing(false)
						continue
					}
					stillPausing = true
					break
				}
			}
			p.mu.Unlock()

			if !stillPausing {
				break
			}

			if !warned && time.Since(start) >= gracefulShutdownPauseSoftTimeout {
				utils.Debug("GracefulShutdown: downloads still pausing after %v, continuing to wait for durable pause", gracefulShutdownPauseSoftTimeout)
				warned = true
			}
			if time.Since(start) >= gracefulShutdownPauseHardTimeout {
				utils.Debug("GracefulShutdown: forcing exit from pausing wait after hard timeout %v", gracefulShutdownPauseHardTimeout)
				break
			}
			<-ticker.C
		}

		// Signal that progressCh must no longer be sent to, so that
		// safeSendProgress calls in workers can abort if the channel is full.
		// This must happen before wg.Wait() to break deadlocks where a worker
		// is blocked on a full channel and preventing wg from completing.
		close(p.progressDone)

		p.wg.Wait() // Blocks until all workers call Done()

		// A worker's physical-claim release trails its last wg.Done by only a
		// few instructions; drain the committed set so a snapshot taken after
		// shutdown observes a fully-settled pool.
		p.mu.Lock()
		for len(p.busy) > 0 {
			p.taskCond.Wait()
		}
		p.mu.Unlock()
	})
}
