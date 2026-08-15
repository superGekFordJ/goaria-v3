import { defineStore } from 'pinia'
import { setupState } from './state'
import { setupActions } from './actions'
import { setupPolling, TaskPolling } from './polling'
import { setupEvents } from './events'

export const useTaskStore = defineStore('task', () => {
  // 1. Setup State
  const state = setupState()

  // 2. Setup Actions (Logic & API)
  const actions = setupActions(state)

  // 3. Setup Events (Subscribers) - Lazy resolved in polling
  const events = setupEvents(state, actions, {} as TaskPolling) // polling dependency resolved below

  // 4. Setup Polling (needs actions & events)
  const polling = setupPolling(state, actions, () => events)

  // 5. Wire up dependencies (Circular)
  // Actions need to restart polling
  actions.setPollingCallbacks(
    polling.startPolling, // restart
    polling.stopPolling, // stop
  )
  actions.setMoveTasksToActive(events.moveTasksToActive)

  // Events (if setupEvents needed polling, which it doesn't currently, but type says so in plan)
  // Actually setupEvents doesn't use polling in implementation above, but good to be consistent.

  return {
    // State
    tasks: state.tasks,
    selectedGids: state.selectedGids,
    selectedGroupKeys: state.selectedGroupKeys,
    syncMode: state.syncMode,
    pollingEnabled: state.pollingEnabled,
    pollingContextEnabled: state.pollingContextEnabled,
    isFetching: state.isFetching,
    isWindowVisible: state.isWindowVisible,
    preferredInterval: state.preferredInterval,
    consecutiveErrors: state.consecutiveErrors,

    // Getters
    activeTasks: state.activeTasks,
    waitingTasks: state.waitingTasks,
    stoppedTasks: state.stoppedTasks,
    allTasksCount: state.allTasksCount,
    allUris: state.allUris,
    selectedTaskCount: state.selectedTaskCount,
    selectedGroupCount: state.selectedGroupCount,
    selectedCount: state.selectedCount,
    isSelected: state.isSelected,
    isGroupSelected: state.isGroupSelected,
    getSelectedGids: state.getSelectedGids,
    getSelectedGroupKeys: state.getSelectedGroupKeys,

    // Actions
    fetchActiveTasks: actions.fetchActiveTasks,
    fetchStoppedTasks: actions.fetchStoppedTasks,
    fetchTasks: actions.fetchTasks,
    addUri: actions.addUri,
    batchAddUri: actions.batchAddUri,
    pause: actions.pause,
    resume: actions.resume,
    remove: actions.remove,
    openTaskFolder: actions.openTaskFolder,

    // Selection Actions
    toggleSelect: actions.toggleSelect,
    toggleSelectGroup: actions.toggleSelectGroup,
    clearSelectedGroup: actions.clearSelectedGroup,
    selectAll: actions.selectAll,
    clearSelection: actions.clearSelection,

    // Batch Actions
    batchPause: actions.batchPause,
    batchResume: actions.batchResume,
    runHeldResume: actions.runHeldResume,
    batchRemove: actions.batchRemove,

    // System Actions
    syncFromSnapshot: actions.syncFromSnapshot,
    minimizeToTray: actions.minimizeToTray,

    // Polling Actions
    startPolling: polling.startPolling,
    stopPolling: polling.stopPolling,
    setWindowVisibility: polling.setWindowVisibility,
  }
})
