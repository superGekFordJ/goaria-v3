// Unified occupancy run facts calculation, scheduler admission reservation pipeline, speed telemetry sampling, and D3 peak efficiency ratchet.

package monitor

import (
	"strings"
	"time"

	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
)

// activeSampleGapCap is 2x the headless tick; longer gaps mean aria2 was
// unreachable or the host slept, so they are not counted as active time.
const activeSampleGapCap = 10 * time.Second

// sampleSpeed 采样速度并更新峰值（tick 路径）
func (t *TaskTracker) sampleSpeed(task *TrackedTask, speed int64) {
	threshold := 3
	if !State.HasWindow() {
		threshold = 1
	}
	t.sampleSpeedInternal(task, speed, threshold)
}

// sampleSpeedInternal 稳定性检测与 PeakSpeed 写入（仅 Aria2 tick 路径）。
// Surge 事件路径已退役峰值采样，不再调用此方法；Surge PeakSpeed 由
// RecordPeakEfficiency（ConvergenceTicker）配对写入。
// PeakEnvKey 经 acceptPeakSpeed 写入；空 CurrentEnvKey 不 wipe 已有指纹。
func (t *TaskTracker) sampleSpeedInternal(task *TrackedTask, speed int64, threshold int) {
	if task.SustainedSpeed > 0 {
		diff := float64(speed-task.SustainedSpeed) / float64(task.SustainedSpeed)
		if diff > -0.15 && diff < 0.15 {
			task.SustainedCount++
		} else {
			task.SustainedSpeed = speed
			task.SustainedCount = 1
		}
	} else {
		task.SustainedSpeed = speed
		task.SustainedCount = 1
	}

	if task.SustainedCount >= threshold && task.CompletedLength > speedstats.MinFileSize {
		if speed > task.PeakSpeed {
			acceptPeakSpeed(task, speed)
		}
	}
}

// D3 ratchet thresholds — mirror internal/smartthread/calc_params.go.
// Defined locally to avoid a dependency cycle on the smartthread package.
const (
	trackerEfficiencyGuardBand = 0.85 // efficiencyGuardBand: single-thread eff degradation guard
	trackerPeakRaiseBand       = 1.05 // peakRaiseBand: noise gate for bumping peak
	trackerPeakSpeedGuardBand  = 0.90 // peakSpeedGuardBand: absolute speed guard for fewer peakWorkers
)

// SetThreadInfo 设置任务的线程信息（由 AddUri 调用）
func (t *TaskTracker) SetThreadInfo(gid string, threadCount int, isExploration bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if tracked := t.tasks[gid]; tracked != nil {
		tracked.ThreadCount = threadCount
		tracked.IsExploration = isExploration
		// 刷新 CreatedAt 以确保复用或濒临删除的任务获得新的宽限期
		tracked.CreatedAt = time.Now()
	} else {
		// 任务尚未被追踪，创建占位
		t.tasks[gid] = &TrackedTask{
			GID:           gid,
			ThreadCount:   threadCount,
			IsExploration: isExploration,
			CreatedAt:     time.Now(),
		}
	}
}

// observeActivationLocked records a real start for the current run. Only the
// first observation of a run establishes the lease anchor and baseline;
// later real-start evidence is idempotent, except that a reliable baseline
// may still upgrade an unknown one. A live activation ends compat claims —
// they existed only to bridge until this genuine start. Caller holds t.mu.
func (t *TaskTracker) observeActivationLocked(tt *TrackedTask, now time.Time, baseline int64, baselineKnown bool) {
	if tt.ActivatedAt.IsZero() {
		tt.ActivatedAt = now
		tt.FirstProgressAt = time.Time{}
		tt.progressBaseline = baseline
		tt.progressBaselineKnown = baselineKnown
		tt.compatClaim = smartthread.CompatNone
		tt.addAnchorAt = time.Time{}
	} else if baselineKnown && !tt.progressBaselineKnown {
		tt.progressBaseline = baseline
		tt.progressBaselineKnown = true
	}
	// A real start consumes the pending reservation wherever it lands —
	// including the reservation-anchored path where ActivatedAt is already
	// set. admissionToken stays as the high-water mark so a duplicate or
	// stale release still refuses.
	tt.admittedNotStarted = false
}

