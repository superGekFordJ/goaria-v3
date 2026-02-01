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

	stopChan      chan struct{}
	forceTickChan chan struct{}
	stopOnce      sync.Once

	// 轮询间隔
	headlessInterval time.Duration
	windowInterval   time.Duration

	// RPC polling optimization
	mu                 sync.Mutex
	shouldFetchStopped bool
	lastStopped        []rpc.Task

	// Previous tick state for transition detection
	prevActiveGids  map[string]bool
	prevWaitingGids map[string]bool
}

func New(app *application.App, hub *events.Hub, systray *application.SystemTray) *Monitor {
	m := &Monitor{
		app:                app,
		hub:                hub,
		systray:            systray,
		stopChan:           make(chan struct{}),
		forceTickChan:      make(chan struct{}, 1),
		headlessInterval:   5 * time.Second, // 无头模式：5秒
		windowInterval:     1 * time.Second, // 窗口模式：1秒（由前端主导）
		shouldFetchStopped: true,            // 初始时获取一次 stopped 任务
		prevActiveGids:     make(map[string]bool),
		prevWaitingGids:    make(map[string]bool),
	}

	// 订阅任务变更事件，触发即时刷新
	hub.SubscribeTaskDelta(func(delta events.TaskDelta) {
		// 仅关注影响列表状态的事件（添加、暂停、恢复、删除）
		// 进度更新 (progress/complete/error) 暂不需要强制刷新，由轮询/pusher处理
		// 修正：AddUri 后需要立即看到任务，所以主要关注 add
		// Pause/Resume/Remove 也会改变 API 返回的状态，所以也需要刷新
		switch delta.Type {
		case "add", "pause", "resume":
			select {
			case m.forceTickChan <- struct{}{}:
				log.Printf("[Monitor] Triggering immediate update used by event: %s", delta.Type)
			default:
				// channel full, update already pending
			}
		case "remove", "complete", "error":
			m.mu.Lock()
			m.shouldFetchStopped = true
			m.mu.Unlock()
			select {
			case m.forceTickChan <- struct{}{}:
				log.Printf("[Monitor] Triggering immediate update (with stopped) used by event: %s", delta.Type)
			default:
			}
		}
	})

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
		case <-m.forceTickChan:
			m.tick()
			// 重置定时器，避免立即再次轮询
			if State.HasWindow() {
				ticker.Reset(m.windowInterval)
			} else {
				ticker.Reset(m.headlessInterval)
			}
		}
	}
}

