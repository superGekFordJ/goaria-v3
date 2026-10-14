package monitor

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
)

// TrackedTask 追踪单个任务的状态
type TrackedTask struct {
	GID             string
	Status          string
	TotalLength     int64
	CompletedLength int64
	CreatedAt       time.Time

	// 速度采样
	PeakSpeed      int64
	SustainedSpeed int64
	SustainedCount int

	// PeakThreadCount records the worker count at the time PeakSpeed was achieved.
	// Written by RecordPeakEfficiency (convergence layer); read by the terminal
	// finish path to write accurate ThreadCount into speedstats.
	PeakThreadCount int

	// BestEff is the session-anchored best single-thread efficiency (only increases).
	// Used by RecordPeakEfficiency as the D3 guard anchor to prevent N creep.
	BestEff int64

	// 线程信息
	ThreadCount   int
	IsExploration bool

	// LiveConnections 记录由引擎进度遥测上报的当前瞬时活跃连接数
	LiveConnections int

	// 文件信息（用于历史记录）
	FilePath      string
	Dir           string
	SourceURL     string
	DownloadGroup *rpc.DownloadGroup

	// Scope/TTFB/Domain/EnvKey（speedstats 扩展）
	Scope         string
	TTFBMs        int64
	Domain        string
	CurrentEnvKey string // current network environment fingerprint, updated on network change
	PeakEnvKey    string // envKey at the time PeakSpeed was achieved, used by AddRecordV2

	// KeepAlive flag — true when initial split < nSat
	IsKeepAlive bool

	// MinChunk is the per-task minimum chunk size (bytes), captured from
	// ThreadParams.MinSize at task-add time. 0 means unknown/uncaptured
	// (non-Surge path, event-created, or restart recovery).
	MinChunk int64

	// TargetBandwidth is the Calculate-clamped occupancy (bytes/s) persisted
	// after a successful Aria2 AddUri for ledger seeding, or the reservation
	// floor written by ReserveAdmission for the managed admission pipeline.
	TargetBandwidth int64
	// AllocatedAt is when TargetBandwidth was last written (bw>0). Distinct
	// from CreatedAt, which SetThreadInfo refreshes for grace-period reuse.
	AllocatedAt time.Time

	// AddedAt is when this session observed the user add (MarkAdded). Zero
	// means unknown (restart recovery, external RPC add). Unlike CreatedAt,
	// placeholders and grace-period refreshes never touch it.
	AddedAt time.Time

	// --- Unified-occupancy run facts (read via smartthread.TaskOccupancy) ---
	// ActivatedAt is the first real activation observation of the current
	// run: authoritative active-list membership or an engine start event
	// admitted past the lifecycle gates. Cleared on real non-active and
	// terminal boundaries; rebuilt by the next real start. Readers,
	// parameter writes, TTFB, and duplicate active events never touch it.
	ActivatedAt time.Time
	// FirstProgressAt stamps the first CompletedLength growth above this
	// run's baseline. Recorded once per run as a fact — including after the
	// lease ends — and never revived by replays or completed regressions.
	FirstProgressAt time.Time
	// progressBaseline is the completed length pinned at activation;
	// progressBaselineKnown is false until a reliable checkpoint exists —
	// the first reliable observation then establishes the baseline without
	// counting as growth.
	progressBaseline      int64
	progressBaselineKnown bool
	// compatClaim marks a retained legacy occupancy claim source (synthetic
	// Surge claim, bounded Aria2 confirmed-add placeholder, transferred
	// resume hold). A real activation clears it.
	compatClaim smartthread.CompatKind
	// addAnchorAt pins the confirmed-add instant for CompatAria2Add. Fixed
	// once at establishment; reads and parameter writes never extend it.
	addAnchorAt time.Time
	// admittedNotStarted is the explicit admitted-not-started reservation
	// fact for the scheduler-managed admission pipeline. admissionToken is
	// the scheduler-issued opportunity identity it belongs to; the pair is
	// written/cleared atomically and the token is kept as a high-water mark
	// afterwards so an older opportunity can never re-reserve the row.
	admittedNotStarted bool
	admissionToken     uint64

	// Active time observed on the aria2 tick path; gaps above
	// activeSampleGapCap are dropped, maxActiveGap bounds the error.
	activeElapsed time.Duration
	lastActiveAt  time.Time
	maxActiveGap  time.Duration

	// Set only on terminal copies by the Surge complete prep.
	terminalElapsed     time.Duration
	peakFromAvgFallback bool

	// terminalEntry/terminalGeneration identify the lifecycle entry and
	// generation that accepted this terminal copy. Set only at acceptance;
	// private so delayed consumers cannot forge identity — a reopen bumps the
	// generation, and remove/re-add replaces the entry pointer.
	terminalEntry      *lifecycleState
	terminalGeneration uint64
}

