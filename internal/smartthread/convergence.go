package smartthread

import (
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

// gidInfo records the domain, scope, and envKey of a previously active gid,
// for bandwidthRelease domain+scope+env matching.
type gidInfo struct {
	Domain string
	Scope  string
	EnvKey string
}

// domainStats aggregates per-limitKey retry and worker counts for domain-level N_max.
type domainStats struct {
	retryCountSum int32
	activeWorkers int
	tasksInDomain int
}

// CompatKind identifies a retained pre-S1 occupancy claim source. A claim is
// compatibility protection only — it is not activation and not admission.
type CompatKind uint8

const (
	// CompatNone: no legacy claim; plain eligibility + real-lease rules apply.
	CompatNone CompatKind = iota
	// CompatSurgeClaim: legacy Surge Target claim — synthetic/empty-status
	// active at add or an early resume-ack active. Counts on empty/active
	// statuses until real activation, an authoritative non-active status,
	// or a terminal boundary ends it.
	CompatSurgeClaim
	// CompatSurgeResume: resume-hold lineage — the paused resume-hold write
	// or its post-transition transfer. Counts on paused/active statuses so
	// a just-resumed task keeps its Target floor until the real start.
	CompatSurgeResume
	// CompatAria2Add: bounded confirmed-AddUri placeholder for non-Surge
	// tasks, anchored at AddAnchorAt for aria2AddPlaceholderWindow.
	CompatAria2Add
)

// TrackedTaskInfo is a minimal scalar view of monitor.TrackedTask for
// convergence, BandwidthLedger seeding, and unified occupancy evaluation.
type TrackedTaskInfo struct {
	GID             string
	Status          string
	Scope           string
	Domain          string
	EnvKey          string
	IsKeepAlive     bool
	CompletedLength int64
	MinChunk        int64 // from ThreadParams.MinSize, for blackout zone detection
	TotalLength     int64 // from TrackedTask.TotalLength, for completed-task guard
	ThreadCount     int
	TargetBandwidth int64
	AllocatedAt     time.Time
	TelemetryBps    int64 // Cache DownloadSpeed pad; measured value incl. 0
	TelemetryValid  bool  // TelemetryBps holds a measured cache value
	RawBps          int64 // Surge engine raw sample; used only when MacroReady
	MacroReady      bool  // Surge: Convergence macroReady latch
	// ActivatedAt is the tracker's real activation fact for this run; zero
	// for old active snapshots (telemetry-only). Getters never infer it.
	ActivatedAt time.Time
	// FirstProgressAt is the first completed growth above this activation's
	// baseline; zero when no growth has been recorded yet.
	FirstProgressAt time.Time
	// CompatClaim marks the retained legacy claim source, if any.
	CompatClaim CompatKind
	// AddAnchorAt is the confirmed-add anchor for CompatAria2Add. It is
	// fixed at first establishment; reads/writes never extend it.
	AddAnchorAt time.Time
	// AdmissionReserved is the explicit admitted-not-started fact for a
	// future admission pipeline. The claim reuses ActivatedAt as its anchor
	// and the same 45s/15s lease — a reservation without an anchor or past
	// its lease loses the reservation floor and falls back to ordinary
	// eligibility (a retained waiting claim can still apply). No production
	// writer sets it in S1.
	AdmissionReserved bool
}

// TrackerProvider provides task tracking data to the convergence ticker.
type TrackerProvider interface {
	GetActiveTrackedTasks() []TrackedTaskInfo
	GetScopeAndEnv(gid string) (scope, domain, envKey string, ok bool)
}

// OccupancyProvider supplies the unified occupancy snapshot (active tasks
// plus retained claims and add placeholders). Implemented by
// monitor.trackerAdapter. It MUST NOT call back into ConvergenceTicker
// methods that acquire c.mu — tick overlays Surge raw/ready from c.states
// itself. When unset, tick degrades to the active snapshot so existing
// tests keep working without an extra provider.
type OccupancyProvider interface {
	GetOccupancyTrackedTasks() []TrackedTaskInfo
}

// TelemetryProvider provides per-worker telemetry snapshots.
type TelemetryProvider interface {
	Get(gid string) []types.WorkerSnapshot
}

// PeakEfficiencyRecorder records peak speed/worker pairs into the task tracker.
// Implemented by monitor.trackerAdapter (embedded *TaskTracker).
type PeakEfficiencyRecorder interface {
	RecordPeakEfficiency(gid string, peak int64, workers int)
}

// RateLimitChecker reports whether a download has an active positive bandwidth cap.
// Returns (bps, true) only when an effective per-download rate limit with bps > 0
// is in force. Callers must treat bps <= 0 as not limited (Surge "0"/unlimited).
type RateLimitChecker interface {
	GetRateLimit(gid string) (int64, bool)
}

// Convergence state machine phases
const (
	phaseStable     = 0
	phaseSettling   = 1
	phaseFrozen     = 2
	phaseProbingUp  = 3 // rising cycle (Probe-Up in progress)
	phaseCeilingHit = 4 // rebound after failed up-probe, sleep lock
	phaseFloorHit   = 5 // rebound after failed down-probe, sleep lock (symmetric kneeFrozen)
)

type convergenceState struct {
	// Active probing state machine
	phase                int
	probeCooldown        int
	probeMomentum        bool
	probeBaseline        int64
	probeBaselineWorkers int // worker count before probe-down (for Marginal Drop Ratio)
	lastStep             int
	frozenCooldown       int
	kneeFrozen           bool
	bestEff              int64
	prevCompleted        int64
	prevSampleAt         time.Time
	sustainCount         int

	// Shadow copies of tracker's PeakSpeed/PeakThreadCount for ratchet decisions
	peakSpeed   int64
	peakWorkers int

	// Probe-Up state
	probeUpBaseline        int64 // rawBps before ScaleUp
	probeUpBaselineWorkers int   // worker count before ScaleUp
	probeUpDelta           int   // workers requested for this Probe-Up (+1 or jump)
	ceilingMemory          int64 // rawBps at CeilingHit moment
	ceilingHitCount        int   // consecutive ticks rawBps > ceilingMemory*1.05

	// FloorHit state (symmetric extension of kneeFrozen)
	floorMemory   int64 // rawBps at FloorHit moment
	floorHitCount int   // consecutive ticks rawBps < floorMemory*0.90

	// bandwidthRelease delay compensation: last known rawBps, set after
	// rawBps is computed in processTask.
	lastRawBps int64

	// macroReady latches true on first D2 (or blackout) rawBps sample.
	// Distinguishes cold (never sampled) from hot-zero (sampled 0 B/s).
	// Survives window invalidation; cleared only when state is deleted.
	macroReady bool

	// tail blackout zone: permanently suppresses all macro decisions when
	// totalRemaining < activeWorkers × effectiveMinChunk. Not reset on
	// active-set change — permanent until gid disappears from active list.
	blackout bool
}

// pendingRelease tracks domain bandwidth awaiting deferred settle (resource, not
// a beneficiary voucher). Keyed by limitKey(scope, domain) on ConvergenceTicker.
type pendingRelease struct {
	envKey            string // arming disappearance env (election filter)
	preDeathDomainBps int64  // capability lower-bound evidence, NOT a voucher
	// deferrals counts dirty ticks where the elected beneficiary sat inside
	// the perturbation margin. Clean ticks do not decrement it; same-domain
	// re-arms keep it; a fresh arm starts at zero.
	deferrals int
}

// Cap on dirty-window deferrals before dropping the arm (degrade to Probe-Up).
const pendingReleaseMaxDeferrals = 3

type ConvergenceTicker struct {
	engine       *rpc.HybridEngine
	tracker      TrackerProvider
	occupancy    OccupancyProvider // nil → occupancy snapshot = active snapshot
	telemetry    TelemetryProvider
	peakRecorder PeakEfficiencyRecorder
	rateChecker  RateLimitChecker
	limits       *ServerLimitStore

	mu                sync.Mutex
	states            map[string]*convergenceState
	prevActiveGids    map[string]gidInfo         // gid → gidInfo, for bandwidth borrowing diff
	rotationCounter   int                        // fair rotation for beneficiary election
	prevActiveSpeeds  map[string]int64           // gid → last known rawBps, for delay compensation
	domainUnlockTicks map[string]int             // limitKey → consecutive zero-retry ticks, for domain-level N_max unlock
	pendingReleases   map[string]*pendingRelease // limitKey → deferred bandwidth handover
	// prevActiveOccupancy stores each sampled-active GID's unified occupancy
	// contribution as of the previous completed tick — zero values included.
	// nil means no sampled tick has completed yet (bootstrap); a tick that saw
	// an empty set stores a non-nil empty map. Exit-side perturbation uses the
	// stored values verbatim and never re-evaluates old leases at a new now.
	prevActiveOccupancy map[string]int64
	stopChan            chan struct{}
	stopOnce            sync.Once
	wg                  sync.WaitGroup

	// Injected at construction to avoid background goroutine reads of config.
	interval       time.Duration
	maxConnections int
}

func NewConvergenceTicker(
	engine *rpc.HybridEngine,
	tracker TrackerProvider,
	telemetry TelemetryProvider,
	peakRecorder PeakEfficiencyRecorder,
	rateChecker RateLimitChecker,
	convergenceIntervalSec int,
	maxConnections int,
) *ConvergenceTicker {
	interval := convergenceInterval
	if convergenceIntervalSec > 0 {
		interval = time.Duration(convergenceIntervalSec) * time.Second
	}
	if maxConnections <= 0 {
		maxConnections = defaultMaxConnections()
	}
	if maxConnections > config.MaxConnectionsUpper {
		maxConnections = config.MaxConnectionsUpper
	}
	return &ConvergenceTicker{
		engine:            engine,
		tracker:           tracker,
		telemetry:         telemetry,
		peakRecorder:      peakRecorder,
		rateChecker:       rateChecker,
		limits:            GetDefaultServerLimits(),
		states:            make(map[string]*convergenceState),
		prevActiveGids:    make(map[string]gidInfo),
		prevActiveSpeeds:  make(map[string]int64),
		domainUnlockTicks: make(map[string]int),
		pendingReleases:   make(map[string]*pendingRelease),
		stopChan:          make(chan struct{}),
		interval:          interval,
		maxConnections:    maxConnections,
	}
}

// SetOccupancyProvider wires the unified occupancy snapshot used by the tick
// for domain occupancy. Must be called before Start; nil is a safe no-op.
func (c *ConvergenceTicker) SetOccupancyProvider(p OccupancyProvider) {
	if c == nil || p == nil {
		return
	}
	c.occupancy = p
}

func (c *ConvergenceTicker) Start() {
	c.wg.Add(1)
	go c.run()
}

func (c *ConvergenceTicker) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopChan)
	})
	c.wg.Wait()
}

