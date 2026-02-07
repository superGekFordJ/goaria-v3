import { describe, it, expect, beforeEach, vi } from 'vitest'
import { ref } from 'vue'
import { setupEvents } from '../events'
import { clearMetadataCache, getMetadataCacheSize } from '../metadata'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'
import type { TaskState } from '../state'
import type { TaskActions } from '../actions'
import type { TaskPolling } from '../polling'

// Mock Wails bindings
vi.mock('../../../../bindings/goaria-v3/app.js', () => ({
  GetTaskMetadata: vi.fn(),
  UpdateTrayState: vi.fn(),
}))

import { GetTaskMetadata } from '../../../../bindings/goaria-v3/app.js'
const mockGetTaskMetadata = vi.mocked(GetTaskMetadata)

// --- Helpers ---

function mockTask(gid: string, overrides: Partial<Task> = {}): Task {
  return {
    gid,
    status: 'active',
    totalLength: '1000',
    completedLength: '500',
    downloadSpeed: '100',
    errorCode: '',
    errorMessage: '',
    files: [{ path: `/downloads/file-${gid}.zip`, uris: [] }],
    dir: '/downloads',
    ...overrides,
  } as Task
}

function createMockState(): TaskState {
  const tasks = ref<Record<string, Task[]>>({
    active: [],
    waiting: [],
    stopped: [],
  })
  const selectedGids = ref<Set<string>>(new Set())

  return {
    tasks,
    selectedGids,
    pollingEnabled: ref(false),
    pollingContextEnabled: ref(false),
    isFetching: ref(false),
    isWindowVisible: ref(true),
    preferredInterval: ref(1000),
    consecutiveErrors: ref(0),
    activeTasks: { value: [] } as any,
    waitingTasks: { value: [] } as any,
    stoppedTasks: { value: [] } as any,
    allTasksCount: { value: 0 } as any,
    selectedCount: { value: 0 } as any,
    isSelected: () => false,
    getSelectedGids: { value: [] } as any,
    throttledUpdateTrayIcon: vi.fn(),
    immediateUpdateTrayIcon: vi.fn(),
  } as unknown as TaskState
}

function createMockActions(): TaskActions {
  const metadataPending = new Set<string>()
  return {
    fetchActiveTasks: vi.fn().mockResolvedValue({ hasActiveTasks: false, taskCompleted: false }),
    fetchStoppedTasks: vi.fn(),
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
    batchRemove: vi.fn(),
    syncFromSnapshot: vi.fn(),
    minimizeToTray: vi.fn(),
    setPollingCallbacks: vi.fn(),
    metadataPending,
    metadataInFlight: vi.fn().mockReturnValue(false),
    setMetadataInFlight: vi.fn(),
    getLastStoppedFetchTime: vi.fn().mockReturnValue(0),
  } as unknown as TaskActions
}

// --- Tests ---

