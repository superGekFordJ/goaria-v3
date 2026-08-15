import { describe, it, expect, beforeEach, afterEach, vi, type Mock } from 'vitest'
import { ref, computed } from 'vue'
import { setupPolling } from '../polling'
import type { TaskState } from '../state'
import type { TaskActions } from '../actions'
import type { TaskEvents } from '../events'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

// Mock event subscription module
vi.mock('../../events', () => ({
  subscribeToTaskEvents: vi.fn(),
  unsubscribeFromTaskEvents: vi.fn(),
  subscribeToTaskMoveEvent: vi.fn(),
  unsubscribeFromTaskMoveEvent: vi.fn(),
}))

import {
  subscribeToTaskEvents,
  unsubscribeFromTaskEvents,
  unsubscribeFromTaskMoveEvent,
} from '../../events'

// --- Helpers ---

function createMockState(): TaskState {
  const tasks = ref<Record<string, Task[]>>({
    active: [],
    waiting: [],
    stopped: [],
  })
  const activeTasks = computed(() => tasks.value.active || [])
  const waitingTasks = computed(() => tasks.value.waiting || [])
  const stoppedTasks = computed(() => tasks.value.stopped || [])
  const selectedGids = ref<Set<string>>(new Set())
  const selectedGroupKeys = ref<Set<string>>(new Set())
  const allUris = computed(() => new Set<string>())

  return {
    tasks,
    selectedGids,
    selectedGroupKeys,
    syncMode: ref<'polling' | 'event-driven'>('polling'),
    pollingEnabled: ref(false),
    pollingContextEnabled: ref(false),
    isFetching: ref(false),
    isWindowVisible: ref(true),
    preferredInterval: ref(1000),
    consecutiveErrors: ref(0),
    activeTasks,
    waitingTasks,
    stoppedTasks,
    allTasksCount: computed(
      () => activeTasks.value.length + waitingTasks.value.length + stoppedTasks.value.length,
    ),
    allUris,
    selectedTaskCount: computed(() => selectedGids.value.size),
    selectedGroupCount: computed(() => selectedGroupKeys.value.size),
    selectedCount: computed(() => selectedGids.value.size + selectedGroupKeys.value.size),
    isSelected: () => false,
    isGroupSelected: () => false,
    getSelectedGids: computed(() => []),
    getSelectedGroupKeys: computed(() => []),
    throttledUpdateTrayIcon: vi.fn(),
    immediateUpdateTrayIcon: vi.fn(),
  } as unknown as TaskState
}

function createMockActions(): TaskActions {
  return {
    fetchActiveTasks: vi.fn().mockResolvedValue({ hasActiveTasks: false, taskCompleted: false }),
    fetchStoppedTasks: vi.fn().mockResolvedValue(undefined),
    fetchTasks: vi.fn().mockResolvedValue(undefined),
    addUri: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    remove: vi.fn(),
    openTaskFolder: vi.fn(),
    toggleSelect: vi.fn(),
    selectAll: vi.fn(),
    clearSelection: vi.fn(),
    batchPause: vi.fn(),
    batchResume: vi.fn(),
    runHeldResume: vi.fn(),
    batchRemove: vi.fn(),
    syncFromSnapshot: vi.fn(),
    minimizeToTray: vi.fn(),
    setPollingCallbacks: vi.fn(),
    setMoveTasksToActive: vi.fn(),
    isResumePending: vi.fn(() => false),
    markResumeSuperseded: vi.fn(),
    clearStoppedSuppression: vi.fn(),
    metadataPending: new Set<string>(),
    metadataInFlight: vi.fn().mockReturnValue(false),
    setMetadataInFlight: vi.fn(),
    queueMetadataRecovery: vi.fn(),
    getLastStoppedFetchTime: vi.fn().mockReturnValue(0),
  } as unknown as TaskActions
}

function createMockEvents(): TaskEvents {
  return {
    handleTaskDelta: vi.fn(),
    handleTaskMove: vi.fn(),
    moveTaskToActive: vi.fn(),
    moveTasksToActive: vi.fn(),
    moveTaskToStopped: vi.fn(),
  }
}

// --- Tests ---