func (c *ConvergenceTicker) run() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.currentInterval())
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.tick()
			ticker.Reset(c.currentInterval())
		}
	}
}

func (c *ConvergenceTicker) currentInterval() time.Duration {
	return c.interval
}

func (c *ConvergenceTicker) clampPositiveDelta(currentWorkers, requested int) int {
	if requested <= 0 {
		return requested
	}
	if c.maxConnections <= 0 {
		return 0
	}
	headroom := c.maxConnections - currentWorkers
	if headroom <= 0 {
		return 0
	}
	return min(requested, headroom)
}

// pendingScale collects batched scale operations to execute after all tasks are processed.
type pendingScale struct {
	gid    string
	scope  string
	domain string
	envKey string
	delta  int
}

func (c *ConvergenceTicker) tick() {
	c.tickAt(time.Now())
}

// tickAt runs one convergence pass at the supplied instant. One now per tick:
// lease checks, provider evaluation, D2 sampling and release/settle all share
// it so TTL edges cannot flicker mid-tick.
func (c *ConvergenceTicker) tickAt(now time.Time) {
	if c.engine == nil || c.tracker == nil || c.telemetry == nil {
		return
	}

	activeTasks := c.tracker.GetActiveTrackedTasks()
	activeGids := make(map[string]gidInfo) // gid → gidInfo
	var pending []pendingScale
	pendingGids := make(map[string]bool) // GIDs already scaled this tick
	// approvedDelta: same-tick positive ScaleUp accumulator.
	// Keys: approvedScopeKey / approvedDomainKey (env-blind BW) /
	// approvedNMaxKey (env-blind N_max pending, aligned with limitKey).
	approvedDelta := make(map[string]int)

	// Sampled-active: genuinely-started tasks on both engines, deduped by GID
	// (first row wins). Waiting claims, admission reservations and synthetic
	// actives stay out of the membership diff and Surge processing — they
	// still occupy budget through the wider occupancy snapshot below.
	sampled := make([]TrackedTaskInfo, 0, len(activeTasks))
	seenSampled := make(map[string]struct{}, len(activeTasks))
	for _, t := range activeTasks {
		if !sampledActiveTask(t) {
			continue
		}
		if _, dup := seenSampled[t.GID]; dup {
			continue
		}
		seenSampled[t.GID] = struct{}{}
		sampled = append(sampled, t)
	}
	// surgeTasks copies pre-overlay structs: raw/ready overlays later land on
	// sampled[i]/occTasks[i] only — never read .RawBps/.MacroReady here.
	var surgeTasks []TrackedTaskInfo
	for _, t := range sampled {
		activeGids[t.GID] = gidInfo{Domain: t.Domain, Scope: t.Scope, EnvKey: t.EnvKey}
		if strings.HasPrefix(t.GID, "sg_") {
			surgeTasks = append(surgeTasks, t)
		}
	}
	// One telemetry observation per Surge task this tick: margin evaluation
	// and processTask share it so the same worker count gates both.
	surgeStats := make([][]types.WorkerSnapshot, len(surgeTasks))
	for i := range surgeTasks {
		surgeStats[i] = c.telemetry.Get(surgeTasks[i].GID)
	}

	// Unified occupancy snapshot: includes Aria2 tasks, waiting claims, and
	// add placeholders — wider than the active set but never widens
	// processTask. The provider returns a per-call snapshot safe to overlay.
	var occTasks []TrackedTaskInfo
	if c.occupancy != nil {
		occTasks = c.occupancy.GetOccupancyTrackedTasks()
	}
	if occTasks == nil {
		// Fallback to the active snapshot (unit tests); copy so the Surge
		// raw overlay below cannot alias-mutate the active-set input.
		occTasks = make([]TrackedTaskInfo, len(activeTasks))
		copy(occTasks, activeTasks)
	}

	// Freeze everything the diff and per-task margins need in one lock:
	// previous membership maps, pre-D2 state raw/ready overlaid onto the
	// sampled inputs, and pre-D2 verdict-margin parameters.
	c.mu.Lock()
	prevGids := c.prevActiveGids
	prevOcc := c.prevActiveOccupancy
	for i := range sampled {
		if !strings.HasPrefix(sampled[i].GID, "sg_") {
			continue
		}
		if s, ok := c.states[sampled[i].GID]; ok && s != nil {
			sampled[i].RawBps = s.lastRawBps
			sampled[i].MacroReady = s.macroReady
		}
	}
	margins := make([]verdictMarginRef, len(surgeTasks))
	for i := range surgeTasks {
		margins[i] = verdictMargin(c.states[surgeTasks[i].GID], len(surgeStats[i]))
	}
	var netDelta int64
	var changedDomains map[string]bool
	var perturbUnknown bool
	if prevOcc == nil {
		// Bootstrap: no completed sampled tick yet — no membership
		// perturbation, and no domain counter reset on first sight.
		changedDomains = make(map[string]bool)
	} else {
		netDelta, changedDomains, perturbUnknown = occupancyDelta(sampled, activeGids, prevGids, prevOcc, now)
	}
	c.mu.Unlock()

	// A task is affected only when the net occupancy shift reaches half the
	// throughput its pending decision window can move. Margin-less windows
	// degrade conservatively once perturbation is non-zero or unknown.
	var affected map[string]bool
	if perturbUnknown || netDelta > 0 {
		affected = make(map[string]bool, len(surgeTasks))
		for i := range surgeTasks {
			margin, ok := probeMarginThreshold(margins[i])
			if perturbUnknown || !ok || float64(netDelta) >= margin {
				affected[surgeTasks[i].GID] = true
			}
		}
	}

	// Domain Pre-pass: aggregate retryCountSum and activeWorkers per limitKey.
	dStats := make(map[string]*domainStats)
	for i, task := range surgeTasks {
		if task.Scope == "" || task.Domain == "" {
			continue
		}
		// Align processTask complete guard: 100% tasks still listed must not
		// inflate N_max / release clamp. TotalLength==0 (unknown size) counts.
		if task.TotalLength > 0 && task.CompletedLength >= task.TotalLength {
			continue
		}
		stats := surgeStats[i]
		if len(stats) == 0 {
			continue
		}
		lk := limitKey(task.Scope, task.Domain)
		ds, ok := dStats[lk]
		if !ok {
			ds = &domainStats{}
			dStats[lk] = ds
		}
		ds.activeWorkers += len(stats)
		ds.tasksInDomain++
		for _, ws := range stats {
			ds.retryCountSum += ws.RetryCount
		}
	}

	// Domain-level N_max fuse and unlock under c.mu (domainUnlockTicks is
	// shared state). A domain whose member set actually changed also resets
	// its unlock confirmation — without recounting this dirty tick as
	// confirmation one. The fuse stays independent of per-task dirty windows.
	c.mu.Lock()
	for lk, ds := range dStats {
		threshold := int32(connErrorThreshold)
		if scaled := int32(2 * ds.tasksInDomain); scaled > threshold {
			threshold = scaled
		}
		switch {
		case ds.retryCountSum >= threshold:
			if _, hasLimit := c.limits.GetNMax(lk); !hasLimit {
				c.limits.SetNMax(lk, ds.activeWorkers)
				log.Printf("[convergence] server-limit-fuse: limitKey=%s N_max=%d locked (retryCountSum=%d tasksInDomain=%d)",
					lk, ds.activeWorkers, ds.retryCountSum, ds.tasksInDomain)
			}
			c.domainUnlockTicks[lk] = 0
		case changedDomains[lk]:
			c.domainUnlockTicks[lk] = 0
		case ds.retryCountSum == 0:
			nMax, hasLimit := c.limits.GetNMax(lk)
			if hasLimit {
				c.domainUnlockTicks[lk]++
				if c.domainUnlockTicks[lk] >= lockUnlockConfirmTicks {
					c.limits.Clear(lk)
					c.domainUnlockTicks[lk] = 0
					log.Printf("[convergence] server-limit-unlock: limitKey=%s N_max=%d cleared (zero retries for %d ticks, %d workers)",
						lk, nMax, lockUnlockConfirmTicks, ds.activeWorkers)
				}
			} else {
				c.domainUnlockTicks[lk] = 0
			}
		default:
			c.domainUnlockTicks[lk] = 0
		}
	}

	// Prune domainUnlockTicks entries for domains no longer active this tick.
	for lk := range c.domainUnlockTicks {
		if _, ok := dStats[lk]; !ok {
			delete(c.domainUnlockTicks, lk)
		}
	}

	// Prune pendingReleases when the domain has no remaining active tasks.
	for lk := range c.pendingReleases {
		if _, ok := dStats[lk]; !ok {
			delete(c.pendingReleases, lk)
		}
	}
	c.mu.Unlock()

	// Domain macro occupancy (env-blind, keyed by limitKey). Overlay Surge
	// raw/ready from c.states under lock — never via the public lock-taking
	// accessors — then evaluate the same per-task occupancy helper outside
	// the lock. Still-listed complete peers may contribute while excluded
	// from dStats workers (physical occupancy).
	c.mu.Lock()
	for i := range occTasks {
		if !strings.HasPrefix(occTasks[i].GID, "sg_") {
			continue
		}
		if s, ok := c.states[occTasks[i].GID]; ok && s != nil {
			occTasks[i].RawBps = s.lastRawBps
			occTasks[i].MacroReady = s.macroReady
		}
	}
	c.mu.Unlock()
	domainMacroBps := make(map[string]int64)
	for _, task := range occTasks {
		if task.Scope == "" || task.Domain == "" {
			continue
		}
		if occ := TaskOccupancy(task, now); occ > 0 {
			lk := limitKey(task.Scope, task.Domain)
			domainMacroBps[lk] = saturatingAdd64(domainMacroBps[lk], occ)
		}
	}

	for i, task := range surgeTasks {
		if ps, ok := c.processTaskStats(task, surgeStats[i], affected[task.GID], approvedDelta, dStats, domainMacroBps, now); ok {
			if ps.delta > 0 {
				approvedDelta[approvedScopeKey(ps.scope, ps.envKey)] += ps.delta
				if ps.domain != "" {
					approvedDelta[approvedDomainKey(ps.scope, ps.domain)] += ps.delta
					approvedDelta[approvedNMaxKey(ps.scope, ps.domain)] += ps.delta
				}
			}
			pending = append(pending, ps)
			pendingGids[ps.gid] = true
		}
	}

	// Update prevActiveSpeeds for bandwidthRelease delay compensation.
	// Preserve lastRawBps > 0 guard so dirty ticks do not wipe pre-death
	// domain sums needed for pendingReleases.preDeathDomainBps.
	// Post-D2, the same state overlay also feeds next tick's exit-side
	// occupancy: every sampled-active GID (both engines) gets a stored
	// value, zero included, evaluated at this tick's now.
	c.mu.Lock()
	for _, task := range surgeTasks {
		if s, ok := c.states[task.GID]; ok && s.lastRawBps > 0 {
			c.prevActiveSpeeds[task.GID] = s.lastRawBps
		}
	}
	for i := range sampled {
		if !strings.HasPrefix(sampled[i].GID, "sg_") {
			continue
		}
		if s, ok := c.states[sampled[i].GID]; ok && s != nil {
			sampled[i].RawBps = s.lastRawBps
			sampled[i].MacroReady = s.macroReady
		}
	}
	c.mu.Unlock()
	nextPrevOcc := make(map[string]int64, len(sampled))
	for i := range sampled {
		nextPrevOcc[sampled[i].GID] = TaskOccupancy(sampled[i], now)
	}

	// Deferred settle of previously armed domains first, then arm new
	// disappearances (never settle an entry created earlier in this call).
	pending = append(pending, c.settlePendingReleases(surgeTasks, pendingGids, approvedDelta, dStats, affected, occTasks, now)...)
	pending = append(pending, c.bandwidthRelease(surgeTasks, activeGids, pendingGids, approvedDelta, dStats, domainMacroBps, now)...)

	// Self-cleanup: remove states for GIDs no longer sampled-active.
	// prevActiveSpeeds must prune by activeGids (not only via states): RemoveTask
	// deletes states early while intentionally keeping speeds until this tick.
	c.mu.Lock()
	for gid := range c.states {
		if _, ok := activeGids[gid]; !ok {
			delete(c.states, gid)
		}
	}
	for gid := range c.prevActiveSpeeds {
		if _, ok := activeGids[gid]; !ok {
			delete(c.prevActiveSpeeds, gid)
		}
	}
	// Replace previous membership + occupancy snapshots only after this
	// tick's diff, settle and release consumers have run.
	c.prevActiveGids = activeGids
	c.prevActiveOccupancy = nextPrevOcc
	c.mu.Unlock()

	// Batch: execute all pending scale operations after processing all tasks.
	// D5: check ScaleWorkers return value — if 0, don't advance state.
	for _, ps := range pending {
		actual := c.engine.ScaleWorkers(ps.gid, ps.delta)
		if actual == 0 {
			// Engine couldn't execute the scale — don't advance state
			c.mu.Lock()
			if s, ok := c.states[ps.gid]; ok {
				switch {
				case ps.delta > 0 && s.kneeFrozen:
					// Rebound failed — preserve frozen state so cooldown can run
					// and eventually clear kneeFrozen. Otherwise kneeFrozen orphans:
					// ScaleUp permanently suppressed, probe-down continues past knee.
					s.phase = phaseFrozen
					s.frozenCooldown = frozenCooldownCycles
				case ps.delta < 0 && s.phase == phaseCeilingHit:
					// CeilingHit rebound refused by engine — preserve lock, cooldown already set
				default:
					s.phase = phaseStable
					s.probeBaseline = 0
					s.probeBaselineWorkers = 0
					s.probeUpBaseline = 0
					s.probeUpBaselineWorkers = 0
					s.probeUpDelta = 0
					s.lastStep = 0
				}
			}
			c.mu.Unlock()
			log.Printf("[convergence] scale-workers no-op: gid=%s delta=%d (engine returned 0)", ps.gid, ps.delta)
		} else if ps.delta > 0 && actual > 0 && actual < ps.delta {
			c.adjustProbeUpDeltaAfterPartialScale(ps.gid, actual)
		}
	}
}

