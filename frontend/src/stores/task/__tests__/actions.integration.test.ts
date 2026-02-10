import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setupState } from '../state'
import { setupActions } from '../actions'
import { clearMetadataCache } from '../metadata'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

// Mock Wails bindings
vi.mock('../../../../bindings/goaria-v3/app.js', () => ({
  GetTasks: vi.fn(),
  GetActiveTasks: vi.fn(),
  GetStoppedTasks: vi.fn(),
  GetTaskMetadata: vi.fn(),
  AddUri: vi.fn(),
  PauseTask: vi.fn(),
  ResumeTask: vi.fn(),
  RemoveTask: vi.fn(),
  OpenFolder: vi.fn(),
  BatchPause: vi.fn(),
  BatchResume: vi.fn(),
  BatchRemove: vi.fn(),
  GetFullSnapshot: vi.fn(),
  MinimizeToTray: vi.fn(),
  UpdateTrayState: vi.fn(),
}))

import {
  GetTasks,
  GetActiveTasks,
  GetStoppedTasks,
  GetTaskMetadata,
} from '../../../../bindings/goaria-v3/app.js'

const mockGetActiveTasks = vi.mocked(GetActiveTasks)
const mockGetStoppedTasks = vi.mocked(GetStoppedTasks)
const mockGetTasks = vi.mocked(GetTasks)
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

// --- Tests ---

describe('setupActions — integration', () => {
  let state: ReturnType<typeof setupState>
  let actions: ReturnType<typeof setupActions>
  let stopPollingCalled: boolean

  beforeEach(() => {
    vi.clearAllMocks()
    clearMetadataCache()
    state = setupState()
    actions = setupActions(state)
    stopPollingCalled = false

    // Wire up polling callbacks
    actions.setPollingCallbacks(
      () => {}, // restart
      (_disableContext: boolean) => { stopPollingCalled = true }, // stop
    )
  })

  // =====================================================
  // fetchActiveTasks
  // =====================================================
  describe('fetchActiveTasks', () => {
    it('should merge active and waiting tasks from GetActiveTasks', async () => {
      mockGetActiveTasks.mockResolvedValue({
        active: [mockTask('a1'), mockTask('a2')],
        waiting: [mockTask('w1')],
      } as unknown as { active: Task[]; waiting: Task[] })

      const result = await actions.fetchActiveTasks()

      expect(state.tasks.value.active.length).toBe(2)
      expect(state.tasks.value.waiting.length).toBe(1)
      expect(result.hasActiveTasks).toBe(true)
    })

    it('should deduplicate tasks already in stopped list', async () => {
      state.tasks.value.stopped = [mockTask('a1', { status: 'complete' })]

      mockGetActiveTasks.mockResolvedValue({
        active: [mockTask('a1'), mockTask('a2')],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })

      await actions.fetchActiveTasks()

      // a1 is in stopped, so it should be filtered out from active
      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.active[0].gid).toBe('a2')
    })

    it('should trigger metadata fetch for tasks missing files[0].path', async () => {
      mockGetActiveTasks.mockResolvedValue({
        active: [mockTask('a1', { files: [] })],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })
      mockGetTaskMetadata.mockResolvedValue({} as Record<string, Task>)

      await actions.fetchActiveTasks()

      // GetTaskMetadata should be called for the task missing metadata
      expect(mockGetTaskMetadata).toHaveBeenCalled()
    })

    it('should handle errors and increment consecutiveErrors', async () => {
      mockGetActiveTasks.mockRejectedValue(new Error('Network error'))

      const result = await actions.fetchActiveTasks()

      expect(result.hasActiveTasks).toBe(false)
      expect(state.consecutiveErrors.value).toBe(1)
    })

    it('should reset consecutiveErrors on success', async () => {
      state.consecutiveErrors.value = 2
      mockGetActiveTasks.mockResolvedValue({ active: [], waiting: [] } as unknown as { active: Task[]; waiting: Task[] })

      await actions.fetchActiveTasks()

      expect(state.consecutiveErrors.value).toBe(0)
    })
  })

  // =====================================================
  // fetchStoppedTasks
  // =====================================================
  describe('fetchStoppedTasks', () => {
    it('should merge stopped tasks from GetStoppedTasks', async () => {
      mockGetStoppedTasks.mockResolvedValue([
        mockTask('s1', { status: 'complete' }),
        mockTask('s2', { status: 'error' }),
      ] as Task[])

      await actions.fetchStoppedTasks()

      expect(state.tasks.value.stopped.length).toBe(2)
    })

    it('should not include active/waiting GIDs in stopped list', async () => {
      state.tasks.value.active = [mockTask('overlap')]
      mockGetStoppedTasks.mockResolvedValue([
        mockTask('overlap', { status: 'complete' }),
        mockTask('s1', { status: 'complete' }),
      ] as Task[])

      await actions.fetchStoppedTasks()

      const stoppedGids = state.tasks.value.stopped.map(t => t.gid)
      expect(stoppedGids).not.toContain('overlap')
      expect(stoppedGids).toContain('s1')
    })
  })

  // =====================================================
  // fetchTasks
  // =====================================================
  describe('fetchTasks', () => {
    it('should update all three lists from GetTasks', async () => {
      mockGetTasks.mockResolvedValue({
        active: [mockTask('a1')],
        waiting: [mockTask('w1')],
        stopped: [mockTask('s1', { status: 'complete' })],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      await actions.fetchTasks()

      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.waiting.length).toBe(1)
      expect(state.tasks.value.stopped.length).toBe(1)
    })

    it('should apply metadata from cache', async () => {
      mockGetTasks.mockResolvedValue({
        active: [mockTask('a1', { files: [{ path: '/downloads/test.zip', uris: [] }] })],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      await actions.fetchTasks()

      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/test.zip')
    })

    it('should reset consecutiveErrors on success', async () => {
      state.consecutiveErrors.value = 2
      mockGetTasks.mockResolvedValue({
        active: [],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      await actions.fetchTasks()

      expect(state.consecutiveErrors.value).toBe(0)
    })
  })

  // =====================================================
  // Error circuit breaker
  // =====================================================
  describe('error circuit breaker', () => {
    it('should call _stopPollingCallback after 3 consecutive errors', async () => {
      mockGetActiveTasks.mockRejectedValue(new Error('fail'))

      await actions.fetchActiveTasks()
      expect(stopPollingCalled).toBe(false)
      expect(state.consecutiveErrors.value).toBe(1)

      await actions.fetchActiveTasks()
      expect(stopPollingCalled).toBe(false)
      expect(state.consecutiveErrors.value).toBe(2)

      await actions.fetchActiveTasks()
      expect(stopPollingCalled).toBe(true)
      expect(state.consecutiveErrors.value).toBe(3)
    })
  })
})
