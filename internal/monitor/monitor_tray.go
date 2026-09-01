package monitor

import (
	"fmt"

	"goaria-v3/internal/tray"
)

// TraySnapshot 托盘模式下的最小数据结构
type TraySnapshot struct {
	ActiveCount  int  `json:"activeCount"`
	WaitingCount int  `json:"waitingCount"`
	HasActive    bool `json:"hasActive"`
	HasPaused    bool `json:"hasPaused"`
	HasError     bool `json:"hasError"`
}

// buildTraySnapshot 构建托盘快照（从 Cache 读取合并切片）
func (m *Monitor) buildTraySnapshot() TraySnapshot {
	active := Cache.GetActive()
	waiting := Cache.GetWaiting()

	hasActive := len(active) > 0
	hasPaused := false
	hasError := false

	for _, t := range active {
		if t.Status == "paused" {
			hasPaused = true
		}
		if t.Status == "error" {
			hasError = true
		}
	}
	for _, t := range waiting {
		if t.Status == "paused" {
			hasPaused = true
		}
		if t.Status == "error" {
			hasError = true
		}
	}

	return TraySnapshot{
		ActiveCount:  len(active),
		WaitingCount: len(waiting),
		HasActive:    hasActive,
		HasPaused:    hasPaused,
		HasError:     hasError,
	}
}

func (m *Monitor) updateTrayIcon() {
	if m.systray == nil {
		return
	}

	hasActive, hasPaused, hasError, activeCount, waitingCount := State.GetTrayState()

	var state tray.TrayState
	switch {
	case hasError:
		state = tray.StateError
	case hasActive:
		state = tray.StateActive
	case hasPaused:
		state = tray.StatePaused
	default:
		state = tray.StateIdle
	}

	m.systray.SetIcon(tray.GetIconForState(state))

	// 更新 tooltip
	// 1. 下载中：GoAria - 3 个任务下载中
	// 2. 仅等待/暂停：GoAria - 2 个任务等待中
	// 3. 空闲：GoAria - Download Manager
	var tooltip string
	switch {
	case activeCount > 0:
		tooltip = fmt.Sprintf("GoAria - %d 个任务下载中", activeCount)
	case waitingCount > 0:
		tooltip = fmt.Sprintf("GoAria - %d 个任务等待中", waitingCount)
	default:
		tooltip = "GoAria - Download Manager"
	}
	m.systray.SetTooltip(tooltip)
}
