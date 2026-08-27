package smartthread

import (
	"log"
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

// TrackedTaskInfo is a minimal view of monitor.TrackedTask for convergence
// and BandwidthLedger hybrid occupancy seeding.
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
	TelemetryBps    int64 // Cache EMA / DownloadSpeed pad, or LastRawBps when ready
	MacroReady      bool  // Surge: Convergence macroReady latch
}

// TrackerProvider provides task tracking data to the convergence ticker.
type TrackerProvider interface {
	GetActiveTrackedTasks() []TrackedTaskInfo
	GetScopeAndEnv(gid string) (scope, domain, envKey string, ok bool)
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
	deferrals         int
}

// Cap on nested invalidate deferrals before dropping the arm (degrade to Probe-Up).
const pendingReleaseMaxDeferrals = 3

type ConvergenceTicker struct {
	engine       *rpc.HybridEngine
	tracker      TrackerProvider
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
	stopChan          chan struct{}
	stopOnce          sync.Once
	wg                sync.WaitGroup

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
	if c.engine == nil || c.tracker == nil || c.telemetry == nil {
		return
	}

	activeTasks := c.tracker.GetActiveTrackedTasks()
	activeGids := make(map[string]gidInfo) // gid → gidInfo
	var pending []pendingScale
	pendingGids := make(map[string]bool) // GIDs already scaled this tick
	// approvedDelta: same-tick positive ScaleUp accumulator.
	// Keys: approvedScopeKey / approvedDomainKey (env-aware BW) /
	// approvedNMaxKey (env-blind N_max pending, aligned with limitKey).
	approvedDelta := make(map[string]int)

	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		activeGids[task.GID] = gidInfo{Domain: task.Domain, Scope: task.Scope, EnvKey: task.EnvKey}
	}

	// Detect active-set changes (Edge Case 3: multi-task bandwidth confusion).
	// Only invalidate when we had a previous active set — going from empty to non-empty
	// is initialization, not a change.
	windowInvalidated := false
	c.mu.Lock()
	if len(c.prevActiveGids) > 0 {
		if !sameActiveSet(c.prevActiveGids, activeGids) {
			windowInvalidated = true
			c.domainUnlockTicks = make(map[string]int)
			for _, s := range c.states {
				s.prevCompleted = 0
				s.prevSampleAt = time.Time{}
				s.probeBaseline = 0
				s.probeBaselineWorkers = 0
				s.lastStep = 0
				s.phase = phaseStable
				s.sustainCount = 0
				s.kneeFrozen = false
				s.frozenCooldown = 0
				s.probeMomentum = false
				s.probeCooldown = probeIntervalCycles
				s.probeUpBaseline = 0
				s.probeUpBaselineWorkers = 0
				s.probeUpDelta = 0
				s.ceilingMemory = 0
				s.ceilingHitCount = 0
				s.floorMemory = 0
				s.floorHitCount = 0
				// Blackout never re-enters D2; clearing lastRawBps would leave
				// macroReady+(0) forever while workers still move bytes.
				if !s.blackout {
					s.lastRawBps = 0
				}
			}
		}
	}
	c.mu.Unlock()

	// Domain Pre-pass: aggregate retryCountSum and activeWorkers per limitKey.
	dStats := make(map[string]*domainStats)
	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		if task.Scope == "" || task.Domain == "" {
			continue
		}
		// Align processTask complete guard: 100% tasks still listed must not
		// inflate N_max / release clamp. TotalLength==0 (unknown size) counts.
		if task.TotalLength > 0 && task.CompletedLength >= task.TotalLength {
			continue
		}
		stats := c.telemetry.Get(task.GID)
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

	// Domain-level N_max fuse and unlock.
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
	c.mu.Lock()
	for lk := range c.pendingReleases {
		if _, ok := dStats[lk]; !ok {
			delete(c.pendingReleases, lk)
		}
	}
	c.mu.Unlock()

	// Domain macro occupancy snapshot (env-aware). Separate short lock — no
	// telemetry/provider calls. Still-listed complete peers may contribute
	// lastRawBps while excluded from dStats workers (physical occupancy).
	domainMacroBps := make(map[string]int64)
	c.mu.Lock()
	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		if task.Scope == "" || task.Domain == "" {
			continue
		}
		s, ok := c.states[task.GID]
		if !ok || s == nil || !s.macroReady {
			continue
		}
		domainMacroBps[approvedDomainKey(task.Scope, task.Domain, task.EnvKey)] += s.lastRawBps
	}
	c.mu.Unlock()

	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		if ps, ok := c.processTask(task, windowInvalidated, approvedDelta, dStats, domainMacroBps); ok {
			if ps.delta > 0 {
				approvedDelta[approvedScopeKey(ps.scope, ps.envKey)] += ps.delta
				if ps.domain != "" {
					approvedDelta[approvedDomainKey(ps.scope, ps.domain, ps.envKey)] += ps.delta
					approvedDelta[approvedNMaxKey(ps.scope, ps.domain)] += ps.delta
				}
			}
			pending = append(pending, ps)
			pendingGids[ps.gid] = true
		}
	}

	// Update prevActiveSpeeds for bandwidthRelease delay compensation.
	// Preserve lastRawBps > 0 guard so invalidate ticks do not wipe pre-death
	// domain sums needed for pendingReleases.preDeathDomainBps.
	c.mu.Lock()
	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		if s, ok := c.states[task.GID]; ok && s.lastRawBps > 0 {
			c.prevActiveSpeeds[task.GID] = s.lastRawBps
		}
	}
	c.mu.Unlock()

	// Deferred settle of previously armed domains first, then arm new
	// disappearances (never settle an entry created earlier in this call).
	pending = append(pending, c.settlePendingReleases(activeTasks, activeGids, pendingGids, approvedDelta, dStats, windowInvalidated)...)
	pending = append(pending, c.bandwidthRelease(activeTasks, activeGids, pendingGids, approvedDelta, dStats, domainMacroBps)...)

	// Self-cleanup: remove states for GIDs no longer active.
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
	// Update prevActiveGids for next tick's bandwidth borrowing diff
	c.prevActiveGids = activeGids
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
func (c *ConvergenceTicker) bandwidthRelease(activeTasks []TrackedTaskInfo, activeGids map[string]gidInfo, pendingGids map[string]bool, approvedDelta map[string]int, dStats map[string]*domainStats, domainMacroBps map[string]int64) []pendingScale {
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

		disappearedSpeed := d.speed
		scope, domain, envKey := elected.scope, elected.domain, elected.envKey
		domainOcc := int64(0)
		if domainMacroBps != nil {
			domainOcc = domainMacroBps[approvedDomainKey(scope, domain, envKey)]
		}
		// Provider (Macro → LastRawBps) locks c.mu; must not call under our lock.
		c.mu.Unlock()
		vOK := c.checkVAvailableWithCompensation(scope, domain, envKey, approvedDelta, domainOcc, disappearedSpeed, 1)
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
				approvedDelta[approvedDomainKey(elected.scope, elected.domain, elected.envKey)] += 1
				approvedDelta[approvedNMaxKey(elected.scope, elected.domain)] += 1
			}
		}
		log.Printf("[convergence] bandwidth-borrow-arm: gid=%s scope=%s envKey=%s domain=%s released by completed gid=%s (elected, %d candidates, free +1)",
			elected.gid, elected.scope, elected.envKey, elected.domain, gid, elected.candidateCount)
	}
	return releases
}

