package monitor

import (
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
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
	tracker *TaskTracker
	pusher  *Pusher

	stopChan chan struct{}
	stopOnce sync.Once

	// 轮询间隔
	headlessInterval time.Duration
	windowInterval   time.Duration
}

func New(app *application.App, hub *events.Hub, systray *application.SystemTray) *Monitor {
	m := &Monitor{
		app:              app,
		hub:              hub,
		systray:          systray,
		stopChan:         make(chan struct{}),
		headlessInterval: 5 * time.Second, // 无头模式：5秒
		windowInterval:   1 * time.Second, // 窗口模式：1秒（由前端主导）
	}

	// 创建任务追踪器
	m.tracker = NewTaskTracker()

	// 创建增量推送器
	m.pusher = NewPusher(hub)

	// 注册到全局状态
	State.SetTracker(m.tracker)

	return m
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
	// 获取所有任务列表
	active, err := rpc.TellActive()
	if err != nil {
		log.Printf("[Monitor] TellActive error: %v", err)
		return
	}
	waiting, _ := rpc.TellWaiting(0, 100)
	stopped, _ := rpc.TellStopped(0, 100)

	// 更新缓存
	Cache.UpdateFromAria2(active, waiting, stopped)

	// 更新追踪器并获取已完成任务
	completedTasks := m.tracker.Update(active, waiting, stopped)

	// 处理已完成任务（写入历史和速度统计）
	for _, task := range completedTasks {
		m.handleTaskComplete(task)
	}

	// 构建托盘快照
	snapshot := m.buildTraySnapshot(active, waiting)

	// 更新托盘状态（仅在变化时）
	if State.UpdateTrayState(snapshot.HasActive, snapshot.HasPaused, snapshot.HasError, snapshot.ActiveCount) {
		m.updateTrayIcon()
	}

	// 如果有窗口，通过事件推送数据
	if State.HasWindow() {
		m.hub.EmitTraySnapshot(snapshot)

		// 推送任务完成事件（通过 pusher 批量推送）
		for _, task := range completedTasks {
			eventType := "complete"
			if task.Status == "error" {
				eventType = "error"
			}
			m.pusher.Queue(events.TaskDelta{
				Type: eventType,
				GID:  task.GID,
			})
		}
	}
}

// handleTaskComplete 处理任务完成
func (m *Monitor) handleTaskComplete(task *TrackedTask) {
	if task == nil || task.FilePath == "" {
		return
	}

	log.Printf("[Monitor] Task completed: %s, peak speed: %d B/s", task.GID, task.PeakSpeed)

	// 1. 记录速度统计（仅 >50MB 文件）
	if task.TotalLength > 50*1024*1024 && task.PeakSpeed > 0 {
		threadCount := task.ThreadCount
		isExploration := task.IsExploration

		// 如果没有追踪到线程数，使用全局配置
		if threadCount <= 0 {
			threadCount, _ = strconv.Atoi(config.Current.MaxConnections)
			if threadCount <= 0 {
				threadCount = 16
			}
			// 尝试判断是否为探索任务
			isExploration = smartthread.ShouldExplore(task.SourceURL)
		}

		speedstats.AddRecord(task.PeakSpeed, threadCount, task.TotalLength, isExploration)
		log.Printf("[Monitor] Speed stats recorded: peak=%d, threads=%d, exploration=%v",
			task.PeakSpeed, threadCount, isExploration)
	}

	// 2. 写入历史记录
	history.Add(history.HistoryEntry{
		GID:             task.GID,
		Title:           filepath.Base(task.FilePath),
		Dir:             task.Dir,
		Path:            task.FilePath,
		TotalLength:     fmt.Sprintf("%d", task.TotalLength),
		CompletedLength: fmt.Sprintf("%d", task.CompletedLength),
		Source:          task.SourceURL,
	})
	log.Printf("[Monitor] History recorded: %s", task.GID)
}

// buildTraySnapshot 构建托盘快照
func (m *Monitor) buildTraySnapshot(active, waiting []rpc.Task) TraySnapshot {
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