// observeCompletedLocked folds a reliable completed-length observation into
// the current run's facts. Without a live activation it is a no-op (the
// value still updates CompletedLength via the caller, which is the baseline
// source for the next activation). While activated it establishes an
// unknown baseline — not growth — or stamps the first genuine growth above
// the known baseline. Regressions and replayed values never rewind the
// baseline or mint growth. Caller holds t.mu.
func (t *TaskTracker) observeCompletedLocked(tt *TrackedTask, now time.Time, completed int64) {
	if tt.ActivatedAt.IsZero() {
		return
	}
	if !tt.progressBaselineKnown {
		tt.progressBaseline = completed
		tt.progressBaselineKnown = true
		return
	}
	if completed > tt.progressBaseline && tt.FirstProgressAt.IsZero() {
		tt.FirstProgressAt = now
	}
}

// clearActivationFactsLocked ends the current run's activation eligibility.
// Compat claims are the caller's decision (terminal also releases them;
// retry/pause keeps the Surge claim). Caller holds t.mu.
func clearActivationFactsLocked(tt *TrackedTask) {
	tt.ActivatedAt = time.Time{}
	tt.FirstProgressAt = time.Time{}
	tt.progressBaseline = 0
	tt.progressBaselineKnown = false
}

// applyAuthoritativeStatusLocked writes an observed status and settles the
// per-run occupancy facts: a non-active boundary ends activation eligibility;
// a terminal status additionally releases compat claims; and an authoritative
// Aria2 (non-sg_) waiting/paused ends the add placeholder. Writing "active"
// never creates activation — that requires ObserveActivation evidence.
// Caller holds t.mu.
func applyAuthoritativeStatusLocked(tt *TrackedTask, status string) {
	tt.Status = status
	pending := tt.admittedNotStarted
	switch status {
	case "complete", "error":
		clearActivationFactsLocked(tt)
		tt.compatClaim = smartthread.CompatNone
		tt.addAnchorAt = time.Time{}
		if tt.admissionToken > 0 {
			// Terminal boundary ends the reservation lineage.
			tt.admittedNotStarted = false
			tt.TargetBandwidth = 0
			tt.AllocatedAt = time.Time{}
		}
		return
	case "active":
		if pending {
			// A status write is not start evidence; ObserveActivation
			// consumes the pending flag when the real start lands.
			return
		}
		// Status alone is not start evidence — keep any live activation.
		return
	default:
		if pending {
			if status == "waiting" {
				// A waiting ack/poll restates the same queueing opportunity,
				// not a new one — keep the reservation anchor, floor, and flag.
				return
			}
			// Any other non-terminal boundary (paused, unknown) ends the
			// admitted lineage: clear pending, floor, and anchor so an
			// expired reservation can never harden into a legacy claim.
			tt.admittedNotStarted = false
			tt.TargetBandwidth = 0
			tt.AllocatedAt = time.Time{}
		}
		clearActivationFactsLocked(tt)
		if !strings.HasPrefix(tt.GID, "sg_") {
			// Authoritative Aria2 waiting/paused ends the add placeholder
			// immediately; a later re-add must anchor anew.
			tt.compatClaim = smartthread.CompatNone
			tt.addAnchorAt = time.Time{}
		}
	}
}

// AdmissionTicket is the comparable identity of one admitted-not-started
// reservation: the GID plus the scheduler-issued opportunity token
// (scheduler.AdmissionKey). The zero value means "no reservation". It is a
// plain value pair — not an opaque ownership credential; the existing
// lifecycle gate plus the row's pending token provide conditional release.
type AdmissionTicket struct {
	GID   string
	Token uint64
}

