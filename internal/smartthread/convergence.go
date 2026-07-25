package smartthread

import (
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

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

// TrackedTaskInfo is a minimal view of monitor.TrackedTask for convergence.
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

// RateLimitChecker reports whether a download is currently rate-limited.
// Returns (bps, true) if an effective per-download or global rate limit is active.
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

	// Probe-Up state (defined here, logic filled by later spec)
	probeUpBaseline        int64 // rawBps before +1
	probeUpBaselineWorkers int   // worker count before +1
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

type ConvergenceTicker struct {
	engine       *rpc.HybridEngine
	tracker      TrackerProvider
	telemetry    TelemetryProvider
	peakRecorder PeakEfficiencyRecorder
	rateChecker  RateLimitChecker
	limits       *ServerLimitStore

	mu                sync.Mutex
	states            map[string]*convergenceState
	prevActiveGids    map[string]gidInfo // gid → gidInfo, for bandwidth borrowing diff
	rotationCounter   int                // fair rotation for beneficiary election
	prevActiveSpeeds  map[string]int64   // gid → last known rawBps, for delay compensation
	domainUnlockTicks map[string]int     // limitKey → consecutive zero-retry ticks, for domain-level N_max unlock
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
		maxConnections = 8
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

// pendingScale collects batched scale operations to execute after all tasks are processed.
type pendingScale struct {
	gid    string
	scope  string
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
	pendingGids := make(map[string]bool)  // GIDs already scaled this tick
	approvedDelta := make(map[string]int) // key = scope+envKey → accumulated positive delta this tick

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

	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		if ps, ok := c.processTask(task, windowInvalidated, approvedDelta); ok {
			if ps.delta > 0 {
				approvedDelta[ps.scope+ps.envKey] += ps.delta
			}
			pending = append(pending, ps)
			pendingGids[ps.gid] = true
		}
	}

	// Update prevActiveSpeeds for bandwidthRelease delay compensation.
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

	// Bandwidth borrowing: detect tasks that disappeared since last tick.
	// For each completed task, elect a single same-domain+scope beneficiary.
	// Skip GIDs already scaled by processTask to prevent double ScaleUp.
	pending = append(pending, c.bandwidthRelease(activeTasks, activeGids, pendingGids, approvedDelta, dStats)...)

	// Self-cleanup: remove states for GIDs no longer active
	c.mu.Lock()
	for gid := range c.states {
		if _, ok := activeGids[gid]; !ok {
			delete(c.states, gid)
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
					s.lastStep = 0
				}
			}
			c.mu.Unlock()
			log.Printf("[convergence] scale-workers no-op: gid=%s delta=%d (engine returned 0)", ps.gid, ps.delta)
		}
	}
}

