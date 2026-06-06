import { describe, it, expect, beforeEach, vi } from 'vitest'
import { ref, computed } from 'vue'
import { setupEvents } from '../events'
import { clearMetadataCache, getMetadataCacheSize } from '../metadata'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'
import type { TaskState } from '../state'
import type { TaskActions } from '../actions'
import type { TaskPolling } from '../polling'

const mockGroup = {
  id: 'dg-events',
  kind: 'batch',
  name: 'Batch 2026-05-07 dg-events',
  folder_name: 'Batch 2026-05-07 dg-events',
  dir: '/downloads/Batch 2026-05-07 dg-events',
  item_count: 5,
  created_at: 1770000000,
}

// Mock Wails bindings
vi.mock('../../../../bindings/goaria-v3/app.js', () => ({
  UpdateTrayState: vi.fn(),
}))

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
  const selectedGroupKeys = ref<Set<string>>(new Set())
  const activeTasks = computed(() => tasks.value.active || [])
  const waitingTasks = computed(() => tasks.value.waiting || [])
  const stoppedTasks = computed(() => tasks.value.stopped || [])
  const allUris = computed(() => {
    const uris = new Set<string>()
    for (const list of [activeTasks.value, waitingTasks.value, stoppedTasks.value]) {
      for (const task of list) {
        for (const file of task.files || []) {
          for (const uri of file.uris || []) {
            if (uri?.uri) uris.add(uri.uri)
          }
        }
      }
    }
    return uris
  })

  return {
    tasks,
    selectedGids,
    selectedGroupKeys,
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
    metadataInFlight: vi.fn().mockReturnValue(false),
    setMetadataInFlight: vi.fn(),
    queueMetadataRecovery: vi.fn(),
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
      const task = mockTask('gid-1', {
        completedLength: '100',
        downloadSpeed: '50',
        totalLength: '1000',
      })
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

      await events.handleTaskDelta({
        type: 'progress',
        gid: 'gid-1',
        payload: { completedLength: '200' },
      })

      expect(actions.queueMetadataRecovery).toHaveBeenCalledWith('gid-1')
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

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-new',
        payload: newTask as unknown as Record<string, unknown>,
      })

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

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-new',
        payload: incompleteTask as unknown as Record<string, unknown>,
      })

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

    it('should merge explicit backend-provided group metadata into an existing active task without duplicating or losing rich data', async () => {
      state.tasks.value.active = [
        mockTask('gid-explicit-group', {
          files: [{ path: '/downloads/rich-explicit.iso', uris: [] }],
          dir: '/downloads/rich-dir',
          totalLength: '9000',
          completedLength: '3000',
          downloadSpeed: '777',
          download_group: undefined,
        }),
      ]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-explicit-group',
        payload: {
          gid: 'gid-explicit-group',
          status: 'active',
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
          download_group: mockGroup,
        },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/rich-explicit.iso')
      expect(state.tasks.value.active[0].dir).toBe('/downloads/rich-dir')
      expect(state.tasks.value.active[0].totalLength).toBe('9000')
      expect(state.tasks.value.active[0].completedLength).toBe('3000')
      expect(state.tasks.value.active[0].downloadSpeed).toBe('777')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('should merge explicit backend-provided group metadata into an existing waiting task without duplicating it', async () => {
      state.tasks.value.waiting = [
        mockTask('gid-explicit-waiting-group', {
          status: 'waiting',
          files: [{ path: '/downloads/waiting-explicit.iso', uris: [] }],
          dir: '/downloads/waiting-dir',
          totalLength: '5000',
          completedLength: '1000',
          downloadSpeed: '0',
          download_group: undefined,
        }),
      ]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-explicit-waiting-group',
        payload: {
          gid: 'gid-explicit-waiting-group',
          status: 'waiting',
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
          download_group: mockGroup,
        },
      })

      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting).toHaveLength(1)
      expect(state.tasks.value.waiting[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.waiting[0].files[0].path).toBe('/downloads/waiting-explicit.iso')
      expect(state.tasks.value.waiting[0].dir).toBe('/downloads/waiting-dir')
      expect(state.tasks.value.waiting[0].totalLength).toBe('5000')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
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

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-w',
        payload: newTask as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.waiting.length).toBe(0)
    })

    it('should enrich an existing Lite active task from a later full add payload', async () => {
      state.tasks.value.active = [
        mockTask('gid-lite-active', {
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        }),
      ]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-lite-active',
        payload: mockTask('gid-lite-active', {
          files: [{ path: '/downloads/resolved-active.zip', uris: [] }],
          dir: '/downloads',
          totalLength: '4096',
          completedLength: '1024',
          downloadSpeed: '256',
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/resolved-active.zip')
      expect(state.tasks.value.active[0].dir).toBe('/downloads')
      expect(state.tasks.value.active[0].totalLength).toBe('4096')
      expect(state.tasks.value.active[0].completedLength).toBe('1024')
      expect(state.tasks.value.active[0].downloadSpeed).toBe('256')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('should enrich an existing waiting task and move it to active without duplicates', async () => {
      state.tasks.value.waiting = [
        mockTask('gid-lite-waiting', {
          status: 'waiting',
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        }),
      ]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-lite-waiting',
        payload: mockTask('gid-lite-waiting', {
          status: 'active',
          files: [{ path: '/downloads/resolved-waiting.zip', uris: [] }],
          dir: '/downloads',
          totalLength: '8192',
          completedLength: '2048',
          downloadSpeed: '512',
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.active[0].gid).toBe('gid-lite-waiting')
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/resolved-waiting.zip')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('should not let a sparse add payload overwrite richer existing metadata or speed', async () => {
      state.tasks.value.active = [
        mockTask('gid-rich', {
          files: [{ path: '/downloads/rich.iso', uris: [] }],
          dir: '/downloads/rich',
          totalLength: '9000',
          completedLength: '3000',
          downloadSpeed: '333',
          download_group: mockGroup,
        }),
      ]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-rich',
        payload: {
          gid: 'gid-rich',
          status: 'active',
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/rich.iso')
      expect(state.tasks.value.active[0].dir).toBe('/downloads/rich')
      expect(state.tasks.value.active[0].totalLength).toBe('9000')
      expect(state.tasks.value.active[0].completedLength).toBe('3000')
      expect(state.tasks.value.active[0].downloadSpeed).toBe('333')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('should queue metadata recovery for a sparse add payload targeting an existing Lite active task', async () => {
      state.tasks.value.active = [
        mockTask('gid-lite-sparse', {
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        }),
      ]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-lite-sparse',
        payload: {
          gid: 'gid-lite-sparse',
          status: 'active',
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].gid).toBe('gid-lite-sparse')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
      expect(actions.queueMetadataRecovery).toHaveBeenCalledWith('gid-lite-sparse')
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
          downloadSpeed: '0',
        },
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

    it('should apply payload updates to already-stopped task from a later complete payload', async () => {
      state.tasks.value.stopped = [
        mockTask('gid-s', {
          status: 'complete',
          completedLength: '0',
          totalLength: '0',
        }),
      ]

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

    it('should merge group metadata from already-stopped complete payload', async () => {
      state.tasks.value.stopped = [
        mockTask('gid-stopped-group', {
          status: 'complete',
          download_group: undefined,
        }),
      ]

      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-stopped-group',
        payload: mockTask('gid-stopped-group', {
          status: 'complete',
          download_group: mockGroup,
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.stopped).toHaveLength(1)
      expect(state.tasks.value.stopped[0].download_group?.id).toBe('dg-events')
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
      state.tasks.value.active = [
        mockTask('gid-e2', { status: 'active', errorCode: '', errorMessage: '' }),
      ]

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

    it('should merge group metadata from already-stopped error payload', async () => {
      state.tasks.value.stopped = [
        mockTask('gid-error-group', {
          status: 'error',
          download_group: undefined,
        }),
      ]

      await events.handleTaskDelta({
        type: 'error',
        gid: 'gid-error-group',
        payload: mockTask('gid-error-group', {
          status: 'error',
          errorCode: '1',
          errorMessage: 'Network error',
          download_group: mockGroup,
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.stopped).toHaveLength(1)
      expect(state.tasks.value.stopped[0].download_group?.id).toBe('dg-events')
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

    it('should preserve richer data when a resume move payload is sparse or stale', () => {
      state.tasks.value.waiting = [
        mockTask('gid-resume', {
          status: 'waiting',
          files: [{ path: '/downloads/resume.iso', uris: [] }],
          dir: '/downloads/resume',
          totalLength: '9000',
          completedLength: '4500',
          downloadSpeed: '888',
          download_group: mockGroup,
        }),
      ]

      events.handleTaskMove({
        gid: 'gid-resume',
        from: 'waiting',
        to: 'active',
        task: {
          gid: 'gid-resume',
          status: 'active',
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        },
      })

      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/resume.iso')
      expect(state.tasks.value.active[0].dir).toBe('/downloads/resume')
      expect(state.tasks.value.active[0].totalLength).toBe('9000')
      expect(state.tasks.value.active[0].completedLength).toBe('4500')
      expect(state.tasks.value.active[0].downloadSpeed).toBe('888')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
    })

    it('should preserve richer metadata when a pause move payload is sparse', () => {
      state.tasks.value.active = [
        mockTask('gid-pause', {
          status: 'active',
          files: [{ path: '/downloads/pause.iso', uris: [] }],
          dir: '/downloads/pause',
          totalLength: '7000',
          completedLength: '3500',
          downloadSpeed: '444',
        }),
      ]

      events.handleTaskMove({
        gid: 'gid-pause',
        from: 'active',
        to: 'waiting',
        task: {
          gid: 'gid-pause',
          status: 'paused',
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        },
      })

      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting).toHaveLength(1)
      expect(state.tasks.value.waiting[0].files[0].path).toBe('/downloads/pause.iso')
      expect(state.tasks.value.waiting[0].dir).toBe('/downloads/pause')
      expect(state.tasks.value.waiting[0].totalLength).toBe('7000')
      expect(state.tasks.value.waiting[0].completedLength).toBe('3500')
    })

    it('should accept genuinely richer payload metadata for a moved Lite task', () => {
      clearMetadataCache()
      state.tasks.value.waiting = [
        mockTask('gid-rich-move', {
          status: 'waiting',
          files: [],
          dir: '',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        }),
      ]

      events.handleTaskMove({
        gid: 'gid-rich-move',
        from: 'waiting',
        to: 'active',
        task: mockTask('gid-rich-move', {
          status: 'active',
          files: [{ path: '/downloads/rich-move.zip', uris: [] }],
          dir: '/downloads',
          totalLength: '777',
          completedLength: '123',
          downloadSpeed: '456',
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/rich-move.zip')
      expect(state.tasks.value.active[0].dir).toBe('/downloads')
      expect(state.tasks.value.active[0].totalLength).toBe('777')
      expect(state.tasks.value.active[0].completedLength).toBe('123')
      expect(state.tasks.value.active[0].downloadSpeed).toBe('456')
      expect(getMetadataCacheSize()).toBe(1)
    })
  })
})
