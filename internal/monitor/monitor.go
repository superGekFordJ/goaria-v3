package monitor

import (
	"fmt"
	"log"
	"sync"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/tray"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TraySnapshot 托盘模式下的最小数据结构
type TraySnapshot struct {
	ActiveCount  int  `json:"activeCount"`
	WaitingCount int  `json:"waitingCount"`
	HasActive    bool `json:"hasActive"`
	HasPaused    bool `json:"hasPaused"`
	HasError     bool `json:"hasError"`
}

// Monitor 后端监控器
type Monitor struct {
	app     *application.App
	hub     *events.Hub
	systray *application.SystemTray

	stopChan chan struct{}
	stopOnce sync.Once

	// 轮询间隔
	headlessInterval time.Duration
	windowInterval   time.Duration
}

func New(app *application.App, hub *events.Hub, systray *application.SystemTray) *Monitor {
	return &Monitor{
		app:              app,
		hub:              hub,
		systray:          systray,
		stopChan:         make(chan struct{}),
		headlessInterval: 5 * time.Second, // 无头模式：5秒
		windowInterval:   1 * time.Second, // 窗口模式：1秒（由前端主导）
	}
}

func (m *Monitor) Start() {
	go m.runLoop()
}

func (m *Monitor) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopChan)
	})
}

func (m *Monitor) runLoop() {
	ticker := time.NewTicker(m.headlessInterval)
	defer ticker.Stop()

	// 启动时立即执行一次
	m.tick()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.tick()
			// 根据窗口状态动态调整间隔
			if State.HasWindow() {
				ticker.Reset(m.windowInterval)
			} else {
				ticker.Reset(m.headlessInterval)
			}
		}
	}
}

func (m *Monitor) tick() {
	// 仅获取托盘所需的最小数据
	snapshot := m.fetchTraySnapshot()

	// 更新托盘状态（仅在变化时）
	if State.UpdateTrayState(snapshot.HasActive, snapshot.HasPaused, snapshot.HasError, snapshot.ActiveCount) {
		m.updateTrayIcon()
	}

	// 如果有窗口，通过事件推送数据（前端仍主导高频轮询）
	// 无头模式下不推送，节省资源
	if State.HasWindow() {
		m.hub.EmitTraySnapshot(snapshot)
	}
}

func (m *Monitor) fetchTraySnapshot() TraySnapshot {
	// 使用轻量 API 仅获取状态计数
	active, err := rpc.TellActive()
	if err != nil {
		log.Printf("[Monitor] TellActive error: %v", err)
		return TraySnapshot{}
	}

	waiting, _ := rpc.TellWaiting(0, 100)

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

	hasActive, hasPaused, hasError, count := State.GetTrayState()

	var state tray.TrayState
	if hasError {
		state = tray.StateError
	} else if hasActive {
		state = tray.StateActive
	} else if hasPaused {
		state = tray.StatePaused
	} else {
		state = tray.StateIdle
	}

	m.systray.SetIcon(tray.GetIconForState(state))

	// 更新 tooltip 显示活跃任务数
	if count > 0 {
		m.systray.SetTooltip(fmt.Sprintf("GoAria - %d 个任务下载中", count))
	} else {
		m.systray.SetTooltip("GoAria - Download Manager")
	}
}