// settlePendingReleases settles previously armed domains using post-processTask
// fresh lastRawBps. Never settles using wiped arm-tick occupancy.
func (c *ConvergenceTicker) settlePendingReleases(
	activeTasks []TrackedTaskInfo,
	activeGids map[string]gidInfo,
	pendingGids map[string]bool,
	approvedDelta map[string]int,
	dStats map[string]*domainStats,
	windowInvalidated bool,
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

	if windowInvalidated {
		for _, lk := range armedKeys {
			pr := c.pendingReleases[lk]
			if pr == nil {
				continue
			}
			pr.deferrals++
			if pr.deferrals > pendingReleaseMaxDeferrals {
				delete(c.pendingReleases, lk)
				log.Printf("[convergence] bandwidth-borrow-drop: limitKey=%s deferrals exceeded (%d)", lk, pr.deferrals)
			}
		}
		return nil
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

		// Fresh occupancy: env-matched macroReady lastRawBps after processTask.
		var currentDomainBps int64
		for _, t := range activeTasks {
			if !strings.HasPrefix(t.GID, "sg_") {
				continue
			}
			if t.Scope != scope || t.Domain != domain || t.EnvKey != envKey {
				continue
			}
			s := c.states[t.GID]
			if s == nil || !s.macroReady {
				continue
			}
			currentDomainBps += s.lastRawBps
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
		vOK := c.checkVAvailable(scope, domain, envKey, approvedDelta, domainOcc, delta)
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
				approvedDelta[approvedDomainKey(elected.scope, elected.domain, elected.envKey)] += delta
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

// sameActiveSet compares two gid→gidInfo maps by key set only.
func sameActiveSet(a, b map[string]gidInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
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
// Lock topology: must NOT be called while holding c.mu. The activeBandwidth
// provider (MacroBandwidthByScope) may call LastRawBps which locks c.mu.
func (c *ConvergenceTicker) checkVAvailable(scope, domain, envKey string, approvedDelta map[string]int, domainOcc int64, needed int) bool {
	if needed <= 0 {
		return false
	}
	vThreadAvg := c.computeVThreadAvg(domain, scope, envKey)
	neededHeadroom := int64(needed) * vThreadAvg

	scopeOK := true
	if globalPeak, ok := speedstats.GetGlobalPeak(scope, envKey); ok && globalPeak > 0 {
		activeBw := activeBandwidthProvider(scope, envKey)
		effectiveScope := activeBw + int64(approvedDelta[approvedScopeKey(scope, envKey)])*vThreadAvg
		scopeOK = globalPeak-effectiveScope >= neededHeadroom
	}

	domainOK := true
	if domain != "" {
		if domainPeak, ok := speedstats.GetDomainPeak(domain, scope, envKey); ok && domainPeak > 0 {
			effectiveDomain := domainOcc + int64(approvedDelta[approvedDomainKey(scope, domain, envKey)])*vThreadAvg
			domainOK = domainPeak-effectiveDomain >= neededHeadroom
		}
	}
	return scopeOK && domainOK
}

// checkVAvailableWithCompensation is like checkVAvailable but subtracts
// disappearedSpeed from the scope dim only. activeBandwidthProvider may still
// count a disappeared task for 1–5s; domainMacroBps is built from activeTasks
// and already excludes the disappeared gid, so compensating the domain dim
// would over-allow. When disappearedSpeed is 0, this degrades to checkVAvailable.
//
// Lock topology: must NOT be called while holding c.mu (same as checkVAvailable).
func (c *ConvergenceTicker) checkVAvailableWithCompensation(scope, domain, envKey string, approvedDelta map[string]int, domainOcc int64, disappearedSpeed int64, needed int) bool {
	if needed <= 0 {
		return false
	}
	vThreadAvg := c.computeVThreadAvg(domain, scope, envKey)
	neededHeadroom := int64(needed) * vThreadAvg

	scopeOK := true
	if globalPeak, ok := speedstats.GetGlobalPeak(scope, envKey); ok && globalPeak > 0 {
		activeBw := activeBandwidthProvider(scope, envKey)
		effectiveScope := activeBw + int64(approvedDelta[approvedScopeKey(scope, envKey)])*vThreadAvg
		compensatedScope := max(effectiveScope-disappearedSpeed, 0)
		scopeOK = globalPeak-compensatedScope >= neededHeadroom
	}

	domainOK := true
	if domain != "" {
		if domainPeak, ok := speedstats.GetDomainPeak(domain, scope, envKey); ok && domainPeak > 0 {
			effectiveDomain := domainOcc + int64(approvedDelta[approvedDomainKey(scope, domain, envKey)])*vThreadAvg
			domainOK = domainPeak-effectiveDomain >= neededHeadroom
		}
	}
	return scopeOK && domainOK
}

// processTask evaluates a single task and returns a pending scale operation if one is needed.
// windowInvalidated indicates the active set changed this tick — skip probe/ratchet decisions.
// dStats / domainMacroBps / approvedDelta may be nil in unit tests (degrade safely).
func (c *ConvergenceTicker) processTask(task TrackedTaskInfo, windowInvalidated bool, approvedDelta map[string]int, dStats map[string]*domainStats, domainMacroBps map[string]int64) (pendingScale, bool) {
	gid := task.GID

	stats := c.telemetry.Get(gid)
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
	// activeWorkers × effectiveMinChunk. Runs before windowInvalidated
	// early-return because blackout is permanent.
	if s.blackout {
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = time.Now()
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

			// Final RecordPeakEfficiency before permanent sleep — blackout
			// early-return is before D3 ratchet, so ratchet is permanently
			// skipped. Compute rawBps from last tick's baseline and latch
			// macro occupancy so ledger still counts this gid (SPEC-180).
			if s.prevCompleted > 0 && !s.prevSampleAt.IsZero() {
				dt := time.Since(s.prevSampleAt)
				if dt > 0 {
					finalRawBps := max(int64(float64(task.CompletedLength-s.prevCompleted)/dt.Seconds()), 0)
					s.lastRawBps = finalRawBps
					s.macroReady = true
					if finalRawBps > 0 && currentWorkers > 0 && c.peakRecorder != nil {
						c.peakRecorder.RecordPeakEfficiency(gid, finalRawBps, currentWorkers)
					}
				}
			}

			s.prevCompleted = task.CompletedLength
			s.prevSampleAt = time.Now()
			return pendingScale{}, false
		}
	}

	// Window invalidation: skip D2/D3 and reset sustain. Rate-limit does not
	// participate — it only gates Probe-Up/Probe-Down below.
	if windowInvalidated {
		s.sustainCount = 0
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = time.Now()
		return pendingScale{}, false
	}

	now := time.Now()

	// D2: Compute raw throughput from CompletedLength delta.
	if s.prevSampleAt.IsZero() || s.prevCompleted == 0 {
		// First sample — store baseline, no decision
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = now
		return pendingScale{}, false
	}

	dt := now.Sub(s.prevSampleAt)
	if dt <= 0 {
		return pendingScale{}, false
	}
	rawBps := max(int64(float64(task.CompletedLength-s.prevCompleted)/dt.Seconds()), 0)
	s.lastRawBps = rawBps
	s.macroReady = true

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
					domainOcc = domainMacroBps[approvedDomainKey(scope, domain, envKey)]
				}
				// Provider (Macro → LastRawBps) locks c.mu; release before call.
				c.mu.Unlock()
				vAvailable := c.checkVAvailable(scope, domain, envKey, approvedDelta, domainOcc, 1)
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

// RemoveTask clears per-gid convergence state immediately so LastRawBps
// returns (0, false). prevActiveGids / prevActiveSpeeds are preserved until the
// next tick replaces/prunes them, so complete/delete can still participate in
// disappearance diff and windowInvalidated.
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