// adjustProbeUpDeltaAfterPartialScale keeps GainRatio aligned when the engine
// landed fewer workers than requested on a probing jump.
func (c *ConvergenceTicker) adjustProbeUpDeltaAfterPartialScale(gid string, actual int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.states[gid]; ok && s != nil && s.phase == phaseProbingUp && s.probeUpDelta > actual {
		s.probeUpDelta = actual
	}
}

// bandwidthRelease detects tasks that disappeared since last tick, arms a
// pendingRelease for deferred settle, and elects a same-domain beneficiary for
// a free +1 (non-probing) to prevent thundering-herd ScaleUp.
// GIDs already scaled this tick (pendingGids) are skipped.
func (c *ConvergenceTicker) bandwidthRelease(activeTasks []TrackedTaskInfo, activeGids map[string]gidInfo, pendingGids map[string]bool, approvedDelta map[string]int, dStats map[string]*domainStats, domainMacroBps map[string]int64, now time.Time) []pendingScale {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Snapshot disappearances under lock. Unlock-before-provider must not
	// range over live prevActiveGids — another tick mutation (or future
	// writer) must not race mid-iteration. RemoveTask only clears states now
	// (SPEC-243); prev* stay until this tick replaces/prunes them.
	type disappearance struct {
		gid   string
		info  gidInfo
		speed int64
	}
	var disappeared []disappearance
	for gid, info := range c.prevActiveGids {
		if _, ok := activeGids[gid]; ok {
			continue
		}
		disappeared = append(disappeared, disappearance{
			gid:   gid,
			info:  info,
			speed: c.prevActiveSpeeds[gid],
		})
	}

	var releases []pendingScale
	for _, d := range disappeared {
		gid, info := d.gid, d.info
		// Only Surge donors arm/elect handovers: Aria2 exits feed the net
		// perturbation and domain membership diff, never bandwidth releases.
		if !strings.HasPrefix(gid, "sg_") {
			continue
		}
		// Unknown ownership: empty Domain must not wildcard across domains.
		if info.Domain == "" {
			continue
		}

		// Arm: env-matched pre-death domain sum (includes disappeared gid).
		var preDeath int64
		for pgid, pinfo := range c.prevActiveGids {
			if pinfo.Domain == info.Domain && pinfo.Scope == info.Scope && pinfo.EnvKey == info.EnvKey {
				preDeath += c.prevActiveSpeeds[pgid]
			}
		}
		if info.Scope != "" {
			lk := limitKey(info.Scope, info.Domain)
			if existing, ok := c.pendingReleases[lk]; ok && existing != nil {
				// Same limitKey re-arm: raise capability floor; keep first envKey.
				if preDeath > existing.preDeathDomainBps {
					existing.preDeathDomainBps = preDeath
				}
			} else {
				c.pendingReleases[lk] = &pendingRelease{
					envKey:            info.EnvKey,
					preDeathDomainBps: preDeath,
				}
			}
		}

		elected, ok := c.electReleaseBeneficiary(activeTasks, info.Scope, info.Domain, info.EnvKey, pendingGids, approvedDelta, dStats, 1)
		if !ok {
			continue
		}

		scope, domain, envKey := elected.scope, elected.domain, elected.envKey
		domainOcc := int64(0)
		if domainMacroBps != nil {
			domainOcc = domainMacroBps[limitKey(scope, domain)]
		}
		// The departed gid is normally already absent from the tracker, so
		// unified occupancy no longer counts it — subtracting its last raw
		// speed here would cancel surviving peers or claims. Run the normal
		// headroom check; residual lag conservatively defers to next tick.
		// Provider (occupancy → LastRawBps) locks c.mu; must not call under
		// our lock.
		c.mu.Unlock()
		vOK := c.checkVAvailable(scope, domain, envKey, approvedDelta, domainOcc, 1, now)
		c.mu.Lock()
		// Elected state may vanish during unlock (RemoveTask).
		if c.states[elected.gid] == nil {
			continue
		}
		if !vOK {
			continue
		}
		if c.clampPositiveDelta(elected.currentWorkers, 1) <= 0 {
			continue
		}

		releases = append(releases, pendingScale{gid: elected.gid, scope: elected.scope, domain: elected.domain, envKey: elected.envKey, delta: 1})
		pendingGids[elected.gid] = true
		if approvedDelta != nil {
			approvedDelta[approvedScopeKey(elected.scope, elected.envKey)] += 1
			if elected.domain != "" {
				approvedDelta[approvedDomainKey(elected.scope, elected.domain)] += 1
				approvedDelta[approvedNMaxKey(elected.scope, elected.domain)] += 1
			}
		}
		log.Printf("[convergence] bandwidth-borrow-arm: gid=%s scope=%s envKey=%s domain=%s released by completed gid=%s (elected, %d candidates, free +1)",
			elected.gid, elected.scope, elected.envKey, elected.domain, gid, elected.candidateCount)
	}
	return releases
}

