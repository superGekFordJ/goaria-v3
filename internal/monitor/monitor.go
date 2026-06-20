package monitor

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/config"
	"goaria-v3/internal/events"
	"goaria-v3/internal/history"
	"goaria-v3/internal/rpc"
	"goaria-v3/internal/smartthread"
	"goaria-v3/internal/speedstats"
	surgeEvents "goaria-v3/internal/surge/engine/events"
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
	engine  rpc.DownloadEngine
	tracker *TaskTracker
	pusher  *Pusher

	stopChan      chan struct{}
	forceTickChan chan struct{}
	stopOnce      sync.Once

	// 轮询间隔
	headlessInterval time.Duration
	windowInterval   time.Duration

	// RPC polling optimization
	mu                   sync.Mutex
	shouldFetchStopped   bool
	lastStopped          []rpc.Task
	lastStoppedFetchTime time.Time

	// Recently deleted tasks to filter out during engine/cache synchronization races
	deletedGids map[string]time.Time

	// Completed tasks reported by event bridge or ws that are pending verification in stopped list
	pendingCompleteGids map[string]time.Time

	// Previous tick state for transition detection
	prevActiveGids  map[string]bool
	prevWaitingGids map[string]bool
}

func New(app *application.App, hub *events.Hub, systray *application.SystemTray, engine rpc.DownloadEngine) *Monitor {
	m := &Monitor{
		app:                  app,
		hub:                  hub,
		systray:              systray,
		engine:               engine,
		stopChan:             make(chan struct{}),
		forceTickChan:        make(chan struct{}, 1),
		headlessInterval:     5 * time.Second, // 无头模式：5秒
		windowInterval:       1 * time.Second, // 窗口模式：1秒（由前端主导）
		shouldFetchStopped:   true,            // 初始时获取一次 stopped 任务
		lastStoppedFetchTime: time.Now(),
		deletedGids:          make(map[string]time.Time),
		pendingCompleteGids:  make(map[string]time.Time),
		prevActiveGids:       make(map[string]bool),
		prevWaitingGids:      make(map[string]bool),
	}

	Cache.engine = engine

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
			if delta.GID != "" {
				m.pendingCompleteGids[delta.GID] = time.Now()
			}
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

	// 启动 Surge 事件监听桥接
	m.startSurgeEventBridge()

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
	var (
		active    []rpc.Task
		waiting   []rpc.Task
		stopped   []rpc.Task
		activeErr error
		wg        sync.WaitGroup
	)

	// 清理过期的待完成任务（超过 30 秒）
	m.mu.Lock()
	now := time.Now()
	for gid, t := range m.pendingCompleteGids {
		if now.Sub(t) > 30*time.Second {
			delete(m.pendingCompleteGids, gid)
		}
	}
	m.mu.Unlock()

	// 1. 获取 Active 任务
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		active, err = m.engine.TellActiveLite()
		if err != nil {
			log.Printf("[Monitor] TellActiveLite error: %v, retrying with full request", err)
			// Fallback: 尝试获取完整信息（兜底策略）
			active, err = m.engine.TellActive()
			if err != nil {
				log.Printf("[Monitor] TellActive fallback error: %v", err)
				activeErr = err
			}
		}
	}()

	// 2. 获取 Waiting 任务
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		waiting, err = m.engine.TellWaitingLite(0, 100)
		if err != nil {
			log.Printf("[Monitor] TellWaitingLite error: %v, retrying with full request", err)
			waiting, _ = m.engine.TellWaiting(0, 100)
		}
	}()

	// 3. 获取 Stopped 任务 (仅在需要时、有待确认完成的任务、或定期兜底刷新时)
	m.mu.Lock()
	fetchStopped := m.shouldFetchStopped || len(m.pendingCompleteGids) > 0 || time.Since(m.lastStoppedFetchTime) > 10*time.Second
	m.mu.Unlock()

	if fetchStopped {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			// 优化：使用 Lite 接口减少数据量
			stopped, err = m.engine.TellStoppedLite(0, 100)
			if err != nil {
				log.Printf("[Monitor] TellStoppedLite error: %v, retrying with full request", err)
				stopped, err = m.engine.TellStopped(0, 100)
				if err != nil {
					log.Printf("[Monitor] TellStopped fallback error: %v", err)
					m.mu.Lock()
					stopped = m.lastStopped
					m.mu.Unlock()
				}
			}
			// 注意：lastStopped 将在 enrichTasks 后更新，确保缓存包含完整文件信息
		}()
	} else {
		m.mu.Lock()
		stopped = m.lastStopped
		m.mu.Unlock()
	}

	wg.Wait()

	if activeErr != nil {
		return
	}

	// 过滤掉最近被删除的任务，防止网络或进程通信竞态导致已删除任务被再次缓存或上报
	active = m.filterDeletedTasks(active)
	waiting = m.filterDeletedTasks(waiting)
	stopped = m.filterDeletedTasks(stopped)

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

	missingMetadataSet := make(map[string]struct{}, len(allTasks))
	missingMetadataGids := make([]string, 0, len(allTasks))
	for _, task := range allTasks {
		// 如果缓存中没有有效元数据（新任务或被污染的缓存），则发起完整请求
		// 使用 HasValidMetadata 检测并修复被污染的空文件列表缓存
		if !Cache.HasValidMetadata(task.GID) {
			if _, exists := missingMetadataSet[task.GID]; exists {
				continue
			}
			missingMetadataSet[task.GID] = struct{}{}
			missingMetadataGids = append(missingMetadataGids, task.GID)
		}
	}
	Cache.PrefetchMetadataMulti(missingMetadataGids)

	// 2. 使用缓存的元数据丰富轻量任务
	Cache.EnrichTasks(active)
	Cache.EnrichTasks(waiting)
	Cache.EnrichTasks(stopped)
	HydrateTaskGroups(active)
	HydrateTaskGroups(waiting)
	HydrateTaskGroups(stopped)

	// 更新 lastStopped 缓存（包含已丰富的文件信息）
	// 这确保下次 tick 使用缓存时，任务已有正确的文件名
	if fetchStopped {
		m.mu.Lock()
		m.lastStopped = stopped
		m.shouldFetchStopped = false
		m.lastStoppedFetchTime = time.Now()
		m.mu.Unlock()
	}

	// 检测列表转移并发射事件（在有窗口时）
	// 仅检测 active <-> waiting，stopped 转移由 complete/error 事件处理
	if State.HasWindow() {
		m.detectAndEmitTaskMoves(active, waiting)
	}

	// 检测新增任务并通过 Pusher 推送 add 事件（必须在 updatePrevGids 之前执行）
	if State.HasWindow() {
		hasNewTasks := false
		for _, task := range active {
			if !m.prevActiveGids[task.GID] && !m.prevWaitingGids[task.GID] {
				m.pusher.Queue(events.TaskDelta{
					Type:    "add",
					GID:     task.GID,
					Payload: task,
				})
				hasNewTasks = true
				log.Printf("[Monitor] New task detected (active): %s", task.GID)
			}
		}
		for _, task := range waiting {
			if !m.prevActiveGids[task.GID] && !m.prevWaitingGids[task.GID] {
				m.pusher.Queue(events.TaskDelta{
					Type:    "add",
					GID:     task.GID,
					Payload: task,
				})
				hasNewTasks = true
				log.Printf("[Monitor] New task detected (waiting): %s", task.GID)
			}
		}
		if hasNewTasks {
			m.pusher.FlushNow()
		}
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
		// 任务已成功确认移至已完成列表，从待处理集合中移除
		m.mu.Lock()
		delete(m.pendingCompleteGids, task.GID)
		if strings.HasPrefix(task.GID, "ar_") {
			delete(m.pendingCompleteGids, task.GID[3:])
		} else if strings.HasPrefix(task.GID, "sg_") {
			delete(m.pendingCompleteGids, task.GID[3:])
		}
		m.mu.Unlock()
	}

	// 构建托盘快照
	snapshot := m.buildTraySnapshot(active, waiting)

	// 更新托盘状态（仅在变化时）
	if State.UpdateTrayState(snapshot.HasActive, snapshot.HasPaused, snapshot.HasError, snapshot.ActiveCount, snapshot.WaitingCount) {
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

		// 2. 推送任务完成事件（通过 pusher 批量推送，携带完整 rpc.Task payload）
		for _, task := range completedTasks {
			// 关键修复：快速完成的小文件，通过 Tracker 拿到的 TotalLength 可能仍为 0
			// 必须在压入 Pusher 前利用 TellStatus 获取真正的数据负载，防止 0B 覆盖前端
			if (task.Status == "complete" || task.Status == "error") && (task.TotalLength == 0 || task.CompletedLength == 0) {
				if statusTask, err := m.engine.TellStatus(task.GID, nil); err == nil {
					if task.TotalLength == 0 {
						task.TotalLength = parseInt64(statusTask.TotalLength)
					}
					if task.CompletedLength == 0 {
						task.CompletedLength = parseInt64(statusTask.CompletedLength)
					}
				}
			}

			eventType := "complete"
			if task.Status == "error" {
				eventType = "error"
			}

			fullTask := rpc.Task{
				GID:             task.GID,
				Status:          task.Status,
				TotalLength:     fmt.Sprintf("%d", task.TotalLength),
				CompletedLength: fmt.Sprintf("%d", task.CompletedLength),
				DownloadSpeed:   "0",
				Dir:             task.Dir,
				DownloadGroup:   copyDownloadGroup(task.DownloadGroup),
			}
			if task.FilePath != "" {
				fullTask.Files = []rpc.File{{
					Path: task.FilePath,
					Uris: []rpc.Uri{{Uri: task.SourceURL}},
				}}
			}

			m.pusher.Queue(events.TaskDelta{
				Type:    eventType,
				GID:     task.GID,
				Payload: fullTask,
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
			if task.DownloadGroup == nil && meta.DownloadGroup != nil {
				task.DownloadGroup = copyDownloadGroup(meta.DownloadGroup)
			}
			log.Printf("[Monitor] Recovered file info from cache for task: %s -> %s", task.GID, task.FilePath)
		}
	}
	if task.DownloadGroup == nil {
		task.DownloadGroup = Cache.GetTaskGroup(task.GID)
		if task.DownloadGroup == nil {
			task.DownloadGroup = GetStoredTaskGroup(task.GID)
		}
	}

	// 如果仍然为空，尝试直接调用 RPC 获取
	if task.FilePath == "" {
		if t, err := m.engine.TellStatus(task.GID, nil); err == nil && len(t.Files) > 0 && t.Files[0].Path != "" {
			task.FilePath = t.Files[0].Path
			task.Dir = t.Dir
			if len(t.Files[0].Uris) > 0 {
				task.SourceURL = t.Files[0].Uris[0].Uri
			}
			if task.DownloadGroup == nil && t.DownloadGroup != nil {
				task.DownloadGroup = copyDownloadGroup(t.DownloadGroup)
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
			if isExploration && threadCount > 1 {
				threadCount = (threadCount + 1) / 2
				if threadCount < 1 {
					threadCount = 1
				}
			}
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
		DownloadGroup:   copyDownloadGroup(task.DownloadGroup),
	})
	if task.DownloadGroup != nil {
		RemoveTaskGroup(task.GID)
		QueueDownloadGroupName(task.DownloadGroup.ID)
	}
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

	// 异步更新托盘，避免阻塞主循环
	go func() {
		m.systray.SetIcon(tray.GetIconForState(state))
		time.Sleep(100 * time.Millisecond) // 在协程中等待，确保图标更新完成后再设置 tooltip

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
	}()
}

// InvalidateTask 使指定任务的缓存失效并发送删除事件
// 用于 RemoveTask 后确保前端和缓存同步
func (m *Monitor) InvalidateTask(gid string) {
	// 1. 清理元数据缓存
	Cache.InvalidateMetadata(gid)
	RemoveTaskGroup(gid)

	// 2. 从 lastStopped 缓存中移除
	m.mu.Lock()
	m.deletedGids[gid] = time.Now()
	delete(m.pendingCompleteGids, gid)

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

func (m *Monitor) startSurgeEventBridge() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-m.stopChan
		cancel()
	}()

	stream, cleanup, err := m.engine.StreamEvents(ctx)
	if err != nil {
		log.Printf("[Monitor] Failed to subscribe to Surge event stream: %v", err)
		return
	}
	if stream == nil {
		return
	}
	go func() {
		defer cleanup()
		for {
			select {
			case <-ctx.Done():
				return
			case rawEvt, ok := <-stream:
				if !ok {
					return
				}
				m.handleSurgeEvent(rawEvt)
			}
		}
	}()
}

func (m *Monitor) handleSurgeEvent(rawEvt any) {
	var deltaType string
	var gid string

	switch ev := rawEvt.(type) {
	case surgeEvents.DownloadQueuedMsg:
		deltaType = "add"
		gid = "sg_" + ev.DownloadID
	case surgeEvents.DownloadStartedMsg:
		deltaType = "add"
		gid = "sg_" + ev.DownloadID
	case surgeEvents.DownloadResumedMsg:
		deltaType = "resume"
		gid = "sg_" + ev.DownloadID
	case surgeEvents.DownloadPausedMsg:
		deltaType = "pause"
		gid = "sg_" + ev.DownloadID
	case surgeEvents.DownloadCompleteMsg:
		deltaType = "complete"
		gid = "sg_" + ev.DownloadID
	case surgeEvents.DownloadErrorMsg:
		deltaType = "error"
		gid = "sg_" + ev.DownloadID
	case surgeEvents.DownloadRemovedMsg:
		deltaType = "remove"
		gid = "sg_" + ev.DownloadID
	default:
		return
	}

	// 引入小延迟并使用 goroutine 异步触发，确保 SQLite 事务优先落盘，且不会阻塞 Surge 事件流通道
	go func(dt string, g string) {
		if dt == "complete" || dt == "error" || dt == "remove" || dt == "pause" {
			time.Sleep(150 * time.Millisecond)
		}
		m.hub.NotifyInternal(events.TaskDelta{Type: dt, GID: g})
	}(deltaType, gid)

	log.Printf("[Monitor] Surge Event: %s -> %s (gid: %s)", deltaType, gid, gid)
}

func (m *Monitor) filterDeletedTasks(tasks []rpc.Task) []rpc.Task {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 清理已过期的删除过滤（超过 15 秒）
	now := time.Now()
	for g, t := range m.deletedGids {
		if now.Sub(t) > 15*time.Second {
			delete(m.deletedGids, g)
		}
	}

	if len(m.deletedGids) == 0 {
		return tasks
	}

	filtered := make([]rpc.Task, 0, len(tasks))
	for _, t := range tasks {
		if _, deleted := m.deletedGids[t.GID]; !deleted {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