func (m *Monitor) tick() {
	// 获取轻量任务列表（不含文件列表）
	active, err := rpc.TellActiveLite()
	if err != nil {
		log.Printf("[Monitor] TellActive error: %v", err)
		return
	}
	waiting, _ := rpc.TellWaitingLite(0, 100)

	// 3. 获取 Stopped 任务 (仅在需要时或首次启动时)
	m.mu.Lock()
	fetchStopped := m.shouldFetchStopped
	m.mu.Unlock()

	var stopped []rpc.Task
	if fetchStopped {
		var err error
		// 优化：使用 Lite 接口减少数据量
		stopped, err = rpc.TellStoppedLite(0, 100)
		if err != nil {
			log.Printf("[Monitor] TellStopped error: %v", err)
			m.mu.Lock()
			stopped = m.lastStopped
			m.mu.Unlock()
		}
		// 注意：lastStopped 将在 enrichTasks 后更新，确保缓存包含完整文件信息
	} else {
		m.mu.Lock()
		stopped = m.lastStopped
		m.mu.Unlock()
	}

	// 1. 检查并补充元数据（如果缺少）
	allTasks := make([]*rpc.Task, 0, len(active)+len(waiting)+len(stopped))
	for i := range active {
		allTasks = append(allTasks, &active[i])
	}
	for i := range waiting {
		allTasks = append(allTasks, &waiting[i])
	}
	for i := range stopped {
		allTasks = append(allTasks, &stopped[i])
	}

	for _, task := range allTasks {
		// 如果缓存中没有有效元数据（新任务或被污染的缓存），则发起完整请求
		// 使用 HasValidMetadata 检测并修复被污染的空文件列表缓存
		if !Cache.HasValidMetadata(task.GID) {
			Cache.PrefetchMetadata(task.GID)
		}
	}

	// 2. 使用缓存的元数据丰富轻量任务
	enrichTasks := func(tasks []rpc.Task) {
		for i := range tasks {
			meta := Cache.GetMetadata(tasks[i].GID)
			if meta != nil {
				tasks[i].Title = meta.Title
				// 构造一个包含首个文件信息的 Files 列表，满足前端和 Tracker 的基本需求
				if len(meta.Files) > 0 {
					tasks[i].Files = []rpc.File{
						{
							Path: meta.Files[0],
							Uris: []rpc.Uri{{Uri: meta.SourceURL}},
						},
					}
				}
			}
		}
	}

	enrichTasks(active)
	enrichTasks(waiting)
	enrichTasks(stopped)

	// 更新 lastStopped 缓存（包含已丰富的文件信息）
	// 这确保下次 tick 使用缓存时，任务已有正确的文件名
	if fetchStopped {
		m.mu.Lock()
		m.lastStopped = stopped
		m.shouldFetchStopped = false
		m.mu.Unlock()
	}

	// 检测列表转移并发射事件（在有窗口时）
	// 仅检测 active <-> waiting，stopped 转移由 complete/error 事件处理
	if State.HasWindow() {
		m.detectAndEmitTaskMoves(active, waiting)
	}

	// 更新前一次的 GID 集合（用于下次检测）
	m.updatePrevGids(active, waiting)

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

		// 1. 推送任务进度 (progress)
		for _, task := range active {
			// 仅推送 active 任务的进度
			// 使用 map reduce payload 大小
			payload := map[string]interface{}{
				"completedLength": task.CompletedLength,
				"downloadSpeed":   task.DownloadSpeed,
				"totalLength":     task.TotalLength,
				"errorCode":       task.ErrorCode,
				"errorMessage":    task.ErrorMessage,
			}

			m.pusher.Queue(events.TaskDelta{
				Type:    "progress",
				GID:     task.GID,
				Payload: payload,
			})
		}

		// 2. 推送任务完成事件（通过 pusher 批量推送）
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
	if task == nil {
		return
	}

	// 如果 FilePath 为空，尝试从元数据缓存获取
	// 场景：快速完成的任务可能在 Tracker 更新前已完成，导致 FilePath 未被填充
	if task.FilePath == "" {
		if meta := Cache.GetMetadata(task.GID); meta != nil && len(meta.Files) > 0 {
			task.FilePath = meta.Files[0]
			task.Dir = meta.Dir
			if meta.SourceURL != "" {
				task.SourceURL = meta.SourceURL
			}
			log.Printf("[Monitor] Recovered file info from cache for task: %s -> %s", task.GID, task.FilePath)
		}
	}

	// 如果仍然为空，尝试直接调用 RPC 获取
	if task.FilePath == "" {
		if t, err := rpc.TellStatus(task.GID); err == nil && t != nil && len(t.Files) > 0 && t.Files[0].Path != "" {
			task.FilePath = t.Files[0].Path
			task.Dir = t.Dir
			if len(t.Files[0].Uris) > 0 {
				task.SourceURL = t.Files[0].Uris[0].Uri
			}
			log.Printf("[Monitor] Recovered file info from RPC for task: %s -> %s", task.GID, task.FilePath)
		}
	}

	if task.FilePath == "" {
		log.Printf("[Monitor] Task %s completed but no file path available, skipping history", task.GID)
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

// InvalidateTask 使指定任务的缓存失效并发送删除事件
// 用于 RemoveTask 后确保前端和缓存同步
func (m *Monitor) InvalidateTask(gid string) {
	// 1. 清理元数据缓存
	Cache.InvalidateMetadata(gid)

	// 2. 从 lastStopped 缓存中移除
	m.mu.Lock()
	newStopped := make([]rpc.Task, 0, len(m.lastStopped))
	for _, t := range m.lastStopped {
		if t.GID != gid {
			newStopped = append(newStopped, t)
		}
	}
	m.lastStopped = newStopped
	m.shouldFetchStopped = true // 标记需要重新获取
	m.mu.Unlock()

	// 3. 发送删除事件通知前端
	if m.hub != nil {
		m.hub.EmitTaskDelta(events.TaskDelta{
			Type: "remove",
			GID:  gid,
		})
	}

	log.Printf("[Monitor] Task invalidated: %s", gid)
}

// detectAndEmitTaskMoves 检测任务列表转移并发射事件
// 注意：仅检测 active <-> waiting 转移，stopped 转移由 complete/error 事件处理
func (m *Monitor) detectAndEmitTaskMoves(active, waiting []rpc.Task) {
	// 构建当前 GID 集合
	currentActiveGids := make(map[string]bool)
	currentWaitingGids := make(map[string]bool)

	activeByGid := make(map[string]*rpc.Task)
	waitingByGid := make(map[string]*rpc.Task)

	for i := range active {
		currentActiveGids[active[i].GID] = true
		activeByGid[active[i].GID] = &active[i]
	}
	for i := range waiting {
		currentWaitingGids[waiting[i].GID] = true
		waitingByGid[waiting[i].GID] = &waiting[i]
	}

	// 检测 active -> waiting (pause)
	for gid := range currentWaitingGids {
		if m.prevActiveGids[gid] {
			if task := waitingByGid[gid]; task != nil {
				m.hub.EmitTaskMove(events.TaskMove{
					GID:  gid,
					From: "active",
					To:   "waiting",
					Task: task,
				})
				log.Printf("[Monitor] Task moved: %s active -> waiting", gid)
			}
		}
	}

	// 检测 waiting -> active (resume)
	for gid := range currentActiveGids {
		if m.prevWaitingGids[gid] {
			if task := activeByGid[gid]; task != nil {
				m.hub.EmitTaskMove(events.TaskMove{
					GID:  gid,
					From: "waiting",
					To:   "active",
					Task: task,
				})
				log.Printf("[Monitor] Task moved: %s waiting -> active", gid)
			}
		}
	}

	// 注意：不检测 -> stopped 转移
	// 任务完成/出错由 complete/error 事件处理，避免重复事件导致前端双重添加
}

// updatePrevGids 更新前一次的 GID 集合
func (m *Monitor) updatePrevGids(active, waiting []rpc.Task) {
	m.prevActiveGids = make(map[string]bool)
	m.prevWaitingGids = make(map[string]bool)

	for _, t := range active {
		m.prevActiveGids[t.GID] = true
	}
	for _, t := range waiting {
		m.prevWaitingGids[t.GID] = true
	}
}