// settlePendingReleases settles previously armed domains using post-processTask
// fresh lastRawBps. Never settles using wiped arm-tick occupancy. An arm whose
// elected beneficiary sits inside this tick's perturbation margin defers in
// place — at most pendingReleaseMaxDeferrals times before the arm drops. The
// beneficiary is never swapped for a cleaner peer, and no-candidate / denied
// paths keep the arm without consuming deferrals.
func (c *ConvergenceTicker) settlePendingReleases(
	activeTasks []TrackedTaskInfo,
	pendingGids map[string]bool,
	approvedDelta map[string]int,
	dStats map[string]*domainStats,
	affected map[string]bool,
	occTasks []TrackedTaskInfo,
	now time.Time,
) []pendingScale {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.pendingReleases) == 0 {
		return nil
	}

	// Snapshot keys under lock so unlock-before-provider is safe.
	armedKeys := make([]string, 0, len(c.pendingReleases))
	for lk, pr := range c.pendingReleases {
		if pr == nil {
			continue
		}
		armedKeys = append(armedKeys, lk)
	}

	// Re-overlay Surge raw/ready onto the occupancy snapshot: processTask
	// just advanced lastRawBps, so the pre-pass values are stale. Unified
	// domain occupancy must settle on post-processTask measurements.
	for i := range occTasks {
		if !strings.HasPrefix(occTasks[i].GID, "sg_") {
			continue
		}
		if s, ok := c.states[occTasks[i].GID]; ok && s != nil {
			occTasks[i].RawBps = s.lastRawBps
			occTasks[i].MacroReady = s.macroReady
		}
	}

	var settles []pendingScale
	for _, lk := range armedKeys {
		pr := c.pendingReleases[lk]
		if pr == nil {
			continue
		}
		// Parse scope|domain from limitKey.
		parts := strings.SplitN(lk, "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			delete(c.pendingReleases, lk)
			continue
		}
		scope, domain := parts[0], parts[1]
		envKey := pr.envKey
		preDeath := pr.preDeathDomainBps

		elected, okElect := c.electReleaseBeneficiary(activeTasks, scope, domain, envKey, pendingGids, approvedDelta, dStats, 1)
		if !okElect {
			// Keep armed unless prune already dropped idle domains.
			continue
		}
		if affected[elected.gid] {
			// The elected beneficiary sits inside the dirty window: keep the
			// arm but postpone judgment — never swap to a cleaner peer, never
			// disarm on a dirty gap read. Cap postponements so a stale arm
			// cannot park forever.
			pr.deferrals++
			if pr.deferrals > pendingReleaseMaxDeferrals {
				delete(c.pendingReleases, lk)
				log.Printf("[convergence] bandwidth-borrow-drop: limitKey=%s deferrals exceeded (%d)", lk, pr.deferrals)
			}
			continue
		}

		// Fresh unified domain occupancy (env-blind): both engines, compat
		// claims and placeholders count toward the post-processTask domain
		// fill level. Beneficiary election above stays env-matched.
		var currentDomainBps int64
		for _, t := range occTasks {
			if t.Scope != scope || t.Domain != domain {
				continue
			}
			currentDomainBps = saturatingAdd64(currentDomainBps, TaskOccupancy(t, now))
		}

		targetDomainBps := preDeath
		c.mu.Unlock()
		if peak, okPeak := speedstats.GetDomainPeak(domain, scope, envKey); okPeak && peak > targetDomainBps {
			targetDomainBps = peak
		}
		c.mu.Lock()
		if c.states[elected.gid] == nil {
			continue
		}
		if targetDomainBps <= 0 {
			delete(c.pendingReleases, lk)
			continue
		}

		electedLastRaw := int64(0)
		if s := c.states[elected.gid]; s != nil {
			electedLastRaw = s.lastRawBps
		}
		cw := elected.currentWorkers
		observedEff := int64(0)
		if cw > 0 {
			observedEff = electedLastRaw / int64(cw)
		}

		gap := max(targetDomainBps-currentDomainBps, 0)

		delta := 0
		switch {
		case gap == 0:
			// Domain already filled — disarm before cold degrade to +1.
			// (observedEff<=0 + gap==0 must not keep-arm forever emitting free +1.)
			delete(c.pendingReleases, lk)
			log.Printf("[convergence] bandwidth-borrow-disarm: limitKey=%s gap=0 observedEff=%d (fills domain)",
				lk, observedEff)
			continue
		case observedEff <= 0:
			// Cold/stalled with remaining gap: degrade to status-quo +1 (not historical Calculate).
			delta = 1
		case gap < observedEff:
			// Insufficient for a full worker — disarm before ceilDiv.
			delete(c.pendingReleases, lk)
			log.Printf("[convergence] bandwidth-borrow-disarm: limitKey=%s gap=%d observedEff=%d (fills domain)",
				lk, gap, observedEff)
			continue
		default:
			desired := int(ceilDiv(gap, observedEff))
			delta = max(minInt(desired, cw), 1)
		}

		// Clamp chain: N_max → V_available → maxConnections → rateLimited.
		// V_available deny skips (keeps arm) rather than reducing delta — intentional
		// anti-overshoot; partial reduce under cold-peak allow would be riskier.
		domainWorkers := cw
		if dStats != nil {
			if ds, ok := dStats[lk]; ok {
				domainWorkers = ds.activeWorkers
			}
		}
		if headroom, hasLimit := c.domainNMaxHeadroom(scope, domain, domainWorkers, approvedDelta); hasLimit {
			if headroom <= 0 {
				continue // keep armed; no deferral bump
			}
			delta = minInt(delta, headroom)
		}
		if delta <= 0 {
			continue
		}

		domainOcc := currentDomainBps
		c.mu.Unlock()
		vOK := c.checkVAvailable(scope, domain, envKey, approvedDelta, domainOcc, delta, now)
		c.mu.Lock()
		s := c.states[elected.gid]
		if s == nil {
			continue
		}
		if !vOK {
			continue // keep armed; headroom deny is not a dirty-window deferral
		}

		delta = c.clampPositiveDelta(cw, delta)
		if delta <= 0 {
			continue
		}

		if c.rateChecker != nil {
			if bps, limited := c.rateChecker.GetRateLimit(elected.gid); limited && bps > 0 {
				continue // keep armed; no deferral bump
			}
		}

		// Jump settlement (delta > 1) enters Probe-Up for GainRatio; delta==1 stays free.
		if delta > 1 {
			s.probeUpBaseline = electedLastRaw
			s.probeUpBaselineWorkers = cw
			s.probeUpDelta = delta
			s.phase = phaseProbingUp
		}

		settles = append(settles, pendingScale{
			gid: elected.gid, scope: elected.scope, domain: elected.domain, envKey: elected.envKey, delta: delta,
		})
		pendingGids[elected.gid] = true
		if approvedDelta != nil {
			approvedDelta[approvedScopeKey(elected.scope, elected.envKey)] += delta
			if elected.domain != "" {
				approvedDelta[approvedDomainKey(elected.scope, elected.domain)] += delta
				approvedDelta[approvedNMaxKey(elected.scope, elected.domain)] += delta
			}
		}
		// Keep arm for multi-step doubling until gap==0 / gap < observedEff or prune.
		log.Printf("[convergence] bandwidth-borrow-settle: gid=%s scope=%s envKey=%s domain=%s delta=%d gap=%d observedEff=%d currentDomain=%d target=%d",
			elected.gid, elected.scope, elected.envKey, elected.domain, delta, gap, observedEff, currentDomainBps, targetDomainBps)
	}
	return settles
}

type releaseCandidate struct {
	gid            string
	scope          string
	envKey         string
	domain         string
	currentWorkers int
	candidateCount int
}

