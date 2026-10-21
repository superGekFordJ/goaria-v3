// Package smartthread: reactive bandwidth release, deferred settlement, and beneficiary election.

package smartthread

import (
	"log"
	"sort"
	"strings"
	"time"

	"goaria-v3/internal/speedstats"
)

// gidInfo records the domain, scope, and envKey of a previously active gid,
// for bandwidthRelease domain+scope+env matching.
type gidInfo struct {
	Domain string
	Scope  string
	EnvKey string
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
		// Fresh telemetry read (not the tick-frozen surgeStats): worker count
		// only orders candidates and pre-filters zero-telemetry rows; the
		// elected delta is re-clamped downstream, so a mid-tick drift is
		// bounded and harmless.
		cw := len(c.telemetry.Get(t.GID))
		if cw == 0 {
			continue
		}
		s := c.getOrCreateState(t.GID)
		// Skip phaseProbingUp: arm free +1 / settle jump must not corrupt an
		// in-flight GainRatio baseline (narrow same-tick race after Probe-Up).
		// Settling and stable+lastStep are open Probe-Down windows — an
		// election must not overwrite a pending verdict with phaseProbingUp.
		if s.kneeFrozen || s.phase == phaseCeilingHit || s.phase == phaseProbingUp ||
			s.phase == phaseSettling || (s.phase == phaseStable && s.lastStep > 0) || s.blackout {
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