// activeSampleGapCap is 2x the headless tick; longer gaps mean aria2 was
// unreachable or the host slept, so they are not counted as active time.
const activeSampleGapCap = 10 * time.Second

// lifecycleState is the per-GID gate for stopped→live retirement vs terminal
// accept/history write. generation bumps only on ReopenAfterStoppedToLive;
// terminalHandled is scoped to the current generation.
type lifecycleState struct {
	mu              sync.Mutex
	generation      uint64
	terminalHandled bool
	revision        uint64
}

// lifecycleStamp includes entry identity, so deletion and same-ID re-add differ.
type lifecycleStamp struct {
	entry    *lifecycleState
	revision uint64
}

func (t *TaskTracker) captureLifecycleStamp(gid string) lifecycleStamp {
	t.mu.RLock()
	defer t.mu.RUnlock()
	entry := t.getLifecycle(gid)
	return lifecycleStamp{entry: entry, revision: entry.revision}
}

func (t *TaskTracker) lifecycleStampCurrent(gid string, stamp lifecycleStamp) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.lifecycleMu.Lock()
	entry := t.lifecycle[gid]
	t.lifecycleMu.Unlock()
	return entry != nil && entry == stamp.entry && entry.revision == stamp.revision
}

// Caller holds tracker.mu; even a deduplicated terminal invalidates older reads.
func (t *TaskTracker) advanceLifecycleRevisionLocked(gid string) {
	t.getLifecycle(gid).revision++
}

// TaskTracker 后端任务追踪器
type TaskTracker struct {
	mu    sync.RWMutex
	tasks map[string]*TrackedTask // gid -> task

	// 已处理的完成任务 GID（防止重复处理）；与 lifecycle.terminalHandled 同世代保持一致
	processedComplete map[string]bool

	lifecycleMu sync.Mutex
	lifecycle   map[string]*lifecycleState

	// Test-only: runs inside retire after reopen, before history.Remove.
	retireBetweenReopenAndRemove func(string)
	// Test-only: runs after MoveTaskToActive inside the resume gate, before retire.
	afterMoveBeforeRetire func(string)
}

// NewTaskTracker 创建新的任务追踪器
func NewTaskTracker() *TaskTracker {
	return &TaskTracker{
		tasks:             make(map[string]*TrackedTask),
		processedComplete: make(map[string]bool),
		lifecycle:         make(map[string]*lifecycleState),
	}
}