// electReleaseBeneficiary picks the lowest-worker eligible peer (fair rotation
// on ties). Caller must hold c.mu. needed is the N_max slot request (arm/settle
// election uses 1; settle further clamps delta after computation).
func (c *ConvergenceTicker) electReleaseBeneficiary(
	activeTasks []TrackedTaskInfo,
	matchScope, matchDomain, matchEnvKey string,
	pendingGids map[string]bool,
	approvedDelta map[string]int,
	dStats map[string]*domainStats,
	needed int,
) (releaseCandidate, bool) {
	if needed <= 0 {
		needed = 1
	}
	var candidates []releaseCandidate
	for _, t := range activeTasks {
		if !strings.HasPrefix(t.GID, "sg_") {
			continue
		}
		if t.Domain != matchDomain || t.Scope != matchScope || t.EnvKey != matchEnvKey {
			continue
		}
		if pendingGids[t.GID] {
			continue
		}
		if t.TotalLength > 0 && t.CompletedLength >= t.TotalLength {
			continue
		}
		cw := len(c.telemetry.Get(t.GID))
		if cw == 0 {
			continue
		}
		s := c.getOrCreateState(t.GID)
		// Skip phaseProbingUp: arm free +1 / settle jump must not corrupt an
		// in-flight GainRatio baseline (narrow same-tick race after Probe-Up).
		if s.kneeFrozen || s.phase == phaseCeilingHit || s.phase == phaseProbingUp || s.blackout {
			continue
		}
		lk := ""
		if t.Scope != "" && t.Domain != "" {
			lk = limitKey(t.Scope, t.Domain)
		}
		if lk != "" {
			// Align Probe-Up: missing dStats falls back to candidate workers.
			domainWorkers := cw
			if dStats != nil {
				if ds, ok := dStats[lk]; ok {
					domainWorkers = ds.activeWorkers
				}
			}
			if headroom, hasLimit := c.domainNMaxHeadroom(t.Scope, t.Domain, domainWorkers, approvedDelta); hasLimit {
				if headroom < needed {
					continue
				}
			}
		}
		candidates = append(candidates, releaseCandidate{
			gid:            t.GID,
			scope:          t.Scope,
			envKey:         t.EnvKey,
			domain:         t.Domain,
			currentWorkers: cw,
		})
	}
	if len(candidates) == 0 {
		return releaseCandidate{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].currentWorkers < candidates[j].currentWorkers
	})
	minWorkers := candidates[0].currentWorkers
	tiedCount := 0
	for tiedCount < len(candidates) && candidates[tiedCount].currentWorkers == minWorkers {
		tiedCount++
	}
	electedIdx := c.rotationCounter % tiedCount
	c.rotationCounter++
	elected := candidates[electedIdx]
	elected.candidateCount = len(candidates)
	return elected, true
}

// sampledActiveTask reports whether a tracker row is real engine activity for
// the two-engine membership diff and Surge processing. Waiting claims,
// admission reservations and explicit synthetic actives (a compat claim with
// no activation anchor) are excluded; legacy actives without an anchor stay
// telemetry-only members.
func sampledActiveTask(t TrackedTaskInfo) bool {
	if t.GID == "" || t.Status != "active" || t.AdmissionReserved {
		return false
	}
	if t.ActivatedAt.IsZero() && t.CompatClaim != CompatNone {
		return false
	}
	return true
}

// saturatingAddChecked accumulates non-negative occupancy like
// saturatingAdd64 but also reports when the true sum overflowed int64 — the
// caller then treats the whole perturbation window as unknown.
func saturatingAddChecked(a, b int64) (int64, bool) {
	if b < 0 {
		return a, false
	}
	sum := a + b
	if sum < a {
		return math.MaxInt64, true
	}
	return sum, false
}

// verdictMarginRef holds the pre-D2 decision parameters gating a task's
// pending probe verdict. ok=false means no trustworthy margin exists — a
// non-zero or unknown membership perturbation then counts the task
// conservatively as affected.
type verdictMarginRef struct {
	step       int
	refBps     int64
	refWorkers int
	ok         bool
}

// verdictMargin picks the parameters of the decision window a membership
// perturbation could plausibly flip: the in-flight Probe-Up, the pending
// Probe-Down (settling or the following stable+lastStep verdict tick), or the
// ordinary per-sample margin of one worker. Caller holds c.mu; workers is
// this tick's observed worker count.
func verdictMargin(s *convergenceState, workers int) verdictMarginRef {
	switch {
	case s == nil:
		return verdictMarginRef{}
	case s.phase == phaseProbingUp:
		step := s.probeUpDelta
		if step <= 0 {
			step = 1
		}
		return verdictMarginRef{
			step:       step,
			refBps:     s.probeUpBaseline,
			refWorkers: s.probeUpBaselineWorkers,
			ok:         s.probeUpBaseline > 0 && s.probeUpBaselineWorkers > 0,
		}
	case s.phase == phaseSettling || (s.phase == phaseStable && s.lastStep > 0):
		return verdictMarginRef{
			step:       s.lastStep,
			refBps:     s.probeBaseline,
			refWorkers: s.probeBaselineWorkers,
			ok:         s.lastStep > 0 && s.probeBaseline > 0 && s.probeBaselineWorkers > 0,
		}
	default:
		return verdictMarginRef{
			step:       1,
			refBps:     s.lastRawBps,
			refWorkers: workers,
			ok:         s.macroReady && s.lastRawBps > 0 && workers > 0,
		}
	}
}

// probeMarginThreshold is the occupancy shift that could plausibly flip the
// gated verdict — half the throughput the decision window can move by.
// Malformed windows report ok=false so callers can degrade conservatively.
func probeMarginThreshold(m verdictMarginRef) (float64, bool) {
	if !m.ok {
		return 0, false
	}
	margin := 0.5 * (float64(m.refBps) / float64(m.refWorkers)) * float64(m.step)
	if !(margin > 0) || math.IsInf(margin, 0) || math.IsNaN(margin) {
		return 0, false
	}
	return margin, true
}

// occupancyDelta computes the net membership perturbation between the
// previous and current sampled-active sets:
// E = |Σ entry current occupancy − Σ exit previous-tick occupancy|.
// Entries evaluate TaskOccupancy on this tick's overlaid inputs; exits use
// the stored previous values verbatim. unknown is set when an exit lacks its
// stored value or a side saturates. changedDomains collects every limitKey
// whose member set actually changed — including zero-net perturbations and
// same-GID scope/domain reclassification; env-only churn is not membership.
func occupancyDelta(sampled []TrackedTaskInfo, activeGids map[string]gidInfo,
	prevGids map[string]gidInfo, prevOcc map[string]int64, now time.Time,
) (delta int64, changedDomains map[string]bool, unknown bool) {
	changedDomains = make(map[string]bool)
	var entrySum, exitSum int64
	var ovf bool
	for i := range sampled {
		t := sampled[i]
		prev, existed := prevGids[t.GID]
		if !existed {
			entrySum, ovf = saturatingAddChecked(entrySum, TaskOccupancy(t, now))
			unknown = unknown || ovf
			if t.Scope != "" && t.Domain != "" {
				changedDomains[limitKey(t.Scope, t.Domain)] = true
			}
			continue
		}
		if prev.Scope != t.Scope || prev.Domain != t.Domain {
			if prev.Scope != "" && prev.Domain != "" {
				changedDomains[limitKey(prev.Scope, prev.Domain)] = true
			}
			if t.Scope != "" && t.Domain != "" {
				changedDomains[limitKey(t.Scope, t.Domain)] = true
			}
		}
	}
	for gid, prev := range prevGids {
		if _, still := activeGids[gid]; still {
			continue
		}
		v, ok := prevOcc[gid]
		if !ok {
			unknown = true
		} else {
			exitSum, ovf = saturatingAddChecked(exitSum, v)
			unknown = unknown || ovf
		}
		if prev.Scope != "" && prev.Domain != "" {
			changedDomains[limitKey(prev.Scope, prev.Domain)] = true
		}
	}
	if entrySum >= exitSum {
		delta = entrySum - exitSum
	} else {
		delta = exitSum - entrySum
	}
	return delta, changedDomains, unknown
}

// clampedBps converts a non-negative bytes/sec measurement to int64, clamping
// overflow to MaxInt64 instead of letting float→int wrap produce a fake zero.
func clampedBps(v float64) int64 {
	if v <= 0 {
		return 0
	}
	if v >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(v)
}

// domainNMaxHeadroom returns remaining N_max slots for scope|domain after
// counting domainWorkers and same-tick approvedNMaxKey pending.
// hasLimit false → unlimited (caller skips clamp). Empty scope/domain → no limit.
func (c *ConvergenceTicker) domainNMaxHeadroom(scope, domain string, domainWorkers int, approvedDelta map[string]int) (headroom int, hasLimit bool) {
	if c == nil || scope == "" || domain == "" {
		return 0, false
	}
	nMax, ok := c.limits.GetNMax(limitKey(scope, domain))
	if !ok {
		return 0, false
	}
	pending := 0
	if approvedDelta != nil {
		pending = approvedDelta[approvedNMaxKey(scope, domain)]
	}
	return nMax - domainWorkers - pending, true
}

// computeVThreadAvg returns the estimated per-thread average throughput for
// V_available checks. Tries domain-specific p75 first, falls back to
// scope-wide p75 with 0.5x penalty, then clamps to minThreadEfficiency.
func (c *ConvergenceTicker) computeVThreadAvg(domain, scope, envKey string) int64 {
	vThreadAvg, ok := speedstats.GetRecentPeakByDomain(domain, scope, envKey)
	if !ok {
		vThreadAvg, ok = speedstats.GetRecentPeakByScope(scope, envKey)
		if ok {
			vThreadAvg /= 2
		}
	}
	if !ok || vThreadAvg < minThreadEfficiency {
		vThreadAvg = minThreadEfficiency
	}
	return vThreadAvg
}

