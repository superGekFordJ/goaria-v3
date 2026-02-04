package monitor

import (
	"fmt"
	"sync"
	"time"

	"goaria-v3/internal/rpc"
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

	// 线程信息
	ThreadCount   int
	IsExploration bool

	// 文件信息（用于历史记录）
	FilePath  string
	Dir       string
	SourceURL string
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
					completed = append(completed, tracked)
					t.processedComplete[task.GID] = true
				}
			} else {
				// 新发现的已完成任务（可能是重启后）
				tracked = t.createTrackedTask(task)
				tracked.Status = task.Status
				t.tasks[task.GID] = tracked
				completed = append(completed, tracked)
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
			delete(t.processedComplete, gid)
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

	// 速度采样（仅 >50MB 文件）
	speed := parseInt64(task.DownloadSpeed)
	if speed > 0 && tracked.TotalLength > 50*1024*1024 {
		t.sampleSpeed(tracked, speed)
	}
}

// sampleSpeed 采样速度并更新峰值
func (t *TaskTracker) sampleSpeed(task *TrackedTask, speed int64) {
	// 稳定性检测：误差在 15% 以内视为平稳
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

	// 分模式阈值：窗口模式 3 次（约 3 秒），无头模式 1 次（约 5 秒）
	threshold := 3
	if !State.HasWindow() {
		threshold = 1
	}

	if task.SustainedCount >= threshold && task.CompletedLength > 50*1024*1024 {
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

// fillTaskInfo 填充任务完整信息（从 stopped 任务）
// 注意：task 应该已经被 enrichTasks() 丰富过，包含文件信息
func (t *TaskTracker) fillTaskInfo(tracked *TrackedTask, task rpc.Task) {
	tracked.TotalLength = parseInt64(task.TotalLength)
	tracked.CompletedLength = parseInt64(task.CompletedLength)
	tracked.Dir = task.Dir

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

// RemoveTask 从追踪器中移除任务
func (t *TaskTracker) RemoveTask(gid string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.tasks, gid)
	delete(t.processedComplete, gid)
}

// parseInt64 解析字符串为 int64
func parseInt64(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}
