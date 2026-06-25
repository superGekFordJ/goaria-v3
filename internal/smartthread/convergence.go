package smartthread

import (
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/speedstats"
	"goaria-v3/internal/surge/engine/types"
)

// TrackedTaskInfo is a minimal view of monitor.TrackedTask for convergence.
type TrackedTaskInfo struct {
	GID             string
	Status          string
	Scope           string
	Domain          string
	IsKeepAlive     bool
	CompletedLength int64
}

// TrackerProvider provides task tracking data to the convergence ticker.
type TrackerProvider interface {
	GetActiveTrackedTasks() []TrackedTaskInfo
	GetScope(gid string) (scope, domain string, ok bool)
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
	phaseStable   = 0
	phaseSettling = 1
	phaseFrozen   = 2
)

type convergenceState struct {
	// Legacy fields (kept for ratio<0.5 Drain and bandwidth borrowing)
	scaleDownCycles int
	scaleUpCycles   int
	releaseCycles   int

	// Active probing state machine
	phase          int
	probeCooldown  int
	probeMomentum  bool
	probeBaseline  int64
	lastStep       int
	frozenCooldown int
	kneeFrozen     bool
	bestEff        int64
	prevCompleted  int64
	prevSampleAt   time.Time
	sustainCount   int

	// Shadow copies of tracker's PeakSpeed/PeakThreadCount for ratchet decisions
	peakSpeed   int64
	peakWorkers int
}

type ConvergenceTicker struct {
	engine       *rpc.HybridEngine
	tracker      TrackerProvider
	telemetry    TelemetryProvider
	peakRecorder PeakEfficiencyRecorder
	rateChecker  RateLimitChecker
	limits       *ServerLimitStore

	mu             sync.Mutex
	states         map[string]*convergenceState
	prevActiveGids map[string]string // gid → scope, for bandwidth borrowing diff
	stopChan       chan struct{}
	stopOnce       sync.Once
}

func NewConvergenceTicker(
	engine *rpc.HybridEngine,
	tracker TrackerProvider,
	telemetry TelemetryProvider,
	peakRecorder PeakEfficiencyRecorder,
	rateChecker RateLimitChecker,
) *ConvergenceTicker {
	return &ConvergenceTicker{
		engine:         engine,
		tracker:        tracker,
		telemetry:      telemetry,
		peakRecorder:   peakRecorder,
		rateChecker:    rateChecker,
		limits:         GetDefaultServerLimits(),
		states:         make(map[string]*convergenceState),
		prevActiveGids: make(map[string]string),
		stopChan:       make(chan struct{}),
	}
}

func (c *ConvergenceTicker) Start() {
	go c.run()
}

func (c *ConvergenceTicker) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopChan)
	})
}

func (c *ConvergenceTicker) run() {
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
	if config.Current != nil && config.Current.ConvergenceInterval > 0 {
		return time.Duration(config.Current.ConvergenceInterval) * time.Second
	}
	return convergenceInterval
}

// pendingScale collects batched scale operations to execute after all tasks are processed.
type pendingScale struct {
	gid   string
	delta int
}

