package monitor

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

	// terminalToken is the tracker-global lifecycle token that accepted this
	// terminal copy. Set only at acceptance; private so delayed consumers
	// cannot forge identity — a reopen or remove/re-add mints a strictly
	// larger token, retiring every earlier copy.
	terminalToken uint64
}

// lifecycleState is the per-GID gate for stopped→live retirement vs terminal
// accept/history write. token is replaced with a strictly larger value only
// on ReopenAfterStoppedToLive (or fresh entry creation); terminalHandled is
// scoped to the current token.
type lifecycleState struct {
	mu              sync.Mutex
	token           uint64
	terminalHandled bool
}

// TaskTracker 后端任务追踪器
type TaskTracker struct {
	mu    sync.RWMutex
	tasks map[string]*TrackedTask // gid -> task

	// 已处理的完成任务 GID（防止重复处理）；与 lifecycle.terminalHandled 同 token 保持一致
	processedComplete map[string]bool

	lifecycleMu sync.Mutex
	lifecycle   map[string]*lifecycleState

	// nextLifecycleToken is the single monotone source for every lifecycle
	// token: first entry creation and each stopped→live reopen draw from it,
	// so a same-GID rebuild can never collide with a retired copy.
	nextLifecycleToken atomic.Uint64

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
		ls = &lifecycleState{token: t.nextLifecycleToken.Add(1)}
		t.lifecycle[gid] = ls
	}
	return ls
}

// markTerminalAcceptedLocked records terminal acceptance for the current
// token. Caller must hold tracker.mu.
func (t *TaskTracker) markTerminalAcceptedLocked(gid string) {
	if t.processedComplete == nil {
		t.processedComplete = make(map[string]bool)
	}
	t.processedComplete[gid] = true
	ls := t.getLifecycle(gid)
	ls.terminalHandled = true
}

// stampTerminalCopyLocked tags an accepted terminal copy with the lifecycle
// token current at acceptance. Caller holds tracker.mu.
func (t *TaskTracker) stampTerminalCopyLocked(gid string, copy *TrackedTask) {
	if copy == nil {
		return
	}
	copy.terminalToken = t.getLifecycle(gid).token
}

// TerminalCopyCurrent reports whether task is still the accepted terminal copy
// of its lifecycle token: same token, still accepted. Call under
// RunUnderLifecycle for gate ordering; copies produced before a reopen or a
// remove/re-add carry a smaller token and are always rejected.
func (t *TaskTracker) TerminalCopyCurrent(gid string, task *TrackedTask) bool {
	if t == nil || task == nil || task.terminalToken == 0 {
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
	return current != nil && current.token == task.terminalToken && current.terminalHandled
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
// the current token. Caller must hold tracker.mu (or RLock).
func (t *TaskTracker) isTerminalAcceptedLocked(gid string) bool {
	if !t.processedComplete[gid] {
		return false
	}
	ls := t.getLifecycle(gid)
	return ls.terminalHandled
}

// TerminalAcceptedInCurrentGeneration reports whether gid is marked complete
// for the current lifecycle token (call under RunUnderLifecycle when
// racing retire). The name is kept for API stability.
func (t *TaskTracker) TerminalAcceptedInCurrentGeneration(gid string) bool {
	if t == nil || gid == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.isTerminalAcceptedLocked(gid)
}

// LifecycleToken returns the current stopped→live lifecycle token for tests.
func (t *TaskTracker) LifecycleToken(gid string) uint64 {
	if t == nil || gid == "" {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.getLifecycle(gid).token
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
// Does not mint a new lifecycle token.
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
// Mints a strictly larger lifecycle token — retiring every terminal copy
// stamped by the previous run — and clears the token-scoped terminal marker.
// The explicit reopen ends the previous run's lease facts and admission
// lineage; "active" here never counts as real start evidence.
func (t *TaskTracker) ReopenAfterStoppedToLive(gid, status string) {
	if t == nil || gid == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ls := t.getLifecycle(gid)
	ls.token = t.nextLifecycleToken.Add(1)
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
// 返回副本供 finishTerminalCopy 使用；当前 token 已接受则返回 nil
func (t *TaskTracker) MarkCompleteFromEvent(gid string, status string) *TrackedTask {
	t.mu.Lock()
	defer t.mu.Unlock()

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

// parseInt64 解析字符串为 int64
func parseInt64(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}