// checkVAvailable returns true when both scope and domain bandwidth headroom
// allow adding needed workers (intersection). Missing peak on a dimension
// allows that dimension (cold-start must not lock). Same-tick approvedDelta
// counts already-approved workers via approvedScopeKey / approvedDomainKey
// (not multiplied by needed).
//
// Lock topology: must NOT be called while holding c.mu. The occupancy
// provider (MacroOccupancyByScope) may call LastRawBps which locks c.mu.
func (c *ConvergenceTicker) checkVAvailable(scope, domain, envKey string, approvedDelta map[string]int, domainOcc int64, needed int, now time.Time) bool {
	if needed <= 0 {
		return false
	}
	vThreadAvg := c.computeVThreadAvg(domain, scope, envKey)
	neededHeadroom := int64(needed) * vThreadAvg

	scopeOK := true
	if globalPeak, ok := speedstats.GetGlobalPeak(scope, envKey); ok && globalPeak > 0 {
		activeBw := activeBandwidthProvider(scope, envKey, now)
		effectiveScope := activeBw + int64(approvedDelta[approvedScopeKey(scope, envKey)])*vThreadAvg
		scopeOK = globalPeak-effectiveScope >= neededHeadroom
	}

	domainOK := true
	if domain != "" {
		if domainPeak, ok := speedstats.GetDomainPeak(domain, scope, envKey); ok && domainPeak > 0 {
			effectiveDomain := domainOcc + int64(approvedDelta[approvedDomainKey(scope, domain)])*vThreadAvg
			domainOK = domainPeak-effectiveDomain >= neededHeadroom
		}
	}
	return scopeOK && domainOK
}

// processTask evaluates a single task and returns a pending scale operation
// if one is needed. affected marks a task inside this tick's net membership-
// perturbation margin: it still runs D2 measurement and advances its sample
// baseline, but the pending probe verdict is cancelled unjudged and D3 /
// smart-unlock / new-probe decisions are skipped for the dirty tick.
// dStats / domainMacroBps / approvedDelta may be nil in unit tests (degrade
// safely). now is the tick-captured evaluation instant shared by occupancy
// checks and sampling.
func (c *ConvergenceTicker) processTask(task TrackedTaskInfo, affected bool, approvedDelta map[string]int, dStats map[string]*domainStats, domainMacroBps map[string]int64, now time.Time) (pendingScale, bool) {
	return c.processTaskStats(task, c.telemetry.Get(task.GID), affected, approvedDelta, dStats, domainMacroBps, now)
}

