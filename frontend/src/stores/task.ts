import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import {
  GetTasks,
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

  const pollingTimer = ref<ReturnType<typeof setInterval> | null>(null)
  const isWindowVisible = ref(true)
  const currentInterval = ref(1000)
  const consecutiveErrors = ref(0)
  const MAX_CONSECUTIVE_ERRORS = 3

  // Getters
  const activeTasks = computed(() => tasks.value.active || [])
  const waitingTasks = computed(() => tasks.value.waiting || [])
  const stoppedTasks = computed(() => tasks.value.stopped || [])

  const allTasksCount = computed(() => {
    return activeTasks.value.length + waitingTasks.value.length + stoppedTasks.value.length
  })

  /**
   * Fetch task list from Aria2 via Go backend
   */
  async function fetchTasks() {
    try {
      const res = await GetTasks()
      // Reset error count on success
      consecutiveErrors.value = 0

      // Ensure we always have arrays even if backend returns empty/null
      tasks.value = {
        active: res.active || [],
        waiting: res.waiting || [],
        stopped: res.stopped || [],
      }

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
    if (pollingTimer.value) return

    currentInterval.value = interval
    fetchTasks()
    pollingTimer.value = setInterval(fetchTasks, interval)
  }

  /**
   * Adjust polling based on window visibility
   * Hidden: switch to slow polling (3s) to keep tray icon updated but save CPU
   * Visible: resume normal polling
   */
  function setWindowVisibility(visible: boolean) {
    isWindowVisible.value = visible
    
    // Clear existing timer first
    if (pollingTimer.value) {
      clearInterval(pollingTimer.value)
      pollingTimer.value = null
    }

    if (visible) {
      // Resume normal fast polling (default 1000ms or last set interval)
      const interval = currentInterval.value < 1000 ? 1000 : currentInterval.value
      startPolling(interval)
    } else {
      // Slow background polling (3000ms) for tray icon updates
      fetchTasks()
      pollingTimer.value = setInterval(fetchTasks, 3000)
    }
  }

  /**
   * Stop task polling
   */
  function stopPolling() {
    if (pollingTimer.value) {
      clearInterval(pollingTimer.value)
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