// Update 根据 Aria2 返回的任务列表更新追踪状态
// 返回已完成的任务列表（仅首次检测到的）
func (t *TaskTracker) Update(active, waiting, stopped []rpc.Task) []*TrackedTask {
	t.mu.Lock()
	defer t.mu.Unlock()

	var completed []*TrackedTask
	currentGids := make(map[string]bool)

	// 处理活跃任务：更新速度采样
	for _, task := range active {
		currentGids[task.GID] = true
		t.updateActiveTask(task)
	}

	// 处理等待任务
	for _, task := range waiting {
		currentGids[task.GID] = true
		t.ensureTracked(task)
		tracked := t.tasks[task.GID]
		// Authoritative aria2 waiting/paused: record the observed status and
		// a reliable completed value (baseline source for the next run), and
		// end any synthetic-active claim or add placeholder — an engine-queued
		// row never counts occupancy. No isTerminalAcceptedLocked gate here,
		// by ordering: Cache.UpdateFromAria2 runs earlier in the same tick and
		// already reopened every gid legitimately leaving stopped (clearing
		// the accepted marker), so a still-accepted terminal has no live-list
		// authority to protect. Reordering those calls would break that.
		applyAuthoritativeStatusLocked(tracked, task.Status)
		if cl := parseInt64(task.CompletedLength); cl > 0 {
			tracked.CompletedLength = cl
		}
		// Paused and queued aria2 tasks both sit in tellWaiting; stop the clock.
		tracked.lastActiveAt = time.Time{}
	}

	// 处理已停止任务：检测新完成
	for _, task := range stopped {
		currentGids[task.GID] = true
		if task.Status == "complete" || task.Status == "error" {
			t.advanceLifecycleRevisionLocked(task.GID)
			// Generation-scoped: do not take lifecycle lock here (holds tracker.mu).
			if t.isTerminalAcceptedLocked(task.GID) {
				continue
			}

			if tracked := t.tasks[task.GID]; tracked != nil {
				if tracked.Status != "complete" && tracked.Status != "error" {
					// 状态变为完成，触发完成处理
					applyAuthoritativeStatusLocked(tracked, task.Status)
					t.fillTaskInfo(tracked, task)
					cp := copyTrackedTask(tracked)
					t.markTerminalAcceptedLocked(task.GID)
					t.stampTerminalCopyLocked(task.GID, cp)
					completed = append(completed, cp)
				}
			} else {
				// 新发现的已完成任务（可能是重启后）
				tracked = t.createTrackedTask(task)
				applyAuthoritativeStatusLocked(tracked, task.Status)
				t.tasks[task.GID] = tracked
				cp := copyTrackedTask(tracked)
				t.markTerminalAcceptedLocked(task.GID)
				t.stampTerminalCopyLocked(task.GID, cp)
				completed = append(completed, cp)
			}
		}
	}

	// 清理已移除的任务
	for gid, tracked := range t.tasks {
		if strings.HasPrefix(gid, "sg_") {
			continue
		}
		if !currentGids[gid] {
			// 给新创建的任务 5 秒宽限期，避免 Aria2 尚未报告时的竞态条件
			if time.Since(tracked.CreatedAt) < TaskGracePeriod {
				continue
			}

			delete(t.tasks, gid)
			// 不要在此处清理 processedComplete — 防止引擎瞬时故障期间重复处理完成事件
		}
	}

	return completed
}

// updateActiveTask 更新活跃任务的速度采样
func (t *TaskTracker) updateActiveTask(task rpc.Task) {
	tracked := t.tasks[task.GID]
	if tracked == nil {
		tracked = t.createTrackedTask(task)
		t.tasks[task.GID] = tracked
	}

	now := time.Now()
	if !tracked.lastActiveAt.IsZero() {
		if gap := now.Sub(tracked.lastActiveAt); gap > 0 && gap <= activeSampleGapCap {
			tracked.activeElapsed += gap
			if gap > tracked.maxActiveGap {
				tracked.maxActiveGap = gap
			}
		}
	}
	tracked.lastActiveAt = now

	// Authoritative active-list membership is real start evidence: the
	// observation's own completed value is the reliable run baseline, and
	// it also ends any confirmed-add placeholder for this gid.
	completed := parseInt64(task.CompletedLength)
	t.observeActivationLocked(tracked, now, completed, true)
	t.observeCompletedLocked(tracked, now, completed)

	// 更新进度
	tracked.Status = task.Status
	tracked.CompletedLength = completed
	tracked.TotalLength = parseInt64(task.TotalLength)

	// 如果 FilePath 为空但 task 有文件信息，补充之
	// 场景：首次创建时 Lite 任务无文件，后续 tick enrichTasks 后有了
	if tracked.FilePath == "" && len(task.Files) > 0 && task.Files[0].Path != "" {
		tracked.FilePath = task.Files[0].Path
		tracked.Dir = task.Dir
		if len(task.Files[0].Uris) > 0 {
			tracked.SourceURL = task.Files[0].Uris[0].Uri
		}
	}
	if task.DownloadGroup != nil {
		tracked.DownloadGroup = copyDownloadGroup(task.DownloadGroup)
	}

	// 速度采样（仅 >50MB 文件）
	speed := parseInt64(task.DownloadSpeed)
	if speed > 0 && tracked.TotalLength > speedstats.MinFileSize {
		t.sampleSpeed(tracked, speed)
	}
}

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

