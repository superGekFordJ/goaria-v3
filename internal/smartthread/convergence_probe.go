// Package smartthread: active probing state machine, throughput estimation, and dynamic exploration decisions.

package smartthread

import (
	"log"
	"time"

	"goaria-v3/internal/surge/types"
)

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
	// settleWaitTicks counts eligible settling observations that have not yet
	// reached the requested post-drain headcount. Reset by every probe reset
	// seam (cancelPendingVerdict, fresh probe issue, settle-done, scale no-op).
	settleWaitTicks int
	frozenCooldown  int
	kneeFrozen      bool
	bestEff         int64
	prevCompleted   int64
	prevSampleAt    time.Time
	sustainCount    int

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

// adjustProbeUpDeltaAfterPartialScale keeps GainRatio aligned when the engine
// landed fewer workers than requested on a probing jump.
func (c *ConvergenceTicker) adjustProbeUpDeltaAfterPartialScale(gid string, actual int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.states[gid]; ok && s != nil && s.phase == phaseProbingUp && s.probeUpDelta > actual {
		s.probeUpDelta = actual
	}
}

// processTask evaluates a single task and returns a pending scale operation
// if one is needed. affected is the caller's dirty hint for a task inside
// this tick's net membership-perturbation margin — the callee also
// self-detects a same-window pause→resume on the pre-update anchor and
// treats it as dirty either way. A dirty task still runs D2 measurement and
// advances its sample baseline, but the pending probe verdict is cancelled
// unjudged and D3 / smart-unlock / new-probe decisions are skipped for the
// dirty tick.
// dStats / domainMacroBps / approvedDelta may be nil in unit tests (degrade
// safely). now is the tick-captured evaluation instant shared by occupancy
// checks and sampling.
func (c *ConvergenceTicker) processTask(task TrackedTaskInfo, affected bool, approvedDelta map[string]int, dStats map[string]*domainStats, domainMacroBps map[string]int64, now time.Time) (pendingScale, bool) {
	return c.processTaskStats(task, c.telemetry.Get(task.GID), affected, approvedDelta, dStats, domainMacroBps, now)
}