func (c *ConvergenceTicker) tick() {
	if c.engine == nil || c.tracker == nil || c.telemetry == nil {
		return
	}

	activeTasks := c.tracker.GetActiveTrackedTasks()
	activeGids := make(map[string]string) // gid → scope
	var pending []pendingScale
	pendingGids := make(map[string]bool) // GIDs already scaled this tick

	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		activeGids[task.GID] = task.Scope
	}

	// Detect active-set changes (Edge Case 3: multi-task bandwidth confusion).
	// Only invalidate when we had a previous active set — going from empty to non-empty
	// is initialization, not a change.
	windowInvalidated := false
	c.mu.Lock()
	if len(c.prevActiveGids) > 0 {
		if !sameActiveSet(c.prevActiveGids, activeGids) {
			windowInvalidated = true
			for _, s := range c.states {
				s.prevCompleted = 0
				s.prevSampleAt = time.Time{}
				s.probeBaseline = 0
				s.lastStep = 0
				s.phase = phaseStable
				s.sustainCount = 0
				s.kneeFrozen = false
				s.frozenCooldown = 0
				s.probeMomentum = false
				s.probeCooldown = probeIntervalCycles
			}
		}
	}
	c.mu.Unlock()

	for _, task := range activeTasks {
		if !strings.HasPrefix(task.GID, "sg_") {
			continue
		}
		if ps, ok := c.processTask(task, windowInvalidated); ok {
			pending = append(pending, ps)
			pendingGids[ps.gid] = true
		}
	}

	// Bandwidth borrowing: detect tasks that disappeared since last tick.
	// For each completed task, find same-scope keep-alive tasks and trigger
	// ScaleUp with degraded filter (bandwidthReleaseCycles = 1).
	// Skip GIDs already scaled by processTask to prevent double ScaleUp.
	c.mu.Lock()
	for gid, scope := range c.prevActiveGids {
		if activeGids[gid] != "" {
			continue // still active
		}
		// Task disappeared — look for same-scope keep-alive tasks to benefit
		for _, t := range activeTasks {
			if !strings.HasPrefix(t.GID, "sg_") || !t.IsKeepAlive {
				continue
			}
			if t.Scope != scope {
				continue
			}
			if pendingGids[t.GID] {
				continue // already scaled by processTask this tick
			}
			s := c.getOrCreateState(t.GID)
			if s.kneeFrozen {
				continue // knee frozen — suppress ScaleUp (D4 step 7)
			}
			s.releaseCycles++
			if s.releaseCycles >= bandwidthReleaseCycles {
				pending = append(pending, pendingScale{gid: t.GID, delta: 1})
				pendingGids[t.GID] = true
				log.Printf("[convergence] bandwidth-borrow: gid=%s scope=%s released by completed gid=%s",
					t.GID, scope, gid)
				s.releaseCycles = 0
				s.scaleUpCycles = 0
			}
		}
	}
	c.mu.Unlock()

	// Self-cleanup: remove states for GIDs no longer active
	c.mu.Lock()
	for gid := range c.states {
		if activeGids[gid] == "" {
			delete(c.states, gid)
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
				if ps.delta > 0 && s.kneeFrozen {
					// Rebound failed — preserve frozen state so cooldown can run
					// and eventually clear kneeFrozen. Otherwise kneeFrozen orphans:
					// ScaleUp permanently suppressed, probe-down continues past knee.
					s.phase = phaseFrozen
					s.frozenCooldown = frozenCooldownCycles
				} else {
					s.phase = phaseStable
					s.probeBaseline = 0
					s.lastStep = 0
				}
			}
			c.mu.Unlock()
			log.Printf("[convergence] scale-workers no-op: gid=%s delta=%d (engine returned 0)", ps.gid, ps.delta)
		}
	}
}

