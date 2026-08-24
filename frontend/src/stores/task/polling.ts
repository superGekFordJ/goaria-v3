import { TaskState } from './state'
import { TaskActions } from './actions'
import { useConfigStore } from '../config'
import {
  subscribeToTaskEvents,
  unsubscribeFromTaskEvents,
  subscribeToTaskMoveEvent,
  unsubscribeFromTaskMoveEvent,
  type TaskMove,
} from '../events'
import { TaskEvents } from './events'

export function setupPolling(
  state: TaskState,
  actions: TaskActions,
  getEvents: () => TaskEvents | undefined, // Lazy resolution to avoid circular dependency
) {
  const {
    pollingEnabled,
    pollingContextEnabled,
    isWindowVisible,
    preferredInterval,
    isFetching,
    tasks,
    syncMode,
    throttledUpdateTrayIcon,
  } = state

  const { fetchActiveTasks, fetchStoppedTasks, fetchTasks, getLastStoppedFetchTime } = actions

  // Polling State
  const pollingTimer = { value: null as ReturnType<typeof setTimeout> | null }
  let pollingGeneration = 0
  let eventsSubscribed = false

  // Constants
  const IDLE_INTERVAL = 5000
  const LOW_FREQ_INTERVAL = 30000

  // Event Subscription Helpers
  function initEventSubscription() {
    if (eventsSubscribed) return
    const events = getEvents()
    if (!events) return

    subscribeToTaskEvents(
      events.handleTaskDelta,
      () => {
        fetchTasks()
      },
      connected => {
        if (import.meta.env.DEV) console.debug('[Events] Aria2 connection:', connected)
        try {
          useConfigStore().setAria2Connected(connected)
        } catch {
          // Pinia inactive in unit tests
        }
        if (connected) fetchTasks()
      },
    )

    // Bridge cold-boot timing: subscribe first, then immediately read initial status
    try {
      void useConfigStore().refreshAria2Connected()
    } catch {
      // Pinia inactive in unit tests
    }

    subscribeToTaskMoveEvent((move: TaskMove) => {
      events.handleTaskMove(move)
    })

    eventsSubscribed = true
  }

  function cleanupEventSubscription() {
    if (!eventsSubscribed) return
    unsubscribeFromTaskEvents()
    unsubscribeFromTaskMoveEvent()
    eventsSubscribed = false
  }

  function startPollingInternal(interval: number) {
    if (import.meta.env.DEV) {
      console.debug(`[Polling] Starting with interval ${interval}ms, syncMode=${syncMode.value}`)
    }
    pollingEnabled.value = true
    const gen = ++pollingGeneration

    if (pollingTimer.value) {
      clearTimeout(pollingTimer.value)
      pollingTimer.value = null
    }

    initEventSubscription()

    let didInitialSync = false

    const runPolling = async () => {
      if (!pollingEnabled.value || pollingGeneration !== gen) return
      if (isFetching.value) {
        if (syncMode.value === 'polling') {
          pollingTimer.value = setTimeout(runPolling, interval)
        }
        return
      }

      isFetching.value = true
      let nextInterval: number

      try {
        if (!didInitialSync) {
          didInitialSync = true
          await fetchActiveTasks()
        }

        // Event-driven mode: after initial sync, do not continue polling
        if (syncMode.value === 'event-driven') {
          const now = Date.now()
          if (now - getLastStoppedFetchTime() > LOW_FREQ_INTERVAL) {
            fetchStoppedTasks()
          }
          throttledUpdateTrayIcon()
          isFetching.value = false
          return
        }

        // Polling mode: keep existing logic
        const hasActive = tasks.value.active.length > 0
        if (hasActive) {
          nextInterval = 3000
        } else {
          if (isWindowVisible.value) {
            nextInterval = IDLE_INTERVAL
          } else {
            nextInterval = interval
          }
        }

        const now = Date.now()
        if (now - getLastStoppedFetchTime() > LOW_FREQ_INTERVAL) {
          fetchStoppedTasks()
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

  function startPolling(interval: number = 1000) {
    preferredInterval.value = interval
    pollingContextEnabled.value = true

    if (!isWindowVisible.value) {
      startPollingInternal(3000)
      return
    }
    startPollingInternal(interval)
  }

  function stopPolling(disableContext: boolean = true) {
    pollingEnabled.value = false
    pollingGeneration++
    if (disableContext) {
      pollingContextEnabled.value = false
      cleanupEventSubscription()
      // Note: trayUpdateTimer is managed in state.ts/actions.ts via throttledUpdateTrayIcon
    }
    if (pollingTimer.value) {
      clearTimeout(pollingTimer.value)
      pollingTimer.value = null
    }
  }

  function setWindowVisibility(visible: boolean) {
    if (isWindowVisible.value === visible) return
    isWindowVisible.value = visible

    if (syncMode.value === 'event-driven') {
      // Event-driven mode: only do a one-shot sync on window focus restore
      if (visible && pollingContextEnabled.value && !isFetching.value) {
        fetchActiveTasks()
      }
      return
    }

    // Polling mode: keep existing logic
    stopPolling(false)

    if (!pollingContextEnabled.value) return

    if (visible) {
      const interval = preferredInterval.value < 1000 ? 1000 : preferredInterval.value
      startPollingInternal(interval)
    } else {
      startPollingInternal(3000)
    }
  }

  return {
    startPolling,
    stopPolling,
    setWindowVisibility,
    startPollingInternal, // Export for internal use if needed
  }
}

export type TaskPolling = ReturnType<typeof setupPolling>
