import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { computed, defineComponent, nextTick, watch } from 'vue'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { CancellablePromise } from '@wailsio/runtime'
import { setupState } from '../state'
import { setupActions } from '../actions'
import { setupEvents } from '../events'
import { setupPolling, type TaskPolling } from '../polling'
import { clearMetadataCache, getMetadataCacheSize } from '../metadata'
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
  ResumeDownloadGroup: vi.fn(),
  GetDownloadGroups: vi.fn(),
  GetDownloadGroupDetail: vi.fn(),
}))

vi.mock('../../events', () => ({
  subscribeToTaskEvents: vi.fn(),
  unsubscribeFromTaskEvents: vi.fn(),
  subscribeToTaskMoveEvent: vi.fn(),
  unsubscribeFromTaskMoveEvent: vi.fn(),
}))

import {
  GetTasks,
  GetActiveTasks,
  GetStoppedTasks,
  GetTaskMetadata,
  AddUri,
  PauseTask,
  ResumeTask,
  BatchResume,
  GetFullSnapshot,
  ResumeDownloadGroup,
} from '../../../../bindings/goaria-v3/internal/wailsapp/app.js'
import { useTaskStore } from '../../task'
import { useDownloadGroupStore } from '../../downloadGroups'
import { subscribeToTaskMoveEvent } from '../../events'

const mockGetActiveTasks = vi.mocked(GetActiveTasks)
const mockGetStoppedTasks = vi.mocked(GetStoppedTasks)
const mockGetTasks = vi.mocked(GetTasks)
const mockGetTaskMetadata = vi.mocked(GetTaskMetadata)
const mockAddUri = vi.mocked(AddUri)
const mockPauseTask = vi.mocked(PauseTask)
const mockResumeTask = vi.mocked(ResumeTask)
const mockBatchResume = vi.mocked(BatchResume)
const mockGetFullSnapshot = vi.mocked(GetFullSnapshot)
const mockResumeDownloadGroup = vi.mocked(ResumeDownloadGroup)

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

function waitingOrder(state: ReturnType<typeof setupState>) {
  return state.tasks.value.waiting.map(t => t.gid).join(',')
}

