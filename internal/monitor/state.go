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
	activeCount atomic.Int32
}

var State = &AppState{}

func (s *AppState) SetWindowExists(exists bool) {
	s.windowExists.Store(exists)
}

func (s *AppState) HasWindow() bool {
	return s.windowExists.Load()
}

func (s *AppState) UpdateTrayState(hasActive, hasPaused, hasError bool, activeCount int) bool {
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
	s.activeCount.Store(int32(activeCount))
	return changed
}

func (s *AppState) GetTrayState() (hasActive, hasPaused, hasError bool, activeCount int) {
	return s.hasActive.Load(), s.hasPaused.Load(), s.hasError.Load(), int(s.activeCount.Load())
}
