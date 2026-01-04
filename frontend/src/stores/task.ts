import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  GetTasks,
  GetActiveProgress,
  GetActiveTasks,
  GetStoppedTasks,
  GetTaskMetadata,
  AddUri,
  PauseTask,
  ResumeTask,
  RemoveTask,
  OpenFolder,
  UpdateTrayState,
  BatchPause,
  BatchResume,
  BatchRemove,
} from '../../bindings/goaria-v3/app'
import { Task, TaskProgress } from '../../bindings/goaria-v3/internal/rpc/models'
import { subscribeToTaskEvents, unsubscribeFromTaskEvents } from './events'

/**
 * 浅比对两个任务是否相等（仅比较高频变化字段）
 * 高频字段：completedLength, downloadSpeed, status
 * 低频字段：files, dir, errorCode 等变化极少
 */
function isTaskEqual(a: Task, b: Task): boolean {
  if (!a || !b) return false
  return (
    a.gid === b.gid &&
    a.status === b.status &&
    a.completedLength === b.completedLength &&
    a.downloadSpeed === b.downloadSpeed &&
    a.totalLength === b.totalLength &&
    a.errorCode === b.errorCode
  )
}

/**
 * 增量合并任务列表
 * @param oldList 当前列表
 * @param newList 新获取的列表
 * @returns { merged: Task[], changed: boolean }
 */
function mergeTasks(oldList: Task[], newList: Task[]): { merged: Task[]; changed: boolean } {
  // 快速路径：两个列表都为空
  if (oldList.length === 0 && newList.length === 0) {
    return { merged: oldList, changed: false }
  }
  
  // 快速路径：旧列表为空，直接返回新列表
  if (oldList.length === 0) {
    return { merged: newList, changed: true }
  }
  
  const oldMap = new Map(oldList.map(t => [t.gid, t]))
  
  // 检查是否有任务增减
  if (oldList.length !== newList.length) {
    return { merged: newList, changed: true }
  }
  
  // 检查 GID 集合是否一致
  const oldGids = new Set(oldList.map(t => t.gid))
  for (const newTask of newList) {
    if (!oldGids.has(newTask.gid)) {
      return { merged: newList, changed: true }
    }
  }
  
  // 逐个比对，保留未变化任务的引用
  let changed = false
  const merged = newList.map(newTask => {
    const oldTask = oldMap.get(newTask.gid)
    if (oldTask && isTaskEqual(oldTask, newTask)) {
      return oldTask // 保持原引用
    }
    changed = true
    return newTask
  })
  
  // 开发模式日志
  if (import.meta.env.DEV && changed) {
    const changedCount = merged.filter((t, i) => t !== oldList[i]).length
    console.debug(`[Polling] Merged ${newList.length} tasks, ${changedCount} changed`)
  }
  
  return { merged, changed }
}

/**
 * 按 GID 去重（复用 Set 实例减少 GC 压力）
 */
const _dedupGidSet = new Set<string>()
function dedupByGid(list: Task[]): Task[] {
  _dedupGidSet.clear()
  return (list || []).filter(t => {
    const gid = t?.gid
    if (!gid || _dedupGidSet.has(gid)) return false
    _dedupGidSet.add(gid)
    return true
  })
}

// 低频通道间隔常量
const PROGRESS_INTERVAL = 1000 // 1s
const LOW_FREQ_INTERVAL = 30000 // 30s

