package monitor

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/rpc"
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
	// Written by RecordPeakEfficiency (convergence layer); read by handleTaskComplete
	// to write accurate ThreadCount into speedstats.
	PeakThreadCount int

	// BestEff is the session-anchored best single-thread efficiency (only increases).
	// Used by RecordPeakEfficiency as the D3 guard anchor to prevent N creep.
	BestEff int64

	// 线程信息
	ThreadCount   int
	IsExploration bool

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
	// after successful AddUri/Resume for hybrid ledger seeding.
	TargetBandwidth int64
	// AllocatedAt is when TargetBandwidth was last written (bw>0). Distinct
	// from CreatedAt, which SetThreadInfo refreshes for grace-period reuse.
	AllocatedAt time.Time
	// resumeOccupancyHold is set when SetTargetBandwidth writes while Status
	// is still "paused" (Resume hook runs before EventResumed). Lets
	// GetOccupancyTrackedTasks see the claim without treating long-paused
	// tasks as holding bandwidth. Cleared whenever status changes.
	resumeOccupancyHold bool
}

// lifecycleState is the per-GID gate for stopped→live retirement vs terminal
// accept/history write. generation bumps only on ReopenAfterStoppedToLive;
// terminalHandled is scoped to the current generation.
type lifecycleState struct {
	mu              sync.Mutex
	generation      uint64
	terminalHandled bool
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
	}

	// 处理已停止任务：检测新完成
	for _, task := range stopped {
		currentGids[task.GID] = true
		if task.Status == "complete" || task.Status == "error" {
			// Generation-scoped: do not take lifecycle lock here (holds tracker.mu).
			if t.isTerminalAcceptedLocked(task.GID) {
				continue
			}

			if tracked := t.tasks[task.GID]; tracked != nil {
				if tracked.Status != "complete" && tracked.Status != "error" {
					// 状态变为完成，触发完成处理
					tracked.Status = task.Status
					tracked.resumeOccupancyHold = false
					t.fillTaskInfo(tracked, task)
					completed = append(completed, copyTrackedTask(tracked))
					t.markTerminalAcceptedLocked(task.GID)
				}
			} else {
				// 新发现的已完成任务（可能是重启后）
				tracked = t.createTrackedTask(task)
				tracked.Status = task.Status
				tracked.resumeOccupancyHold = false
				t.tasks[task.GID] = tracked
				completed = append(completed, copyTrackedTask(tracked))
				t.markTerminalAcceptedLocked(task.GID)
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

	// 更新进度
	tracked.Status = task.Status
	tracked.resumeOccupancyHold = false
	tracked.CompletedLength = parseInt64(task.CompletedLength)
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

// GetThreadInfo 获取任务的线程信息
func (t *TaskTracker) GetThreadInfo(gid string) (threadCount int, isExploration bool, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if tracked := t.tasks[gid]; tracked != nil && tracked.ThreadCount > 0 {
		return tracked.ThreadCount, tracked.IsExploration, true
	}
	return 0, false, false
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
			tracked.Status = status
			tracked.resumeOccupancyHold = false
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
	ls := t.getLifecycle(gid)
	ls.mu.Lock()
	defer ls.mu.Unlock()
	fn()
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
		tracked.Status = status
		// Resume claims while paused use resumeOccupancyHold; any status
		// transition (EventResumed→active, user pause, terminal) clears it.
		tracked.resumeOccupancyHold = false
	}
}

// ReopenAfterStoppedToLive clears terminal dedup so a confirmed stopped→live
// resume can accept a later complete/error. Call only from authoritative
// resume paths (alongside history retirement), never from generic pause/status.
// Bumps lifecycle generation and clears the generation-scoped terminal marker.
func (t *TaskTracker) ReopenAfterStoppedToLive(gid, status string) {
	if t == nil || gid == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ls := t.getLifecycle(gid)
	ls.generation++
	ls.terminalHandled = false
	delete(t.processedComplete, gid)
	if status == "" {
		status = "active"
	}
	if tracked := t.tasks[gid]; tracked != nil {
		tracked.Status = status
		tracked.resumeOccupancyHold = false
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
		tracked.CompletedLength = completedLength
	}
}

// MarkCompleteFromEvent 从 Surge complete/error 事件标记完成
// 返回副本供 handleTaskComplete 使用；当前世代已接受则返回 nil
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

	tracked.Status = status
	tracked.resumeOccupancyHold = false
	t.markTerminalAcceptedLocked(gid)
	return copyTrackedTask(tracked)
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

// GetScope returns the task's scope/domain info (backward-compatible wrapper).
func (t *TaskTracker) GetScope(gid string) (scope, domain string, ok bool) {
	s, d, _, ok := t.GetScopeAndEnv(gid)
	return s, d, ok
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
	t.mu.Lock()
	delete(t.tasks, gid)
	delete(t.processedComplete, gid)
	t.mu.Unlock()

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
// When bw>0, refreshes AllocatedAt (Add/Resume re-allocation). If Status is
// still empty, sets Status="active" (placeholders then appear in both
// occupancy seeding and Convergence GetActiveTrackedTasks — intentional
// defense-in-depth for the AddURI pre-event window). If Status is "paused"
// (Resume hook before EventResumed), sets resumeOccupancyHold so occupancy
// seeding sees the claim without flipping paused→active.
func (t *TaskTracker) SetTargetBandwidth(gid string, bw int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tracked := t.tasks[gid]
	if tracked == nil {
		tracked = &TrackedTask{GID: gid, CreatedAt: time.Now()}
		t.tasks[gid] = tracked
	}
	tracked.TargetBandwidth = bw
	if bw > 0 {
		tracked.AllocatedAt = time.Now()
		switch tracked.Status {
		case "":
			tracked.Status = "active"
		case "paused":
			tracked.resumeOccupancyHold = true
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

// GetOccupancyTrackedTasks returns tasks that should seed BandwidthLedger
// occupancy. Includes Status=="active", AddURI placeholders
// (Status=="" && TargetBandwidth>0), waiting claims with TargetBandwidth>0
// (queued Surge hold), and just-resumed claims still marked paused
// (resumeOccupancyHold). Excludes ordinary paused (no hold), waiting with
// bw==0, complete, error, and empty-status without bw.
// Does not change Convergence GetActiveTrackedTasks filter semantics.
func (t *TaskTracker) GetOccupancyTrackedTasks() []TrackedTask {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]TrackedTask, 0, len(t.tasks))
	for _, tt := range t.tasks {
		switch tt.Status {
		case "active":
			result = append(result, *tt)
		case "":
			if tt.TargetBandwidth > 0 {
				result = append(result, *tt)
			}
		case "waiting":
			if tt.TargetBandwidth > 0 {
				result = append(result, *tt)
			}
		case "paused":
			if tt.resumeOccupancyHold && tt.TargetBandwidth > 0 {
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