function stoppedOrder(state: ReturnType<typeof setupState>) {
  return state.tasks.value.stopped.map(t => t.gid).join(',')
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

function membershipKey(state: ReturnType<typeof setupState>) {
  return [
    state.tasks.value.active.map(t => t.gid).join(','),
    state.tasks.value.waiting.map(t => t.gid).join(','),
    state.tasks.value.stopped.map(t => t.gid).join(','),
  ].join('|')
}

function watchMembership(state: ReturnType<typeof setupState>) {
  let changes = 0
  let last = membershipKey(state)
  const stop = watch(
    () => state.tasks.value,
    () => {
      const next = membershipKey(state)
      if (next !== last) {
        changes++
        last = next
      }
    },
    { flush: 'sync' },
  )
  return {
    get changes() {
      return changes
    },
    stop,
  }
}

function wireMover(state: ReturnType<typeof setupState>, actions: ReturnType<typeof setupActions>) {
  const events = setupEvents(state, actions, {} as TaskPolling)
  actions.setTaskMover(events.moveTasks)
  return events
}

// --- Tests ---

describe('setupActions — integration', () => {
  let state: ReturnType<typeof setupState>
  let actions: ReturnType<typeof setupActions>
  let stopPollingCalled: boolean

  beforeEach(() => {
    vi.clearAllMocks()
    clearMetadataCache()
    setActivePinia(createPinia())
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

    it('keeps pending local stopped rows when GetStoppedTasks omits them', async () => {
      state.tasks.value.stopped = [
        mockTask('sg_s1', { status: 'error' }),
        mockTask('sg_s2', { status: 'error' }),
      ]
      const deferred = createControlledPromise<{ gid: string; ok: boolean }[]>()
      mockBatchResume.mockReturnValue(asCancellable(deferred.promise) as never)
      mockGetStoppedTasks.mockResolvedValue([] as Task[])
      wireMover(state, actions)
      const membership = watchMembership(state)

      const resumePromise = actions.batchResume(['sg_s2', 'sg_s1'])
      await flushPromises()
      await actions.fetchStoppedTasks()

      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['sg_s1', 'sg_s2'])
      expect(state.tasks.value.active).toHaveLength(0)
      expect(membership.changes).toBe(0)

      deferred.resolve([
        { gid: 'sg_s1', ok: true },
        { gid: 'sg_s2', ok: true },
      ])
      await resumePromise
      membership.stop()

      // A resume acknowledgement lands in waiting; only authoritative active
      // evidence may move a task to active.
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_s1', 'sg_s2'])
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.stopped).toHaveLength(0)
      expect(mockGetTasks).not.toHaveBeenCalled()
    })

    it('does not admit a pending live GID onto stopped', async () => {
      state.tasks.value.waiting = [mockTask('sg_live', { status: 'paused' })]
      const deferred = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(asCancellable(deferred.promise) as never)
      mockGetStoppedTasks.mockResolvedValue([
        mockTask('sg_live', { status: 'complete' }),
        mockTask('sg_other', { status: 'complete' }),
      ] as Task[])
      wireMover(state, actions)

      const resumePromise = actions.resume('sg_live')
      await flushPromises()
      await actions.fetchStoppedTasks()

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_live'])
      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['sg_other'])

      deferred.resolve(undefined)
      await resumePromise
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

    it('fetchTasks prependUnknownFrom prepends unknowns; all-known keeps local order', async () => {
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
      expect(activeOrder(state)).toBe('c,a,b')
    })

    it('fetchActiveTasks never hoists known GIDs; field-change polls keep local order', async () => {
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
      expect(activeOrder(state)).toBe('c,a,b')
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

  describe('local order on full reads', () => {
    let polling: ReturnType<typeof setupPolling> | undefined

    afterEach(() => {
      polling?.stopPolling(true)
      polling = undefined
    })

    function wireRealPolling() {
      const events = setupEvents(state, actions, {} as TaskPolling)
      const nextPolling = setupPolling(state, actions, () => events)
      polling = nextPolling
      actions.setPollingCallbacks(nextPolling.startPolling, nextPolling.stopPolling)
      state.pollingContextEnabled.value = true
      state.isWindowVisible.value = true
      return { events, polling: nextPolling }
    }

    it('keeps sequential addUri newest-first when backend returns FIFO', async () => {
      mockAddUri.mockResolvedValueOnce('a')
      mockGetTasks.mockResolvedValueOnce({
        active: [mockTask('a')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.addUri('https://example.com/a.zip')
      expect(activeOrder(state)).toBe('a')

      mockAddUri.mockResolvedValueOnce('b')
      mockGetTasks.mockResolvedValueOnce({
        active: [mockTask('a'), mockTask('b')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.addUri('https://example.com/b.zip')
      expect(activeOrder(state)).toBe('b,a')

      mockAddUri.mockResolvedValueOnce('c')
      mockGetTasks.mockResolvedValueOnce({
        active: [mockTask('a'), mockTask('b'), mockTask('c')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.addUri('https://example.com/c.zip')
      expect(activeOrder(state)).toBe('c,b,a')
    })

    it('plain fetchTasks keeps local order and leads with never-seen GIDs', async () => {
      state.tasks.value = {
        active: [mockTask('c'), mockTask('a'), mockTask('b')],
        waiting: [],
        stopped: [],
      }

      mockGetTasks.mockResolvedValueOnce({
        active: [mockTask('a'), mockTask('b'), mockTask('c')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.fetchTasks()
      expect(activeOrder(state)).toBe('c,a,b')

      mockGetTasks.mockResolvedValueOnce({
        active: [mockTask('a'), mockTask('b'), mockTask('c'), mockTask('d')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.fetchTasks()
      expect(activeOrder(state)).toBe('d,c,a,b')

      mockGetTasks.mockResolvedValueOnce({
        active: [mockTask('a'), mockTask('c')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.fetchTasks()
      expect(activeOrder(state)).toBe('c,a')
    })

    it('PAUSE-VOID: pause calls PauseTask without GetTasks, avoids stale snapshot overwrite, and allows lifecycle events to migrate', async () => {
      const events = wireMover(state, actions)
      mockPauseTask.mockResolvedValue(undefined as never)
      mockGetTasks.mockResolvedValue({
        active: [mockTask('b'), mockTask('c')],
        waiting: [mockTask('w1', { status: 'paused' }), mockTask('a', { status: 'paused' })],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      state.tasks.value = {
        active: [mockTask('c'), mockTask('a'), mockTask('b')],
        waiting: [mockTask('w1', { status: 'paused' })],
        stopped: [],
      }

      await actions.pause('a')

      // 1. PauseTask was called, but GetTasks was NOT called
      expect(mockPauseTask).toHaveBeenCalledWith('a')
      expect(mockGetTasks).not.toHaveBeenCalled()

      // 2. IPC completion does NOT overwrite state with stale active snapshot
      expect(activeOrder(state)).toBe('c,a,b')
      expect(waitingOrder(state)).toBe('w1')

      // 3. Subsequent real pause/task:move event migrates task 'a' to top of waiting in local order
      events.handleTaskMove({ gid: 'a', from: 'active', to: 'waiting', task: {} })
      expect(activeOrder(state)).toBe('c,b')
      expect(waitingOrder(state)).toBe('a,w1')
    })

    it('pause followed by complete delta directly enters stopped without second pause or resume', async () => {
      const events = wireMover(state, actions)
      mockPauseTask.mockResolvedValue(undefined as never)

      state.tasks.value = {
        active: [mockTask('c'), mockTask('a'), mockTask('b')],
        waiting: [mockTask('w1', { status: 'paused' })],
        stopped: [],
      }

      await actions.pause('a')
      expect(mockPauseTask).toHaveBeenCalledWith('a')
      expect(mockGetTasks).not.toHaveBeenCalled()

      // Late complete delta arrives: task 'a' completes and moves directly to stopped
      await events.handleTaskDelta({
        type: 'complete',
        gid: 'a',
        payload: {
          status: 'complete',
          completedLength: '1000',
          totalLength: '1000',
        },
      })

      expect(activeOrder(state)).toBe('c,b')
      expect(waitingOrder(state)).toBe('w1')
      expect(stoppedOrder(state)).toBe('a')
      expect(state.tasks.value.stopped[0].status).toBe('complete')
    })

    it('waiting lists follow the same local-order rule on fetchTasks and fetchActiveTasks', async () => {
      const paused = (gid: string) => mockTask(gid, { status: 'paused' })
      state.tasks.value = {
        active: [],
        waiting: [paused('w2'), paused('w1')],
        stopped: [],
      }

      mockGetTasks.mockResolvedValue({
        active: [],
        waiting: [paused('w1'), paused('w2'), paused('w3')],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.fetchTasks()
      expect(waitingOrder(state)).toBe('w3,w2,w1')

      state.tasks.value = {
        active: [],
        waiting: [paused('w2'), paused('w1')],
        stopped: [],
      }
      mockGetActiveTasks.mockResolvedValue({
        active: [],
        waiting: [paused('w1'), paused('w2'), paused('w3')],
      } as unknown as { active: Task[]; waiting: Task[] })
      await actions.fetchActiveTasks()
      expect(waitingOrder(state)).toBe('w3,w2,w1')
    })

    it('places a never-seen waiting GID first while a resume hold is active', async () => {
      state.tasks.value.waiting = ['A', 'B', 'C', 'D'].map(gid =>
        mockTask(gid, { status: 'paused' }),
      )
      const deferred = createControlledPromise<{ gid: string; ok: boolean; error?: string }[]>()
      mockBatchResume.mockReturnValue(asCancellable(deferred.promise) as never)
      mockGetActiveTasks.mockResolvedValue({
        active: [],
        waiting: [mockTask('fresh', { status: 'paused' })],
      } as unknown as { active: Task[]; waiting: Task[] })
      wireMover(state, actions)

      const resumePromise = actions.batchResume(['D', 'C', 'B', 'A'])
      await flushPromises()
      await actions.fetchActiveTasks()

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['fresh', 'A', 'B', 'C', 'D'])
      expect(state.tasks.value.active).toHaveLength(0)

      const membership = watchMembership(state)
      deferred.resolve(['A', 'B', 'C', 'D'].map(gid => ({ gid, ok: true })))
      await resumePromise
      membership.stop()

      // No active evidence was observed, so confirmed GIDs acknowledge
      // waiting in place behind the never-seen lead row.
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual([
        'fresh',
        'A',
        'B',
        'C',
        'D',
      ])
      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(membership.changes).toBe(0)
    })

    it('addUri restart poll cannot flatten order (real setupPolling)', async () => {
      wireRealPolling()
      state.tasks.value = {
        active: [mockTask('a'), mockTask('b')],
        waiting: [],
        stopped: [],
      }

      mockAddUri.mockResolvedValue('c')
      mockGetTasks.mockResolvedValue({
        active: [mockTask('a'), mockTask('b'), mockTask('c')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      mockGetActiveTasks.mockResolvedValue({
        active: [
          mockTask('a', { downloadSpeed: '200' }),
          mockTask('b', { downloadSpeed: '200' }),
          mockTask('c', { downloadSpeed: '200' }),
        ],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })

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
      await flushPromises()
      await nextTick()
      probe.unmount()

      expect(preOrders).toEqual(['c,a,b'])
      expect(postOrders).toEqual(['c,a,b'])
      expect(activeOrder(state)).toBe('c,a,b')
      expect(mockGetActiveTasks).toHaveBeenCalled()
    })

    it('window focus re-sync keeps local order', async () => {
      const wired = wireRealPolling()
      state.tasks.value = {
        active: [mockTask('c'), mockTask('a'), mockTask('b')],
        waiting: [],
        stopped: [],
      }
      mockGetActiveTasks.mockResolvedValue({
        active: [
          mockTask('a', { downloadSpeed: '200' }),
          mockTask('b', { downloadSpeed: '200' }),
          mockTask('c', { downloadSpeed: '200' }),
        ],
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })

      wired.polling.setWindowVisibility(false)
      wired.polling.setWindowVisibility(true)
      await flushPromises()

      expect(activeOrder(state)).toBe('c,a,b')
      expect(mockGetActiveTasks).toHaveBeenCalled()
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
    it('ACK-NOT-START: resume reopens a stopped error row into waiting with cleared errors', async () => {
      state.tasks.value.stopped = [
        mockTask('sg_r1', { status: 'error', errorCode: '1', errorMessage: 'fail' }),
      ]
      mockResumeTask.mockResolvedValue(undefined as never)
      wireMover(state, actions)

      await actions.resume('sg_r1')

      expect(mockResumeTask).toHaveBeenCalledWith('sg_r1')
      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_r1'])
      expect(state.tasks.value.waiting[0].status).toBe('waiting')
      expect(state.tasks.value.waiting[0].errorCode).toBe('')
      expect(state.tasks.value.waiting[0].errorMessage).toBe('')
      expect(state.tasks.value.waiting[0].downloadSpeed).toBe('0')
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
      wireMover(state, actions)

      const resumePromise = actions.resume('sg_race')
      actions.markResumeSuperseded('sg_race')
      deferred.resolve(undefined)
      await resumePromise

      expect(mockGetTasks).toHaveBeenCalled()
      expect(state.tasks.value.active.some(t => t.gid === 'sg_race')).toBe(false)
      expect(state.tasks.value.stopped.some(t => t.gid === 'sg_race')).toBe(true)
    })

    it('does not revive a superseded GID from a late dest-active move', async () => {
      state.tasks.value.waiting = [mockTask('sg_late', { status: 'paused' })]
      const deferred = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(asCancellable(deferred.promise) as never)
      mockGetTasks.mockResolvedValue({
        active: [],
        waiting: [],
        stopped: [mockTask('sg_late', { status: 'complete' })],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      const events = wireMover(state, actions)

      const resumePromise = actions.resume('sg_late')
      await flushPromises()
      actions.markResumeSuperseded('sg_late')
      events.handleTaskMove({
        gid: 'sg_late',
        from: 'waiting',
        to: 'active',
        task: { gid: 'sg_late', status: 'active' },
      })

      expect(state.tasks.value.active.some(t => t.gid === 'sg_late')).toBe(false)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_late'])

      deferred.resolve(undefined)
      await resumePromise

      expect(mockGetTasks).toHaveBeenCalled()
      expect(state.tasks.value.active.some(t => t.gid === 'sg_late')).toBe(false)
    })

    it('supersedes on dest-stopped during IPC and does not revive after ResumeTask', async () => {
      state.tasks.value.waiting = [mockTask('sg_term', { status: 'paused' })]
      const deferred = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(asCancellable(deferred.promise) as never)
      mockGetTasks.mockResolvedValue({
        active: [],
        waiting: [],
        stopped: [mockTask('sg_term', { status: 'complete' })],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      const events = wireMover(state, actions)

      const resumePromise = actions.resume('sg_term')
      await flushPromises()
      events.handleTaskMove({
        gid: 'sg_term',
        from: 'waiting',
        to: 'stopped',
        task: { gid: 'sg_term', status: 'complete' },
      })

      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['sg_term'])
      expect(actions.isResumePending('sg_term')).toBe(true)

      deferred.resolve(undefined)
      await resumePromise

      expect(mockGetTasks).toHaveBeenCalled()
      expect(state.tasks.value.active.some(t => t.gid === 'sg_term')).toBe(false)
      expect(state.tasks.value.stopped.some(t => t.gid === 'sg_term')).toBe(true)
      expect(actions.isResumePending('sg_term')).toBe(false)
    })

    it('holds dest-active until GetTasks finishes after empty confirmed', async () => {
      state.tasks.value.waiting = [mockTask('sg_snap', { status: 'paused' })]
      const ipc = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(asCancellable(ipc.promise) as never)
      const snapshot = createControlledPromise<{
        active: Task[]
        waiting: Task[]
        stopped: Task[]
      }>()
      mockGetTasks.mockReturnValue(asCancellable(snapshot.promise) as never)
      const events = wireMover(state, actions)

      const resumePromise = actions.resume('sg_snap')
      await flushPromises()
      actions.markResumeSuperseded('sg_snap')
      ipc.resolve(undefined)
      await flushPromises()

      expect(mockGetTasks).toHaveBeenCalled()
      expect(actions.isResumePending('sg_snap')).toBe(true)

      events.handleTaskMove({
        gid: 'sg_snap',
        from: 'waiting',
        to: 'active',
        task: { gid: 'sg_snap', status: 'active' },
      })
      expect(state.tasks.value.active.some(t => t.gid === 'sg_snap')).toBe(false)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_snap'])

      snapshot.resolve({
        active: [],
        waiting: [],
        stopped: [mockTask('sg_snap', { status: 'complete' })],
      })
      await resumePromise

      expect(state.tasks.value.active.some(t => t.gid === 'sg_snap')).toBe(false)
      expect(state.tasks.value.stopped.some(t => t.gid === 'sg_snap')).toBe(true)
      expect(actions.isResumePending('sg_snap')).toBe(false)
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

    it('HELD-ACTIVE-BOTH-OUTCOMES: holds dest-active moves during ResumeTask then moves once on success', async () => {
      state.tasks.value.waiting = [mockTask('sg_one', { status: 'paused' })]
      const deferred = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(asCancellable(deferred.promise) as never)
      const events = wireMover(state, actions)
      const membership = watchMembership(state)

      const resumePromise = actions.resume('sg_one')
      await flushPromises()
      events.handleTaskMove({
        gid: 'sg_one',
        from: 'waiting',
        to: 'active',
        task: { gid: 'sg_one', status: 'active' },
      })

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_one'])
      expect(membership.changes).toBe(0)

      deferred.resolve(undefined)
      await resumePromise
      membership.stop()

      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_one'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(membership.changes).toBe(1)
    })
  })

  describe('batchResume', () => {
    it('should optimistically move each GID into waiting via the real mover without fetchTasks', async () => {
      state.tasks.value.stopped = [
        mockTask('sg_b1', { status: 'error' }),
        mockTask('sg_b2', { status: 'error' }),
      ]
      mockBatchResume.mockResolvedValue([
        { gid: 'sg_b1', ok: true },
        { gid: 'sg_b2', ok: true },
      ] as never)
      wireMover(state, actions)

      await actions.batchResume(['sg_b1', 'sg_b2'])

      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_b1', 'sg_b2'])
      expect(state.tasks.value.waiting.every(t => t.status === 'waiting')).toBe(true)
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
      wireMover(state, actions)

      await actions.batchResume(['sg_ok', 'sg_fail'])

      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_ok'])
      expect(state.tasks.value.waiting[0].status).toBe('waiting')
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

    it('holds dest-active moves during BatchResume then prepends waiting relative order in one assign', async () => {
      state.tasks.value.waiting = ['A', 'B', 'C', 'D'].map(gid =>
        mockTask(gid, { status: 'paused' }),
      )
      const deferred = createControlledPromise<{ gid: string; ok: boolean; error?: string }[]>()
      mockBatchResume.mockReturnValue(asCancellable(deferred.promise) as never)
      const events = wireMover(state, actions)
      const membership = watchMembership(state)

      const resumePromise = actions.batchResume(['D', 'C', 'B', 'A'])
      await flushPromises()
      for (const gid of ['A', 'B', 'C', 'D']) {
        events.handleTaskMove({
          gid,
          from: 'waiting',
          to: 'active',
          task: { gid, status: 'active' },
        })
      }

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(state.tasks.value.active).toHaveLength(0)
      expect(membership.changes).toBe(0)

      deferred.resolve([
        { gid: 'D', ok: true },
        { gid: 'C', ok: true },
        { gid: 'B', ok: true },
        { gid: 'A', ok: true },
      ])
      await resumePromise
      membership.stop()

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(membership.changes).toBe(1)
    })

    it('STRICT-OK-SET: only explicit-ok GIDs acknowledge waiting in a mixed paused batch', async () => {
      state.tasks.value.waiting = ['A', 'B', 'C', 'D'].map(gid =>
        mockTask(gid, { status: 'paused' }),
      )
      mockBatchResume.mockResolvedValue([
        { gid: 'A', ok: true },
        { gid: 'B', ok: false, error: 'unpause failed' },
        { gid: 'C', ok: true },
        { gid: 'D', ok: false, error: 'unpause failed' },
      ] as never)
      wireMover(state, actions)
      const membership = watchMembership(state)

      await actions.batchResume(['D', 'C', 'B', 'A'])
      membership.stop()

      expect(mockGetTasks).not.toHaveBeenCalled()
      // Paused waiting rows stay in place; only successful acks flip to
      // 'waiting', failures keep 'paused'. No active without evidence.
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(state.tasks.value.waiting.map(t => t.status)).toEqual([
        'waiting',
        'paused',
        'waiting',
        'paused',
      ])
      expect(membership.changes).toBe(0)
    })

    it('does not assign or fetch when every confirmed GID is already active', async () => {
      state.tasks.value.active = ['A', 'B', 'C', 'D'].map(gid => mockTask(gid))
      mockBatchResume.mockResolvedValue(
        ['A', 'B', 'C', 'D'].map(gid => ({ gid, ok: true })) as never,
      )
      wireMover(state, actions)
      const membership = watchMembership(state)

      await actions.batchResume(['A', 'B', 'C', 'D'])
      membership.stop()

      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(membership.changes).toBe(0)
    })

    it('freezes in-flight fetchActiveTasks membership until BatchResume resolves', async () => {
      state.tasks.value.waiting = ['A', 'B', 'C', 'D'].map(gid =>
        mockTask(gid, { status: 'paused' }),
      )
      const deferred = createControlledPromise<{ gid: string; ok: boolean; error?: string }[]>()
      mockBatchResume.mockReturnValue(asCancellable(deferred.promise) as never)
      mockGetActiveTasks.mockResolvedValue({
        active: ['A', 'B', 'C', 'D'].map(gid => mockTask(gid)),
        waiting: [],
      } as unknown as { active: Task[]; waiting: Task[] })
      wireMover(state, actions)

      const resumePromise = actions.batchResume(['D', 'C', 'B', 'A'])
      await flushPromises()
      await actions.fetchActiveTasks()

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(state.tasks.value.active).toHaveLength(0)

      deferred.resolve(['A', 'B', 'C', 'D'].map(gid => ({ gid, ok: true })))
      await resumePromise

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(mockGetTasks).not.toHaveBeenCalled()
    })

    it('fetches when a confirmed GID is missing from every list', async () => {
      mockBatchResume.mockResolvedValue([{ gid: 'ghost', ok: true }] as never)
      mockGetTasks.mockResolvedValue({
        active: [mockTask('ghost')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      wireMover(state, actions)

      await actions.batchResume(['ghost'])

      expect(mockGetTasks).toHaveBeenCalled()
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['ghost'])
    })

    it('GENERATION-SUPERSESSION: does not clear a newer overlapping resume gen when the batch finishes', async () => {
      state.tasks.value.waiting = [
        mockTask('A', { status: 'paused' }),
        mockTask('B', { status: 'paused' }),
      ]
      const batchIpc = createControlledPromise<{ gid: string; ok: boolean }[]>()
      const resumeIpc = createControlledPromise<void>()
      mockBatchResume.mockReturnValue(asCancellable(batchIpc.promise) as never)
      mockResumeTask.mockReturnValue(asCancellable(resumeIpc.promise) as never)
      const events = wireMover(state, actions)

      const batchPromise = actions.batchResume(['A', 'B'])
      await flushPromises()
      const resumePromise = actions.resume('A')
      await flushPromises()

      events.handleTaskMove({
        gid: 'A',
        from: 'waiting',
        to: 'active',
        task: { gid: 'A', status: 'active' },
      })
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B'])

      batchIpc.resolve([
        { gid: 'A', ok: true },
        { gid: 'B', ok: true },
      ])
      await batchPromise

      // A stays held by the newer generation; B acknowledges waiting only.
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B'])
      expect(state.tasks.value.waiting[1].status).toBe('waiting')
      expect(actions.isResumePending('A')).toBe(true)
      expect(mockGetTasks).not.toHaveBeenCalled()

      events.handleTaskMove({
        gid: 'A',
        from: 'waiting',
        to: 'active',
        task: { gid: 'A', status: 'active' },
      })
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B'])

      resumeIpc.resolve(undefined)
      await resumePromise

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['A'])
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['B'])
      expect(actions.isResumePending('A')).toBe(false)
    })

    it('does not clear a newer superseded gen when the older batch finishes', async () => {
      state.tasks.value.waiting = [
        mockTask('A', { status: 'paused' }),
        mockTask('B', { status: 'paused' }),
      ]
      const batchIpc = createControlledPromise<{ gid: string; ok: boolean }[]>()
      const resumeIpc = createControlledPromise<void>()
      mockBatchResume.mockReturnValue(asCancellable(batchIpc.promise) as never)
      mockResumeTask.mockReturnValue(asCancellable(resumeIpc.promise) as never)
      mockGetTasks.mockResolvedValue({
        active: [mockTask('B')],
        waiting: [],
        stopped: [mockTask('A', { status: 'complete' })],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      const events = wireMover(state, actions)

      const batchPromise = actions.batchResume(['A', 'B'])
      await flushPromises()
      const resumePromise = actions.resume('A')
      await flushPromises()

      events.handleTaskMove({
        gid: 'A',
        from: 'waiting',
        to: 'stopped',
        task: { gid: 'A', status: 'complete' },
      })
      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['A'])
      expect(actions.isResumePending('A')).toBe(true)

      batchIpc.resolve([
        { gid: 'A', ok: true },
        { gid: 'B', ok: true },
      ])
      await batchPromise

      expect(actions.isResumePending('A')).toBe(true)
      events.handleTaskMove({
        gid: 'A',
        from: 'stopped',
        to: 'active',
        task: { gid: 'A', status: 'active' },
      })
      expect(state.tasks.value.active.some(t => t.gid === 'A')).toBe(false)
      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['A'])

      resumeIpc.resolve(undefined)
      await resumePromise

      expect(mockGetTasks).toHaveBeenCalled()
      expect(state.tasks.value.active.some(t => t.gid === 'A')).toBe(false)
      expect(actions.isResumePending('A')).toBe(false)
    })
  })

  describe('runHeldResume', () => {
    it('holds dest-active moves until the engine callback resolves, then assigns once', async () => {
      state.tasks.value.waiting = ['A', 'B', 'C', 'D'].map(gid =>
        mockTask(gid, { status: 'paused' }),
      )
      const deferred = createControlledPromise<string[]>()
      const events = wireMover(state, actions)
      const membership = watchMembership(state)

      const resumePromise = actions.runHeldResume(['A', 'B', 'C', 'D'], () => deferred.promise, {
        recoverSnapshot: false,
      })
      await flushPromises()
      for (const gid of ['A', 'B', 'C', 'D']) {
        events.handleTaskMove({
          gid,
          from: 'waiting',
          to: 'active',
          task: { gid, status: 'active' },
        })
      }

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(state.tasks.value.active).toHaveLength(0)
      expect(membership.changes).toBe(0)

      deferred.resolve(['A', 'B', 'C', 'D'])
      await resumePromise
      membership.stop()

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(membership.changes).toBe(1)
    })

    it('moves only confirmed engine-ok GIDs in one assign without fetching', async () => {
      state.tasks.value.waiting = ['A', 'B', 'C', 'D'].map(gid =>
        mockTask(gid, { status: 'paused' }),
      )
      wireMover(state, actions)
      const membership = watchMembership(state)

      await actions.runHeldResume(['A', 'B', 'C', 'D'], async () => ['A', 'C'], {
        recoverSnapshot: false,
      })
      membership.stop()

      expect(mockGetTasks).not.toHaveBeenCalled()
      // Confirmed rows acknowledge waiting in place (same row, new status);
      // unconfirmed rows keep 'paused'. No active without active evidence.
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B', 'C', 'D'])
      expect(state.tasks.value.waiting.map(t => t.status)).toEqual([
        'waiting',
        'paused',
        'waiting',
        'paused',
      ])
      expect(membership.changes).toBe(0)
    })

    it('clears pending without fetching when confirmed is empty and recoverSnapshot is false', async () => {
      state.tasks.value.waiting = [mockTask('A', { status: 'paused' })]
      wireMover(state, actions)

      await actions.runHeldResume(['A'], async () => [], { recoverSnapshot: false })

      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(actions.isResumePending('A')).toBe(false)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A'])
    })

    it('clears pending, does not fetch, and rethrows when the engine callback fails', async () => {
      state.tasks.value.waiting = [mockTask('A', { status: 'paused' })]
      wireMover(state, actions)

      await expect(
        actions.runHeldResume(
          ['A'],
          async () => {
            throw new Error('group resume failed')
          },
          { recoverSnapshot: false },
        ),
      ).rejects.toThrow('group resume failed')

      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(actions.isResumePending('A')).toBe(false)
    })

    it('does not let an older batchResume clear a newer runHeldResume gen', async () => {
      state.tasks.value.waiting = [
        mockTask('A', { status: 'paused' }),
        mockTask('B', { status: 'paused' }),
      ]
      const batchIpc = createControlledPromise<{ gid: string; ok: boolean }[]>()
      const heldIpc = createControlledPromise<string[]>()
      mockBatchResume.mockReturnValue(asCancellable(batchIpc.promise) as never)
      const events = wireMover(state, actions)

      const batchPromise = actions.batchResume(['A', 'B'])
      await flushPromises()
      const heldPromise = actions.runHeldResume(['A'], () => heldIpc.promise, {
        recoverSnapshot: false,
      })
      await flushPromises()

      events.handleTaskMove({
        gid: 'A',
        from: 'waiting',
        to: 'active',
        task: { gid: 'A', status: 'active' },
      })
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B'])

      batchIpc.resolve([
        { gid: 'A', ok: true },
        { gid: 'B', ok: true },
      ])
      await batchPromise

      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B'])
      expect(actions.isResumePending('A')).toBe(true)
      expect(mockGetTasks).not.toHaveBeenCalled()

      events.handleTaskMove({
        gid: 'A',
        from: 'waiting',
        to: 'active',
        task: { gid: 'A', status: 'active' },
      })
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['A', 'B'])

      heldIpc.resolve(['A'])
      await heldPromise

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['A'])
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['B'])
      expect(actions.isResumePending('A')).toBe(false)
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

  describe('acceptance matrix', () => {
    it('HELD-ACTIVE-BOTH-OUTCOMES: held active evidence applies once on IPC rejection', async () => {
      state.tasks.value.waiting = [mockTask('sg_held_fail', { status: 'paused' })]
      const deferred = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(asCancellable(deferred.promise) as never)
      mockGetTasks.mockResolvedValue({
        active: [mockTask('sg_held_fail', { status: 'active' })],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      const events = wireMover(state, actions)
      const membership = watchMembership(state)

      const resumePromise = actions.resume('sg_held_fail')
      await flushPromises()
      events.handleTaskMove({
        gid: 'sg_held_fail',
        from: 'waiting',
        to: 'active',
        task: { gid: 'sg_held_fail', status: 'active' },
      })

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_held_fail'])
      expect(state.tasks.value.active).toHaveLength(0)

      deferred.reject(new Error('resume failed'))
      await resumePromise
      membership.stop()

      // Owned evidence applies even on rejection; the recovery read confirms
      // the same active membership without a second assign or rollback.
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_held_fail'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(mockGetTasks).toHaveBeenCalled()
      expect(actions.isResumePending('sg_held_fail')).toBe(false)
      expect(membership.changes).toBe(1)
    })

    it('HELD-ACTIVE-BOTH-OUTCOMES: a later waiting ack does not demote observed active evidence', async () => {
      state.tasks.value.waiting = [mockTask('sg_demote', { status: 'paused' })]
      const deferred = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(asCancellable(deferred.promise) as never)
      const events = wireMover(state, actions)

      const resumePromise = actions.resume('sg_demote')
      await flushPromises()
      events.handleTaskMove({
        gid: 'sg_demote',
        from: 'waiting',
        to: 'active',
        task: { gid: 'sg_demote', status: 'active' },
      })
      await events.handleTaskDelta({
        type: 'resume',
        gid: 'sg_demote',
        payload: { gid: 'sg_demote', status: 'waiting' },
      })

      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_demote'])

      deferred.resolve(undefined)
      await resumePromise

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_demote'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(mockGetTasks).not.toHaveBeenCalled()
    })

    it('SNAPSHOT-FRESHNESS: a stale full read is rejected and the coalesced recovery applies fresh data', async () => {
      state.tasks.value = { active: [mockTask('a1')], waiting: [], stopped: [] }
      const first = createControlledPromise<{
        active: Task[]
        waiting: Task[]
        stopped: Task[]
      }>()
      mockGetTasks.mockReturnValueOnce(asCancellable(first.promise) as never).mockResolvedValue({
        active: [mockTask('a1')],
        waiting: [],
        stopped: [mockTask('stale-only', { status: 'complete' })],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

      const pending = actions.fetchTasks()
      // A lifecycle mutation crosses before the response lands.
      actions.markLifecycleChanged()
      first.resolve({
        active: [],
        waiting: [],
        stopped: [mockTask('stale-only', { status: 'complete' })],
      })
      await pending

      // The stale response never overwrote membership or cached its rows.
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['a1'])
      expect(state.tasks.value.stopped).toHaveLength(0)
      expect(getMetadataCacheSize()).toBe(0)

      // Exactly one coalesced recovery read runs and its fresh data lands.
      await flushPromises()

      expect(mockGetTasks).toHaveBeenCalledTimes(2)
      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['stale-only'])
      expect(getMetadataCacheSize()).toBeGreaterThan(0)
    })

    it('SNAPSHOT-FRESHNESS: an overlapping older response cannot overwrite the newer read', async () => {
      const first = createControlledPromise<{
        active: Task[]
        waiting: Task[]
        stopped: Task[]
      }>()
      mockGetTasks.mockReturnValueOnce(asCancellable(first.promise) as never)

      const stale = actions.fetchTasks()
      mockGetTasks.mockResolvedValue({
        active: [mockTask('fresh-winner')],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.fetchTasks()
      first.resolve({
        active: [],
        waiting: [],
        stopped: [mockTask('stale-loser', { status: 'complete' })],
      })
      await stale
      await flushPromises()

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['fresh-winner'])
      expect(state.tasks.value.stopped).toHaveLength(0)
      // A superseded full read coalesces exactly one recovery re-read; the
      // newer response stays authoritative.
      expect(mockGetTasks).toHaveBeenCalledTimes(3)
    })

    it('SNAPSHOT-FRESHNESS: a full read superseded by a narrower read recovers uncovered buckets', async () => {
      state.tasks.value = { active: [mockTask('a1')], waiting: [], stopped: [] }
      const first = createControlledPromise<{
        active: Task[]
        waiting: Task[]
        stopped: Task[]
      }>()
      const completed = mockTask('a1', { status: 'complete', downloadSpeed: '0' })
      mockGetTasks.mockReturnValueOnce(asCancellable(first.promise) as never).mockResolvedValue({
        active: [],
        waiting: [],
        stopped: [completed],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      mockGetStoppedTasks.mockResolvedValue([completed] as Task[])

      const pending = actions.fetchTasks()
      // A narrower stopped read lands mid-flight; its response cannot cover
      // the active/waiting buckets the dropped full read still owned.
      await actions.fetchStoppedTasks()
      first.resolve({
        active: [],
        waiting: [],
        stopped: [completed],
      })
      await pending
      await flushPromises()

      // One coalesced full recovery re-read removes the lingering terminal row.
      expect(mockGetTasks).toHaveBeenCalledTimes(2)
      expect(mockGetStoppedTasks).toHaveBeenCalledTimes(1)
      expect(state.tasks.value.active).toHaveLength(0)
      expect(state.tasks.value.stopped.map(t => t.gid)).toEqual(['a1'])
    })

    it('METADATA-FRESHNESS: a late metadata response cannot poison a removed-and-reopened row', async () => {
      const events = wireMover(state, actions)
      state.tasks.value.active = [
        mockTask('gid-meta', { status: 'active', files: [], dir: '', title: '' }),
      ]
      const metaPromise = createControlledPromise<Record<string, Task>>()
      mockGetTaskMetadata.mockReturnValue(metaPromise.promise as never)

      actions.queueMetadataRecovery('gid-meta')
      expect(mockGetTaskMetadata).toHaveBeenCalledWith(['gid-meta'])

      await events.handleTaskDelta({ type: 'remove', gid: 'gid-meta' })
      await events.handleTaskDelta({
        type: 'add',
        gid: 'gid-meta',
        payload: {
          gid: 'gid-meta',
          status: 'active',
          files: [{ path: '/downloads/new.bin', uris: [] }],
          dir: '/new',
        },
      })
      const replacement = state.tasks.value.active.find(t => t.gid === 'gid-meta')
      expect(replacement?.files[0]?.path).toBe('/downloads/new.bin')

      metaPromise.resolve({
        'gid-meta': mockTask('gid-meta', {
          title: 'STALE TITLE',
          files: [{ path: '/downloads/stale.bin', uris: [] }],
          dir: '/stale',
          totalLength: '9',
          completedLength: '0',
        }),
      })
      await flushPromises()

      const row = state.tasks.value.active.find(t => t.gid === 'gid-meta')
      expect(row).toBe(replacement)
      expect(row?.title).not.toBe('STALE TITLE')
      expect(row?.files[0]?.path).toBe('/downloads/new.bin')
      expect(row?.dir).toBe('/new')
      expect(row?.totalLength).not.toBe('9')
    })

    it('METADATA-FRESHNESS: a late metadata response cannot write stale lifecycle fields onto a reopened row', async () => {
      wireMover(state, actions)
      state.tasks.value.stopped = [
        mockTask('gid-reopen-meta', {
          status: 'error',
          errorCode: '9',
          errorMessage: 'disk full',
          downloadSpeed: '0',
          files: [],
          dir: '',
          title: '',
        }),
      ]
      const stoppedRow = state.tasks.value.stopped[0]
      const metaPromise = createControlledPromise<Record<string, Task>>()
      mockGetTaskMetadata.mockReturnValue(metaPromise.promise as never)
      mockBatchResume.mockResolvedValue([{ gid: 'gid-reopen-meta', ok: true }] as never)

      actions.queueMetadataRecovery('gid-reopen-meta')
      expect(mockGetTaskMetadata).toHaveBeenCalledWith(['gid-reopen-meta'])

      // The same row object reopens stopped -> waiting while the metadata
      // IPC is in flight; its lifecycle fields now belong to a newer fact.
      await actions.batchResume(['gid-reopen-meta'])
      const reopened = state.tasks.value.waiting.find(t => t.gid === 'gid-reopen-meta')
      expect(reopened).toBe(stoppedRow)
      expect(reopened?.status).toBe('waiting')

      metaPromise.resolve({
        'gid-reopen-meta': mockTask('gid-reopen-meta', {
          title: 'Hydrated Title',
          files: [{ path: '/downloads/hydrated.bin', uris: [] }],
          dir: '/downloads/hydrated',
          errorCode: '9',
          errorMessage: 'stale disk full',
          downloadSpeed: '888',
        }),
      })
      await flushPromises()

      const row = state.tasks.value.waiting.find(t => t.gid === 'gid-reopen-meta')
      expect(row?.status).toBe('waiting')
      // Metadata fields hydrate; stale lifecycle fields stay rejected.
      expect(row?.files[0]?.path).toBe('/downloads/hydrated.bin')
      expect(row?.dir).toBe('/downloads/hydrated')
      expect(row?.title).toBe('Hydrated Title')
      expect(row?.errorCode).toBe('')
      expect(row?.errorMessage).toBe('')
      expect(row?.downloadSpeed).toBe('0')
    })

    it('PAUSE-VOID: transport rejection does not fabricate paused status or corrupt membership', async () => {
      state.tasks.value.active = [mockTask('sg_pv', { status: 'active' })]
      mockPauseTask.mockRejectedValue(new Error('transport down'))

      await actions.pause('sg_pv')

      // Resolved-void semantics are stream-authoritative: no local paused
      // fabrication, no blanket read, no membership corruption.
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_pv'])
      expect(state.tasks.value.active[0].status).toBe('active')
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(mockGetTasks).not.toHaveBeenCalled()
    })

    it('GENERATION-SUPERSESSION: a newer pause fences an outstanding resume opportunity', async () => {
      state.tasks.value.waiting = [mockTask('sg_fence', { status: 'paused' })]
      const deferred = createControlledPromise<void>()
      mockResumeTask.mockReturnValue(asCancellable(deferred.promise) as never)
      mockPauseTask.mockResolvedValue(undefined as never)
      mockGetTasks.mockResolvedValue({
        active: [],
        waiting: [mockTask('sg_fence', { status: 'paused' })],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      wireMover(state, actions)

      const resumePromise = actions.resume('sg_fence')
      await flushPromises()
      await actions.pause('sg_fence')
      deferred.resolve(undefined)
      await resumePromise

      // The fenced gen cannot push the paused gid to waiting-ack or active.
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['sg_fence'])
      expect(state.tasks.value.active).toHaveLength(0)
      expect(actions.isResumePending('sg_fence')).toBe(false)
    })

    it('DELTA-ONLY-FLIP: real actions/events reopen an ar_ stopped row into waiting without a move', async () => {
      const events = wireMover(state, actions)
      state.tasks.value.stopped = [
        mockTask('ar_flip', { status: 'error', errorCode: '1', errorMessage: 'fail' }),
      ]

      await events.handleTaskDelta({
        type: 'resume',
        gid: 'ar_flip',
        payload: { gid: 'ar_flip', status: 'waiting' },
      })

      expect(state.tasks.value.stopped).toHaveLength(0)
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['ar_flip'])
      expect(state.tasks.value.waiting[0].status).toBe('waiting')
      expect(state.tasks.value.active).toHaveLength(0)
      expect(mockGetTasks).not.toHaveBeenCalled()
    })

    it('QUEUED-THEN-STARTED: real actions/events place a queued add into waiting then a started add into active', async () => {
      const events = wireMover(state, actions)

      await events.handleTaskDelta({
        type: 'add',
        gid: 'ar_q',
        payload: mockTask('ar_q', { status: 'waiting' }) as unknown as Record<string, unknown>,
      })
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['ar_q'])
      expect(state.tasks.value.active).toHaveLength(0)

      await events.handleTaskDelta({
        type: 'add',
        gid: 'ar_q',
        payload: { gid: 'ar_q', status: 'active' },
      })
      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['ar_q'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(mockGetTasks).not.toHaveBeenCalled()
    })

    it('POLL-RECOVERY-10S: a withheld start transition self-heals through the next authoritative read', async () => {
      wireMover(state, actions)
      state.tasks.value.waiting = [mockTask('ar_heal', { status: 'paused' })]
      mockResumeTask.mockResolvedValue(undefined as never)

      await actions.resume('ar_heal')
      expect(state.tasks.value.waiting.map(t => t.gid)).toEqual(['ar_heal'])
      expect(state.tasks.value.active).toHaveLength(0)

      // No delta/move arrives; the next cache read is authoritative.
      mockGetTasks.mockResolvedValue({
        active: [mockTask('ar_heal', { status: 'active' })],
        waiting: [],
        stopped: [],
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      await actions.fetchTasks()

      expect(state.tasks.value.active.map(t => t.gid)).toEqual(['ar_heal'])
      expect(state.tasks.value.waiting).toHaveLength(0)
      expect(mockResumeTask).toHaveBeenCalledTimes(1)
      expect(actions.isResumePending('ar_heal')).toBe(false)
    })

    it('GROUP-ERROR-RESUME: real task/group stores admit stopped-error candidates and settle strictly on succeeded items', async () => {
      const group = {
        id: 'dg-mixed',
        kind: 'batch',
        name: 'Mixed',
        folder_name: 'Mixed',
        dir: '/downloads/mixed',
        item_count: 4,
        created_at: 1,
      }
      const taskStore = useTaskStore()
      const groupStore = useDownloadGroupStore()
      taskStore.tasks = {
        active: [],
        waiting: [
          mockTask('g_paused', { status: 'paused', download_group: group }),
          mockTask('g_queued', { status: 'waiting', download_group: group }),
        ],
        stopped: [
          mockTask('g_err_a', { status: 'error', errorCode: '9', download_group: group }),
          mockTask('g_err_b', { status: 'error', errorCode: '8', download_group: group }),
          mockTask('g_done', { status: 'complete', download_group: group }),
        ],
      }

      let moveHandler: ((move: unknown) => void) | undefined
      vi.mocked(subscribeToTaskMoveEvent).mockImplementation(cb => {
        moveHandler = cb as (move: unknown) => void
      })
      // The initial sync reads report the same members, so subscription
      // start-up does not disturb the scenario.
      const liveRead = {
        active: [] as Task[],
        waiting: [
          mockTask('g_paused', { status: 'paused', download_group: group }),
          mockTask('g_queued', { status: 'waiting', download_group: group }),
        ],
      }
      const stoppedRead = [
        mockTask('g_err_a', { status: 'error', errorCode: '9', download_group: group }),
        mockTask('g_err_b', { status: 'error', errorCode: '8', download_group: group }),
        mockTask('g_done', { status: 'complete', download_group: group }),
      ]
      mockGetActiveTasks.mockResolvedValue(liveRead as never)
      mockGetStoppedTasks.mockResolvedValue(stoppedRead as never)
      mockGetTasks.mockResolvedValue({
        ...liveRead,
        stopped: stoppedRead,
      } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })
      taskStore.startPolling(1000)
      expect(moveHandler).toBeDefined()
      await flushPromises()
      // Post-seed: the group op itself must not issue extra task reads.
      mockGetTasks.mockClear()
      mockGetActiveTasks.mockClear()

      const ipc = createControlledPromise<{
        items: { gid: string; status: string }[]
      }>()
      mockResumeDownloadGroup.mockReturnValue(asCancellable(ipc.promise) as never)

      const opPromise = groupStore.resumeGroup('dg-mixed')
      await flushPromises()
      expect(mockResumeDownloadGroup).toHaveBeenCalledWith('dg-mixed')

      // Held active evidence during the group IPC.
      moveHandler?.({
        gid: 'g_err_a',
        from: 'stopped',
        to: 'active',
        task: { gid: 'g_err_a', status: 'active' },
      })

      ipc.resolve({
        items: [
          { gid: 'g_paused', status: 'succeeded' },
          { gid: 'g_err_a', status: 'succeeded' },
          { gid: 'g_err_b', status: 'failed' },
        ],
      })
      await opPromise

      const waiting = taskStore.waitingTasks.map(t => t.gid)
      const active = taskStore.activeTasks.map(t => t.gid)
      const stopped = taskStore.stoppedTasks.map(t => t.gid)
      expect(waiting).toEqual(['g_paused', 'g_queued'])
      expect(active).toEqual(['g_err_a'])
      expect(stopped).toEqual(['g_err_b', 'g_done'])
      // The failed item and non-candidates were never moved or re-requested.
      expect(mockGetTasks).not.toHaveBeenCalled()
      expect(mockGetActiveTasks).not.toHaveBeenCalled()
      taskStore.stopPolling(true)
    })
  })
})
