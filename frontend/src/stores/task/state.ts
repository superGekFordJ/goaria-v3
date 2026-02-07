import { ref, computed } from 'vue'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import { UpdateTrayState } from '../../../bindings/goaria-v3/app.js'

export function setupState() {
  // State
  const tasks = ref<Record<string, Task[]>>({
    active: [],
    waiting: [],
    stopped: [],
  })

  // Selection State for batch operations
  const selectedGids = ref<Set<string>>(new Set())

  // Polling & App Flags
  const syncMode = ref<'polling' | 'event-driven'>('event-driven')
  const pollingEnabled = ref(false)
  const pollingContextEnabled = ref(false)
  const isFetching = ref(false)
  const isWindowVisible = ref(true)
  const preferredInterval = ref(1000)
  const consecutiveErrors = ref(0)

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

  // Tray State (UI Logic related to state)
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

  return {
    tasks,
    selectedGids,
    syncMode,
    pollingEnabled,
    pollingContextEnabled,
    isFetching,
    isWindowVisible,
    preferredInterval,
    consecutiveErrors,
    activeTasks,
    waitingTasks,
    stoppedTasks,
    allTasksCount,
    selectedCount,
    isSelected,
    getSelectedGids,
    throttledUpdateTrayIcon,
    immediateUpdateTrayIcon,
  }
}

export type TaskState = ReturnType<typeof setupState>