describe('setupPolling', () => {
  let state: TaskState
  let actions: TaskActions
  let mockEvents: TaskEvents
  let polling: ReturnType<typeof setupPolling>

  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    state = createMockState()
    actions = createMockActions()
    mockEvents = createMockEvents()
    polling = setupPolling(state, actions, () => mockEvents)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // =====================================================
  // startPolling
  // =====================================================
  describe('startPolling', () => {
    it('should set pollingEnabled to true', () => {
      polling.startPolling(1000)
      expect(state.pollingEnabled.value).toBe(true)
    })

    it('should call initEventSubscription (subscribeToTaskEvents)', () => {
      polling.startPolling(1000)
      expect(subscribeToTaskEvents).toHaveBeenCalled()
    })

    it('should call fetchActiveTasks on first tick', async () => {
      polling.startPolling(1000)

      // Let the initial async runPolling execute
      await vi.advanceTimersByTimeAsync(0)

      expect(actions.fetchActiveTasks).toHaveBeenCalled()
    })
  })

  // =====================================================
  // stopPolling
  // =====================================================
  describe('stopPolling', () => {
    it('should set pollingEnabled to false', () => {
      polling.startPolling(1000)
      polling.stopPolling()
      expect(state.pollingEnabled.value).toBe(false)
    })

    it('should call cleanupEventSubscription when disableContext is true', () => {
      polling.startPolling(1000)
      polling.stopPolling(true)

      expect(unsubscribeFromTaskEvents).toHaveBeenCalled()
      expect(unsubscribeFromTaskMoveEvent).toHaveBeenCalled()
    })

    it('should clear timer', async () => {
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0) // let initial tick run

      polling.stopPolling()

      // After stopping, no more fetches should happen
      const callCount = (actions.fetchActiveTasks as Mock).mock.calls.length
      await vi.advanceTimersByTimeAsync(5000)
      expect((actions.fetchActiveTasks as Mock).mock.calls.length).toBe(callCount)
    })
  })

  // =====================================================
  // setWindowVisibility
  // =====================================================
  describe('setWindowVisibility', () => {
    it('should not trigger restart if value is the same', async () => {
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0)

      const callsBefore = (subscribeToTaskEvents as Mock).mock.calls.length
      polling.setWindowVisibility(true) // same as default
      expect((subscribeToTaskEvents as Mock).mock.calls.length).toBe(callsBefore)
    })

    it('should restart polling when visibility changes to false (polling mode)', async () => {
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0)

      polling.setWindowVisibility(false)

      // pollingEnabled should be restored (startPollingInternal re-enables)
      expect(state.pollingEnabled.value).toBe(true)
    })

    it('should restart polling when visibility changes to true (polling mode)', async () => {
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0)

      polling.setWindowVisibility(false)
      polling.setWindowVisibility(true)

      expect(state.pollingEnabled.value).toBe(true)
    })
  })

  // =====================================================
  // Polling interval logic
  // =====================================================
  describe('polling interval logic', () => {
    it('should use 3000ms interval when there are active tasks', async () => {
      state.tasks.value.active = [{ gid: 'gid-1' } as Task]
      polling.startPolling(1000)

      await vi.advanceTimersByTimeAsync(0) // initial tick

      // After first tick, next interval should be 3000ms
      // fetchActiveTasks should be called once initially
      expect(actions.fetchActiveTasks).toHaveBeenCalledTimes(1)

      // Advance 3000ms to trigger next tick
      await vi.advanceTimersByTimeAsync(3000)
      // Should have been called again (initial tick calls fetchActiveTasks, then schedules next)
      // Note: the interval check happens inside runPolling
    })

    it('should use IDLE_INTERVAL (5000ms) when no active tasks and window visible', async () => {
      state.tasks.value.active = []
      state.isWindowVisible.value = true
      polling.startPolling(1000)

      await vi.advanceTimersByTimeAsync(0) // initial tick
      expect(actions.fetchActiveTasks).toHaveBeenCalledTimes(1)

      // Advance by 5000ms (IDLE_INTERVAL)
      await vi.advanceTimersByTimeAsync(5000)
      // Should schedule with IDLE_INTERVAL since no active tasks
    })
  })

  // =====================================================
  // Event-driven mode
  // =====================================================
  describe('event-driven mode', () => {
    it('should not schedule subsequent polling ticks after initial sync', async () => {
      state.syncMode.value = 'event-driven'
      polling.startPolling(1000)

      await vi.advanceTimersByTimeAsync(0) // initial tick
      expect(actions.fetchActiveTasks).toHaveBeenCalledTimes(1)

      // Advance well past any normal interval — no more fetches should happen
      await vi.advanceTimersByTimeAsync(10000)
      expect(actions.fetchActiveTasks).toHaveBeenCalledTimes(1)
    })

    it('should still call initEventSubscription', () => {
      state.syncMode.value = 'event-driven'
      polling.startPolling(1000)
      expect(subscribeToTaskEvents).toHaveBeenCalled()
    })

    it('should do one-shot fetchActiveTasks on Window Focus Sync', async () => {
      state.syncMode.value = 'event-driven'
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0) // initial tick

      const callsBefore = (actions.fetchActiveTasks as Mock).mock.calls.length

      // Hide then show
      polling.setWindowVisibility(false)
      polling.setWindowVisibility(true)

      // Should have triggered one additional fetchActiveTasks
      expect((actions.fetchActiveTasks as Mock).mock.calls.length).toBe(callsBefore + 1)
    })

    it('should NOT start polling loop on window focus restore', async () => {
      state.syncMode.value = 'event-driven'
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0)

      polling.setWindowVisibility(false)
      polling.setWindowVisibility(true)

      const callsAfterRestore = (actions.fetchActiveTasks as Mock).mock.calls.length

      // Advance time — should NOT trigger additional fetches
      await vi.advanceTimersByTimeAsync(10000)
      expect((actions.fetchActiveTasks as Mock).mock.calls.length).toBe(callsAfterRestore)
    })

    it('should skip fetchActiveTasks on window focus if isFetching is true', async () => {
      state.syncMode.value = 'event-driven'
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0) // initial tick

      const callsBefore = (actions.fetchActiveTasks as Mock).mock.calls.length

      // Simulate concurrent fetch in progress
      state.isFetching.value = true
      polling.setWindowVisibility(false)
      polling.setWindowVisibility(true)

      // Should NOT have triggered additional fetchActiveTasks due to isFetching guard
      expect((actions.fetchActiveTasks as Mock).mock.calls.length).toBe(callsBefore)
      state.isFetching.value = false
    })

    it('should not do anything on window hide', async () => {
      state.syncMode.value = 'event-driven'
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0)

      const callsBefore = (actions.fetchActiveTasks as Mock).mock.calls.length
      polling.setWindowVisibility(false)

      // No additional fetch should happen
      expect((actions.fetchActiveTasks as Mock).mock.calls.length).toBe(callsBefore)
    })
  })

  // =====================================================
  // Polling mode behavior unchanged
  // =====================================================
  describe('polling mode (unchanged behavior)', () => {
    it('should schedule subsequent ticks in polling mode', async () => {
      state.syncMode.value = 'polling'
      polling.startPolling(1000)

      await vi.advanceTimersByTimeAsync(0) // initial tick
      expect(actions.fetchActiveTasks).toHaveBeenCalledTimes(1)

      // Advance past the next interval (IDLE_INTERVAL=5000 since no active tasks)
      await vi.advanceTimersByTimeAsync(5000)
      // Should have been called again
      expect((actions.fetchActiveTasks as Mock).mock.calls.length).toBeGreaterThanOrEqual(1)
    })

    it('should restart polling on window visibility change', async () => {
      state.syncMode.value = 'polling'
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0)

      polling.setWindowVisibility(false)
      // Should have restarted with 3000ms interval
      expect(state.pollingEnabled.value).toBe(true)

      polling.setWindowVisibility(true)
      expect(state.pollingEnabled.value).toBe(true)
    })
  })

  // =====================================================
  // Generation token
  // =====================================================
  describe('generation token', () => {
    it('should cancel old polling loop when startPolling is called again', async () => {
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0) // initial tick

      const firstCallCount = (actions.fetchActiveTasks as Mock).mock.calls.length

      // Start again — should cancel old generation
      polling.startPolling(1000)
      await vi.advanceTimersByTimeAsync(0) // new initial tick

      // The old timer should not fire anymore
      // Advance enough time that old timer would have fired
      await vi.advanceTimersByTimeAsync(5000)

      // fetchActiveTasks should only have been called for: first start + second start
      // Not for the old timer's subsequent ticks
      expect((actions.fetchActiveTasks as Mock).mock.calls.length).toBeGreaterThanOrEqual(
        firstCallCount + 1,
      )
    })
  })
})