// bandwidthRelease detects tasks that disappeared since last tick and elects a
// single same-domain+scope beneficiary per disappearance event (lowest
// currentWorkers, fair rotation on tie) to prevent thundering-herd ScaleUp.
// GIDs already scaled by processTask this tick (present in pendingGids) are
// skipped to prevent double ScaleUp.
func (c *ConvergenceTicker) bandwidthRelease(activeTasks []TrackedTaskInfo, activeGids map[string]gidInfo, pendingGids map[string]bool, approvedDelta map[string]int, dStats map[string]*domainStats) []pendingScale {
	c.mu.Lock()
	defer c.mu.Unlock()

	var releases []pendingScale
	for gid, info := range c.prevActiveGids {
		if _, ok := activeGids[gid]; ok {
			continue // still active
		}

		type candidate struct {
			gid            string
			scope          string
			envKey         string
			domain         string
			currentWorkers int
		}
		var candidates []candidate
		for _, t := range activeTasks {
			if !strings.HasPrefix(t.GID, "sg_") {
				continue
			}
			domainMatch := t.Domain == info.Domain
			if info.Domain == "" {
				domainMatch = true
			}
			if !domainMatch || t.Scope != info.Scope || t.EnvKey != info.EnvKey {
				continue
			}
			if pendingGids[t.GID] {
				continue
			}
			s := c.getOrCreateState(t.GID)
			if s.kneeFrozen || s.phase == phaseCeilingHit || s.blackout {
				continue
			}
			cw := len(c.telemetry.Get(t.GID))
			// N_max clamp: skip when domain total workers + 1 would exceed N_max.
			lk := ""
			if t.Scope != "" && t.Domain != "" {
				lk = limitKey(t.Scope, t.Domain)
			}
			if lk != "" {
				if nMax, hasLimit := c.limits.GetNMax(lk); hasLimit {
					domainTotalWorkers := 0
					if ds, ok := dStats[lk]; ok {
						domainTotalWorkers = ds.activeWorkers
					}
					if domainTotalWorkers+1 > nMax {
						continue
					}
				}
			}
			candidates = append(candidates, candidate{
				gid:            t.GID,
				scope:          t.Scope,
				envKey:         t.EnvKey,
				domain:         t.Domain,
				currentWorkers: cw,
			})
		}

		if len(candidates) == 0 {
			continue
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

		disappearedSpeed := c.prevActiveSpeeds[gid]
		if !c.checkVAvailableWithCompensation(elected.scope, elected.domain, elected.envKey, approvedDelta, disappearedSpeed) {
			continue
		}

		releases = append(releases, pendingScale{gid: elected.gid, scope: elected.scope, envKey: elected.envKey, delta: 1})
		pendingGids[elected.gid] = true
		if approvedDelta != nil {
			approvedDelta[elected.scope+elected.envKey]++
		}
		log.Printf("[convergence] bandwidth-borrow: gid=%s scope=%s envKey=%s domain=%s released by completed gid=%s (elected, %d candidates)",
			elected.gid, elected.scope, elected.envKey, elected.domain, gid, len(candidates))
	}
	return releases
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

// computeProbeFloor returns the BBR-aware lower bound for probing.
// Uses speedstats.GetDomainPeak + GetRTprop to estimate BDP, then:
//
//	bbrFloor = ceil(BtlBw * RTprop / W_max)
//
// Falls back to probeFloorWorkers when BBR data is unavailable.
func (c *ConvergenceTicker) computeProbeFloor(domain, scope, envKey string) int {
	btlBw, ok := speedstats.GetDomainPeak(domain, scope, envKey)
	if !ok || btlBw <= 0 {
		return probeFloorWorkers
	}
	rtpropMs, ok := speedstats.GetRTprop(domain, scope, envKey)
	if !ok || rtpropMs <= 0 {
		return probeFloorWorkers
	}
	wMax := c.maxConnections
	if wMax <= 0 {
		wMax = 8
	}
	bdp := float64(btlBw) * (float64(rtpropMs) / 1000.0)
	bbrFloor := int(math.Ceil(bdp / float64(wMax)))
	if bbrFloor < probeFloorWorkers {
		return probeFloorWorkers
	}
	return bbrFloor
}

// computeVThreadAvg returns the estimated per-thread average throughput for
// V_available checks. Tries domain-specific median first, falls back to
// scope-wide median with 0.5x penalty, then clamps to minThreadEfficiency.
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

// checkVAvailable returns true if there is enough global bandwidth headroom
// to add one more worker in the given scope. It accounts for delta already
// approved this tick via approvedDelta to prevent same-tick oversell.
// When globalPeak data is unavailable, returns true (allow — conservative
// only when data exists).
func (c *ConvergenceTicker) checkVAvailable(scope, domain, envKey string, approvedDelta map[string]int) bool {
	globalPeak, ok := speedstats.GetGlobalPeak(scope, envKey)
	if !ok || globalPeak <= 0 {
		return true
	}
	activeBw := activeBandwidthProvider(scope, envKey)
	vThreadAvg := c.computeVThreadAvg(domain, scope, envKey)
	effectiveBw := activeBw + int64(approvedDelta[scope+envKey])*vThreadAvg
	return globalPeak-effectiveBw >= vThreadAvg
}

// checkVAvailableWithCompensation is like checkVAvailable but subtracts
// disappearedSpeed from effectiveBw to compensate for activeBandwidthProvider
// cache lag (a disappeared task's bandwidth may still be counted for 1-5s).
// When disappearedSpeed is 0, this degrades to checkVAvailable.
func (c *ConvergenceTicker) checkVAvailableWithCompensation(scope, domain, envKey string, approvedDelta map[string]int, disappearedSpeed int64) bool {
	globalPeak, ok := speedstats.GetGlobalPeak(scope, envKey)
	if !ok || globalPeak <= 0 {
		return true
	}
	activeBw := activeBandwidthProvider(scope, envKey)
	vThreadAvg := c.computeVThreadAvg(domain, scope, envKey)
	effectiveBw := activeBw + int64(approvedDelta[scope+envKey])*vThreadAvg
	compensatedBw := effectiveBw - disappearedSpeed
	if compensatedBw < 0 {
		compensatedBw = 0
	}
	return globalPeak-compensatedBw >= vThreadAvg
}

// processTask evaluates a single task and returns a pending scale operation if one is needed.
// windowInvalidated indicates the active set changed this tick — skip probe/ratchet decisions.
func (c *ConvergenceTicker) processTask(task TrackedTaskInfo, windowInvalidated bool, approvedDelta map[string]int) (pendingScale, bool) {
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

	// Rate limit guard (Edge Case 4): skip ratchet and probe when rate-limited.
	rateLimited := false
	if c.rateChecker != nil {
		if _, limited := c.rateChecker.GetRateLimit(gid); limited {
			rateLimited = true
		}
	}

	c.mu.Lock()
	s := c.getOrCreateState(gid)
	defer c.mu.Unlock()

	// Tail blackout zone: per-gid permanent sleep when totalRemaining <
	// activeWorkers × effectiveMinChunk. Runs before windowInvalidated/
	// rateLimited early-return because blackout is permanent.
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
					finalRawBps := int64(float64(task.CompletedLength-s.prevCompleted) / dt.Seconds())
					if finalRawBps < 0 {
						finalRawBps = 0
					}
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

	// --- active probing state machine ---
	// Skip probe/ratchet when window invalidated or rate-limited.
	if windowInvalidated || rateLimited {
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
	rawBps := int64(float64(task.CompletedLength-s.prevCompleted) / dt.Seconds())
	if rawBps < 0 {
		rawBps = 0
	}
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

	// Probe-Up evaluation: assess last Tick's +1 using GainRatio.
	// GainRatio = ActualGain / ExpectedGain; success falls through to the
	// Probe-Up trigger, failure rebounds -1 and enters the CeilingHit lock.
	if s.phase == phaseProbingUp && s.probeUpBaseline > 0 && s.probeUpBaselineWorkers > 0 {
		actualGain := float64(rawBps-s.probeUpBaseline) / float64(s.probeUpBaseline)
		if actualGain < 0 {
			actualGain = 0
		}
		expectedGain := 1.0 / float64(s.probeUpBaselineWorkers)
		gainRatio := actualGain / expectedGain

		if gainRatio >= gainRatioThreshold {
			// Up-probe success — return to stable, fall through to Probe-Up trigger for next +1
			s.phase = phaseStable
			log.Printf("[convergence] probe-up-success: gid=%s raw=%d baseline=%d gainRatio=%.2f",
				gid, rawBps, s.probeUpBaseline, gainRatio)
			// Fall through to Probe-Up trigger below
		} else {
			// Ceiling hit — rebound -1 + CeilingHit lock
			if c.peakRecorder != nil && rawBps > 0 && currentWorkers > 0 {
				c.peakRecorder.RecordPeakEfficiency(gid, rawBps, currentWorkers)
			}
			s.ceilingMemory = rawBps
			s.ceilingHitCount = 0
			s.phase = phaseCeilingHit
			s.frozenCooldown = ceilingHitCooldownCycles
			log.Printf("[convergence] ceiling-hit: gid=%s raw=%d baseline=%d gainRatio=%.2f rebound=-1",
				gid, rawBps, s.probeUpBaseline, gainRatio)
			s.probeUpBaseline = 0
			s.probeUpBaselineWorkers = 0
			s.prevCompleted = task.CompletedLength
			s.prevSampleAt = now
			return pendingScale{gid: gid, scope: task.Scope, envKey: task.EnvKey, delta: -1}, true
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
			rebound := (s.lastStep + 1) / 2 // ceil(lastStep/2)
			if rebound < 1 {
				rebound = 1
			}
			// N_max clamp: rebound must not push currentWorkers above N_max.
			var nMax int
			hasLimit := false
			if lk != "" {
				nMax, hasLimit = c.limits.GetNMax(lk)
			}
			if hasLimit {
				headroom := nMax - currentWorkers
				if headroom <= 0 {
					rebound = 0
				} else if rebound > headroom {
					rebound = headroom
				}
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
				return pendingScale{gid: gid, scope: task.Scope, envKey: task.EnvKey, delta: rebound}, true
			}
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
	//   5. N_max not exceeded: !(hasLimit && currentWorkers >= nMax)
	//   6. V_available sufficient: globalPeak - activeBw >= vThreadAvg (or no globalPeak data → allow)
	//   7. !rateLimited
	//   8. !probeMomentum (don't interrupt an active down-probe combo)
	if s.phase == phaseStable && s.bestEff > 0 && !s.probeMomentum {
		newEff := rawBps / int64(currentWorkers)
		preheated := s.peakWorkers > 0 || (s.sustainCount >= peakSustainCycles && s.bestEff > 0)
		if newEff >= int64(float64(s.bestEff)*probeUpEffThreshold) && preheated {
			// N_max check — Domain Pre-pass only SETS N_max; suppression is here.
			var nMax int
			hasLimit := false
			if lk != "" {
				nMax, hasLimit = c.limits.GetNMax(lk)
			}
			if !hasLimit || currentWorkers < nMax {
				vAvailable := c.checkVAvailable(task.Scope, task.Domain, task.EnvKey, approvedDelta)
				if vAvailable && !rateLimited {
					if c.peakRecorder != nil && rawBps > 0 && currentWorkers > 0 {
						c.peakRecorder.RecordPeakEfficiency(gid, rawBps, currentWorkers)
					}
					s.probeUpBaseline = rawBps
					s.probeUpBaselineWorkers = currentWorkers
					s.phase = phaseProbingUp
					s.prevCompleted = task.CompletedLength
					s.prevSampleAt = now
					log.Printf("[convergence] probe-up: gid=%s workers=%d baseline=%d",
						gid, currentWorkers, rawBps)
					return pendingScale{gid: gid, scope: task.Scope, envKey: task.EnvKey, delta: 1}, true
				}
			}
		}
	}

	if s.phase == phaseStable {
		probeFloor := c.computeProbeFloor(task.Domain, task.Scope, task.EnvKey)
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

			if shouldProbe {
				// Skip probe-down when rawBps == 0: a zero-speed task is either
				// dead or stalled. Killing workers won't help and causes a cold
				// probe-down cycle (probeBaseline=0 dead zone).
				if rawBps == 0 {
					s.prevCompleted = task.CompletedLength
					s.prevSampleAt = now
					return pendingScale{}, false
				}

				step := currentWorkers / 8
				if step < 1 {
					step = 1
				}
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
					return pendingScale{gid: gid, scope: task.Scope, envKey: task.EnvKey, delta: -step}, true
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

func (c *ConvergenceTicker) RemoveTask(gid string) {
	c.mu.Lock()
	delete(c.states, gid)
	delete(c.prevActiveGids, gid)
	delete(c.prevActiveSpeeds, gid)
	c.mu.Unlock()
}

// LastRawBps returns the last macro-band rawBps for gid and whether a D2
// sample has latched (macroReady). Missing/cold → (0, false); sampled zero →
// (0, true). Read-only; does not create state.
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