// createTrackedTask 创建追踪任务
// 注意：task 应该已经被 enrichTasks() 丰富过，包含文件信息
func (t *TaskTracker) createTrackedTask(task rpc.Task) *TrackedTask {
	tracked := &TrackedTask{
		GID:             task.GID,
		Status:          task.Status,
		TotalLength:     parseInt64(task.TotalLength),
		CompletedLength: parseInt64(task.CompletedLength),
		Dir:             task.Dir,
		DownloadGroup:   copyDownloadGroup(task.DownloadGroup),
		CreatedAt:       time.Now(),
	}

	// 优先使用 task.Files（已被 enrichTasks 填充）
	if len(task.Files) > 0 && task.Files[0].Path != "" {
		tracked.FilePath = task.Files[0].Path
		if len(task.Files[0].Uris) > 0 {
			tracked.SourceURL = task.Files[0].Uris[0].Uri
		}
	}

	return tracked
}

func copyTrackedTask(task *TrackedTask) *TrackedTask {
	if task == nil {
		return nil
	}
	copy := *task
	copy.DownloadGroup = copyDownloadGroup(task.DownloadGroup)
	return &copy
}

// fillTaskInfo 填充任务完整信息（从 stopped 任务）
// 注意：task 应该已经被 enrichTasks() 丰富过，包含文件信息
func (t *TaskTracker) fillTaskInfo(tracked *TrackedTask, task rpc.Task) {
	tracked.TotalLength = parseInt64(task.TotalLength)
	tracked.CompletedLength = parseInt64(task.CompletedLength)
	tracked.Dir = task.Dir
	if task.DownloadGroup != nil {
		tracked.DownloadGroup = copyDownloadGroup(task.DownloadGroup)
	}

	// 仅当新信息有效时才覆盖（避免用空值覆盖已有值）
	if len(task.Files) > 0 && task.Files[0].Path != "" {
		tracked.FilePath = task.Files[0].Path
		if len(task.Files[0].Uris) > 0 {
			tracked.SourceURL = task.Files[0].Uris[0].Uri
		}
	}
}

// ensureTracked 确保任务被追踪
func (t *TaskTracker) ensureTracked(task rpc.Task) {
	if t.tasks[task.GID] == nil {
		t.tasks[task.GID] = t.createTrackedTask(task)
	}
}

const TaskGracePeriod = 5 * time.Second

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