// sameActiveSet compares two gid→scope maps by key set only.
func sameActiveSet(a, b map[string]string) bool {
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
func (c *ConvergenceTicker) computeProbeFloor(domain, scope string) int {
	btlBw, ok := speedstats.GetDomainPeak(domain, scope)
	if !ok || btlBw <= 0 {
		return probeFloorWorkers
	}
	rtpropMs, ok := speedstats.GetRTprop(domain, scope)
	if !ok || rtpropMs <= 0 {
		return probeFloorWorkers
	}
	wMax := 8
	if config.Current != nil {
		if v, err := strconv.Atoi(config.Current.MaxConnections); err == nil && v > 0 {
			wMax = v
		}
	}
	bdp := float64(btlBw) * (float64(rtpropMs) / 1000.0)
	bbrFloor := int(math.Ceil(bdp / float64(wMax)))
	if bbrFloor < probeFloorWorkers {
		return probeFloorWorkers
	}
	return bbrFloor
}

// processTask evaluates a single task and returns a pending scale operation if one is needed.
// windowInvalidated indicates the active set changed this tick — skip probe/ratchet decisions.
func (c *ConvergenceTicker) processTask(task TrackedTaskInfo, windowInvalidated bool) (pendingScale, bool) {
	gid := task.GID

	stats := c.telemetry.Get(gid)
	if len(stats) == 0 {
		return pendingScale{}, false
	}

	var aggregateSpeed float64
	var retryCountSum int32
	for _, ws := range stats {
		aggregateSpeed += ws.EMASpeed
		retryCountSum += ws.RetryCount
	}
	currentWorkers := len(stats)

	// Domain-isolated V_thread_avg with 0.5x scope fallback (kept for ratio<0.5 Drain).
	vThreadAvg, ok := speedstats.GetRecentPeakByDomain(task.Domain, task.Scope)
	if !ok {
		vThreadAvg, ok = speedstats.GetRecentPeakByScope(task.Scope)
		if ok {
			vThreadAvg /= 2
		}
	}
	if !ok || vThreadAvg <= 0 {
		vThreadAvg = minThreadEfficiency
	}
	if vThreadAvg < minThreadEfficiency {
		vThreadAvg = minThreadEfficiency
	}

	_, domain, _ := c.tracker.GetScope(gid)

	// C1: N_max fuse — detect conn errors and lock N_max (immediate safety, runs regardless of windowInvalidated).
	if retryCountSum >= int32(connErrorThreshold) {
		if _, hasLimit := c.limits.GetNMax(domain); !hasLimit {
			c.limits.SetNMax(domain, currentWorkers)
			log.Printf("[convergence] server-limit-fuse: domain=%s N_max=%d locked (retryCountSum=%d)",
				domain, currentWorkers, retryCountSum)
		}
	}

	nMax, hasLimit := c.limits.GetNMax(domain)

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

	// N_max fuse: if at or above N_max, suppress ScaleUp
	if hasLimit && currentWorkers >= nMax {
		s.scaleUpCycles = 0
		// Still allow probing down
	} else {
		// M1: V_available check — only ScaleUp if global bandwidth has room.
		vAvailable := false
		if globalPeak, ok := speedstats.GetGlobalPeak(task.Scope); ok && globalPeak > 0 {
			activeBw := activeBandwidthProvider(task.Scope)
			vAvailable = globalPeak-activeBw >= vThreadAvg
		} else {
			vAvailable = true
		}

		// Keep-alive ScaleUp (D4 step 7): suppress if kneeFrozen
		if task.IsKeepAlive && vAvailable && !s.kneeFrozen && !rateLimited {
			expectedThroughput := float64(vThreadAvg) * float64(currentWorkers)
			if expectedThroughput > 0 {
				ratio := aggregateSpeed / expectedThroughput
				if ratio >= throughputStableRatio {
					s.scaleUpCycles++
					s.scaleDownCycles = 0
					if s.scaleUpCycles >= scaleUpStableCycles {
						result := pendingScale{gid: gid, delta: 1}
						s.scaleUpCycles = 0
						log.Printf("[convergence] scale-up: gid=%s workers=%d ratio=%.2f keepAlive=true vAvailable=%v",
							gid, currentWorkers, ratio, vAvailable)
						return result, true
					}
				} else {
					s.scaleUpCycles = 0
				}
			}
		}
	}

	// ratio<0.5 complementary Drain (D1): fast-path safety valve for clearly pathological states.
	// Runs regardless of windowInvalidated (immediate safety).
	expectedThroughput := float64(vThreadAvg) * float64(currentWorkers)
	if expectedThroughput > 0 {
		ratio := aggregateSpeed / expectedThroughput
		if ratio < throughputFloorRatio {
			s.scaleDownCycles++
			s.scaleUpCycles = 0
			if s.scaleDownCycles >= scaleDownStableCycles && currentWorkers > 1 {
				s.scaleDownCycles = 0
				s.releaseCycles++
				log.Printf("[convergence] ratio-drain: gid=%s workers=%d ratio=%.2f",
					gid, currentWorkers, ratio)
				return pendingScale{gid: gid, delta: -1}, true
			}
			return pendingScale{}, false
		}
	}
	// Reset drain counter when ratio is healthy
	s.scaleDownCycles = 0

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

	// D2: Settling — refresh baseline, transition to stable, no decision this tick.
	if s.phase == phaseSettling {
		s.probeBaseline = rawBps
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
			if float64(rawBps) > float64(peak)*peakRaiseBand || currentWorkers < int(peakWorkers) {
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

	// D4 step 4: Evaluate last probe (just finished settling with lastStep > 0).
	if s.lastStep > 0 && s.phase == phaseStable {
		drop := 1.0 - float64(rawBps)/float64(s.probeBaseline)
		if s.probeBaseline > 0 {
			switch {
			case float64(rawBps) >= float64(s.probeBaseline)*acceptableEfficiencyBand:
				// Success — solidify + ignite momentum
				s.probeMomentum = true
				s.probeCooldown = 0
				log.Printf("[convergence] probe-success: gid=%s raw=%d baseline=%d drop=%.1f%% momentum=true",
					gid, rawBps, s.probeBaseline, drop*100)
			case float64(rawBps) < float64(s.probeBaseline)*recoverBand:
				// Knee crossed — asymmetric rebound
				rebound := (s.lastStep + 1) / 2 // ceil(lastStep/2)
				if rebound < 1 {
					rebound = 1
				}
				s.kneeFrozen = true
				s.probeMomentum = false
				s.phase = phaseFrozen
				s.frozenCooldown = frozenCooldownCycles
				s.prevCompleted = task.CompletedLength
				s.prevSampleAt = now
				s.lastStep = 0
				log.Printf("[convergence] knee-crossed: gid=%s raw=%d baseline=%d drop=%.1f%% rebound=+%d frozen",
					gid, rawBps, s.probeBaseline, drop*100, rebound)
				return pendingScale{gid: gid, delta: rebound}, true
			default:
				// Gray zone (10%-25%) — solidify but extinguish momentum
				s.probeMomentum = false
				s.probeCooldown = probeIntervalCycles
				log.Printf("[convergence] probe-grayzone: gid=%s raw=%d baseline=%d drop=%.1f%% momentum=false cooldown=%d",
					gid, rawBps, s.probeBaseline, drop*100, s.probeCooldown)
			}
		}
		s.lastStep = 0
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
	if s.phase == phaseStable {
		probeFloor := c.computeProbeFloor(domain, task.Scope)
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
					s.phase = phaseSettling
					s.prevCompleted = task.CompletedLength
					s.prevSampleAt = now
					log.Printf("[convergence] probe-down: gid=%s workers=%d step=%d baseline=%d momentum=%v",
						gid, currentWorkers, step, rawBps, s.probeMomentum)
					return pendingScale{gid: gid, delta: -step}, true
				}
			}
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
	c.mu.Unlock()
}