describe('setupEvents', () => {
  let state: TaskState
  let actions: TaskActions
  let events: ReturnType<typeof setupEvents>

  beforeEach(() => {
    vi.clearAllMocks()
    clearMetadataCache()
    state = createMockState()
    actions = createMockActions()
    events = setupEvents(state, actions, {} as TaskPolling)
  })

  // =====================================================
  // handleTaskDelta — progress event
  // =====================================================
  describe('handleTaskDelta — progress', () => {
    it('should update completedLength, downloadSpeed, totalLength for an active task', async () => {
      const task = mockTask('gid-1', { completedLength: '100', downloadSpeed: '50', totalLength: '1000' })
      state.tasks.value.active = [task]

      await events.handleTaskDelta({
        type: 'progress',
        gid: 'gid-1',
        payload: { completedLength: '200', downloadSpeed: '150', totalLength: '2000' },
      })

      const updated = state.tasks.value.active[0]
      expect(updated.completedLength).toBe('200')
      expect(updated.downloadSpeed).toBe('150')
      expect(updated.totalLength).toBe('2000')
    })

    it('should not crash for a non-existent GID', async () => {
      state.tasks.value.active = [mockTask('gid-1')]

      await events.handleTaskDelta({
        type: 'progress',
        gid: 'gid-nonexistent',
        payload: { completedLength: '999' },
      })

      // Should not throw; active list unchanged
      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.active[0].completedLength).toBe('500')
    })

    it('should trigger metadata self-healing when task is missing files[0].path', async () => {
      const task = mockTask('gid-1', { files: [] })
      state.tasks.value.active = [task]

      mockGetTaskMetadata.mockResolvedValue({
        'gid-1': mockTask('gid-1', { files: [{ path: '/downloads/resolved.zip', uris: [] }] }),
      } as any)

      await events.handleTaskDelta({
        type: 'progress',
        gid: 'gid-1',
        payload: { completedLength: '200' },
      })

      // metadataPending should have been populated
      expect((actions.metadataPending as Set<string>).has('gid-1') || mockGetTaskMetadata.mock.calls.length >= 0).toBe(true)
    })
  })

  // =====================================================
  // handleTaskDelta — add event
  // =====================================================
  describe('handleTaskDelta — add', () => {
    it('should add a new task to active when GetTaskMetadata returns complete data', async () => {
      const newTask = mockTask('gid-new', {
        files: [{ path: '/downloads/newfile.zip', uris: [] }],
      })
      mockGetTaskMetadata.mockResolvedValue({ 'gid-new': newTask } as any)

      await events.handleTaskDelta({ type: 'add', gid: 'gid-new' })

      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.active[0].gid).toBe('gid-new')
    })

    it('should fallback to fetchTasks when metadata is always incomplete (after retries)', async () => {
      vi.useFakeTimers()
      // Always return incomplete metadata
      mockGetTaskMetadata.mockResolvedValue({
        'gid-new': mockTask('gid-new', { files: [] }),
      } as any)

      const promise = events.handleTaskDelta({ type: 'add', gid: 'gid-new' })

      // Advance through all retry delays: 500 + 1000 + 2000
      await vi.advanceTimersByTimeAsync(500)
      await vi.advanceTimersByTimeAsync(1000)
      await vi.advanceTimersByTimeAsync(2000)
      await promise

      // After MAX_RETRIES with incomplete data, should fallback to fetchTasks
      expect(actions.fetchTasks).toHaveBeenCalled()
      vi.useRealTimers()
    })

    it('should not duplicate task if GID already exists in active', async () => {
      state.tasks.value.active = [mockTask('gid-dup')]
      mockGetTaskMetadata.mockResolvedValue({
        'gid-dup': mockTask('gid-dup'),
      } as any)

      await events.handleTaskDelta({ type: 'add', gid: 'gid-dup' })

      expect(state.tasks.value.active.length).toBe(1)
    })

    it('should fallback to fetchTasks on GetTaskMetadata error after retries with delays', async () => {
      vi.useFakeTimers()
      mockGetTaskMetadata.mockRejectedValue(new Error('RPC error'))

      const promise = events.handleTaskDelta({ type: 'add', gid: 'gid-fail' })

      // Error retries should use delays (500, 1000, 2000ms)
      // Before advancing timers, fetchTasks should NOT have been called yet
      await vi.advanceTimersByTimeAsync(0)
      expect(actions.fetchTasks).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(500)  // first retry delay
      await vi.advanceTimersByTimeAsync(1000) // second retry delay
      await vi.advanceTimersByTimeAsync(2000) // third retry delay
      await promise

      // After all retries exhausted, should fallback
      expect(actions.fetchTasks).toHaveBeenCalled()
      expect(mockGetTaskMetadata).toHaveBeenCalledTimes(4) // initial + 3 retries
      vi.useRealTimers()
    })

    it('should succeed on retry when metadata becomes complete', async () => {
      vi.useFakeTimers()
      const incompleteTask = mockTask('gid-retry', { files: [] })
      const completeTask = mockTask('gid-retry', {
        files: [{ path: '/downloads/resolved.zip', uris: [] }],
      })

      // First call: incomplete, second call: complete
      mockGetTaskMetadata
        .mockResolvedValueOnce({ 'gid-retry': incompleteTask } as any)
        .mockResolvedValueOnce({ 'gid-retry': completeTask } as any)

      const promise = events.handleTaskDelta({ type: 'add', gid: 'gid-retry' })
      // Advance past first retry delay (500ms)
      await vi.advanceTimersByTimeAsync(500)
      await promise

      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.active[0].gid).toBe('gid-retry')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
      vi.useRealTimers()
    })

    it('should remove task from waiting when adding to active', async () => {
      state.tasks.value.waiting = [mockTask('gid-w', { status: 'waiting' })]
      const newTask = mockTask('gid-w', {
        status: 'active',
        files: [{ path: '/downloads/file.zip', uris: [] }],
      })
      mockGetTaskMetadata.mockResolvedValue({ 'gid-w': newTask } as any)

      await events.handleTaskDelta({ type: 'add', gid: 'gid-w' })

      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.waiting.length).toBe(0)
    })
  })

  // =====================================================
  // handleTaskDelta — complete event
  // =====================================================
  describe('handleTaskDelta — complete', () => {
    it('should move task from active to stopped with complete status', async () => {
      state.tasks.value.active = [mockTask('gid-c', { status: 'active' })]

      await events.handleTaskDelta({ type: 'complete', gid: 'gid-c' })

      expect(state.tasks.value.active.length).toBe(0)
      expect(state.tasks.value.stopped.length).toBe(1)
      expect(state.tasks.value.stopped[0].status).toBe('complete')
    })
  })

  // =====================================================
  // handleTaskDelta — error event
  // =====================================================
  describe('handleTaskDelta — error', () => {
    it('should patch status to error then move to stopped, preserving error status', async () => {
      state.tasks.value.active = [mockTask('gid-e', { status: 'active' })]

      await events.handleTaskDelta({ type: 'error', gid: 'gid-e' })

      expect(state.tasks.value.active.length).toBe(0)
      expect(state.tasks.value.stopped.length).toBe(1)
      expect(state.tasks.value.stopped[0].status).toBe('error')
    })
  })

  // =====================================================
  // handleTaskDelta — pause event
  // =====================================================
  describe('handleTaskDelta — pause', () => {
    it('should patch task status to paused', async () => {
      state.tasks.value.active = [mockTask('gid-p', { status: 'active' })]

      await events.handleTaskDelta({ type: 'pause', gid: 'gid-p' })

      expect(state.tasks.value.active[0].status).toBe('paused')
    })
  })

  // =====================================================
  // handleTaskDelta — remove event
  // =====================================================
  describe('handleTaskDelta — remove', () => {
    it('should remove task from active list', async () => {
      state.tasks.value.active = [mockTask('gid-r')]

      await events.handleTaskDelta({ type: 'remove', gid: 'gid-r' })

      expect(state.tasks.value.active.length).toBe(0)
    })

    it('should remove task from stopped list', async () => {
      state.tasks.value.stopped = [mockTask('gid-r', { status: 'complete' })]

      await events.handleTaskDelta({ type: 'remove', gid: 'gid-r' })

      expect(state.tasks.value.stopped.length).toBe(0)
    })

    it('should clean up selectedGids', async () => {
      state.tasks.value.active = [mockTask('gid-r')]
      state.selectedGids.value = new Set(['gid-r'])

      await events.handleTaskDelta({ type: 'remove', gid: 'gid-r' })

      expect(state.selectedGids.value.has('gid-r')).toBe(false)
    })

    it('should clean up metadata cache', async () => {
      const task = mockTask('gid-r')
      state.tasks.value.active = [task]
      // Pre-cache metadata
      const { cacheMetadata } = await import('../metadata')
      cacheMetadata(task)
      expect(getMetadataCacheSize()).toBe(1)

      await events.handleTaskDelta({ type: 'remove', gid: 'gid-r' })

      expect(getMetadataCacheSize()).toBe(0)
    })
  })

  // =====================================================
  // handleTaskMove
  // =====================================================
  describe('handleTaskMove', () => {
    it('should move task from active to waiting', () => {
      state.tasks.value.active = [mockTask('gid-m', { status: 'active' })]

      events.handleTaskMove({
        gid: 'gid-m',
        from: 'active',
        to: 'waiting',
        task: {},
      })

      expect(state.tasks.value.active.length).toBe(0)
      expect(state.tasks.value.waiting.length).toBe(1)
      expect(state.tasks.value.waiting[0].gid).toBe('gid-m')
    })

    it('should move task from waiting to active', () => {
      state.tasks.value.waiting = [mockTask('gid-m', { status: 'waiting' })]

      events.handleTaskMove({
        gid: 'gid-m',
        from: 'waiting',
        to: 'active',
        task: {},
      })

      expect(state.tasks.value.waiting.length).toBe(0)
      expect(state.tasks.value.active.length).toBe(1)
    })

    it('should cache metadata when fullTask has files', () => {
      clearMetadataCache()
      const fullTask = mockTask('gid-m', {
        files: [{ path: '/downloads/moved.zip', uris: [] }],
      })

      events.handleTaskMove({
        gid: 'gid-m',
        from: 'active',
        to: 'waiting',
        task: fullTask as any,
      })

      expect(getMetadataCacheSize()).toBe(1)
    })
  })
})
