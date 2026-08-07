package monitor

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"goaria-v3/internal/events"
	"goaria-v3/internal/rpc"
)

func (m *Monitor) hasAria2Tasks() bool {
	for gid := range m.prevActiveGids {
		if strings.HasPrefix(gid, "ar_") {
			return true
		}
	}
	for gid := range m.prevWaitingGids {
		if strings.HasPrefix(gid, "ar_") {
			return true
		}
	}
	return false
}

func (m *Monitor) currentTickInterval() time.Duration {
	if !m.aria2Recovered.Load() {
		return 1 * time.Second
	}
	if State.HasWindow() && (!m.engine.IsSurgeActive() || m.hasAria2Tasks()) {
		return m.windowInterval
	}
	m.mu.Lock()
	fastRetry := time.Now().Before(m.shouldFetchStoppedUntil)
	m.mu.Unlock()
	if fastRetry {
		return m.windowInterval
	}
	return m.headlessInterval
}

func (m *Monitor) tick() {
	var (
		active    []rpc.Task
		waiting   []rpc.Task
		stopped   []rpc.Task
		activeErr error
		wg        sync.WaitGroup
	)

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

	// 3. 获取 Stopped 任务 (仅在需要时或定期兜底刷新时)
	m.mu.Lock()
	fetchStopped := m.shouldFetchStopped || time.Since(m.lastStoppedFetchTime) > 10*time.Second || time.Now().Before(m.shouldFetchStoppedUntil)
	m.mu.Unlock()

	// Refresh the Surge master cache mirror only on the 10s periodic
	// boundary, not on event-triggered fetches, to avoid high-frequency
	// gob.Decode. This syncs non-event-driven master list writes.
	if fetchStopped && m.surgeEng != nil && time.Since(m.lastStoppedFetchTime) > 10*time.Second {
		m.surgeEng.RefreshMasterCache()
	}

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

	if activeErr == nil {
		if !m.aria2Recovered.Swap(true) {
			log.Printf("[Monitor] Aria2 engine first recovery successful")
		}
		m.maybeLogRecoveryComplete()
	}

	if activeErr != nil {
		if m.aria2Recovered.Load() {
			if !m.aria2UnavailableLogged.Swap(true) {
				log.Printf("[Monitor] Aria2 engine became unavailable: %v", activeErr)
			} else {
				log.Printf("[DEBUG] Aria2 engine still unavailable: %v", activeErr)
			}
		} else {
			log.Printf("[Monitor] Aria2 engine unavailable, will retry: %v", activeErr)
		}
		return
	}

	// 过滤掉最近被删除的任务，防止网络或进程通信竞态导致已删除任务被再次缓存或上报
	active = m.filterDeletedTasks(active)
	waiting = m.filterDeletedTasks(waiting)
	stopped = m.filterDeletedTasks(stopped)

	// Defensive: filter out any Surge tasks that slipped through Tell*Lite.
	// Surge tasks are maintained by the event-driven path, not tick polling.
	active = filterSurgeTasks(active)
	waiting = filterSurgeTasks(waiting)
	stopped = filterSurgeTasks(stopped)

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

	// shouldFetchStoppedUntil 短暂过渡兜底窗口（1.5s）：handleSurgeEvent 已在
	// 事件到达时同步更新 masterCache（内存操作领先于 lifecycle worker 的文件
	// 持久化），Gob 竞态已消除。此窗口仅作为事件丢失等极端情况的兜底，保留
	// MoveTaskToStopped 放入的任务不被 UpdateFromAria2 的空 stopped 覆盖。
	if fetchStopped {
		m.mu.Lock()
		fastRetry := time.Now().Before(m.shouldFetchStoppedUntil)
		m.mu.Unlock()
		if fastRetry {
			existingGids := make(map[string]struct{}, len(stopped))
			for _, t := range stopped {
				existingGids[t.GID] = struct{}{}
			}
			for _, t := range Cache.GetStopped() {
				if _, ok := existingGids[t.GID]; ok {
					continue
				}
				// Surge stopped tasks are maintained by the event path; skip.
				if strings.HasPrefix(t.GID, "sg_") {
					continue
				}
				// Per-iteration lock lets InvalidateTask set deletedGids
				// between iterations so later tasks are still caught.
				m.mu.Lock()
				_, deleted := m.deletedGids[t.GID]
				m.mu.Unlock()
				if deleted {
					continue
				}
				stopped = append(stopped, t)
			}
		}
	}

	// Collect per-worker telemetry from Surge engine
	m.collectTelemetry()

	// Canonicalize cross-list membership before lastStopped persist / cache+tracker.
	// Precedence: active > waiting > stopped (strips stale stopped beside fresh live).
	active, waiting, stopped = normalizeAria2TickLists(active, waiting, stopped)

	// Persist normalized stopped after fast-retry. Always rewrite the reuse buffer
	// so stripped conflicts do not linger; only bump fetch time on fetchStopped ticks.
	m.mu.Lock()
	m.lastStopped = copyTaskSlice(stopped)
	if fetchStopped {
		m.shouldFetchStopped = false
		m.lastStoppedFetchTime = time.Now()
	}
	m.mu.Unlock()

	// 更新缓存
	Cache.UpdateFromAria2(active, waiting, stopped)

	// 更新追踪器并获取已完成任务
	completedTasks := m.tracker.Update(active, waiting, stopped)

	// 处理已完成任务（写入历史和速度统计）
	for _, task := range completedTasks {
		m.handleTaskComplete(task)
	}

	// Periodic eviction of orphaned Aria2 metadata entries. Runs at most every
	// metadataCleanupInterval, gated on aria2Recovered. Reuses local slices to
	// avoid deep-copying cache state. Safe to run concurrently with the Surge
	// event path's handleTaskComplete: both use the same mu RWMutex (Lock here
	// vs RLock in GetMetadata), serialized with no nesting.
	m.runMetadataCleanup(active, waiting, stopped)

	// 构建托盘快照
	snapshot := m.buildTraySnapshot()

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
			payload := map[string]string{
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

// detectAndEmitTaskMoves 检测任务列表转移并发射事件
// 注意：仅检测 active <-> waiting 转移，stopped 转移由 complete/error 事件处理
func (m *Monitor) detectAndEmitTaskMoves(active, waiting []rpc.Task) {
	// 构建当前 GID 集合
	currentActiveGids := make(map[string]bool)
	currentWaitingGids := make(map[string]bool)

	activeByGid := make(map[string]*rpc.Task)
	waitingByGid := make(map[string]*rpc.Task)

	for i := range active {
		if strings.HasPrefix(active[i].GID, "sg_") {
			continue
		}
		currentActiveGids[active[i].GID] = true
		activeByGid[active[i].GID] = &active[i]
	}
	for i := range waiting {
		if strings.HasPrefix(waiting[i].GID, "sg_") {
			continue
		}
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
		if strings.HasPrefix(t.GID, "sg_") {
			continue
		}
		m.prevActiveGids[t.GID] = true
	}
	for _, t := range waiting {
		if strings.HasPrefix(t.GID, "sg_") {
			continue
		}
		m.prevWaitingGids[t.GID] = true
	}
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
		// sg_ tasks are removed directly by Cache.RemoveTask, not via tombstone.
		if strings.HasPrefix(t.GID, "sg_") {
			filtered = append(filtered, t)
			continue
		}
		if _, deleted := m.deletedGids[t.GID]; !deleted {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// filterSurgeTasks removes sg_ prefixed tasks from a slice (defensive).
func filterSurgeTasks(tasks []rpc.Task) []rpc.Task {
	filtered := make([]rpc.Task, 0, len(tasks))
	for _, t := range tasks {
		if !strings.HasPrefix(t.GID, "sg_") {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// runMetadataCleanup throttles and runs orphan metadata eviction using the
// local tick slices to build the engine GID set without deep copies. The
// zero-value lastMetadataCleanup triggers a run on the first post-recovery
// tick.
func (m *Monitor) runMetadataCleanup(active, waiting, stopped []rpc.Task) {
	if !m.aria2Recovered.Load() {
		return
	}
	if time.Since(m.lastMetadataCleanup) <= metadataCleanupInterval {
		return
	}
	activeGids := make(map[string]bool, len(active)+len(waiting)+len(stopped))
	for i := range active {
		activeGids[active[i].GID] = true
	}
	for i := range waiting {
		activeGids[waiting[i].GID] = true
	}
	for i := range stopped {
		activeGids[stopped[i].GID] = true
	}
	evicted := Cache.CleanupMetadata(activeGids)
	if evicted > 0 {
		log.Printf("[Monitor] Metadata cleanup: evicted %d orphaned entries", evicted)
	}
	// Unconditional update prevents retrying every tick when nothing needs cleanup.
	m.lastMetadataCleanup = time.Now()
}
