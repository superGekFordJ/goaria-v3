package smartthread

import (
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/types"
)

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
	// AdmissionReserved is the explicit admitted-not-started reservation
	// fact (monitor.ReserveAdmission). The reservation reuses ActivatedAt as
	// its anchor and the same 45s/15s lease, and terminates occupancy
	// evaluation on its own: a live lease claims the Target floor, while an
	// expired or anchor-less reservation contributes telemetry-or-zero by
	// row status (waiting/paused rows contribute zero) — never falling
	// back into a legacy waiting/compat claim.
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
	// Same-GID pause→resume inside one tick gap leaves no membership trace;
	// collect it per-GID so the dirty window below can still invalidate the
	// poisoned sample span.
	reActivated := make(map[string]bool)
	for i := range sampled {
		if !strings.HasPrefix(sampled[i].GID, "sg_") {
			continue
		}
		if s, ok := c.states[sampled[i].GID]; ok && s != nil {
			sampled[i].RawBps = s.lastRawBps
			sampled[i].MacroReady = s.macroReady
			// A row already at 100% is complete-guarded downstream and
			// never re-bases its anchor: recording its re-activation would
			// re-fire every lingering tick and pin the domain's N_max
			// unlock confirmation at zero.
			complete := sampled[i].TotalLength > 0 && sampled[i].CompletedLength >= sampled[i].TotalLength
			if !complete && stateReActivatedInWindow(s, sampled[i].ActivatedAt) {
				reActivated[sampled[i].GID] = true
			}
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
	// A hidden re-activation must not count as this domain's clean unlock
	// confirmation. Merge only after the diff assigns changedDomains, and
	// keep the bootstrap "first sight resets nothing" discipline.
	if prevOcc != nil {
		for gid := range reActivated {
			if info, ok := activeGids[gid]; ok && info.Scope != "" && info.Domain != "" {
				changedDomains[limitKey(info.Scope, info.Domain)] = true
			}
		}
	}
	c.mu.Unlock()

	// A task is affected only when the net occupancy shift reaches half the
	// throughput its pending decision window can move. Margin-less windows
	// degrade conservatively once perturbation is non-zero or unknown. A
	// re-activated GID is dirty on its own — without widening the
	// margin-less fallback for unaffected peers.
	hasPerturbation := perturbUnknown || netDelta > 0
	var affected map[string]bool
	if hasPerturbation || len(reActivated) > 0 {
		affected = make(map[string]bool, len(surgeTasks))
		for i := range surgeTasks {
			gid := surgeTasks[i].GID
			if reActivated[gid] {
				affected[gid] = true
				continue
			}
			if hasPerturbation {
				margin, ok := probeMarginThreshold(margins[i])
				if perturbUnknown || !ok || float64(netDelta) >= margin {
					affected[gid] = true
				}
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
	// from dStats workers (physical occupancy). Deliberately pre-D2: Probe-Up
	// headroom judges this tick's domain fill on the as-entered snapshot;
	// settle re-overlays post-D2 values at its own site.
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
					s.settleWaitTicks = 0
				}
			}
			c.mu.Unlock()
			log.Printf("[convergence] scale-workers no-op: gid=%s delta=%d (engine returned 0)", ps.gid, ps.delta)
		} else if ps.delta > 0 && actual > 0 && actual < ps.delta {
			c.adjustProbeUpDeltaAfterPartialScale(ps.gid, actual)
		}
	}
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

// stateReActivatedInWindow reports whether a same-GID pause→resume landed
// strictly inside the last sample window: the GID never left the sampled
// set, so only a newer activation anchor betrays it. The check reads the
// pre-update anchor; callers hold c.mu. Missing state, missing anchor, and
// anchor-less legacy rows never match. Detection is bounded by tracker
// observability: a pause that never surfaced a fresh ActivatedAt cannot be
// seen here.
func stateReActivatedInWindow(s *convergenceState, activatedAt time.Time) bool {
	return s != nil && !s.prevSampleAt.IsZero() && !activatedAt.IsZero() && activatedAt.After(s.prevSampleAt)
}

// advanceSampleAnchor returns the left endpoint of the next sample window,
// never before this run's activation anchor. Tick capture can lag a µs-late
// ObserveActivation; anchoring below ActivatedAt would re-fire the
// re-activation detector on the next clean tick.
func advanceSampleAnchor(now, activatedAt time.Time) time.Time {
	if activatedAt.After(now) {
		return activatedAt
	}
	return now
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