export const useTaskStore = defineStore('task', () => {
  // State
  const tasks = ref<Record<string, Task[]>>({
    active: [],
    waiting: [],
    stopped: [],
  })

  // Selection State for batch operations
  const selectedGids = ref<Set<string>>(new Set())

  const pollingTimer = ref<ReturnType<typeof setTimeout> | null>(null)
  const pollingEnabled = ref(false)
  const pollingContextEnabled = ref(false)
  const isFetching = ref(false)
  const isWindowVisible = ref(true)
  const preferredInterval = ref(1000)
  const consecutiveErrors = ref(0)
  const MAX_CONSECUTIVE_ERRORS = 3

  let pollingGeneration = 0

  let metadataInFlight = false
  const metadataPending = new Set<string>()

  let eventsSubscribed = false
  
  // 低频通道最后拉取时间
  let lastStoppedFetchTime = 0
  
  // 空闲轮询间隔（无活跃任务时使用）
  const IDLE_INTERVAL = 5000 // 5s

  // Getters
  const activeTasks = computed(() => tasks.value.active || [])
  const waitingTasks = computed(() => tasks.value.waiting || [])
  const stoppedTasks = computed(() => tasks.value.stopped || [])

  const allTasksCount = computed(() => {
    return activeTasks.value.length + waitingTasks.value.length + stoppedTasks.value.length
  })

  // Selection Getters
  const selectedCount = computed(() => selectedGids.value.size)
  const isSelected = (gid: string) => selectedGids.value.has(gid)
  const getSelectedGids = computed(() => Array.from(selectedGids.value))

  /**
   * 更新托盘图标状态（避免创建临时数组）
   */
  let lastTrayState = { hasActive: false, hasPaused: false, hasError: false }
  let trayUpdateTimer: ReturnType<typeof setTimeout> | null = null
  const TRAY_UPDATE_DEBOUNCE = 500

  function computeTrayState() {
    const hasActive = tasks.value.active.length > 0
    const hasPaused =
      tasks.value.waiting.some(t => t.status === 'paused') ||
      tasks.value.active.some(t => t.status === 'paused')
    const hasError =
      tasks.value.active.some(t => t.status === 'error') ||
      tasks.value.waiting.some(t => t.status === 'error')
    return { hasActive, hasPaused, hasError }
  }

  function throttledUpdateTrayIcon() {
    if (trayUpdateTimer) return
    trayUpdateTimer = setTimeout(() => {
      trayUpdateTimer = null
      const newState = computeTrayState()
      if (
        newState.hasActive !== lastTrayState.hasActive ||
        newState.hasPaused !== lastTrayState.hasPaused ||
        newState.hasError !== lastTrayState.hasError
      ) {
        lastTrayState = newState
        UpdateTrayState(newState.hasActive, newState.hasPaused, newState.hasError)
      }
    }, TRAY_UPDATE_DEBOUNCE)
  }

  function immediateUpdateTrayIcon() {
    if (trayUpdateTimer) {
      clearTimeout(trayUpdateTimer)
      trayUpdateTimer = null
    }
    const newState = computeTrayState()
    if (
      newState.hasActive !== lastTrayState.hasActive ||
      newState.hasPaused !== lastTrayState.hasPaused ||
      newState.hasError !== lastTrayState.hasError
    ) {
      lastTrayState = newState
      UpdateTrayState(newState.hasActive, newState.hasPaused, newState.hasError)
    }
  }

  const _progressGidSet = new Set<string>()

  async function patchActiveProgress(): Promise<boolean> {
    try {
      const progresses = await GetActiveProgress()
      _progressGidSet.clear()
      let hasUpdate = false
      let stateMismatch = false
      const needsMetadata: string[] = []

      for (const p of progresses as TaskProgress[]) {
        const gid = p?.gid
        if (!gid) continue
        _progressGidSet.add(gid)

        const task = tasks.value.active.find(t => t.gid === gid)
        if (task) {
          if (task.completedLength !== p.completedLength) {
            task.completedLength = p.completedLength
            hasUpdate = true
          }
          if (task.downloadSpeed !== p.downloadSpeed) {
            task.downloadSpeed = p.downloadSpeed
            hasUpdate = true
          }
          // 检测缺失元数据的任务（totalLength 为空/0 表示 Aria2 尚未解析完成）
          const total = task.totalLength
          if (!total || total === '0') {
            needsMetadata.push(gid)
          }
        } else {
          // aria2 active 列表与本地列表不同步（例如 waiting->active 或新增任务）
          stateMismatch = true
        }
      }

      // aria2 tellActive 不再包含的任务：可能完成/移除/状态变更，需要全量同步一次
      if (!stateMismatch && tasks.value.active.length > 0) {
        for (const t of tasks.value.active) {
          if (!_progressGidSet.has(t.gid)) {
            stateMismatch = true
            break
          }
        }
      }

      if (stateMismatch) {
        await fetchActiveTasks()
        fetchStoppedTasks() // 异步刷新
        return true
      }

      // 按需补全缺失元数据（低频、targeted）
      if (needsMetadata.length > 0) {
        try {
          const metadata = await GetTaskMetadata(needsMetadata)
          for (const gid of needsMetadata) {
            const newData = metadata?.[gid]
            const task = tasks.value.active.find(t => t.gid === gid)
            if (task && newData) {
              // 仅补全缺失字段，保留已有进度
              if (newData.totalLength && newData.totalLength !== '0') {
                task.totalLength = newData.totalLength
                hasUpdate = true
              }
              if (newData.files && newData.files.length > 0) {
                task.files = newData.files
                hasUpdate = true
              }
            }
          }
          if (import.meta.env.DEV) {
            console.debug(`[Progress] Fetched metadata for ${needsMetadata.length} tasks`)
          }
        } catch (err) {
          console.warn('[Progress] Metadata fetch failed:', err)
        }
      }

      if (hasUpdate) {
        tasks.value = { ...tasks.value }
        if (import.meta.env.DEV) {
          console.debug(`[Progress] Patched ${progresses.length} tasks`)
        }
      }

      return hasUpdate
    } catch (err) {
      console.warn('[Progress] Patch failed:', err)
      return false
    }
  }

  async function handleTaskDelta(delta: { type: string; gid: string }) {
    if (import.meta.env.DEV) {
      console.debug('[Events] Handling delta:', delta)
    }

    switch (delta.type) {
      case 'add': {
        try {
          const metadata = await GetTaskMetadata([delta.gid])
          const newTask = metadata?.[delta.gid]
          if (newTask) {
            tasks.value = {
              ...tasks.value,
              active: [newTask, ...tasks.value.active.filter(t => t.gid !== delta.gid)],
              waiting: tasks.value.waiting.filter(t => t.gid !== delta.gid),
            }
          } else {
            await fetchActiveTasks()
          }
        } catch {
          await fetchActiveTasks()
        }
        break
      }

      case 'complete': {
        moveTaskToStopped(delta.gid)
        fetchStoppedTasks()
        break
      }

      case 'pause': {
        patchTaskStatus(delta.gid, 'paused')
        break
      }

      case 'error': {
        patchTaskStatus(delta.gid, 'error')
        break
      }

      case 'remove': {
        removeTaskFromState(delta.gid)
        break
      }
    }

    immediateUpdateTrayIcon()
  }

  function patchTaskStatus(gid: string, status: string) {
    for (const list of [tasks.value.active, tasks.value.waiting]) {
      const task = list.find(t => t.gid === gid)
      if (task && task.status !== status) {
        task.status = status
        tasks.value = { ...tasks.value }
        return
      }
    }
  }

  function moveTaskToStopped(gid: string) {
    let task = tasks.value.active.find(t => t.gid === gid)
    if (!task) {
      task = tasks.value.waiting.find(t => t.gid === gid)
    }

    if (task) {
      task.status = 'complete'
      tasks.value = {
        active: tasks.value.active.filter(t => t.gid !== gid),
        waiting: tasks.value.waiting.filter(t => t.gid !== gid),
        stopped: [task, ...tasks.value.stopped.filter(t => t.gid !== gid)],
      }
    }
  }

  function removeTaskFromState(gid: string) {
    tasks.value = {
      active: tasks.value.active.filter(t => t.gid !== gid),
      waiting: tasks.value.waiting.filter(t => t.gid !== gid),
      stopped: tasks.value.stopped.filter(t => t.gid !== gid),
    }
    // Also remove from selection if present
    if (selectedGids.value.has(gid)) {
      selectedGids.value.delete(gid)
      selectedGids.value = new Set(selectedGids.value)
    }
  }

  function initEventSubscription() {
    if (eventsSubscribed) return
    subscribeToTaskEvents(
      handleTaskDelta,
      () => {
        fetchTasks()
      },
      connected => {
        if (import.meta.env.DEV) {
          console.debug('[Events] Aria2 connection:', connected)
        }
        if (connected) {
          fetchTasks()
        }
      },
    )
    eventsSubscribed = true
  }

  function cleanupEventSubscription() {
    if (!eventsSubscribed) return
    unsubscribeFromTaskEvents()
    eventsSubscribed = false
  }

  /**
   * 高频轮询：仅获取 active + waiting 任务
   * 返回是否有活跃任务（用于调整轮询频率）
   */
  async function fetchActiveTasks(): Promise<{ hasActiveTasks: boolean; taskCompleted: boolean }> {
    try {
      const res = await GetActiveTasks()
      consecutiveErrors.value = 0
      
      const active = dedupByGid(res.active || [])
      const activeGids = new Set(active.map(t => t.gid))
      const waiting = dedupByGid((res.waiting || []).filter((t: Task) => !activeGids.has(t.gid)))
      
      // 检测任务完成：通过数量变化判断（避免创建临时 Set）
      const oldCount = tasks.value.active.length + tasks.value.waiting.length
      const newCount = active.length + waiting.length
      const taskCompleted = newCount < oldCount
      
      // 增量合并
      const activeResult = mergeTasks(tasks.value.active, active)
      const waitingResult = mergeTasks(tasks.value.waiting, waiting)
      
      if (activeResult.changed || waitingResult.changed) {
        tasks.value = {
          active: activeResult.merged,
          waiting: waitingResult.merged,
          stopped: tasks.value.stopped, // 保持 stopped 不变
        }
      }
      
      // 更新托盘图标
      throttledUpdateTrayIcon()
      
      const hasActiveTasks = active.length > 0 || waiting.length > 0
      return { hasActiveTasks, taskCompleted }
      
    } catch (err) {
      handleFetchError(err)
      return { hasActiveTasks: false, taskCompleted: false }
    }
  }

  /**
   * 低频拉取：获取 stopped 任务（按需或定时）
   */
  async function fetchStoppedTasks() {
    try {
      const res = await GetStoppedTasks()
      const stopped = dedupByGid(res || [])
      
      // 与 active/waiting 去重
      const activeGids = new Set(tasks.value.active.map(t => t.gid))
      const waitingGids = new Set(tasks.value.waiting.map(t => t.gid))
      const filteredStopped = stopped.filter(t => !activeGids.has(t.gid) && !waitingGids.has(t.gid))
      
      const stoppedResult = mergeTasks(tasks.value.stopped, filteredStopped)
      if (stoppedResult.changed) {
        tasks.value = {
          ...tasks.value,
          stopped: stoppedResult.merged,
        }
      }
      
      lastStoppedFetchTime = Date.now()
    } catch (err) {
      console.warn('Failed to fetch stopped tasks:', err)
    }
  }

  /**
   * 处理拉取错误（熔断机制）
   */
  function handleFetchError(err: unknown) {
    console.error('Failed to fetch tasks:', err)
    consecutiveErrors.value++
    
    // Circuit breaker: Stop polling if too many consecutive errors
    if (consecutiveErrors.value >= MAX_CONSECUTIVE_ERRORS) {
      console.warn(`Stopped polling after ${MAX_CONSECUTIVE_ERRORS} consecutive errors to prevent log spam.`)
      stopPolling()
    }
  }

  /**
   * Fetch task list from Aria2 via Go backend (Fallback: 全量刷新)
   * Implements two-stage refresh: first get tasks, then recover missing metadata
   */
  async function fetchTasks() {
    try {
      const res = await GetTasks()
      // Reset error count on success
      consecutiveErrors.value = 0

      // Ensure we always have arrays even if backend returns empty/null
      // Also: deduplicate by gid across lists to avoid duplicated keys and UI state reuse
      const active = dedupByGid(res.active || [])
      const activeGids = new Set(active.map(t => t.gid))

      const waiting = dedupByGid((res.waiting || []).filter((t: Task) => !activeGids.has(t.gid)))
      const waitingGids = new Set(waiting.map(t => t.gid))

      const stopped = dedupByGid((res.stopped || []).filter((t: Task) => !activeGids.has(t.gid) && !waitingGids.has(t.gid)))

      const newTasks = { active, waiting, stopped }

      // Stage 2: Identify tasks with missing file paths and fetch metadata
      const tasksNeedingMetadata: string[] = []
      for (const t of [...newTasks.active, ...newTasks.waiting]) {
        if (!t.files?.[0]?.path) {
          tasksNeedingMetadata.push(t.gid)
        }
      }

      // Fetch missing metadata asynchronously (don't block initial render)
      if (tasksNeedingMetadata.length > 0) {
        for (const gid of tasksNeedingMetadata) {
          metadataPending.add(gid)
        }
        if (!metadataInFlight) {
          metadataInFlight = true
          const batch = Array.from(metadataPending)
          metadataPending.clear()
          GetTaskMetadata(batch)
            .then(metadata => {
              if (!metadata) return

              let updated = false
              for (const gid of Object.keys(metadata)) {
                const meta = metadata[gid]
                if (!meta?.files?.[0]?.path) continue

                for (const list of [tasks.value.active, tasks.value.waiting]) {
                  const idx = list.findIndex(t => t.gid === gid)
                  if (idx !== -1) {
                    list[idx] = { ...list[idx], ...meta }
                    updated = true
                  }
                }
              }

              if (updated) {
                tasks.value = { ...tasks.value }
              }
            })
            .catch(err => {
              console.warn('Failed to fetch task metadata:', err)
            })
            .finally(() => {
              metadataInFlight = false
            })
        }
      }

      tasks.value = newTasks

      immediateUpdateTrayIcon()
    } catch (err) {
      console.error('Failed to fetch tasks:', err)
      consecutiveErrors.value++
      
      // Circuit breaker: Stop polling if too many consecutive errors
      if (consecutiveErrors.value >= MAX_CONSECUTIVE_ERRORS) {
        console.warn(`Stopped polling after ${MAX_CONSECUTIVE_ERRORS} consecutive errors to prevent log spam.`)
        stopPolling()
      }
    }
  }

  /**
   * Start polling for task updates
   * @param interval Polling interval in milliseconds
   */
  function startPolling(interval: number = 1000) {
    preferredInterval.value = interval
    pollingContextEnabled.value = true

    if (!isWindowVisible.value) {
      startPollingInternal(3000)
      return
    }
    startPollingInternal(interval)
  }

  function startPollingInternal(interval: number) {
    if (import.meta.env.DEV) {
      console.debug(`[Polling] Starting with interval ${interval}ms, events: ${eventsSubscribed}`)
    }
    pollingEnabled.value = true
    const gen = ++pollingGeneration

    if (pollingTimer.value) {
      clearTimeout(pollingTimer.value)
      pollingTimer.value = null
    }

    initEventSubscription()

    let didInitialSync = false

    // 自适应轮询循环：有活跃任务时仅 patch 进度；无活跃任务时低频同步状态
    const runPolling = async () => {
      if (!pollingEnabled.value || pollingGeneration !== gen) return
      if (isFetching.value) {
        pollingTimer.value = setTimeout(runPolling, interval)
        return
      }

      isFetching.value = true
      let nextInterval = interval

      try {
        if (!didInitialSync) {
          didInitialSync = true
          await fetchActiveTasks()
        }

        const hasActive = tasks.value.active.length > 0
        if (hasActive) {
          await patchActiveProgress()
          // 前台固定 1s 更新进度；后台保持传入 interval（通常为 3000ms）
          nextInterval = isWindowVisible.value ? PROGRESS_INTERVAL : interval
        } else {
          // 无活跃任务：空闲间隔（前台） / 低频间隔（后台）
          if (isWindowVisible.value) {
            nextInterval = IDLE_INTERVAL
          } else {
            nextInterval = interval
          }

          // 空闲时执行一次状态同步（保留 fallback）
          await fetchActiveTasks()
        }

        // 定时刷新 stopped 列表
        const now = Date.now()
        if (now - lastStoppedFetchTime > LOW_FREQ_INTERVAL) {
          fetchStoppedTasks() // 异步执行
        }

        throttledUpdateTrayIcon()
      } finally {
        isFetching.value = false
      }

      if (!pollingEnabled.value || pollingGeneration !== gen) return
      pollingTimer.value = setTimeout(runPolling, nextInterval)
    }

    void runPolling()
  }

  /**
   * Adjust polling based on window visibility
   * Hidden: switch to slow polling (3s) to keep tray icon updated but save CPU
   * Visible: resume normal polling
   */
  function setWindowVisibility(visible: boolean) {
    if (isWindowVisible.value === visible) return
    isWindowVisible.value = visible
    
    // Clear existing timer first
    stopPolling(false)

    // If task list isn't active (e.g. Settings panel), never start polling
    if (!pollingContextEnabled.value) return

    if (visible) {
      // Resume normal fast polling (default 1000ms or last set interval)
      const interval = preferredInterval.value < 1000 ? 1000 : preferredInterval.value
      startPollingInternal(interval)
    } else {
      // Slow background polling (3000ms) for tray icon updates
      startPollingInternal(3000)
    }
  }

  /**
   * Stop task polling
   */
  function stopPolling(disableContext: boolean = true) {
    pollingEnabled.value = false
    pollingGeneration++
    if (disableContext) {
      pollingContextEnabled.value = false

      cleanupEventSubscription()

      if (trayUpdateTimer) {
        clearTimeout(trayUpdateTimer)
        trayUpdateTimer = null
      }
    }
    if (pollingTimer.value) {
      clearTimeout(pollingTimer.value)
      pollingTimer.value = null
    }
  }

  /**
   * Add a new download task
   * @param uri The URL to download
   */
  async function addUri(uri: string) {
    try {
      const res = await AddUri(uri)
      await fetchTasks()
      immediateUpdateTrayIcon()
      
      // 添加任务后重启高频轮询（避免在空闲间隔中等待太久）
      if (pollingContextEnabled.value && isWindowVisible.value) {
        stopPolling(false)
        startPollingInternal(preferredInterval.value)
      }
      
      return res
    } catch (err) {
      console.error('Failed to add URI:', err)
      throw err
    }
  }

  /**
   * Pause a specific task
   */
  async function pause(gid: string) {
    try {
      await PauseTask(gid)
      await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error(`Failed to pause task ${gid}:`, err)
    }
  }

  /**
   * Resume a specific task
   */
  async function resume(gid: string) {
    try {
      await ResumeTask(gid)
      await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error(`Failed to resume task ${gid}:`, err)
    }
  }

  /**
   * Remove a task and optionally its local files (Optimistic Update)
   */
  async function remove(gid: string, deleteFile: boolean) {
    // Optimistic Update: Immediately remove from local state
    tasks.value = {
      active: tasks.value.active.filter(t => t.gid !== gid),
      waiting: tasks.value.waiting.filter(t => t.gid !== gid),
      stopped: tasks.value.stopped.filter(t => t.gid !== gid),
    }

    // Also remove from selection to update UI (BatchActionBar)
    if (selectedGids.value.has(gid)) {
      selectedGids.value.delete(gid)
      selectedGids.value = new Set(selectedGids.value)
    }

    immediateUpdateTrayIcon()

    try {
      await RemoveTask(gid, deleteFile)
    } catch (err) {
      console.error(`Failed to remove task ${gid}:`, err)
      // Rollback: re-fetch tasks if the server call fails
      await fetchTasks()
    }
  }

  /**
   * Open the download folder containing the task's files
   */
  async function openTaskFolder(task: Task) {
    try {
      await OpenFolder(task)
    } catch (err) {
      console.error('Failed to open folder:', err)
    }
  }

  // === Selection Actions ===

  /**
   * Toggle selection state of a task
   */
  function toggleSelect(gid: string) {
    if (selectedGids.value.has(gid)) {
      selectedGids.value.delete(gid)
    } else {
      selectedGids.value.add(gid)
    }
    selectedGids.value = new Set(selectedGids.value) // Trigger reactivity
  }

  /**
   * Select all tasks by their gids
   */
  function selectAll(gids: string[]) {
    selectedGids.value = new Set(gids)
  }

  /**
   * Clear all selections
   */
  function clearSelection() {
    selectedGids.value = new Set()
  }

  // === Batch Operations ===

  /**
   * Batch pause multiple tasks
   */
  async function batchPause(gids: string[]) {
    try {
      await BatchPause(gids)
      await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error('Batch pause failed:', err)
    }
  }

  /**
   * Batch resume multiple tasks
   */
  async function batchResume(gids: string[]) {
    try {
      await BatchResume(gids)
      await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error('Batch resume failed:', err)
    }
  }

  /**
   * Batch remove multiple tasks with optimistic update
   */
  async function batchRemove(gids: string[], deleteFiles: boolean) {
    // Optimistic Update: Immediately remove from local state
    const gidSet = new Set(gids)
    tasks.value = {
      active: tasks.value.active.filter(t => !gidSet.has(t.gid)),
      waiting: tasks.value.waiting.filter(t => !gidSet.has(t.gid)),
      stopped: tasks.value.stopped.filter(t => !gidSet.has(t.gid)),
    }
    clearSelection()
    immediateUpdateTrayIcon()

    try {
      await BatchRemove(gids, deleteFiles)
    } catch (err) {
      console.error('Batch remove failed:', err)
      // Rollback: re-fetch tasks if the server call fails
      await fetchTasks()
    }
  }

  return {
    // State
    tasks,
    selectedGids,
    // Getters
    activeTasks,
    waitingTasks,
    stoppedTasks,
    allTasksCount,
    selectedCount,
    isSelected,
    getSelectedGids,
    // Actions
    fetchTasks,
    fetchStoppedTasks,
    startPolling,
    stopPolling,
    setWindowVisibility,
    addUri,
    pause,
    resume,
    remove,
    openTaskFolder,
    // Selection Actions
    toggleSelect,
    selectAll,
    clearSelection,
    // Batch Actions
    batchPause,
    batchResume,
    batchRemove,
  }
})
