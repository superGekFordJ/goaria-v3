import { beforeEach, describe, expect, it, vi } from 'vitest'
import { isProxy, isReactive } from 'vue'
import { setupState } from '../state'
import { setupActions } from '../actions'
import { setupEvents } from '../events'
import { clearMetadataCache } from '../metadata'
import type { TaskPolling } from '../polling'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

const { mockGetTasks, mockGetStoppedTasks, mockGetFullSnapshot, mockBatchResume } = vi.hoisted(
  () => ({
    mockGetTasks: vi.fn(),
    mockGetStoppedTasks: vi.fn(),
    mockGetFullSnapshot: vi.fn(),
    mockBatchResume: vi.fn(),
  }),
)

vi.mock('../../../../bindings/goaria-v3/internal/wailsapp/app.js', () => ({
  GetTasks: mockGetTasks,
  GetActiveTasks: vi.fn().mockResolvedValue({ active: [], waiting: [] }),
  GetStoppedTasks: mockGetStoppedTasks,
  GetTaskMetadata: vi.fn().mockResolvedValue({}),
  AddUri: vi.fn(),
  BatchAddUri: vi.fn(),
  PauseTask: vi.fn(),
  ResumeTask: vi.fn(),
  RemoveTask: vi.fn(),
  OpenFolder: vi.fn(),
  BatchPause: vi.fn(),
  BatchResume: mockBatchResume,
  BatchRemove: vi.fn(),
  GetFullSnapshot: mockGetFullSnapshot,
  MinimizeToTray: vi.fn(),
  UpdateTrayState: vi.fn(),
}))

vi.mock('../../downloadGroups', () => ({
  useDownloadGroupStore: () => ({
    addPlaceholdersFromDownloadGroups: vi.fn(),
    scheduleAutoSyncImmediate: vi.fn(),
  }),
}))

function mockTask(gid: string, overrides: Partial<Task> = {}): Task {
  return {
    gid,
    status: 'complete',
    totalLength: '1024',
    completedLength: '1024',
    downloadSpeed: '0',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [
      {
        path: `/downloads/${gid}.zip`,
        uris: [{ uri: `https://example.com/${gid}.zip`, status: 'used' }],
      },
    ],
    ...overrides,
  } as Task
}

describe('stopped tasks markRaw reactivity pruning', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    clearMetadataCache()
  })

  it('marks stopped tasks raw across fetchStoppedTasks, fetchTasks, and syncFromSnapshot while keeping active/waiting reactive', async () => {
    const state = setupState()
    const actions = setupActions(state)

    mockGetStoppedTasks.mockResolvedValueOnce([mockTask('s1'), mockTask('s2', { status: 'error' })])
    await actions.fetchStoppedTasks()

    expect(state.tasks.value.stopped).toHaveLength(2)
    for (const task of state.tasks.value.stopped) {
      expect(isProxy(task)).toBe(false)
      expect(isReactive(task)).toBe(false)
      expect(isReactive(task.files)).toBe(false)
    }

    mockGetTasks.mockResolvedValueOnce({
      active: [mockTask('a1', { status: 'active' })],
      waiting: [mockTask('w1', { status: 'paused' })],
      stopped: [mockTask('s3', { status: 'complete' })],
    })
    await actions.fetchTasks()

    expect(isReactive(state.tasks.value.active[0])).toBe(true)
    expect(isReactive(state.tasks.value.waiting[0])).toBe(true)
    expect(isProxy(state.tasks.value.stopped[0])).toBe(false)
    expect(isReactive(state.tasks.value.stopped[0])).toBe(false)

    mockGetFullSnapshot.mockResolvedValueOnce({
      tasks: {
        active: [mockTask('a2', { status: 'active' })],
        waiting: [],
        stopped: [mockTask('s4', { status: 'complete' })],
      },
    })
    await actions.syncFromSnapshot()

    expect(isReactive(state.tasks.value.active[0])).toBe(true)
    expect(isProxy(state.tasks.value.stopped[0])).toBe(false)
    expect(isReactive(state.tasks.value.stopped[0])).toBe(false)
  })

  it('marks tasks folded into stopped raw and replaces row identity when a stopped row is updated in place', async () => {
    const state = setupState()
    const actions = setupActions(state)
    const events = setupEvents(state, actions, {} as TaskPolling)
    actions.setTaskMover(events.moveTasks)

    state.tasks.value.active = [mockTask('a-to-s', { status: 'active', completedLength: '512' })]
    expect(isReactive(state.tasks.value.active[0])).toBe(true)

    events.handleTaskMove({
      gid: 'a-to-s',
      from: 'active',
      to: 'stopped',
      task: { gid: 'a-to-s', status: 'error', errorCode: '1', errorMessage: 'initial' },
    })

    const firstStoppedRow = state.tasks.value.stopped[0]
    expect(isProxy(firstStoppedRow)).toBe(false)
    expect(isReactive(firstStoppedRow)).toBe(false)

    // Subsequent delta updates errorMessage on the already-stopped row:
    // row reference must change so prop-based observers re-render, and stay raw.
    await events.handleTaskDelta({
      type: 'error',
      gid: 'a-to-s',
      payload: {
        gid: 'a-to-s',
        status: 'error',
        errorCode: '9',
        errorMessage: 'insufficient disk space',
      },
    })

    const updatedStoppedRow = state.tasks.value.stopped[0]
    expect(updatedStoppedRow).not.toBe(firstStoppedRow)
    expect(updatedStoppedRow.errorCode).toBe('9')
    expect(updatedStoppedRow.errorMessage).toBe('insufficient disk space')
    expect(isProxy(updatedStoppedRow)).toBe(false)
    expect(isReactive(updatedStoppedRow)).toBe(false)
  })

  it('restores Vue reactivity when a markRaw stopped error task is reopened into waiting/active', async () => {
    const state = setupState()
    const actions = setupActions(state)
    const events = setupEvents(state, actions, {} as TaskPolling)
    actions.setTaskMover(events.moveTasks)

    mockGetStoppedTasks.mockResolvedValueOnce([
      mockTask('err-reopen', {
        status: 'error',
        errorCode: '1',
        errorMessage: 'timeout',
        completedLength: '256',
      }),
    ])
    await actions.fetchStoppedTasks()

    expect(isProxy(state.tasks.value.stopped[0])).toBe(false)
    expect(isReactive(state.tasks.value.stopped[0])).toBe(false)

    mockBatchResume.mockResolvedValueOnce([{ gid: 'err-reopen', ok: true }])
    await actions.batchResume(['err-reopen'])

    expect(state.tasks.value.stopped).toHaveLength(0)
    expect(state.tasks.value.waiting).toHaveLength(1)
    const reopenedWaiting = state.tasks.value.waiting[0]
    expect(isProxy(reopenedWaiting)).toBe(true)
    expect(isReactive(reopenedWaiting)).toBe(true)
    expect(reopenedWaiting.status).toBe('waiting')
    expect(reopenedWaiting.errorCode).toBe('')
  })
})