// ReserveAdmission records an admitted-not-started reservation for one
// scheduler opportunity against an existing waiting sg_ row. It writes the
// reservation facts atomically — pending flag plus the reservation's own
// target floor, anchor, and baseline — without touching Status (the UI never
// sees a synthesized active) and without minting a compat claim. The token
// is a per-pool monotonic identity: a lower or already-consumed token is a
// stale opportunity and refuses. The same ticket applied twice is an
// idempotent no-op that still reports its ticket. now/anchor must be valid;
// a zero anchor would lose its lease immediately on every read. baseline
// is always >= 0 — pass 0 when the caller has no reliable checkpoint
// (baselineKnown=false).
//
// created=false + zero ticket means the call left no reservation; callers
// may ReleaseAdmission only when created=true.
//
// Locking: takes only t.mu — safe to call inside the gid lifecycle gate.
func (t *TaskTracker) ReserveAdmission(
	gid string, token uint64, params smartthread.ThreadParams,
	now time.Time, baseline int64, baselineKnown bool,
) (ticket AdmissionTicket, created bool) {
	if t == nil || !IsSgGid(gid) || token == 0 || now.IsZero() {
		return AdmissionTicket{}, false
	}
	if params.Split <= 0 || params.TargetBandwidth < 0 || params.MinSize < 0 || baseline < 0 {
		return AdmissionTicket{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isTerminalAcceptedLocked(gid) {
		return AdmissionTicket{}, false
	}
	tt := t.tasks[gid]
	if tt == nil || tt.Status != "waiting" {
		return AdmissionTicket{}, false
	}
	switch {
	case tt.admissionToken > token:
		return AdmissionTicket{}, false // older opportunity than the observed high-water mark
	case tt.admissionToken == token && !tt.admittedNotStarted:
		return AdmissionTicket{}, false // same opportunity already consumed or rolled back
	case tt.admissionToken == token:
		return AdmissionTicket{GID: gid, Token: token}, false // idempotent reapply
	}
	tt.admissionToken = token
	tt.admittedNotStarted = true
	// Parameter hints only — the engine patch is applied solely by a
	// successful scheduler Admit; these merely describe the reservation.
	tt.ThreadCount = params.Split
	tt.MinChunk = params.MinSize
	tt.IsExploration = params.IsExploration
	// The reservation owns the Target floor and the activation anchor
	// (shared lease) for its lifetime; both die with it on release or a
	// non-waiting boundary — never promoted into a legacy claim.
	tt.TargetBandwidth = params.TargetBandwidth
	tt.AllocatedAt = now
	tt.ActivatedAt = now
	tt.FirstProgressAt = time.Time{}
	tt.progressBaseline = baseline
	tt.progressBaselineKnown = baselineKnown
	tt.compatClaim = smartthread.CompatNone
	tt.addAnchorAt = time.Time{}
	return AdmissionTicket{GID: gid, Token: token}, true
}

// ReleaseAdmission rolls back a still-pending admission reservation for the
// same lifecycle/token pair: it clears the pending flag, this ticket's
// Target floor, and the reservation-owned activation anchor. It refuses to
// disturb a started task (pending was consumed by ObserveActivation), a
// newer token (a re-admission supersedes stale rollbacks), or a row that
// never held this ticket — including a same-ID replacement.
func (t *TaskTracker) ReleaseAdmission(gid string, token uint64) bool {
	if t == nil || !IsSgGid(gid) || token == 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tt := t.tasks[gid]
	if tt == nil || !tt.admittedNotStarted || tt.admissionToken != token {
		return false
	}
	tt.admittedNotStarted = false
	clearActivationFactsLocked(tt)
	tt.TargetBandwidth = 0
	tt.AllocatedAt = time.Time{}
	return true
}

// ObserveActivation records a real engine start for gid — a Surge started
// event that passed the lifecycle gates or an authoritative engine-active
// poll row. baseline is the reliable completed length at start;
// baselineKnown=false when no trustworthy checkpoint exists (the first
// reliable observation then establishes the baseline without growth).
// Idempotent within a run; never writes status or reopens a terminal —
// callers own lifecycle gating and ordering.
func (t *TaskTracker) ObserveActivation(gid string, baseline int64, baselineKnown bool) {
	if t == nil || gid == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isTerminalAcceptedLocked(gid) {
		return
	}
	tt := t.tasks[gid]
	if tt == nil {
		tt = &TrackedTask{GID: gid, CreatedAt: time.Now()}
		t.tasks[gid] = tt
	}
	t.observeActivationLocked(tt, time.Now(), baseline, baselineKnown)
}

// PendingAdmissionTicket reports the gid's live admitted-not-started ticket,
// or false when no reservation is pending. Read-only; callers compare the
// token against the scheduler opportunity they hold.
func (t *TaskTracker) PendingAdmissionTicket(gid string) (AdmissionTicket, bool) {
	if t == nil {
		return AdmissionTicket{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	tt := t.tasks[gid]
	if tt == nil || !tt.admittedNotStarted || tt.admissionToken == 0 {
		return AdmissionTicket{}, false
	}
	return AdmissionTicket{GID: gid, Token: tt.admissionToken}, true
}

// GetThreadInfo 获取任务的线程信息
func (t *TaskTracker) GetThreadInfo(gid string) (threadCount int, isExploration bool, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if tracked := t.tasks[gid]; tracked != nil && tracked.ThreadCount > 0 {
		return tracked.ThreadCount, tracked.IsExploration, true
	}
	return 0, false, false
}

// UpdateLiveConnections 更新任务运行时的实时瞬时活跃连接数（由引擎进度遥测上报动态更新）
func (t *TaskTracker) UpdateLiveConnections(gid string, connections int) {
	if connections < 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if tracked := t.tasks[gid]; tracked != nil {
		if tracked.LiveConnections == connections {
			return
		}
		tracked.LiveConnections = connections
	} else if connections > 0 {
		t.tasks[gid] = &TrackedTask{
			GID:             gid,
			LiveConnections: connections,
			CreatedAt:       time.Now(),
		}
	}
}

// GetLiveConnections 获取任务的实时瞬时活跃连接数
func (t *TaskTracker) GetLiveConnections(gid string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if tracked := t.tasks[gid]; tracked != nil {
		return tracked.LiveConnections
	}
	return 0
}

// SetScope sets the task's scope/TTFB/domain info (backward-compatible wrapper).
func (t *TaskTracker) SetScope(gid string, scope string, ttfbMs int64, domain string) {
	t.SetScopeAndEnv(gid, scope, ttfbMs, domain, "")
}

// SetScopeAndEnv sets the task's scope/TTFB/domain/envKey info (called by AddUri).
func (t *TaskTracker) SetScopeAndEnv(gid string, scope string, ttfbMs int64, domain string, envKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked := t.tasks[gid]
	if tracked != nil {
		tracked.Scope = scope
		// Don't overwrite an existing TTFB with a non-value (<=0). Resume hook
		// passes ttfbMs=0 when it only wants to update scope/domain/envKey;
		// zeroing here would discard the AddUri probe. New tasks accept raw value.
		if ttfbMs > 0 {
			tracked.TTFBMs = ttfbMs
		}
		tracked.Domain = domain
		tracked.CurrentEnvKey = envKey
		if tracked.PeakEnvKey == "" {
			tracked.PeakEnvKey = envKey // initial value = CurrentEnvKey
		}
	} else {
		t.tasks[gid] = &TrackedTask{
			GID:           gid,
			Scope:         scope,
			TTFBMs:        ttfbMs,
			Domain:        domain,
			CurrentEnvKey: envKey,
			PeakEnvKey:    envKey,
			CreatedAt:     time.Now(),
		}
	}
}

// SetTTFB writes TTFB only, without touching scope/domain/envKey.
// Called from FirstByteMsg handler to asynchronously update TTFB.
func (t *TaskTracker) SetTTFB(gid string, ms int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tracked := t.tasks[gid]
	if tracked != nil && ms > 0 {
		tracked.TTFBMs = ms
	}
}

// GetScopeAndEnv returns the task's scope/domain/envKey info.
func (t *TaskTracker) GetScopeAndEnv(gid string) (scope, domain, envKey string, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tracked := t.tasks[gid]
	if tracked == nil || tracked.Scope == "" {
		return "", "", "", false
	}
	return tracked.Scope, tracked.Domain, tracked.CurrentEnvKey, true
}

// SetTargetBandwidth persists Calculate occupancy for hybrid ledger seeding.
// Aria2-only: Surge admission reservations write their own Target floor via
// ReserveAdmission, so sg_ rows only receive the scalar values here and never
// synthesize an active status or compat claim. For non-Surge gids, bw>0 on an
// empty-status row still sets Status="active" (bounded confirmed-add
// placeholder: defense-in-depth for the AddURI pre-event window).
func (t *TaskTracker) SetTargetBandwidth(gid string, bw int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tracked := t.tasks[gid]
	if tracked == nil {
		tracked = &TrackedTask{GID: gid, CreatedAt: time.Now()}
		t.tasks[gid] = tracked
	}
	tracked.TargetBandwidth = bw
	if bw <= 0 {
		return
	}
	tracked.AllocatedAt = time.Now()
	if strings.HasPrefix(gid, "sg_") {
		return
	}
	if tracked.Status == "" {
		tracked.Status = "active"
	}
}

// GetOccupancyTrackedTasks returns tracker copies that feed the unified
// occupancy snapshot: real active tasks, bounded confirmed-add placeholders,
// and any explicit admitted-not-started reservation. Ordinary waiting/paused
// rows — Surge included — contribute zero unless they hold a reservation;
// terminal tasks are excluded. The getter never writes times or widens the
// active-set semantics used by GetActiveTrackedTasks.
func (t *TaskTracker) GetOccupancyTrackedTasks() []TrackedTask {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]TrackedTask, 0, len(t.tasks))
	for _, tt := range t.tasks {
		switch tt.Status {
		case "complete", "error":
			continue
		}
		if tt.admittedNotStarted {
			result = append(result, *tt)
			continue
		}
		switch tt.Status {
		case "active":
			result = append(result, *tt)
		case "":
			if tt.TargetBandwidth > 0 && tt.compatClaim == smartthread.CompatAria2Add {
				result = append(result, *tt)
			}
		}
	}
	return result
}

// acceptPeakSpeed writes PeakSpeed and, when CurrentEnvKey is non-empty,
// refreshes PeakEnvKey for SPEC-176 peak-time attribution. Empty CurrentEnvKey
// must not wipe an existing PeakEnvKey.
func acceptPeakSpeed(tt *TrackedTask, speed int64) {
	tt.PeakSpeed = speed
	if tt.CurrentEnvKey != "" {
		tt.PeakEnvKey = tt.CurrentEnvKey
	}
}

// RecordPeakEfficiency updates PeakSpeed and PeakThreadCount for the given gid
// using the D3 efficiency-guarded ratchet with bestEff anchoring (Edge Case 14).
// Only accepts the incoming pair when it represents a more optimal working point.
// Rejects bloated N where absolute speed marginally increases but per-thread
// efficiency crashes below the bestEff-anchored guard.
// On every accepted PeakSpeed write, PeakEnvKey is set to CurrentEnvKey when
// non-empty (macro paired-write inherits SPEC-176 peak-time attribution after
// event-path sampling was retired). ThreadCount-only updates do not refresh
// PeakEnvKey. Does not touch sampleSpeedInternal (Aria2 tick path).
func (t *TaskTracker) RecordPeakEfficiency(gid string, peakSpeed int64, peakWorkers int) {
	if peakWorkers <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	tt, ok := t.tasks[gid]
	if !ok {
		return
	}

	newEff := peakSpeed / int64(peakWorkers)
	if newEff > tt.BestEff {
		tt.BestEff = newEff
	}

	if tt.PeakThreadCount == 0 {
		acceptPeakSpeed(tt, peakSpeed)
		tt.PeakThreadCount = peakWorkers
		return
	}

	// D3 ratchet with bestEff-anchored guard (Edge Case 14: anchor on session
	// best efficiency, not current record efficiency, to prevent N creep).
	// Absolute speed guard (peakSpeedGuardBand=0.90): adopting fewer peakWorkers
	// requires the incoming speed to be ≥ 90% of the recorded peak — prevents
	// "缝合怪" records like [32MB/s peak, 4 workers] where the speed was achieved
	// at a much higher worker count.
	guardEff := int64(float64(tt.BestEff) * trackerEfficiencyGuardBand)
	if newEff >= guardEff {
		// Efficiency within 15% of best — accept if throughput improved ≥5%
		// (peakRaiseBand noise gate) or workers reduced at comparable throughput (≥90% of peak).
		if float64(peakSpeed) > float64(tt.PeakSpeed)*trackerPeakRaiseBand {
			if peakSpeed > tt.PeakSpeed {
				acceptPeakSpeed(tt, peakSpeed)
			}
			tt.PeakThreadCount = peakWorkers
		} else if peakWorkers < tt.PeakThreadCount && float64(peakSpeed) >= float64(tt.PeakSpeed)*trackerPeakSpeedGuardBand {
			if peakSpeed > tt.PeakSpeed {
				acceptPeakSpeed(tt, peakSpeed)
			}
			tt.PeakThreadCount = peakWorkers
		}
	} else if float64(peakSpeed) > float64(tt.PeakSpeed)*trackerPeakRaiseBand {
		// Absolute throughput up ≥5% but efficiency below guard — only update
		// PeakSpeed (for V_target/BtlBw), keep efficient PeakThreadCount unchanged.
		acceptPeakSpeed(tt, peakSpeed)
	}
}
