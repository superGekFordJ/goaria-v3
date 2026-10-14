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
vi.mock('../../../../bindings/goaria-v3/internal/wailsapp/app.js', () => ({
  UpdateTrayState: vi.fn(),
}))

// Mock downloadGroups store to avoid Pinia setup in events tests
const downloadGroupMocks = vi.hoisted(() => ({
  scheduleAutoSyncImmediate: vi.fn(),
}))

vi.mock('../../downloadGroups', () => ({
  useDownloadGroupStore: () => downloadGroupMocks,
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
    setTaskMover: vi.fn(),
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

    it('should not add task if GID already exists in stopped (status-less duplicate)', async () => {
      state.tasks.value.stopped = [mockTask('gid-done', { status: 'complete' })]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-done',
        payload: { gid: 'gid-done', files: [], dir: '' },
      })

      expect(state.tasks.value.active.length).toBe(0)
      expect(state.tasks.value.stopped.length).toBe(1)
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('QUEUED-THEN-STARTED: a status-active add reopens a stopped row into active', async () => {
      state.tasks.value.stopped = [mockTask('gid-reopen', { status: 'error' })]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-reopen',
        payload: { gid: 'gid-reopen', status: 'active' },
      })

      expect(state.tasks.value.stopped).toHaveLength(0)
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['gid-reopen'])
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

      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-c',
        payload: { gid: 'gid-c', status: 'complete' },
      })

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
          status: 'complete',
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

    it('TERMINAL-MAP-NO-MOVE: status-less complete patches present data and fetches without moving', async () => {
      state.tasks.value.active = [
        mockTask('gid-map', { status: 'active', completedLength: '10' }),
      ]

      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-map',
        payload: { completedLength: '100', totalLength: '100' },
      })

      expect(actions.fetchTasks).toHaveBeenCalled()
      // Membership unchanged: the kind alone never moves a row to stopped.
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['gid-map'])
      expect(state.tasks.value.active[0].completedLength).toBe('100')
      expect(state.tasks.value.active[0].totalLength).toBe('100')
      expect(state.tasks.value.stopped).toHaveLength(0)
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
          status: 'complete',
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

      await events.handleTaskDelta({
        type: 'error',
        gid: 'gid-e',
        payload: { gid: 'gid-e', status: 'error' },
      })

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
          status: 'error',
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
    it('a status-less pause folds the live row into waiting as paused with zero speed', async () => {
      state.tasks.value.active = [
        mockTask('gid-p', { status: 'active', downloadSpeed: '500' }),
        mockTask('gid-neighbor', { status: 'active' }),
      ]

      await events.handleTaskDelta({ type: 'pause', gid: 'gid-p' })

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['gid-neighbor'])
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['gid-p'])
      expect(state.tasks.value.waiting[0].status).toBe('paused')
      expect(state.tasks.value.waiting[0].downloadSpeed).toBe('0')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('a status-less pause does not reopen a stopped-only row', async () => {
      state.tasks.value.stopped = [mockTask('gid-p', { status: 'complete' })]

      await events.handleTaskDelta({ type: 'pause', gid: 'gid-p' })

      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['gid-p'])
      expect(state.tasks.value.stopped[0].status).toBe('complete')
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('a status-less pause on an unknown GID falls back to fetchTasks', async () => {
      await events.handleTaskDelta({ type: 'pause', gid: 'gid-unknown' })

      expect(actions.fetchTasks).toHaveBeenCalled()
    })
  })

  // =====================================================
  // handleTaskDelta — resume event
  // =====================================================
  describe('handleTaskDelta — resume', () => {
    it('WIRE-ACTUAL-STATUS: a resume delta carrying status active moves waiting to active', async () => {
      state.tasks.value.waiting = [mockTask('gid-r', { status: 'paused' })]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-r',
        payload: { gid: 'gid-r', status: 'active' },
      })
      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-r',
        payload: { gid: 'gid-r', status: 'active' },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].gid).toBe('gid-r')
      expect(state.tasks.value.active[0].status).toBe('active')
      expect(state.tasks.value.waiting).toHaveLength(0)
    })

    it('WIRE-ACTUAL-STATUS: a resume delta carrying status waiting stays in waiting', async () => {
      state.tasks.value.waiting = [mockTask('gid-rw', { status: 'paused' })]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-rw',
        payload: { gid: 'gid-rw', status: 'waiting' },
      })

      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting).toHaveLength(1)
      expect(state.tasks.value.waiting[0].status).toBe('waiting')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('WIRE-ACTUAL-STATUS: status-less resume recovers via fetchTasks', async () => {
      state.tasks.value.waiting = [mockTask('gid-rs', { status: 'paused' })]

      await events.handleTaskDelta({ type: 'resume', gid: 'gid-rs' })

      expect(actions.fetchTasks).toHaveBeenCalled()
      expect(state.tasks.value.active).toHaveLength(0)
    })

    it('STALE-DEMOTION: a resume delta carrying waiting must not demote a live row', async () => {
      const task = mockTask('gid-stale-resume', { status: 'active' })
      state.tasks.value.active = [task]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-stale-resume',
        payload: { gid: 'gid-stale-resume', status: 'waiting', downloadSpeed: '0' },
      })

      expect(actions.fetchTasks).toHaveBeenCalled()
      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].status).toBe('active')
      expect(state.tasks.value.waiting).toHaveLength(0)
    })

    it('STALE-DEMOTION: a queued add delta must not demote a live row', async () => {
      const task = mockTask('gid-stale-add', { status: 'active' })
      state.tasks.value.active = [task]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-stale-add',
        payload: { gid: 'gid-stale-add', status: 'waiting', downloadSpeed: '0' },
      })

      expect(actions.fetchTasks).toHaveBeenCalled()
      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.waiting).toHaveLength(0)
    })

    it('should move an errored stopped task to active without losing group metadata', async () => {
      state.tasks.value.stopped = [
        mockTask('gid-error-resume', {
          status: 'error',
          errorCode: '9',
          errorMessage: 'disk full',
          completedLength: '420',
          download_group: mockGroup,
        }),
      ]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-error-resume',
        payload: { gid: 'gid-error-resume', status: 'active' },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].status).toBe('active')
      expect(state.tasks.value.active[0].errorCode).toBe('')
      expect(state.tasks.value.active[0].errorMessage).toBe('')
      expect(state.tasks.value.active[0].completedLength).toBe('420')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('should collapse a stale active and stopped duplicate', async () => {
      const activeTask = mockTask('gid-resume-duplicate', {
        status: 'active',
        completedLength: '600',
        files: [],
        dir: '',
        title: '',
      })
      state.tasks.value = {
        active: [activeTask],
        waiting: [],
        stopped: [
          mockTask('gid-resume-duplicate', {
            status: 'error',
            completedLength: '500',
            errorCode: '9',
            errorMessage: 'fail',
            title: 'From Stopped',
            files: [{ path: '/downloads/from-stopped.zip', uris: [] }],
            dir: '/downloads/stopped-dir',
            download_group: mockGroup,
          }),
        ],
      }
      const retained = state.tasks.value.active[0]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-resume-duplicate',
        payload: { gid: 'gid-resume-duplicate', status: 'active' },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0]).toBe(retained)
      expect(state.tasks.value.active[0].completedLength).toBe('600')
      expect(state.tasks.value.active[0].title).toBe('From Stopped')
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/from-stopped.zip')
      expect(state.tasks.value.active[0].dir).toBe('/downloads/stopped-dir')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.active[0].errorCode).toBe('')
      expect(state.tasks.value.active[0].errorMessage).toBe('')
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('should backfill stopped rich data onto waiting when both exist without active', async () => {
      state.tasks.value = {
        active: [],
        waiting: [
          mockTask('gid-wait-stop', {
            status: 'paused',
            files: [],
            dir: '',
            title: '',
          }),
        ],
        stopped: [
          mockTask('gid-wait-stop', {
            status: 'error',
            title: 'Stopped Title',
            files: [{ path: '/downloads/ws.zip', uris: [] }],
            dir: '/downloads/ws',
            download_group: mockGroup,
            errorCode: '9',
          }),
        ],
      }

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-wait-stop',
        payload: { gid: 'gid-wait-stop', status: 'active' },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].title).toBe('Stopped Title')
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/ws.zip')
      expect(state.tasks.value.active[0].dir).toBe('/downloads/ws')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.active[0].errorCode).toBe('')
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('should converge to a single active row for move-then-resume order', async () => {
      state.tasks.value.stopped = [
        mockTask('gid-order-mr', {
          status: 'error',
          errorCode: '9',
          download_group: mockGroup,
        }),
      ]

      events.handleTaskMove({
        gid: 'gid-order-mr',
        from: 'stopped',
        to: 'active',
        task: { gid: 'gid-order-mr', status: 'active' },
      })
      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-order-mr',
        payload: { gid: 'gid-order-mr', status: 'active' },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].gid).toBe('gid-order-mr')
      expect(state.tasks.value.active[0].status).toBe('active')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('should converge to a single active row for resume-then-move order', async () => {
      state.tasks.value.stopped = [
        mockTask('gid-order-rm', {
          status: 'error',
          errorCode: '9',
          files: [{ path: '/downloads/order-rm.zip', uris: [] }],
          download_group: mockGroup,
        }),
      ]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-order-rm',
        payload: { gid: 'gid-order-rm', status: 'active' },
      })
      events.handleTaskMove({
        gid: 'gid-order-rm',
        from: 'stopped',
        to: 'active',
        task: { gid: 'gid-order-rm', status: 'active', files: [], dir: '' },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].gid).toBe('gid-order-rm')
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/order-rm.zip')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('should not create a task for an unknown resume GID', async () => {
      await events.handleTaskDelta({ type: 'resume', gid: 'gid-unknown-resume' })

      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
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

    it('should purge all lists when from is wrong but the task exists elsewhere', () => {
      state.tasks.value = {
        active: [],
        waiting: [],
        stopped: [
          mockTask('gid-wrong-from', {
            status: 'error',
            errorCode: '9',
            download_group: mockGroup,
          }),
        ],
      }

      events.handleTaskMove({
        gid: 'gid-wrong-from',
        from: 'waiting',
        to: 'active',
        task: {
          gid: 'gid-wrong-from',
          status: 'active',
        },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].gid).toBe('gid-wrong-from')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('should merge rich stopped data into an existing Lite active dest row', () => {
      const liteActive = mockTask('gid-lite-dest', {
        status: 'active',
        files: [],
        dir: '',
        title: '',
        completedLength: '700',
      })
      state.tasks.value = {
        active: [liteActive],
        waiting: [],
        stopped: [
          mockTask('gid-lite-dest', {
            status: 'error',
            title: 'Rich Stopped',
            files: [{ path: '/downloads/rich-stopped.zip', uris: [] }],
            dir: '/downloads/rich',
            download_group: mockGroup,
            completedLength: '500',
          }),
        ],
      }
      const retained = state.tasks.value.active[0]

      events.handleTaskMove({
        gid: 'gid-lite-dest',
        from: 'waiting',
        to: 'active',
        task: { gid: 'gid-lite-dest', status: 'active', files: [], dir: '' },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0]).toBe(retained)
      expect(state.tasks.value.active[0].title).toBe('Rich Stopped')
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/rich-stopped.zip')
      expect(state.tasks.value.active[0].dir).toBe('/downloads/rich')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-events')
      expect(state.tasks.value.active[0].completedLength).toBe('700')
      expect(state.tasks.value.stopped).toHaveLength(0)
      expect(state.tasks.value.waiting).toHaveLength(0)
    })

    it('should apply payload status and cleared errors onto an existing dest row', () => {
      state.tasks.value = {
        active: [
          mockTask('gid-stale-terminal', {
            status: 'error',
            errorCode: '9',
            errorMessage: 'disk full',
            files: [{ path: '/downloads/stale.zip', uris: [] }],
          }),
        ],
        waiting: [],
        stopped: [],
      }
      const retained = state.tasks.value.active[0]

      events.handleTaskMove({
        gid: 'gid-stale-terminal',
        from: 'waiting',
        to: 'active',
        task: {
          gid: 'gid-stale-terminal',
          status: 'active',
          errorCode: '',
          errorMessage: '',
        },
      })

      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0]).toBe(retained)
      expect(state.tasks.value.active[0].status).toBe('active')
      expect(state.tasks.value.active[0].errorCode).toBe('')
      expect(state.tasks.value.active[0].errorMessage).toBe('')
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/stale.zip')
    })

    it('migrates to stopped on a terminal move', () => {
      state.tasks.value.waiting = [mockTask('gid-term', { status: 'paused' })]

      events.handleTaskMove({
        gid: 'gid-term',
        from: 'waiting',
        to: 'stopped',
        task: { gid: 'gid-term', status: 'complete' },
      })

      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['gid-term'])
    })
  })

  describe('immediate fold — no resume coordination', () => {
    it('a status-less resume delta recovers via fetchTasks and still schedules group auto-sync', async () => {
      state.tasks.value.waiting = [mockTask('gid-statusless', { status: 'paused' })]

      await events.handleTaskDelta({ type: 'resume', gid: 'gid-statusless' })

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['gid-statusless'])
      expect(state.tasks.value.active).toHaveLength(0)
      expect(actions.fetchTasks).toHaveBeenCalled()
      expect(downloadGroupMocks.scheduleAutoSyncImmediate).toHaveBeenCalledWith('resume-delta')
    })

    it('a status-bearing resume delta folds waiting to active immediately', async () => {
      state.tasks.value.waiting = [mockTask('gid-ev', { status: 'paused' })]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-ev',
        payload: { gid: 'gid-ev', status: 'active' },
      })

      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['gid-ev'])
      expect(state.tasks.value.active[0].status).toBe('active')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })
  })

  // =====================================================
  // Authoritative status routing — delta-only transitions
  // =====================================================
  describe('status routing', () => {
    it('DELTA-ONLY-FLIP: same-list paused↔waiting flip keeps the row in place', async () => {
      state.tasks.value.waiting = [
        mockTask('gid-flip', { status: 'paused' }),
        mockTask('gid-other', { status: 'paused' }),
      ]
      const row = state.tasks.value.waiting[0]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-flip',
        payload: { gid: 'gid-flip', status: 'waiting' },
      })

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['gid-flip', 'gid-other'])
      expect(state.tasks.value.waiting[0]).toBe(row)
      expect(state.tasks.value.waiting[0].status).toBe('waiting')
      expect(state.tasks.value.active).toHaveLength(0)
      expect(actions.fetchTasks).not.toHaveBeenCalled()

      await events.handleTaskDelta({
        type: 'pause',
        gid: 'gid-flip',
        payload: { gid: 'gid-flip', status: 'paused' },
      })
      expect(state.tasks.value.waiting[0]).toBe(row)
      expect(state.tasks.value.waiting[0].status).toBe('paused')
    })

    it('DELTA-ONLY-FLIP: a delta-only flip reopens a stopped row into waiting without a move event', async () => {
      state.tasks.value.stopped = [
        mockTask('gid-delta-reopen', {
          status: 'error',
          errorCode: '4',
          errorMessage: 'gone',
          download_group: mockGroup,
        }),
      ]

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-delta-reopen',
        payload: { gid: 'gid-delta-reopen', status: 'waiting' },
      })

      expect(state.tasks.value.stopped).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['gid-delta-reopen'])
      expect(state.tasks.value.waiting[0].status).toBe('waiting')
      expect(state.tasks.value.waiting[0].download_group?.id).toBe('dg-events')
    })

    it('QUEUED-THEN-STARTED: queued add lands in waiting, started add lands in active', async () => {
      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-queued',
        payload: mockTask('gid-queued', {
          status: 'waiting',
          files: [{ path: '/downloads/queued.zip', uris: [] }],
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['gid-queued'])
      expect(state.tasks.value.waiting[0].status).toBe('waiting')

      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-queued',
        payload: { gid: 'gid-queued', status: 'active' },
      })

      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['gid-queued'])
    })

    it('WIRE-ACTUAL-STATUS: unknown status values recover via fetchTasks instead of defaulting to stopped', async () => {
      state.tasks.value.active = [mockTask('gid-weird', { status: 'active' })]

      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-weird',
        payload: { gid: 'gid-weird', status: 'seeding' } as never,
      })

      // 'seeding' is not a recognized status → treated like a status-less
      // terminal: present fields patched, guarded read issued, no move.
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['gid-weird'])
      expect(state.tasks.value.stopped).toHaveLength(0)
      expect(actions.fetchTasks).toHaveBeenCalled()
    })

    it('FAST-TERMINAL-FULL: a waiting row that completes fast lands in stopped with full terminal metadata', async () => {
      state.tasks.value.waiting = [
        mockTask('gid-fast', {
          status: 'waiting',
          files: [],
          dir: '',
          title: '',
          completedLength: '10',
          totalLength: '0',
        }),
      ]

      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-fast',
        payload: mockTask('gid-fast', {
          status: 'complete',
          title: 'Fast Title',
          files: [
            {
              path: '/downloads/fast.bin',
              uris: [{ uri: 'https://example.com/fast.bin', status: 'used' }],
            },
          ],
          dir: '/downloads/fast',
          totalLength: '2048',
          completedLength: '2048',
          downloadSpeed: '0',
          download_group: mockGroup,
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(1)
      const row = state.tasks.value.stopped[0]
      expect(row.status).toBe('complete')
      expect(row.title).toBe('Fast Title')
      expect(row.dir).toBe('/downloads/fast')
      expect(row.files[0].path).toBe('/downloads/fast.bin')
      expect(row.totalLength).toBe('2048')
      expect(row.completedLength).toBe('2048')
      expect(row.downloadSpeed).toBe('0')
      expect(row.download_group?.id).toBe('dg-events')
    })

    it('FAST-TERMINAL-FULL: an unknown-row typed complete creates a rich stopped row, not an empty placeholder', async () => {
      await events.handleTaskDelta({
        type: 'complete',
        gid: 'gid-ghost-complete',
        payload: mockTask('gid-ghost-complete', {
          status: 'complete',
          files: [{ path: '/downloads/ghost.bin', uris: [] }],
          dir: '/downloads',
          totalLength: '512',
          completedLength: '512',
        }) as unknown as Record<string, unknown>,
      })

      expect(state.tasks.value.stopped).toHaveLength(1)
      const row = state.tasks.value.stopped[0]
      expect(row.gid).toBe('gid-ghost-complete')
      expect(row.status).toBe('complete')
      expect(row.files[0].path).toBe('/downloads/ghost.bin')
      expect(row.completedLength).toBe('512')
      expect(actions.fetchTasks).not.toHaveBeenCalled()
    })

    it('FULL-MOVE-TWIN-FOLD: delta-then-move twins converge to exactly one row', async () => {
      state.tasks.value.waiting = [mockTask('gid-twin', { status: 'paused' })]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-twin',
        payload: { gid: 'gid-twin', status: 'active' },
      })
      // The delta fold settled the row; no-op twins keep tasks.value identity.
      const settled = state.tasks.value
      events.handleTaskMove({
        gid: 'gid-twin',
        from: 'waiting',
        to: 'active',
        task: { gid: 'gid-twin', status: 'active', files: [], dir: '' },
      })
      // Duplicate delivery of the same move is a no-op.
      events.handleTaskMove({
        gid: 'gid-twin',
        from: 'waiting',
        to: 'active',
        task: { gid: 'gid-twin', status: 'active', files: [], dir: '' },
      })

      expect(state.tasks.value).toBe(settled)
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['gid-twin'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('FULL-MOVE-TWIN-FOLD: move-then-delta twins converge to exactly one row', async () => {
      state.tasks.value.waiting = [mockTask('gid-twin-md', { status: 'paused' })]

      events.handleTaskMove({
        gid: 'gid-twin-md',
        from: 'waiting',
        to: 'active',
        task: { gid: 'gid-twin-md', status: 'active', files: [], dir: '' },
      })
      const movedRow = state.tasks.value.active[0]
      const settled = state.tasks.value
      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-twin-md',
        payload: { gid: 'gid-twin-md', status: 'active' },
      })
      // Duplicate delivery of the same delta is a no-op.
      await events.handleTaskDelta({
        type: 'resume',
        gid: 'gid-twin-md',
        payload: { gid: 'gid-twin-md', status: 'active' },
      })

      // Identical delta twins are no-ops: no row rebuild, no list churn.
      expect(state.tasks.value).toBe(settled)
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['gid-twin-md'])
      // The delta twin folds into the row the move created, not a rebuild.
      expect(state.tasks.value.active[0]).toBe(movedRow)
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('IDENTITY-HOT-PATH: progress bursts patch in place and identical deltas are no-ops', async () => {
      state.tasks.value.active = [
        mockTask('gid-burst', {
          status: 'active',
          completedLength: '0',
          downloadSpeed: '0',
        }),
        mockTask('gid-neighbor', { status: 'active' }),
      ]
      const task = state.tasks.value.active[0]
      const neighbor = state.tasks.value.active[1]
      const activeList = state.tasks.value.active
      const files = task.files

      for (let i = 1; i <= 10; i++) {
        const payload = { completedLength: String(i * 100), downloadSpeed: '999' }
        const before = state.tasks.value
        await events.handleTaskDelta({ type: 'progress', gid: 'gid-burst', payload })
        // A changed delta shallow-patches: new tasks object, same rows/arrays.
        expect(state.tasks.value).not.toBe(before)
        const settled = state.tasks.value
        // A byte-identical redelivery is a no-op: tasks.value keeps identity.
        await events.handleTaskDelta({ type: 'progress', gid: 'gid-burst', payload })
        expect(state.tasks.value).toBe(settled)
      }

      expect(state.tasks.value.active).toBe(activeList)
      expect(state.tasks.value.active).toHaveLength(2)
      expect(state.tasks.value.active[0]).toBe(task)
      expect(state.tasks.value.active[0].files).toBe(files)
      expect(state.tasks.value.active[0].completedLength).toBe('1000')
      expect(state.tasks.value.active[0].downloadSpeed).toBe('999')
      expect(state.tasks.value.active[1]).toBe(neighbor)
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
      expect(actions.fetchTasks).not.toHaveBeenCalled()
      expect(actions.queueMetadataRecovery).not.toHaveBeenCalled()
    })
  })
})