// MarkAdded records that this session observed the user add of gid. The
// clock starts now so add→first active tick counts; a waiting tick clears it.
// Re-marking an already tracked gid intentionally re-anchors measurement to
// the latest observed add; the discarded window is at most one tick.
// For non-Surge gids it also pins the bounded confirmed-add placeholder —
// only while no authoritative status or real activation exists, so a late
// MarkAdded can never resurrect or extend a placeholder the engine already
// superseded.
func (t *TaskTracker) MarkAdded(gid string) {
	if gid == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	tracked := t.tasks[gid]
	if tracked == nil {
		tracked = &TrackedTask{GID: gid, CreatedAt: now}
		t.tasks[gid] = tracked
	}
	tracked.AddedAt = now
	if !strings.HasPrefix(gid, "sg_") && tracked.ActivatedAt.IsZero() &&
		!t.isTerminalAcceptedLocked(gid) &&
		(tracked.Status == "" || (tracked.Status == "active" && tracked.compatClaim == smartthread.CompatNone)) {
		if tracked.compatClaim == smartthread.CompatNone {
			tracked.compatClaim = smartthread.CompatAria2Add
		}
		if tracked.addAnchorAt.IsZero() {
			tracked.addAnchorAt = now
		}
	}
	tracked.lastActiveAt = now
	tracked.activeElapsed = 0
	tracked.maxActiveGap = 0
	tracked.terminalElapsed = 0
	tracked.peakFromAvgFallback = false
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
// Locking: runs under the gid lifecycle gate plus t.mu — NOT reentrant.
// Callers must not invoke it while already holding the same gid's
// lifecycle gate (RunUnderLifecycle would deadlock on ls.mu).
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
	t.RunUnderLifecycle(gid, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.isTerminalAcceptedLocked(gid) {
			return
		}
		tt := t.tasks[gid]
		if tt == nil || tt.Status != "waiting" {
			return
		}
		switch {
		case tt.admissionToken > token:
			return // older opportunity than the observed high-water mark
		case tt.admissionToken == token && !tt.admittedNotStarted:
			return // same opportunity already consumed or rolled back
		case tt.admissionToken == token:
			ticket = AdmissionTicket{GID: gid, Token: token} // idempotent reapply
			return
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
		ticket = AdmissionTicket{GID: gid, Token: token}
		created = true
	})
	return ticket, created
}

// ReleaseAdmission rolls back a still-pending admission reservation for the
// same lifecycle/token pair: it clears the pending flag, this ticket's
// Target floor, and the reservation-owned activation anchor. It refuses to
// disturb a started task (pending was consumed by ObserveActivation), a
// newer token (a re-admission supersedes stale rollbacks), or a row that
// never held this ticket — including a same-ID replacement.
func (t *TaskTracker) ReleaseAdmission(gid string, token uint64) bool {
	released := false
	if t == nil || !IsSgGid(gid) || token == 0 {
		return false
	}
	t.RunUnderLifecycle(gid, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		tt := t.tasks[gid]
		if tt == nil || !tt.admittedNotStarted || tt.admissionToken != token {
			return
		}
		tt.admittedNotStarted = false
		clearActivationFactsLocked(tt)
		tt.TargetBandwidth = 0
		tt.AllocatedAt = time.Time{}
		released = true
	})
	return released
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

