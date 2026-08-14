import { describe, it, expect, beforeEach, vi } from 'vitest'
import { computed, defineComponent, nextTick, watch } from 'vue'
import { mount } from '@vue/test-utils'
import type { CancellablePromise } from '@wailsio/runtime'
import { setupState } from '../state'
import { setupActions } from '../actions'
import { setupEvents } from '../events'
import type { TaskPolling } from '../polling'
import { clearMetadataCache } from '../metadata'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

const mockGroup = {
  id: 'dg-actions',
  kind: 'batch',
  name: 'Batch 2026-05-07 dg-actions',
  folder_name: 'Batch 2026-05-07 dg-actions',
  dir: '/downloads/Batch 2026-05-07 dg-actions',
  item_count: 5,
  created_at: 1770000000,
}

// Mock Wails bindings
vi.mock('../../../../bindings/goaria-v3/internal/wailsapp/app.js', () => ({
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
  AddUri,
  ResumeTask,
  BatchResume,
  GetFullSnapshot,
} from '../../../../bindings/goaria-v3/internal/wailsapp/app.js'

const mockGetActiveTasks = vi.mocked(GetActiveTasks)
const mockGetStoppedTasks = vi.mocked(GetStoppedTasks)
const mockGetTasks = vi.mocked(GetTasks)
const mockGetTaskMetadata = vi.mocked(GetTaskMetadata)
const mockAddUri = vi.mocked(AddUri)
const mockResumeTask = vi.mocked(ResumeTask)
const mockBatchResume = vi.mocked(BatchResume)
const mockGetFullSnapshot = vi.mocked(GetFullSnapshot)

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

function flushPromises() {
  return new Promise<void>(resolve => setTimeout(resolve, 0))
}

function activeOrder(state: ReturnType<typeof setupState>) {
  return state.tasks.value.active.map(t => t.gid).join(',')
}

function createControlledPromise<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

function asCancellable<T>(promise: Promise<T>): CancellablePromise<T> {
  return Object.assign(promise, {
    cancel: vi.fn().mockResolvedValue(undefined),
    cancelOn: vi.fn().mockReturnValue(promise),
  }) as unknown as CancellablePromise<T>
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
      (_disableContext: boolean) => {
        stopPollingCalled = true
      }, // stop
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
      state.syncMode.value = 'polling'
      state.tasks.value.stopped = [mockTask('a1', { status: 'complete' })]

      mockGetActiveTasks.mockResolvedValue({
        active: [mockTask('a1'), mockTask('a2')],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })

      await actions.fetchActiveTasks()

      // a1 is in stopped, so it should be filtered out from active on first polling sighting
      expect(state.tasks.value.active.length).toBe(1)
      expect(state.tasks.value.active[0].gid).toBe('a2')
    })

    it('should admit a previously suppressed stopped GID on the second consecutive fetch', async () => {
      state.syncMode.value = 'polling'
      state.tasks.value.stopped = [mockTask('a1', { status: 'complete' })]

      mockGetActiveTasks.mockResolvedValue({
        active: [mockTask('a1'), mockTask('a2')],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })

      await actions.fetchActiveTasks()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['a2'])
      expect(state.tasks.value.stopped.some(t => t.gid === 'a1')).toBe(true)

      await actions.fetchActiveTasks()
      expect(state.tasks.value.active.map(t => t.gid).sort()).toEqual(['a1', 'a2'])
      expect(state.tasks.value.stopped.some(t => t.gid === 'a1')).toBe(false)
    })

    it('should admit a previously suppressed stopped GID into waiting on the second fetch', async () => {
      state.syncMode.value = 'polling'
      state.tasks.value.stopped = [mockTask('w1', { status: 'complete' })]

      mockGetActiveTasks.mockResolvedValue({
        active: [],
        waiting: [mockTask('w1', { status: 'paused' }), mockTask('w2')],
      } as unknown as { active: Task[]; waiting: Task[] })

      await actions.fetchActiveTasks()
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['w2'])
      expect(state.tasks.value.stopped.some(t => t.gid === 'w1')).toBe(true)

      await actions.fetchActiveTasks()
      expect(state.tasks.value.waiting.map(t => t.gid).sort()).toEqual(['w1', 'w2'])
      expect(state.tasks.value.stopped.some(t => t.gid === 'w1')).toBe(false)
    })

    it('should admit a local-stopped GID on first sighting in event-driven mode', async () => {
      state.syncMode.value = 'event-driven'
      state.tasks.value.stopped = [mockTask('a1', { status: 'error' })]

      mockGetActiveTasks.mockResolvedValue({
        active: [mockTask('a1'), mockTask('a2')],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })

      await actions.fetchActiveTasks()
      expect(state.tasks.value.active.map(t => t.gid).sort()).toEqual(['a1', 'a2'])
      expect(state.tasks.value.stopped.some(t => t.gid === 'a1')).toBe(false)
    })

    it('should reset one-shot suppression when a GID disappears between fetches', async () => {
      state.syncMode.value = 'polling'
      state.tasks.value.stopped = [mockTask('a1', { status: 'complete' })]

      mockGetActiveTasks.mockResolvedValueOnce({
        active: [mockTask('a1'), mockTask('a2')],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })
      await actions.fetchActiveTasks()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['a2'])

      mockGetActiveTasks.mockResolvedValueOnce({
        active: [mockTask('a2')],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })
      await actions.fetchActiveTasks()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['a2'])
      expect(state.tasks.value.stopped.some(t => t.gid === 'a1')).toBe(true)

      mockGetActiveTasks.mockResolvedValueOnce({
        active: [mockTask('a1'), mockTask('a2')],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })
      await actions.fetchActiveTasks()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['a2'])
      expect(state.tasks.value.stopped.some(t => t.gid === 'a1')).toBe(true)
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

    it('should drain metadata recovery queued while an empty response is in flight', async () => {
      const firstMetadata = createControlledPromise<Record<string, Task | undefined>>()
      mockGetActiveTasks.mockResolvedValue({
        active: [mockTask('a-drain', { files: [], dir: '', totalLength: '0' })],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })
      mockGetTaskMetadata
        .mockReturnValueOnce(asCancellable(firstMetadata.promise))
        .mockResolvedValueOnce({
          'a-drain': mockTask('a-drain', {
            files: [{ path: '/downloads/drained.zip', uris: [] }],
            dir: '/downloads',
            totalLength: '2048',
            download_group: mockGroup,
          }),
        } as Record<string, Task>)

      await actions.fetchActiveTasks()
      expect(mockGetTaskMetadata).toHaveBeenCalledTimes(1)
      expect(mockGetTaskMetadata).toHaveBeenNthCalledWith(1, ['a-drain'])

      actions.queueMetadataRecovery('a-drain')
      expect(mockGetTaskMetadata).toHaveBeenCalledTimes(1)

      firstMetadata.resolve({})
      await flushPromises()

      expect(mockGetTaskMetadata).toHaveBeenCalledTimes(2)
      expect(mockGetTaskMetadata).toHaveBeenNthCalledWith(2, ['a-drain'])
      await flushPromises()

      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/drained.zip')
      expect(state.tasks.value.active[0].dir).toBe('/downloads')
      expect(state.tasks.value.active[0].totalLength).toBe('2048')
      expect(state.tasks.value.active[0].download_group?.id).toBe('dg-actions')
    })

    it('should handle errors and increment consecutiveErrors', async () => {
      mockGetActiveTasks.mockRejectedValue(new Error('Network error'))

      const result = await actions.fetchActiveTasks()

      expect(result.hasActiveTasks).toBe(false)
      expect(state.consecutiveErrors.value).toBe(1)
    })

    it('should reset consecutiveErrors on success', async () => {
      state.consecutiveErrors.value = 2
      mockGetActiveTasks.mockResolvedValue({ active: [], waiting: [] } as unknown as {
        active: Task[]
        waiting: Task[]
      })

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
  // addUri startup-first-add metadata recovery
  // =====================================================
  describe('addUri metadata recovery', () => {
    it('should recover first frontend add Lite metadata without tab switch', async () => {
      mockAddUri.mockResolvedValue('gid-first-add')
      mockGetTasks.mockResolvedValue({
        active: [
          mockTask('gid-first-add', {
            files: [],
            dir: '',
            totalLength: '0',
            completedLength: '0',
            downloadSpeed: '0',
          }),
        ],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      mockGetTaskMetadata
        .mockResolvedValueOnce({ 'gid-first-add': undefined } as Record<string, Task | undefined>)
        .mockResolvedValueOnce({
          'gid-first-add': mockTask('gid-first-add', {
            files: [{ path: '/downloads/first-add.zip', uris: [] }],
            dir: '/downloads',
            totalLength: '4096',
            completedLength: '128',
          }),
        } as Record<string, Task>)

      await actions.addUri('https://example.com/first-add.zip')
      expect(state.tasks.value.active).toHaveLength(1)
      expect(state.tasks.value.active[0].files).toHaveLength(0)
      expect(mockGetTaskMetadata).toHaveBeenCalledTimes(1)

      await flushPromises()
      actions.queueMetadataRecovery('gid-first-add')
      await flushPromises()

      expect(mockGetTaskMetadata).toHaveBeenCalledTimes(2)
      expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/first-add.zip')
      expect(state.tasks.value.active[0].dir).toBe('/downloads')
      expect(state.tasks.value.active[0].totalLength).toBe('4096')
    })
  })

  describe('addUri same-flush prepend', () => {
    it('addUri prepends unknown GIDs in the same fetchTasks assign as the snapshot', async () => {
      mockAddUri.mockResolvedValue('c')
      mockGetTasks.mockResolvedValue({
        active: [mockTask('a'), mockTask('b'), mockTask('c')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      state.tasks.value = {
        active: [mockTask('a'), mockTask('b')],
        waiting: [],
        stopped: [],
      }

      const preOrders: string[] = []
      const postOrders: string[] = []
      const Probe = defineComponent({
        setup() {
          const order = computed(() => activeOrder(state))
          watch(
            order,
            next => {
              preOrders.push(next)
            },
            { flush: 'pre' },
          )
          watch(
            order,
            next => {
              postOrders.push(next)
            },
            { flush: 'post' },
          )
          return () => null
        },
      })
      const probe = mount(Probe)

      await actions.addUri('https://example.com/c.zip')
      await nextTick()
      probe.unmount()

      expect(preOrders).toEqual(['c,a,b'])
      expect(postOrders).toEqual(['c,a,b'])
      expect(activeOrder(state)).toBe('c,a,b')
    })

    it('fetchTasks prependUnknownFrom prepends unknowns; all-known is a no-op', async () => {
      mockGetTasks.mockResolvedValueOnce({
        active: [mockTask('a'), mockTask('b'), mockTask('c')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      state.tasks.value = {
        active: [mockTask('a'), mockTask('b')],
        waiting: [],
        stopped: [],
      }

      await actions.fetchTasks({ prependUnknownFrom: new Set(['a', 'b']) })
      expect(activeOrder(state)).toBe('c,a,b')

      mockGetTasks.mockResolvedValueOnce({
        active: [mockTask('a'), mockTask('b'), mockTask('c')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      await actions.fetchTasks({
        prependUnknownFrom: new Set(['a', 'b', 'c']),
      })
      expect(activeOrder(state)).toBe('a,b,c')
    })

    it('fetchActiveTasks never prepends: equal fields keep the array, field change follows backend order', async () => {
      expect(actions.fetchActiveTasks.length).toBe(0)

      state.tasks.value = {
        active: [mockTask('c'), mockTask('a'), mockTask('b')],
        waiting: [],
        stopped: [],
      }
      const storedActive = state.tasks.value.active

      mockGetActiveTasks.mockResolvedValueOnce({
        active: [mockTask('a'), mockTask('b'), mockTask('c')],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })

      await actions.fetchActiveTasks()
      expect(state.tasks.value.active).toBe(storedActive)
      expect(activeOrder(state)).toBe('c,a,b')

      mockGetActiveTasks.mockResolvedValueOnce({
        active: [
          mockTask('a', { downloadSpeed: '200' }),
          mockTask('b'),
          mockTask('c', { downloadSpeed: '200' }),
        ],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })

      await actions.fetchActiveTasks()
      expect(state.tasks.value.active).not.toBe(storedActive)
      expect(activeOrder(state)).toBe('a,b,c')
    })

    it('in-flight task:add during AddUri still ends prepended (knownGids captured before RPC)', async () => {
      const events = setupEvents(state, actions, {} as TaskPolling)
      state.tasks.value = {
        active: [mockTask('a'), mockTask('b')],
        waiting: [],
        stopped: [],
      }

      mockAddUri.mockImplementation(() =>
        asCancellable(
          (async () => {
            await events.handleTaskDelta({
              type: 'add',
              gid: 'c',
              payload: mockTask('c') as unknown as Record<string, unknown>,
            })
            return 'c'
          })(),
        ),
      )
      mockGetTasks.mockResolvedValue({
        active: [mockTask('a'), mockTask('b'), mockTask('c')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      await actions.addUri('https://example.com/c.zip')
      expect(activeOrder(state)).toBe('c,a,b')
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

  // =====================================================
  // resume / syncFromSnapshot
  // =====================================================
  describe('resume', () => {
    it('should optimistically move stopped task to active via real moveTaskToActive', async () => {
      state.tasks.value.stopped = [
        mockTask('sg_r1', { status: 'error', errorCode: '1', errorMessage: 'fail' }),
      ]
      mockResumeTask.mockResolvedValue(undefined as never)
      const events = setupEvents(state, actions, {} as TaskPolling)
      actions.setMoveTaskToActive(events.moveTaskToActive)

      await actions.resume('sg_r1')

      expect(mockResumeTask).toHaveBeenCalledWith('sg_r1')
      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_r1'])
      expect(state.tasks.value.active[0].status).toBe('active')
      expect(state.tasks.value.active[0].errorCode).toBe('')
      expect(state.tasks.value.stopped.some(t => t.gid === 'sg_r1')).toBe(false)
    })

    it('should skip optimistic move when terminal event lands during ResumeTask IPC', async () => {
      state.tasks.value.stopped = [
        mockTask('sg_race', { status: 'error', errorCode: '1', errorMessage: 'fail' }),
      ]
      const deferred = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(deferred.promise as never)
      mockGetTasks.mockResolvedValue({
        active: [],
        waiting: [],
        stopped: [mockTask('sg_race', { status: 'complete' })],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      const events = setupEvents(state, actions, {} as TaskPolling)
      actions.setMoveTaskToActive(events.moveTaskToActive)

      const resumePromise = actions.resume('sg_race')
      actions.markResumeSuperseded('sg_race')
      deferred.resolve(undefined)
      await resumePromise

      expect(mockGetTasks).toHaveBeenCalled()
      expect(state.tasks.value.active.some(t => t.gid === 'sg_race')).toBe(false)
      expect(state.tasks.value.stopped.some(t => t.gid === 'sg_race')).toBe(true)
    })

    it('should fetchTasks when move callback is unset after successful ResumeTask', async () => {
      state.tasks.value.stopped = [mockTask('sg_r_unset', { status: 'error' })]
      mockResumeTask.mockResolvedValue(undefined as never)
      mockGetTasks.mockResolvedValue({
        active: [mockTask('sg_r_unset')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      await actions.resume('sg_r_unset')

      expect(mockGetTasks).toHaveBeenCalled()
      expect(state.tasks.value.active.some(t => t.gid === 'sg_r_unset')).toBe(true)
    })

    it('should fetchTasks when ResumeTask fails', async () => {
      mockResumeTask.mockRejectedValue(new Error('resume failed'))
      mockGetTasks.mockResolvedValue({
        active: [],
        waiting: [],
        stopped: [mockTask('sg_r2', { status: 'error' })],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      await actions.resume('sg_r2')

      expect(mockGetTasks).toHaveBeenCalled()
    })
  })

  describe('batchResume', () => {
    it('should optimistically move each GID via real moveTaskToActive without fetchTasks', async () => {
      state.tasks.value.stopped = [
        mockTask('sg_b1', { status: 'error' }),
        mockTask('sg_b2', { status: 'error' }),
      ]
      mockBatchResume.mockResolvedValue([
        { gid: 'sg_b1', ok: true },
        { gid: 'sg_b2', ok: true },
      ] as never)
      const events = setupEvents(state, actions, {} as TaskPolling)
      actions.setMoveTaskToActive(events.moveTaskToActive)

      await actions.batchResume(['sg_b1', 'sg_b2'])

      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(state.tasks.value.active.map(t => t.gid).sort()).toEqual(['sg_b1', 'sg_b2'])
      expect(state.tasks.value.stopped).toHaveLength(0)
    })

    it('should only optimistically move OK GIDs from BatchResume results', async () => {
      state.tasks.value.stopped = [
        mockTask('sg_ok', { status: 'error' }),
        mockTask('sg_fail', { status: 'error' }),
      ]
      mockBatchResume.mockResolvedValue([
        { gid: 'sg_ok', ok: true },
        { gid: 'sg_fail', ok: false, error: 'unpause failed' },
      ] as never)
      const events = setupEvents(state, actions, {} as TaskPolling)
      actions.setMoveTaskToActive(events.moveTaskToActive)

      await actions.batchResume(['sg_ok', 'sg_fail'])

      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_ok'])
      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['sg_fail'])
    })

    it('should fetchTasks when BatchResume fails', async () => {
      mockBatchResume.mockRejectedValue(new Error('batch resume failed'))
      mockGetTasks.mockResolvedValue({
        active: [],
        waiting: [],
        stopped: [mockTask('sg_bf', { status: 'error' })],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      await actions.batchResume(['sg_bf'])

      expect(mockGetTasks).toHaveBeenCalled()
    })
  })

  describe('syncFromSnapshot', () => {
    it('should dedupe stopped GIDs that are also in active/waiting', async () => {
      mockGetFullSnapshot.mockResolvedValue({
        tasks: {
          active: [mockTask('twin', { status: 'active' })],
          waiting: [],
          stopped: [mockTask('twin', { status: 'complete' }), mockTask('only-stopped')],
        },
        trayState: { hasActive: true, hasPaused: false, hasError: false },
      } as never)

      await actions.syncFromSnapshot()

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['twin'])
      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['only-stopped'])
    })
  })
})