func (c *ConvergenceTicker) processTaskStats(task TrackedTaskInfo, stats []types.WorkerSnapshot, affected bool, approvedDelta map[string]int, dStats map[string]*domainStats, domainMacroBps map[string]int64, now time.Time) (pendingScale, bool) {
	gid := task.GID

	if len(stats) == 0 {
		return pendingScale{}, false
	}

	// Completed-task guard: skip all convergence logic when the task has reached
	// 100% completion but hasn't yet left the active list. TotalLength == 0 means
	// unknown size — skip guard, fall through to normal logic.
	if task.TotalLength > 0 && task.CompletedLength >= task.TotalLength {
		return pendingScale{}, false
	}

	currentWorkers := len(stats)

	// Domain-level N_max key. Empty when scope/domain is missing — callers
	// guard with lk != "" before querying the Store.
	lk := ""
	if task.Scope != "" && task.Domain != "" {
		lk = limitKey(task.Scope, task.Domain)
	}

	// Edge Case 4: positive per-download cap suppresses Probe-Up/Probe-Down only.
	// D2 rawBps / D3 RecordPeakEfficiency still run under a true rate limit.
	rateLimited := false
	if c.rateChecker != nil {
		if bps, limited := c.rateChecker.GetRateLimit(gid); limited && bps > 0 {
			rateLimited = true
		}
	}

	c.mu.Lock()
	s := c.getOrCreateState(gid)
	defer c.mu.Unlock()

	// Tail blackout zone: per-gid permanent sleep when totalRemaining <
	// activeWorkers × effectiveMinChunk. Blackout is permanent — never reset.
	if s.blackout {
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = now
		return pendingScale{}, false
	}

	// Only evaluate blackout when chunk telemetry is populated. Workers
	// without chunk data (ChunkLength==0) haven't received assignments yet,
	// so they're excluded from the threshold — counting them would inflate
	// the bar and risk a permanent false trigger during mixed-state startup.
	hasChunkData := false
	chunkWorkers := 0
	totalRemaining := int64(0)
	for _, ws := range stats {
		if ws.ChunkLength > 0 {
			hasChunkData = true
			chunkWorkers++
		}
		remaining := (ws.ChunkStart + ws.ChunkLength) - ws.ChunkOffset
		if remaining > 0 {
			totalRemaining += remaining
		}
	}
	if hasChunkData {
		effectiveMinChunk := task.MinChunk
		if effectiveMinChunk <= 0 {
			effectiveMinChunk = minChunkSize
		}
		if totalRemaining < int64(chunkWorkers)*effectiveMinChunk {
			s.blackout = true
			log.Printf("[convergence] blackout-triggered: gid=%s totalRemaining=%d chunkWorkers=%d minChunk=%d",
				gid, totalRemaining, chunkWorkers, effectiveMinChunk)

			// Final measurement before permanent sleep — same validity rules
			// as D2: a real window only (no fake raw from a broken baseline).
			// A dirty or anomalous tick skips the terminal record but still
			// latches usable raw/ready evidence (SPEC-180 occupancy).
			if !s.prevSampleAt.IsZero() {
				if dt := now.Sub(s.prevSampleAt); dt > 0 &&
					s.prevCompleted >= 0 && task.CompletedLength >= s.prevCompleted {
					finalRawBps := clampedBps(float64(task.CompletedLength-s.prevCompleted) / dt.Seconds())
					s.lastRawBps = finalRawBps
					s.macroReady = true
					if !affected && finalRawBps > 0 && currentWorkers > 0 && c.peakRecorder != nil {
						c.peakRecorder.RecordPeakEfficiency(gid, finalRawBps, currentWorkers)
					}
				}
			}

			s.prevCompleted = task.CompletedLength
			s.prevSampleAt = now
			return pendingScale{}, false
		}
	}

	// D2 runs before any verdict work: raw throughput and macroReady must
	// stay fresh even on a dirty tick — occupancy consumers and the next
	// tick's exit-side values read them.
	if s.prevSampleAt.IsZero() {
		// First sample — store baseline, no decision. A zero prevCompleted
		// with a real timestamp is a valid baseline, not a missing one.
		// Clamp a pathological negative like the anomaly re-base does.
		s.prevCompleted = max(task.CompletedLength, 0)
		s.prevSampleAt = now
		return pendingScale{}, false
	}

	dt := now.Sub(s.prevSampleAt)
	if dt <= 0 {
		// Broken time window: keep the last measurement, re-base from a
		// non-negative endpoint, and drop any pending verdict unjudged —
		// the baseline it spanned no longer exists.
		s.cancelPendingVerdict()
		s.prevCompleted = max(task.CompletedLength, 0)
		s.prevSampleAt = now
		return pendingScale{}, false
	}
	if s.prevCompleted < 0 || task.CompletedLength < 0 || task.CompletedLength < s.prevCompleted {
		// Counter discontinuity (regression or negative endpoint): publish a
		// measured zero for occupancy consumers, re-base to a non-negative
		// endpoint, and produce no knee/ceiling verdict from a broken window.
		s.lastRawBps = 0
		s.macroReady = true
		s.cancelPendingVerdict()
		s.prevCompleted = max(task.CompletedLength, 0)
		s.prevSampleAt = now
		return pendingScale{}, false
	}
	rawBps := clampedBps(float64(task.CompletedLength-s.prevCompleted) / dt.Seconds())
	s.lastRawBps = rawBps
	s.macroReady = true

	if affected {
		// Dirty window: cancel the pending probe verdict unjudged and skip
		// settling/D3/smart-unlock/new probes this round while keeping all
		// long-term memory. Cooldowns tick down once, saturated at zero — a
		// lock whose cooldown lands on zero here holds until the next clean
		// tick performs the normal expiry transition.
		s.cancelPendingVerdict()
		if s.probeCooldown > 0 {
			s.probeCooldown--
		}
		if s.frozenCooldown > 0 {
			s.frozenCooldown--
		}
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = now
		return pendingScale{}, false
	}

	// D2: Settling — transition to stable, no decision this tick.
	// Do NOT overwrite probeBaseline — it must retain the pre-probe throughput
	// for the marginal drop ratio evaluation in the next tick.
	if s.phase == phaseSettling {
		s.phase = phaseStable
		s.sustainCount = 0 // C3: reset so peakSustainCycles stability is required after settling
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = now
		return pendingScale{}, false
	}

	// D3: Peak efficiency ratchet with bestEff-anchored guard.
	newEff := rawBps / int64(currentWorkers)
	if newEff > s.bestEff {
		s.bestEff = newEff
	}
	s.sustainCount++

	clean := s.sustainCount >= peakSustainCycles
	if clean && s.bestEff > 0 {
		guardEff := int64(float64(s.bestEff) * efficiencyGuardBand)
		// Use state-local shadow for ratchet decision
		peak := s.peakSpeed
		peakWorkers := int64(s.peakWorkers)

		adopted := false
		switch {
		case peakWorkers == 0:
			peak = rawBps
			peakWorkers = int64(currentWorkers)
			adopted = true
		case newEff >= guardEff:
			if float64(rawBps) > float64(peak)*peakRaiseBand {
				// Higher throughput at acceptable efficiency — adopt
				if rawBps > peak {
					peak = rawBps
				}
				peakWorkers = int64(currentWorkers)
				adopted = true
			} else if currentWorkers < int(peakWorkers) && float64(rawBps) >= float64(peak)*peakSpeedGuardBand {
				// Fewer workers at speed ≥ 90% of peak — adopt (prevents 缝合怪:
				// never let a fraction-of-peak speed claim the peak's worker slot)
				if rawBps > peak {
					peak = rawBps
				}
				peakWorkers = int64(currentWorkers)
				adopted = true
			}
		case float64(rawBps) > float64(peak)*peakRaiseBand:
			// Absolute throughput up but efficiency crashed — only update peak, keep peakWorkers
			peak = rawBps
			adopted = true
		}

		if adopted {
			s.peakSpeed = peak
			s.peakWorkers = int(peakWorkers)
			if c.peakRecorder != nil {
				c.peakRecorder.RecordPeakEfficiency(gid, peak, int(peakWorkers))
			}
		}
	}

	// Probe-Up evaluation: assess last Tick's ScaleUp using GainRatio.
	// GainRatio = ActualGain / ExpectedGain; success falls through to the
	// Probe-Up trigger, failure rebounds and enters the CeilingHit lock.
	if s.phase == phaseProbingUp && s.probeUpBaseline > 0 && s.probeUpBaselineWorkers > 0 {
		actualGain := float64(rawBps-s.probeUpBaseline) / float64(s.probeUpBaseline)
		if actualGain < 0 {
			actualGain = 0
		}
		probeDelta := s.probeUpDelta
		if probeDelta <= 0 {
			probeDelta = 1
		}
		expectedGain := float64(probeDelta) / float64(s.probeUpBaselineWorkers)
		gainRatio := actualGain / expectedGain

		if gainRatio >= gainRatioThreshold {
			// Up-probe success — return to stable, fall through to Probe-Up trigger for next +1
			s.phase = phaseStable
			log.Printf("[convergence] probe-up-success: gid=%s raw=%d baseline=%d gainRatio=%.2f",
				gid, rawBps, s.probeUpBaseline, gainRatio)
			// Fall through to Probe-Up trigger below
		} else {
			// Ceiling hit — rebound -(probeUpDelta+1)/2 + CeilingHit lock
			rebound := max((probeDelta+1)/2, 1)
			if c.peakRecorder != nil && rawBps > 0 && currentWorkers > 0 {
				c.peakRecorder.RecordPeakEfficiency(gid, rawBps, currentWorkers)
			}
			s.ceilingMemory = rawBps
			s.ceilingHitCount = 0
			s.phase = phaseCeilingHit
			s.frozenCooldown = ceilingHitCooldownCycles
			log.Printf("[convergence] ceiling-hit: gid=%s raw=%d baseline=%d gainRatio=%.2f rebound=-%d",
				gid, rawBps, s.probeUpBaseline, gainRatio, rebound)
			s.probeUpBaseline = 0
			s.probeUpBaselineWorkers = 0
			s.probeUpDelta = 0
			s.prevCompleted = task.CompletedLength
			s.prevSampleAt = now
			return pendingScale{gid: gid, scope: task.Scope, domain: task.Domain, envKey: task.EnvKey, delta: -rebound}, true
		}
	}

	// D4 step 4: Evaluate last probe using Marginal Drop Ratio.
	// ExpectedDrop = lastStep / probeBaselineWorkers (if threads are equally productive,
	// cutting lastStep out of N should drop speed proportionally).
	// ActualDrop = 1 - rawBps / probeBaseline.
	// DropRatio = ActualDrop / ExpectedDrop.
	// DropRatio ≤ 0.5 → actual drop far less than expected → plateau zone → success.
	// DropRatio > 0.5 → actual drop matches/exceeds expected → linear zone → knee crossed.
	if s.lastStep > 0 && s.phase == phaseStable && s.probeBaseline > 0 && s.probeBaselineWorkers > 0 {
		actualDrop := 1.0 - float64(rawBps)/float64(s.probeBaseline)
		if actualDrop < 0 {
			actualDrop = 0 // speed increased — definitely plateau
		}
		expectedDrop := float64(s.lastStep) / float64(s.probeBaselineWorkers)
		dropRatio := actualDrop / expectedDrop

		if dropRatio <= marginalDropThreshold {
			// Success — actual drop far less than expected, threads were redundant
			s.probeMomentum = true
			s.probeCooldown = 0
			log.Printf("[convergence] probe-success: gid=%s raw=%d baseline=%d dropRatio=%.2f momentum=true",
				gid, rawBps, s.probeBaseline, dropRatio)
		} else {
			// Knee crossed — actual drop matches/exceeds expected, threads were productive
			rebound := max(
				// ceil(lastStep/2)
				(s.lastStep+1)/2, 1)
			// N_max clamp: rebound must not push domain workers above N_max.
			// Knee rebound intentionally skips V_available (known non-goal): it
			// restores productive workers after Probe-Down overshoot, not a free ScaleUp.
			domainWorkers := currentWorkers
			if dStats != nil {
				if ds, ok := dStats[lk]; ok {
					domainWorkers = ds.activeWorkers
				}
			}
			if headroom, hasLimit := c.domainNMaxHeadroom(task.Scope, task.Domain, domainWorkers, approvedDelta); hasLimit {
				if headroom <= 0 {
					rebound = 0
				} else if rebound > headroom {
					rebound = headroom
				}
			}
			if rebound > 0 {
				rebound = c.clampPositiveDelta(currentWorkers, rebound)
			}
			s.kneeFrozen = true
			s.probeMomentum = false
			s.phase = phaseFloorHit
			s.frozenCooldown = floorHitCooldownCycles
			s.floorMemory = rawBps
			s.floorHitCount = 0
			if c.peakRecorder != nil && rawBps > 0 && currentWorkers > 0 {
				c.peakRecorder.RecordPeakEfficiency(gid, rawBps, currentWorkers)
			}
			s.prevCompleted = task.CompletedLength
			s.prevSampleAt = now
			s.lastStep = 0
			if rebound > 0 {
				log.Printf("[convergence] knee-crossed: gid=%s raw=%d baseline=%d dropRatio=%.2f rebound=+%d floorHit",
					gid, rawBps, s.probeBaseline, dropRatio, rebound)
				return pendingScale{gid: gid, scope: task.Scope, domain: task.Domain, envKey: task.EnvKey, delta: rebound}, true
			}
			nMax, _ := c.limits.GetNMax(lk)
			log.Printf("[convergence] knee-crossed: gid=%s raw=%d baseline=%d dropRatio=%.2f rebound=0 (N_max=%d clamp) floorHit",
				gid, rawBps, s.probeBaseline, dropRatio, nMax)
			return pendingScale{}, false
		}
		s.lastStep = 0
	}

	// CeilingHit processing: smart unlock debounce + cooldown countdown.
	// Smart unlock: consecutive >= lockUnlockConfirmTicks ticks with
	// rawBps > ceilingMemory*ceilingUnlockRatio → server sped up → unlock.
	// Cooldown expiry: no speedup detected → return to stable, allow Probe-Down.
	if s.phase == phaseCeilingHit {
		if s.ceilingMemory > 0 && float64(rawBps) > float64(s.ceilingMemory)*ceilingUnlockRatio {
			s.ceilingHitCount++
			if s.ceilingHitCount >= lockUnlockConfirmTicks {
				s.phase = phaseStable
				s.ceilingHitCount = 0
				s.sustainCount = 0
				s.kneeFrozen = false
				s.probeMomentum = false
				s.probeCooldown = probeIntervalCycles
				log.Printf("[convergence] ceiling-unlocked: gid=%s raw=%d ceiling=%d (server sped up)",
					gid, rawBps, s.ceilingMemory)
				s.ceilingMemory = 0
				s.frozenCooldown = 0
				s.prevCompleted = task.CompletedLength
				s.prevSampleAt = now
				return pendingScale{}, false
			}
		} else {
			s.ceilingHitCount = 0
		}
		s.frozenCooldown--
		if s.frozenCooldown <= 0 {
			s.phase = phaseStable
			s.sustainCount = 0
			s.kneeFrozen = false
			s.probeMomentum = false
			s.probeCooldown = probeIntervalCycles
			s.ceilingHitCount = 0
			log.Printf("[convergence] ceiling-cooldown-expired: gid=%s allowing probe-down", gid)
			s.ceilingMemory = 0
		}
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = now
		return pendingScale{}, false
	}

	// FloorHit processing: smart unlock debounce + cooldown countdown.
	// Smart unlock: consecutive >= lockUnlockConfirmTicks ticks with
	// rawBps < floorMemory*floorUnlockRatio → network congested → reactivate down-probing.
	// Cooldown expiry: no slowdown detected → return to stable, allow Probe-Up.
	if s.phase == phaseFloorHit {
		if s.floorMemory > 0 && float64(rawBps) < float64(s.floorMemory)*floorUnlockRatio {
			s.floorHitCount++
			if s.floorHitCount >= lockUnlockConfirmTicks {
				s.phase = phaseStable
				s.floorHitCount = 0
				s.sustainCount = 0
				s.kneeFrozen = false
				s.probeMomentum = false
				s.probeCooldown = probeIntervalCycles
				log.Printf("[convergence] floor-unlocked: gid=%s raw=%d floor=%d (network congested)",
					gid, rawBps, s.floorMemory)
				s.floorMemory = 0
				s.frozenCooldown = 0
				s.prevCompleted = task.CompletedLength
				s.prevSampleAt = now
				return pendingScale{}, false
			}
		} else {
			s.floorHitCount = 0
		}
		s.frozenCooldown--
		if s.frozenCooldown <= 0 {
			s.phase = phaseStable
			s.sustainCount = 0
			s.kneeFrozen = false
			s.probeMomentum = false
			s.probeCooldown = probeIntervalCycles
			s.floorHitCount = 0
			log.Printf("[convergence] floor-cooldown-expired: gid=%s allowing probe-up", gid)
			s.floorMemory = 0
		}
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = now
		return pendingScale{}, false
	}

	// D4 step 6: Frozen cooldown
	if s.phase == phaseFrozen {
		s.frozenCooldown--
		if s.frozenCooldown <= 0 {
			s.phase = phaseStable
			s.kneeFrozen = false
			s.probeMomentum = false
			s.probeCooldown = probeIntervalCycles
			s.sustainCount = 0 // C3: reset so peakSustainCycles stability is required after frozen expires
			log.Printf("[convergence] frozen-expired: gid=%s allowing cold re-probe", gid)
		}
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = now
		return pendingScale{}, false
	}

	// D4 step 5: Initiate next probe (phase==stable, currentWorkers > probeFloor).
	// Before the first ratchet adoption (peakWorkers==0), require
	// peakSustainCycles stable samples so probeBaseline is not based on a
	// single potentially-transient measurement.

	// Probe-Up trigger: when stable, preheated, bestEff established, and
	// current efficiency near best → +1 up-probe. Trigger conditions:
	//   1. phase == phaseStable
	//   2. bestEff > 0 (prevents premature probing before efficiency baseline)
	//   3. newEff >= bestEff*probeUpEffThreshold (current efficiency within 5% of best)
	//   4. preheated: peakWorkers > 0 || (sustainCount >= peakSustainCycles && bestEff > 0)
	//   5. N_max not exceeded: domainWorkers + domainApprovedDelta + 1 <= nMax
	//   6. V_available sufficient: scope ∩ domain headroom (cold peak → allow that dim)
	//   7. !rateLimited
	//   8. !probeMomentum (don't interrupt an active down-probe combo)
	if s.phase == phaseStable && s.bestEff > 0 && !s.probeMomentum {
		newEff := rawBps / int64(currentWorkers)
		preheated := s.peakWorkers > 0 || (s.sustainCount >= peakSustainCycles && s.bestEff > 0)
		if newEff >= int64(float64(s.bestEff)*probeUpEffThreshold) && preheated {
			// N_max check — domain-aggregated workers + same-tick domain approvedDelta.
			domainWorkers := currentWorkers
			if dStats != nil {
				if ds, ok := dStats[lk]; ok {
					domainWorkers = ds.activeWorkers
				}
			}
			headroom, hasLimit := c.domainNMaxHeadroom(task.Scope, task.Domain, domainWorkers, approvedDelta)
			if !hasLimit || headroom >= 1 {
				scope, domain, envKey := task.Scope, task.Domain, task.EnvKey
				domainOcc := int64(0)
				if domainMacroBps != nil {
					domainOcc = domainMacroBps[limitKey(scope, domain)]
				}
				// Provider (occupancy → LastRawBps) locks c.mu; release before call.
				c.mu.Unlock()
				vAvailable := c.checkVAvailable(scope, domain, envKey, approvedDelta, domainOcc, 1, now)
				c.mu.Lock()
				s = c.states[gid]
				if s == nil {
					return pendingScale{}, false
				}
				if vAvailable && !rateLimited {
					delta := c.clampPositiveDelta(currentWorkers, 1)
					if delta > 0 {
						if c.peakRecorder != nil && rawBps > 0 && currentWorkers > 0 {
							c.peakRecorder.RecordPeakEfficiency(gid, rawBps, currentWorkers)
						}
						s.probeUpBaseline = rawBps
						s.probeUpBaselineWorkers = currentWorkers
						s.probeUpDelta = delta
						s.phase = phaseProbingUp
						s.prevCompleted = task.CompletedLength
						s.prevSampleAt = now
						log.Printf("[convergence] probe-up: gid=%s workers=%d baseline=%d",
							gid, currentWorkers, rawBps)
						return pendingScale{gid: gid, scope: task.Scope, domain: task.Domain, envKey: task.EnvKey, delta: delta}, true
					}
				}
			}
		}
	}

	if s.phase == phaseStable {
		probeFloor := probeFloorWorkers
		if currentWorkers > probeFloor && (s.peakWorkers > 0 || s.sustainCount >= peakSustainCycles) {
			shouldProbe := false
			if s.probeMomentum && s.probeCooldown == 0 {
				shouldProbe = true
			} else {
				if s.probeCooldown > 0 {
					s.probeCooldown--
				}
				if s.probeCooldown == 0 {
					shouldProbe = true
				}
			}

			if shouldProbe && !rateLimited {
				// Skip probe-down when rawBps == 0: a zero-speed task is either
				// dead or stalled. Killing workers won't help and causes a cold
				// probe-down cycle (probeBaseline=0 dead zone).
				if rawBps == 0 {
					s.prevCompleted = task.CompletedLength
					s.prevSampleAt = now
					return pendingScale{}, false
				}

				step := max(currentWorkers/8, 1)
				// Don't cross probeFloor
				if currentWorkers-step < probeFloor {
					step = currentWorkers - probeFloor
				}
				if step > 0 {
					s.lastStep = step
					s.probeBaseline = rawBps
					s.probeBaselineWorkers = currentWorkers
					s.phase = phaseSettling
					s.prevCompleted = task.CompletedLength
					s.prevSampleAt = now
					log.Printf("[convergence] probe-down: gid=%s workers=%d step=%d baseline=%d momentum=%v",
						gid, currentWorkers, step, rawBps, s.probeMomentum)
					if c.peakRecorder != nil && rawBps > 0 && currentWorkers > 0 {
						c.peakRecorder.RecordPeakEfficiency(gid, rawBps, currentWorkers)
					}
					return pendingScale{gid: gid, scope: task.Scope, domain: task.Domain, envKey: task.EnvKey, delta: -step}, true
				}
			}
		}

		// Floor reached: down-probe combo hit the probe floor. Clear momentum
		// so it doesn't linger as inert residue; Probe-Up may now explore upward.
		if currentWorkers <= probeFloor && s.probeMomentum {
			s.probeMomentum = false
			s.probeCooldown = probeIntervalCycles
			log.Printf("[convergence] probe-floor-reached: gid=%s workers=%d momentum cleared",
				gid, currentWorkers)
		}
	}

	s.prevCompleted = task.CompletedLength
	s.prevSampleAt = now
	return pendingScale{}, false
}