// GetTrackedTask returns a value copy of the tracked entry.
func (t *TaskTracker) GetTrackedTask(gid string) (TrackedTask, bool) {
	if t == nil {
		return TrackedTask{}, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	tracked := t.tasks[gid]
	if tracked == nil {
		return TrackedTask{}, false
	}
	return *copyTrackedTask(tracked), true
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

func (t *TaskTracker) SetTaskGroup(gid string, group rpc.DownloadGroup) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tracked := t.tasks[gid]
	if tracked == nil {
		tracked = &TrackedTask{GID: gid, CreatedAt: time.Now()}
		t.tasks[gid] = tracked
	}
	tracked.DownloadGroup = copyDownloadGroup(&group)
}

func (t *TaskTracker) GetTaskGroup(gid string) *rpc.DownloadGroup {
	t.mu.RLock()
	defer t.mu.RUnlock()
	tracked := t.tasks[gid]
	if tracked == nil {
		return nil
	}
	return copyDownloadGroup(tracked.DownloadGroup)
}

func (t *TaskTracker) UpdateTaskGroupName(groupKey, name, status string) int {
	groupKey = strings.TrimSpace(groupKey)
	name = strings.TrimSpace(name)
	status = strings.TrimSpace(status)
	if groupKey == "" || name == "" || !rpc.IsDownloadGroupNameStatus(status) {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	changed := 0
	for _, tracked := range t.tasks {
		if tracked == nil || tracked.DownloadGroup == nil || tracked.DownloadGroup.ID != groupKey {
			continue
		}
		if tracked.DownloadGroup.Name == name && tracked.DownloadGroup.NameStatus == status {
			continue
		}
		updated := *tracked.DownloadGroup
		updated.Name = name
		updated.NameStatus = status
		tracked.DownloadGroup = &updated
		changed++
	}
	return changed
}

// EnsureTrackedFromEvent 从 Surge 事件创建或更新 tracker 条目
// 用于 DownloadStartedMsg / DownloadQueuedMsg，不等 tick
func (t *TaskTracker) EnsureTrackedFromEvent(gid string, totalLength int64, sourceURL string, threadCount int, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked := t.tasks[gid]
	if tracked != nil {
		if totalLength > 0 {
			tracked.TotalLength = totalLength
		}
		if sourceURL != "" && tracked.SourceURL == "" {
			tracked.SourceURL = sourceURL
		}
		if threadCount > 0 && tracked.ThreadCount == 0 {
			tracked.ThreadCount = threadCount
		}
		// Don't resurrect a terminal task (complete/error) back to a
		// non-terminal status from a late/out-of-order event or reconcile.
		if status != "" && !t.isTerminalAcceptedLocked(gid) {
			applyAuthoritativeStatusLocked(tracked, status)
		}
		return
	}

	t.tasks[gid] = &TrackedTask{
		GID:         gid,
		Status:      status,
		TotalLength: totalLength,
		SourceURL:   sourceURL,
		ThreadCount: threadCount,
		CreatedAt:   time.Now(),
	}
	if t.tasks[gid].Status == "" {
		t.tasks[gid].Status = "active"
	}
}

// EnsureTrackedComplete updates or initializes tracker entry on task completion.
// Ensures both TotalLength and CompletedLength are synchronized to totalLength.
func (t *TaskTracker) EnsureTrackedComplete(gid string, totalLength int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked := t.tasks[gid]
	if tracked != nil {
		if totalLength > 0 {
			tracked.TotalLength = totalLength
			tracked.CompletedLength = totalLength
		}
		return
	}

	t.tasks[gid] = &TrackedTask{
		GID:             gid,
		Status:          "complete",
		TotalLength:     totalLength,
		CompletedLength: totalLength,
		CreatedAt:       time.Now(),
	}
}

// RunUnderLifecycle serializes stopped→live retirement with terminal acceptance
// for a single GID. fn must not call RunUnderLifecycle for the same GID.
// Lock order: acquire the GID lifecycle lock before TaskTracker.mu.
func (t *TaskTracker) RunUnderLifecycle(gid string, fn func()) {
	if fn == nil {
		return
	}
	if t == nil || gid == "" {
		fn()
		return
	}
	for {
		ls := t.getLifecycle(gid)
		ls.mu.Lock()
		t.lifecycleMu.Lock()
		current := t.lifecycle[gid] == ls
		t.lifecycleMu.Unlock()
		if !current {
			ls.mu.Unlock()
			continue
		}
		defer ls.mu.Unlock()
		fn()
		return
	}
}

func (t *TaskTracker) getLifecycle(gid string) *lifecycleState {
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	if t.lifecycle == nil {
		t.lifecycle = make(map[string]*lifecycleState)
	}
	ls := t.lifecycle[gid]
	if ls == nil {
		ls = &lifecycleState{}
		t.lifecycle[gid] = ls
	}
	return ls
}

// markTerminalAcceptedLocked records terminal acceptance for the current
// generation. Caller must hold tracker.mu.
func (t *TaskTracker) markTerminalAcceptedLocked(gid string) {
	if t.processedComplete == nil {
		t.processedComplete = make(map[string]bool)
	}
	t.processedComplete[gid] = true
	ls := t.getLifecycle(gid)
	ls.terminalHandled = true
}

// stampTerminalCopyLocked tags an accepted terminal copy with the accepting
// lifecycle entry and generation. Caller holds tracker.mu.
func (t *TaskTracker) stampTerminalCopyLocked(gid string, copy *TrackedTask) {
	if copy == nil {
		return
	}
	ls := t.getLifecycle(gid)
	copy.terminalEntry = ls
	copy.terminalGeneration = ls.generation
}

// TerminalCopyCurrent reports whether task is still the accepted terminal copy
// of its generation: same lifecycle entry, same generation, still accepted.
// Call under RunUnderLifecycle for gate ordering; stale copies produced before
// a reopen/remove/re-add are rejected even when the numeric generation matches.
func (t *TaskTracker) TerminalCopyCurrent(gid string, task *TrackedTask) bool {
	if t == nil || task == nil || task.terminalEntry == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.processedComplete[gid] {
		return false
	}
	t.lifecycleMu.Lock()
	current := t.lifecycle[gid]
	t.lifecycleMu.Unlock()
	return current == task.terminalEntry && current.generation == task.terminalGeneration && current.terminalHandled
}

// SurgeGIDs snapshots tracked Surge GIDs so poll reconciliation can union
// tracker-only candidates (cache lost the row but tracker still tracks it).
func (t *TaskTracker) SurgeGIDs() []string {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, 0, len(t.tasks))
	for gid := range t.tasks {
		if IsSgGid(gid) {
			out = append(out, gid)
		}
	}
	return out
}

// isTerminalAcceptedLocked reports whether gid already accepted a terminal for
// the current generation. Caller must hold tracker.mu (or RLock).
func (t *TaskTracker) isTerminalAcceptedLocked(gid string) bool {
	if !t.processedComplete[gid] {
		return false
	}
	ls := t.getLifecycle(gid)
	return ls.terminalHandled
}

// TerminalAcceptedInCurrentGeneration reports whether gid is marked complete
// for the current lifecycle generation (call under RunUnderLifecycle when
// racing retire).
func (t *TaskTracker) TerminalAcceptedInCurrentGeneration(gid string) bool {
	if t == nil || gid == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.isTerminalAcceptedLocked(gid)
}

// LifecycleGeneration returns the current stopped→live generation for tests.
func (t *TaskTracker) LifecycleGeneration(gid string) uint64 {
	if t == nil || gid == "" {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.getLifecycle(gid).generation
}

// HasLifecycleEntry reports whether a lifecycle map entry exists (tests).
func (t *TaskTracker) HasLifecycleEntry(gid string) bool {
	if t == nil || gid == "" {
		return false
	}
	t.lifecycleMu.Lock()
	defer t.lifecycleMu.Unlock()
	_, ok := t.lifecycle[gid]
	return ok
}

// SetStatusFromEvent updates a tracked task's status from a Surge lifecycle
// event (pause/resume) without re-running the full ensure logic. No-op if
// the task is not yet tracked (reconcileSurgeCache will seed it).
// Does not bump lifecycle generation.
func (t *TaskTracker) SetStatusFromEvent(gid string, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Don't resurrect a terminal task (complete/error) back to a non-terminal
	// status from a late/out-of-order event or reconcile mismatch.
	if t.isTerminalAcceptedLocked(gid) {
		return
	}
	if tracked := t.tasks[gid]; tracked != nil {
		// Status alone never creates activation; a non-active boundary ends
		// this run's eligibility.
		applyAuthoritativeStatusLocked(tracked, status)
	}
}

// ReopenAfterStoppedToLive clears terminal dedup so a confirmed stopped→live
// resume can accept a later complete/error. Call only from authoritative
// resume paths (alongside history retirement), never from generic pause/status.
// Bumps lifecycle generation and clears the generation-scoped terminal marker.
// The explicit reopen ends the previous run's lease facts and admission
// lineage; "active" here never counts as real start evidence.
func (t *TaskTracker) ReopenAfterStoppedToLive(gid, status string) {
	if t == nil || gid == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ls := t.getLifecycle(gid)
	ls.generation++
	ls.revision++
	ls.terminalHandled = false
	delete(t.processedComplete, gid)
	if status == "" {
		status = "active"
	}
	if tracked := t.tasks[gid]; tracked != nil {
		if tracked.admissionToken > 0 {
			// An explicit reopen ends the admission lineage: pending flag,
			// floor, and anchor go together so no stale reservation lease
			// survives into the next run.
			tracked.admittedNotStarted = false
			tracked.TargetBandwidth = 0
			tracked.AllocatedAt = time.Time{}
		}
		clearActivationFactsLocked(tracked)
		applyAuthoritativeStatusLocked(tracked, status)
	}
}

// UpdateProgressFromEvent refreshes tracker TotalLength/CompletedLength from a
// Surge Progress/BatchProgress event. Lengths only — does not touch PeakSpeed,
// PeakThreadCount, PeakEnvKey, or Sustained*. ConvergenceTicker derives rawBps
// from CompletedLength deltas; PeakSpeed ownership stays with RecordPeakEfficiency.
func (t *TaskTracker) UpdateProgressFromEvent(gid string, totalLength, completedLength int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked := t.tasks[gid]
	if tracked == nil {
		return
	}

	if totalLength > 0 {
		tracked.TotalLength = totalLength
	}
	if completedLength > 0 {
		// Growth is a fact only inside a live activation run; queued/paused
		// checkpoints and terminal length fills merely seed the stored
		// completed value used as the next run's baseline source.
		t.observeCompletedLocked(tracked, time.Now(), completedLength)
		tracked.CompletedLength = completedLength
	}
}

// MarkCompleteFromEvent 从 Surge complete/error 事件标记完成
// 返回副本供 finishTerminalCopy 使用；当前世代已接受则返回 nil
func (t *TaskTracker) MarkCompleteFromEvent(gid string, status string) *TrackedTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.advanceLifecycleRevisionLocked(gid)

	tracked := t.tasks[gid]
	if tracked == nil {
		return nil
	}
	if t.isTerminalAcceptedLocked(gid) {
		return nil
	}

	applyAuthoritativeStatusLocked(tracked, status)
	t.markTerminalAcceptedLocked(gid)
	copy := copyTrackedTask(tracked)
	t.stampTerminalCopyLocked(gid, copy)
	return copy
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

// RemoveTask 从追踪器中移除任务
func (t *TaskTracker) RemoveTask(gid string) {
	t.RunUnderLifecycle(gid, func() { t.removeTaskLocked(gid) })
}

// Caller holds the GID gate; removal is its last local write.
func (t *TaskTracker) removeTaskLocked(gid string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.tasks, gid)
	delete(t.processedComplete, gid)
	t.lifecycleMu.Lock()
	delete(t.lifecycle, gid)
	t.lifecycleMu.Unlock()
}

// SetKeepAlive sets the IsKeepAlive flag for a tracked task.
func (t *TaskTracker) SetKeepAlive(gid string, keepAlive bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tracked := t.tasks[gid]
	if tracked != nil {
		tracked.IsKeepAlive = keepAlive
	}
}

// SetMinChunk sets the per-task minimum chunk size (from ThreadParams.MinSize).
// Called at task-add time (Surge path) by the tasks service.
func (t *TaskTracker) SetMinChunk(gid string, minChunk int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tracked := t.tasks[gid]
	if tracked != nil {
		tracked.MinChunk = minChunk
	} else {
		t.tasks[gid] = &TrackedTask{
			GID:       gid,
			MinChunk:  minChunk,
			CreatedAt: time.Now(),
		}
	}
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

// GetActiveTrackedTasks returns copies of all tracked tasks with status "active".
func (t *TaskTracker) GetActiveTrackedTasks() []TrackedTask {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]TrackedTask, 0, len(t.tasks))
	for _, tt := range t.tasks {
		if tt.Status == "active" {
			result = append(result, *tt)
		}
	}
	return result
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

// parseInt64 解析字符串为 int64
func parseInt64(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}
