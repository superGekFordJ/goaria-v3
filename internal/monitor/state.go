package monitor

import (
	"sync"
	"sync/atomic"
)

// AppState 管理应用级状态
type AppState struct {
	mu sync.RWMutex

	// 窗口状态
	windowExists atomic.Bool

	// 托盘状态缓存（用于变化检测）
	hasActive atomic.Bool
	hasPaused atomic.Bool
	hasError  atomic.Bool

	// 活跃任务数（用于托盘提示）
	activeCount  atomic.Int32
	waitingCount atomic.Int32

	// 任务追踪器（后端自洽核心）
	tracker *TaskTracker

	// 监控器引用（用于任务删除时的缓存失效）
	monitor *Monitor
}

var State = &AppState{}

func (s *AppState) SetWindowExists(exists bool) {
	s.windowExists.Store(exists)
}

func (s *AppState) HasWindow() bool {
	return s.windowExists.Load()
}

func (s *AppState) UpdateTrayState(hasActive, hasPaused, hasError bool, activeCount, waitingCount int) bool {
	changed := false
	if s.hasActive.Swap(hasActive) != hasActive {
		changed = true
	}
	if s.hasPaused.Swap(hasPaused) != hasPaused {
		changed = true
	}
	if s.hasError.Swap(hasError) != hasError {
		changed = true
	}
	if s.activeCount.Swap(int32(activeCount)) != int32(activeCount) {
		changed = true
	}
	if s.waitingCount.Swap(int32(waitingCount)) != int32(waitingCount) {
		changed = true
	}
	return changed
}

func (s *AppState) GetTrayState() (hasActive, hasPaused, hasError bool, activeCount, waitingCount int) {
	return s.hasActive.Load(), s.hasPaused.Load(), s.hasError.Load(), int(s.activeCount.Load()), int(s.waitingCount.Load())
}

// SetTracker 设置任务追踪器
func (s *AppState) SetTracker(t *TaskTracker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracker = t
}

// GetTracker 获取任务追踪器
func (s *AppState) GetTracker() *TaskTracker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tracker
}

// SetMonitor 设置监控器引用
func (s *AppState) SetMonitor(m *Monitor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.monitor = m
}

// GetMonitor 获取监控器引用
func (s *AppState) GetMonitor() *Monitor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.monitor
}