func (c *ConvergenceTicker) getOrCreateState(gid string) *convergenceState {
	s, ok := c.states[gid]
	if !ok {
		s = &convergenceState{}
		c.states[gid] = s
	}
	return s
}

// cancelPendingVerdict drops the in-flight probe verdict without judging it:
// an open Probe-Up window loses its baseline fields; an open Probe-Down
// window (settling, or the stable+lastStep verdict tick) loses step, baseline
// and the unfinished combo's momentum. Long-term memory — peaks, locks,
// sustain, raw — is untouched, and physical worker changes already executed
// are never rolled back.
func (s *convergenceState) cancelPendingVerdict() {
	switch {
	case s.phase == phaseProbingUp:
		s.probeUpBaseline = 0
		s.probeUpBaselineWorkers = 0
		s.probeUpDelta = 0
		s.phase = phaseStable
	case s.phase == phaseSettling || (s.phase == phaseStable && s.lastStep > 0):
		s.lastStep = 0
		s.probeBaseline = 0
		s.probeBaselineWorkers = 0
		s.probeMomentum = false
		s.phase = phaseStable
	}
}

// RemoveTask clears per-gid convergence state immediately so LastRawBps
// returns (0, false). prevActiveGids / prevActiveSpeeds / prevActiveOccupancy
// are preserved until the next tick replaces/prunes them, so complete/delete
// can still participate in the disappearance diff and exit-side occupancy.
func (c *ConvergenceTicker) RemoveTask(gid string) {
	c.mu.Lock()
	delete(c.states, gid)
	c.mu.Unlock()
}

// LastRawBps returns the last macro-band rawBps for gid and whether a D2
// sample has latched (macroReady). Missing/cold → (0, false); sampled zero →
// (0, true). Read-only; does not create state.
//
// Lock topology: acquires c.mu. Callers that already hold c.mu must not invoke
// this (or MacroBandwidthByScope) — unlock first or pass a precomputed occupancy.
func (c *ConvergenceTicker) LastRawBps(gid string) (bps int64, ready bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.states[gid]
	if !ok || s == nil {
		return 0, false
	}
	return s.lastRawBps, s.macroReady
}

// SumLastRawBps sums lastRawBps for ready sg_ tasks matching scope+envKey.
// Ready-only (no Cache cold pad); MacroBandwidth mixes pads in monitor.
func (c *ConvergenceTicker) SumLastRawBps(scope, envKey string) int64 {
	if c == nil || c.tracker == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var sum int64
	for _, task := range c.tracker.GetActiveTrackedTasks() {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		if task.Scope != scope || task.EnvKey != envKey {
			continue
		}
		s, ok := c.states[task.GID]
		if !ok || s == nil || !s.macroReady {
			continue
		}
		sum += s.lastRawBps
	}
	return sum
}

// InjectMacroOccupancyForTest sets lastRawBps/macroReady for cross-package unit tests.
func (c *ConvergenceTicker) InjectMacroOccupancyForTest(gid string, bps int64, ready bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.getOrCreateState(gid)
	s.lastRawBps = bps
	s.macroReady = ready
}