func (c *ConvergenceTicker) processTaskStats(task TrackedTaskInfo, stats []types.WorkerSnapshot, affected bool, approvedDelta map[string]int, dStats map[string]*domainStats, domainMacroBps map[string]int64, now time.Time) (pendingScale, bool) {
	gid := task.GID

	if len(stats) == 0 {
		// Telemetry gap on a dirty tick: an armed verdict must not
		// survive to be judged across a pollution-spanning window.
		// Detection runs under the lock — a same-window pause→resume is
		// invisible to the caller's affected flag. Cancel it and re-base
		// the sample window so the next measured tick cannot double-count
		// the gap. Existing state only — never create state on a
		// no-telemetry path.
		c.mu.Lock()
		s := c.states[gid]
		if isDirty := affected || stateReActivatedInWindow(s, task.ActivatedAt); isDirty && s != nil {
			s.cancelPendingVerdict()
			if s.probeCooldown > 0 {
				s.probeCooldown--
			}
			if s.frozenCooldown > 0 {
				s.frozenCooldown--
			}
			s.prevCompleted = max(task.CompletedLength, 0)
			s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
		}
		c.mu.Unlock()
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

	// A same-window pause→resume poisons every in-flight verdict and the
	// terminal-peak write below even on an externally clean tick — detect
	// it on the pre-update anchor before any branch re-bases the window.
	affected = affected || stateReActivatedInWindow(s, task.ActivatedAt)

	// Tail blackout zone: per-gid permanent sleep when totalRemaining <
	// activeWorkers × effectiveMinChunk. Blackout is permanent — never reset.
	if s.blackout {
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
			s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
		return pendingScale{}, false
	}

	dt := now.Sub(s.prevSampleAt)
	if dt <= 0 {
		// Broken time window: keep the last measurement, re-base from a
		// non-negative endpoint, and drop any pending verdict unjudged —
		// the baseline it spanned no longer exists.
		s.cancelPendingVerdict()
		s.prevCompleted = max(task.CompletedLength, 0)
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
		return pendingScale{}, false
	}

	// D2: Settling — hold until the observed worker count reaches the
	// requested post-drain headcount. Do NOT overwrite probeBaseline — it
	// must retain the pre-probe throughput for the marginal drop ratio
	// evaluation on a later clean tick.
	if s.phase == phaseSettling {
		targetWorkers := s.probeBaselineWorkers - s.lastStep
		if currentWorkers > targetWorkers {
			s.settleWaitTicks++
			if s.settleWaitTicks >= maxProbeSettleTicks {
				log.Printf("[convergence] probe-down-abandoned: gid=%s workers=%d target=%d wait=%d/%d",
					gid, currentWorkers, targetWorkers, s.settleWaitTicks, maxProbeSettleTicks)
				s.cancelPendingVerdict()
				s.probeCooldown = probeIntervalCycles
			} else {
				log.Printf("[convergence] settling-prolonged: gid=%s workers=%d target=%d wait=%d/%d",
					gid, currentWorkers, targetWorkers, s.settleWaitTicks, maxProbeSettleTicks)
			}
			s.prevCompleted = task.CompletedLength
			s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
			return pendingScale{}, false
		}
		// Physical reduction observed — exit settling but keep this mixed
		// window unjudged: only the next clean sample may run the verdict.
		s.phase = phaseStable
		s.settleWaitTicks = 0
		s.sustainCount = 0 // C3: reset so peakSustainCycles stability is required after settling
		s.prevCompleted = task.CompletedLength
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
		log.Printf("[convergence] settling-done: gid=%s workers=%d target=%d",
			gid, currentWorkers, targetWorkers)
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
			s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
			return pendingScale{gid: gid, scope: task.Scope, domain: task.Domain, envKey: task.EnvKey, delta: -rebound}, true
		}
	}

	// D4 step 4: Evaluate last probe using Marginal Drop Ratio.
	// The verdict only runs when the observed reduction equals the requested
	// step exactly; zero/negative, partial, and excessive reductions all void
	// the experiment — the headcount change it measured is not ours.
	// ExpectedDrop = actualStep / probeBaselineWorkers (if threads are equally
	// productive, cutting actualStep out of N should drop speed proportionally).
	// ActualDrop = 1 - rawBps / probeBaseline.
	// DropRatio = ActualDrop / ExpectedDrop.
	// DropRatio ≤ 0.5 → actual drop far less than expected → plateau zone → success.
	// DropRatio > 0.5 → actual drop matches/exceeds expected → linear zone → knee crossed.
	if s.lastStep > 0 && s.phase == phaseStable && s.probeBaseline > 0 && s.probeBaselineWorkers > 0 {
		actualStep := s.probeBaselineWorkers - currentWorkers
		if actualStep != s.lastStep {
			log.Printf("[convergence] probe-down-void: gid=%s step=%d actual=%d workers=%d baselineWorkers=%d",
				gid, s.lastStep, actualStep, currentWorkers, s.probeBaselineWorkers)
			s.cancelPendingVerdict()
			s.probeCooldown = probeIntervalCycles
			s.prevCompleted = task.CompletedLength
			s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
			return pendingScale{}, false
		}
		actualDrop := 1.0 - float64(rawBps)/float64(s.probeBaseline)
		if actualDrop < 0 {
			actualDrop = 0 // speed increased — definitely plateau
		}
		expectedDrop := float64(actualStep) / float64(s.probeBaselineWorkers)
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
			s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
				s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
				s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
		s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
						s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
					s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
					return pendingScale{}, false
				}

				step := max(currentWorkers/8, 1)
				// Don't cross probeFloor
				if currentWorkers-step < probeFloor {
					step = currentWorkers - probeFloor
				}
				if step > 0 {
					s.lastStep = step
					s.settleWaitTicks = 0
					s.probeBaseline = rawBps
					s.probeBaselineWorkers = currentWorkers
					s.phase = phaseSettling
					s.prevCompleted = task.CompletedLength
					s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
	s.prevSampleAt = advanceSampleAnchor(now, task.ActivatedAt)
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
	s.settleWaitTicks = 0
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
