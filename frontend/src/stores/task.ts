import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  GetTasks,
  GetTaskMetadata,
  AddUri,
  PauseTask,
  ResumeTask,
  RemoveTask,
  OpenFolder,
  UpdateTrayState,
} from '../../bindings/goaria-v3/app'
import { Task } from '../../bindings/goaria-v3/internal/rpc/models'

export const useTaskStore = defineStore('task', () => {
  // State
  const tasks = ref<Record<string, Task[]>>({
    active: [],
    waiting: [],
    stopped: [],
  })

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

  // Getters
  const activeTasks = computed(() => tasks.value.active || [])
  const waitingTasks = computed(() => tasks.value.waiting || [])
  const stoppedTasks = computed(() => tasks.value.stopped || [])

  const allTasksCount = computed(() => {
    return activeTasks.value.length + waitingTasks.value.length + stoppedTasks.value.length
  })

  /**
   * Fetch task list from Aria2 via Go backend
   * Implements two-stage refresh: first get tasks, then recover missing metadata
   */
  async function fetchTasks() {
    try {
      const res = await GetTasks()
      // Reset error count on success
      consecutiveErrors.value = 0

      const dedupByGid = (list: Task[]) => {
        const seen = new Set<string>()
        return (list || []).filter(t => {
          const gid = t?.gid
          if (!gid) return false
          if (seen.has(gid)) return false
          seen.add(gid)
          return true
        })
      }

      // Ensure we always have arrays even if backend returns empty/null
      // Also: deduplicate by gid across lists to avoid duplicated keys and UI state reuse
      const active = dedupByGid(res.active || [])
      const activeGids = new Set(active.map(t => t.gid))

      const waiting = dedupByGid((res.waiting || []).filter(t => !activeGids.has(t.gid)))
      const waitingGids = new Set(waiting.map(t => t.gid))

      const stopped = dedupByGid((res.stopped || []).filter(t => !activeGids.has(t.gid) && !waitingGids.has(t.gid)))

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

      // Update tray icon based on task states
      const hasActive = tasks.value.active.length > 0
      const hasPaused = tasks.value.waiting.some(t => t.status === 'paused') ||
                        tasks.value.active.some(t => t.status === 'paused')
      const hasError = [...tasks.value.active, ...tasks.value.waiting, ...tasks.value.stopped]
                        .some(t => t.status === 'error')
      UpdateTrayState(hasActive, hasPaused, hasError)
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
    pollingEnabled.value = true
    const gen = ++pollingGeneration

    if (pollingTimer.value) {
      clearTimeout(pollingTimer.value)
      pollingTimer.value = null
    }

    const run = async () => {
      if (!pollingEnabled.value || pollingGeneration !== gen) return
      if (isFetching.value) {
        if (pollingEnabled.value && pollingGeneration === gen) {
          pollingTimer.value = setTimeout(run, interval)
        }
        return
      }

      isFetching.value = true
      try {
        await fetchTasks()
      } finally {
        isFetching.value = false
      }

      if (!pollingEnabled.value || pollingGeneration !== gen) return
      pollingTimer.value = setTimeout(run, interval)
    }

    void run()
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

  return {
    // State
    tasks,
    // Getters
    activeTasks,
    waitingTasks,
    stoppedTasks,
    allTasksCount,
    // Actions
    fetchTasks,
    startPolling,
    stopPolling,
    setWindowVisibility,
    addUri,
    pause,
    resume,
    remove,
    openTaskFolder,
  }
})
