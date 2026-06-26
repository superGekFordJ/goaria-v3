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

	// Scope/TTFB/Domain（speedstats 扩展）
	Scope  string
	TTFBMs int64
	Domain string

	// KeepAlive flag — true when initial split < nSat
	IsKeepAlive bool

	// MinChunk is the per-task minimum chunk size (bytes), captured from
	// ThreadParams.MinSize at task-add time. 0 means unknown/uncaptured
	// (non-Surge path, event-created, or restart recovery).
	MinChunk int64
}

// TaskTracker 后端任务追踪器
type TaskTracker struct {
	mu    sync.RWMutex
	tasks map[string]*TrackedTask // gid -> task

	// 已处理的完成任务 GID（防止重复处理）
	processedComplete map[string]bool
}

// NewTaskTracker 创建新的任务追踪器
func NewTaskTracker() *TaskTracker {
	return &TaskTracker{
		tasks:             make(map[string]*TrackedTask),
		processedComplete: make(map[string]bool),
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
			// 检查是否已处理过
			if t.processedComplete[task.GID] {
				continue
			}

			if tracked := t.tasks[task.GID]; tracked != nil {
				if tracked.Status != "complete" && tracked.Status != "error" {
					// 状态变为完成，触发完成处理
					tracked.Status = task.Status
					t.fillTaskInfo(tracked, task)
					completed = append(completed, copyTrackedTask(tracked))
					t.processedComplete[task.GID] = true
				}
			} else {
				// 新发现的已完成任务（可能是重启后）
				tracked = t.createTrackedTask(task)
				tracked.Status = task.Status
				t.tasks[task.GID] = tracked
				completed = append(completed, copyTrackedTask(tracked))
				t.processedComplete[task.GID] = true
			}
		}
	}

	// 清理已移除的任务
	for gid, tracked := range t.tasks {
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

// sampleSpeedInternal 共享稳定性检测和峰值速度逻辑
// 临时桥接：事件路径的 SampleSpeedFromEvent 依赖此方法，SPEC-169 将用
// DownloadCompleteMsg 携带的 peak speed 替代实时采样，届时可移除事件路径调用
//
// SustainedCount 在 tick 和事件路径间共享：若 tick 样本（5s 间隔）与事件样本
// （200ms 间隔）速度差异 >15%，tick 会重置 SustainedCount=1。事件路径在 ~0.4s
// （2 个 200ms 事件）内即可恢复，对 PeakSpeed 取最大值语义无害。
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
			task.PeakSpeed = speed
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
func (t *TaskTracker) EnsureTrackedFromEvent(gid string, totalLength int64, sourceURL string, threadCount int) {
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
		return
	}

	t.tasks[gid] = &TrackedTask{
		GID:         gid,
		Status:      "active",
		TotalLength: totalLength,
		SourceURL:   sourceURL,
		ThreadCount: threadCount,
		CreatedAt:   time.Now(),
	}
}

// SampleSpeedFromEvent 从 Surge ProgressMsg 事件采样速度
// 事件路径阈值：窗口模式 2 次（~0.4s @ 200ms），无头模式 1 次
// 临时桥接：SPEC-169 将用 DownloadCompleteMsg 携带的 peak speed 替代此方法
func (t *TaskTracker) SampleSpeedFromEvent(gid string, speed int64, totalLength int64, completedLength int64) {
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

	if totalLength > 0 && totalLength <= speedstats.MinFileSize {
		return
	}

	threshold := 2
	if !State.HasWindow() {
		threshold = 1
	}
	t.sampleSpeedInternal(tracked, speed, threshold)
}

// MarkCompleteFromEvent 从 Surge complete/error 事件标记完成
// 返回副本供 handleTaskComplete 使用，已处理则返回 nil
func (t *TaskTracker) MarkCompleteFromEvent(gid string, status string) *TrackedTask {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked := t.tasks[gid]
	if tracked == nil {
		return nil
	}
	if t.processedComplete[gid] {
		return nil
	}

	tracked.Status = status
	t.processedComplete[gid] = true
	return copyTrackedTask(tracked)
}

// SetScope 设置任务的 scope/TTFB/domain 信息（由 AddUri 调用）
func (t *TaskTracker) SetScope(gid string, scope string, ttfbMs int64, domain string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked := t.tasks[gid]
	if tracked != nil {
		tracked.Scope = scope
		tracked.TTFBMs = ttfbMs
		tracked.Domain = domain
	} else {
		t.tasks[gid] = &TrackedTask{
			GID:       gid,
			Scope:     scope,
			TTFBMs:    ttfbMs,
			Domain:    domain,
			CreatedAt: time.Now(),
		}
	}
}

// GetScope 返回任务的 scope/domain 信息
func (t *TaskTracker) GetScope(gid string) (scope, domain string, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tracked := t.tasks[gid]
	if tracked == nil || tracked.Scope == "" {
		return "", "", false
	}
	return tracked.Scope, tracked.Domain, true
}

// RemoveTask 从追踪器中移除任务
func (t *TaskTracker) RemoveTask(gid string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.tasks, gid)
	delete(t.processedComplete, gid)
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

// RecordPeakEfficiency updates PeakSpeed and PeakThreadCount for the given gid
// using the D3 efficiency-guarded ratchet with bestEff anchoring (Edge Case 14).
// Only accepts the incoming pair when it represents a more optimal working point.
// Rejects bloated N where absolute speed marginally increases but per-thread
// efficiency crashes below the bestEff-anchored guard.
// This is the convergence layer's paired write entry — it does not touch
// sampleSpeedInternal's existing PeakSpeed logic.
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
		tt.PeakSpeed = peakSpeed
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
				tt.PeakSpeed = peakSpeed
			}
			tt.PeakThreadCount = peakWorkers
		} else if peakWorkers < tt.PeakThreadCount && float64(peakSpeed) >= float64(tt.PeakSpeed)*trackerPeakSpeedGuardBand {
			if peakSpeed > tt.PeakSpeed {
				tt.PeakSpeed = peakSpeed
			}
			tt.PeakThreadCount = peakWorkers
		}
	} else if float64(peakSpeed) > float64(tt.PeakSpeed)*trackerPeakRaiseBand {
		// Absolute throughput up ≥5% but efficiency below guard — only update
		// PeakSpeed (for V_target/BtlBw), keep efficient PeakThreadCount unchanged.
		tt.PeakSpeed = peakSpeed
	}
}

// parseInt64 解析字符串为 int64
func parseInt64(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}
