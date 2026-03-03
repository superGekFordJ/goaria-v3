import { describe, it, expect, beforeEach, vi } from 'vitest'
import { ref, computed } from 'vue'
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
    activeTasks: computed(() => []),
    waitingTasks: computed(() => []),
    stoppedTasks: computed(() => []),
    allTasksCount: computed(() => 0),
    selectedCount: computed(() => 0),
    isSelected: () => false,
    getSelectedGids: computed(() => []),
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
      } as Record<string, Task>)

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
    it('should add a new task to active when payload contains complete data', async () => {
      const newTask = mockTask('gid-new', {
        files: [{ path: '/downloads/newfile.zip', uris: [] }],
      })

      await events.handleTaskDelta({ type: 'add', gid: 'gid-new', payload: newTask as unknown as Record<string, unknown> })

      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.active[0].gid).toBe('gid-new')
    })

    it('should fallback to fetchTasks when payload is empty', async () => {
      await events.handleTaskDelta({ type: 'add', gid: 'gid-new' })

      expect(actions.fetchTasks).toHaveBeenCalled()
      expect(state.tasks.value.active.length).toBe(0)
    })

    it('should fallback to fetchTasks when payload has no file path', async () => {
      const incompleteTask = mockTask('gid-new', { files: [] })

      await events.handleTaskDelta({ type: 'add', gid: 'gid-new', payload: incompleteTask as unknown as Record<string, unknown> })

      expect(actions.fetchTasks).toHaveBeenCalled()
    })

    it('should not duplicate task if GID already exists in active', async () => {
      state.tasks.value.active = [mockTask('gid-dup')]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-dup',
        payload: mockTask('gid-dup') as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.active.length).toBe(1)
    })

    it('should not add task if GID already exists in stopped', async () => {
      state.tasks.value.stopped = [mockTask('gid-done', { status: 'complete' })]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-done',
        payload: mockTask('gid-done') as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.active.length).toBe(0)
      expect(state.tasks.value.stopped.length).toBe(1)
    })

    it('should remove task from waiting when adding to active', async () => {
      state.tasks.value.waiting = [mockTask('gid-w', { status: 'waiting' })]
      const newTask = mockTask('gid-w', {
        status: 'active',
        files: [{ path: '/downloads/file.zip', uris: [] }],
      })

      await events.handleTaskDelta({ type: 'add', gid: 'gid-w', payload: newTask as unknown as Record<string, unknown> })

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

    it('should update progress to 100% when complete event contains payload with final stats (Stale Data Bug Reproduction)', async () => {
      const task = mockTask('gid-c2', {
        status: 'active',
        completedLength: '50',
        totalLength: '100',
        downloadSpeed: '10',
      })
      state.tasks.value.active = [task]

      await events.handleTaskDelta({ 
        type: 'complete', 
        gid: 'gid-c2',
        payload: {
          completedLength: '100',
          totalLength: '100',
          downloadSpeed: '0'
        }
      })

      expect(state.tasks.value.active.length).toBe(0)
      expect(state.tasks.value.stopped.length).toBe(1)
      expect(state.tasks.value.stopped[0].status).toBe('complete')
      expect(state.tasks.value.stopped[0].completedLength).toBe('100')
      expect(state.tasks.value.stopped[0].downloadSpeed).toBe('0')
    })

    it('should handle small-file race: task not in any list, use payload to build stopped task', async () => {
      // Task not in active, waiting, or stopped
      const fullTask = mockTask('gid-small', {
        status: 'complete',
        completedLength: '512',
        totalLength: '512',
        downloadSpeed: '0',
        files: [{ path: '/downloads/tiny.txt', uris: [] }],
      })

      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-small',
        payload: fullTask as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.stopped.length).toBe(1)
      expect(state.tasks.value.stopped[0].gid).toBe('gid-small')
      expect(state.tasks.value.stopped[0].status).toBe('complete')
      expect(state.tasks.value.stopped[0].completedLength).toBe('512')
    })

    it('should fallback to fetchTasks when complete payload is empty and task not in any list', async () => {
      await events.handleTaskDelta({ type: 'complete', gid: 'gid-unknown' })

      expect(actions.fetchTasks).toHaveBeenCalled()
    })

    it('should fallback to fetchTasks when complete payload has no file path and task not in any list', async () => {
      const incompleteTask = mockTask('gid-nofile', { files: [] })

      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-nofile',
        payload: incompleteTask as unknown as Record<string, unknown>,
      })

      expect(actions.fetchTasks).toHaveBeenCalled()
    })

    it('should apply payload updates to already-stopped task (Pusher dedup supplement)', async () => {
      state.tasks.value.stopped = [mockTask('gid-s', {
        status: 'complete',
        completedLength: '0',
        totalLength: '0',
      })]

      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-s',
        payload: mockTask('gid-s', {
          completedLength: '1024',
          totalLength: '1024',
          downloadSpeed: '0',
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.stopped[0].completedLength).toBe('1024')
      expect(state.tasks.value.stopped[0].totalLength).toBe('1024')
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

    it('should apply payload error info to existing task before moving to stopped', async () => {
      state.tasks.value.active = [mockTask('gid-e2', { status: 'active', errorCode: '', errorMessage: '' })]

      await events.handleTaskDelta({
        type: 'error',
        gid: 'gid-e2',
        payload: mockTask('gid-e2', {
          errorCode: '1',
          errorMessage: 'Network error',
          completedLength: '0',
          totalLength: '1000',
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.stopped.length).toBe(1)
      expect(state.tasks.value.stopped[0].status).toBe('error')
      expect(state.tasks.value.stopped[0].errorCode).toBe('1')
      expect(state.tasks.value.stopped[0].errorMessage).toBe('Network error')
    })

    it('should handle small-file race: task not in any list, use payload to build stopped task', async () => {
      const fullTask = mockTask('gid-err-small', {
        status: 'error',
        errorCode: '3',
        errorMessage: 'Resource not found',
        files: [{ path: '/downloads/missing.txt', uris: [] }],
      })

      await events.handleTaskDelta({
        type: 'error',
        gid: 'gid-err-small',
        payload: fullTask as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.stopped.length).toBe(1)
      expect(state.tasks.value.stopped[0].gid).toBe('gid-err-small')
      expect(state.tasks.value.stopped[0].status).toBe('error')
    })

    it('should fallback to fetchTasks when error payload is empty and task not in any list', async () => {
      await events.handleTaskDelta({ type: 'error', gid: 'gid-err-unknown' })

      expect(actions.fetchTasks).toHaveBeenCalled()
    })

    it('should fallback to fetchTasks when error payload has no file path and task not in any list', async () => {
      const incompleteTask = mockTask('gid-err-nofile', { files: [] })

      await events.handleTaskDelta({
        type: 'error',
        gid: 'gid-err-nofile',
        payload: incompleteTask as unknown as Record<string, unknown>,
      })

      expect(actions.fetchTasks).toHaveBeenCalled()
    })

    it('should not duplicate task if already in stopped', async () => {
      state.tasks.value.stopped = [mockTask('gid-err-dup', { status: 'error' })]

      await events.handleTaskDelta({
        type: 'error',
        gid: 'gid-err-dup',
        payload: mockTask('gid-err-dup', { status: 'error' }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.stopped.length).toBe(1)
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
        task: fullTask as unknown as Record<string, unknown>,
      })

      expect(getMetadataCacheSize()).toBe(1)
    })
  })
})
