import { describe, it, expect, beforeEach, vi } from 'vitest'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'
import { setupState } from '../state'
import { setupActions } from '../actions'
import { setupEvents } from '../events'
import type { TaskPolling } from '../polling'
import { isDuplicateUri } from '../../../utils/url'
import { clearMetadataCache } from '../metadata'

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
  GetTaskMetadata,
  RemoveTask,
} from '../../../../bindings/goaria-v3/internal/wailsapp/app.js'

const mockGetTasks = vi.mocked(GetTasks)
const mockGetTaskMetadata = vi.mocked(GetTaskMetadata)
const mockRemoveTask = vi.mocked(RemoveTask)

function task(gid: string, status: string, uri?: string, overrides: Partial<Task> = {}): Task {
  return {
    gid,
    status,
    totalLength: '1000',
    completedLength: status === 'active' ? '500' : '1000',
    downloadSpeed: status === 'active' ? '100' : '0',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: uri ? [{ path: `/downloads/${gid}.bin`, uris: [{ uri, status: 'used' }] }] : [],
    ...overrides,
  } as Task
}

describe('duplicate URI lookup index', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    clearMetadataCache()
  })

  it('preserves empty input, trim, exact-match, and allUris.has behavior', () => {
    const state = setupState()
    const activeUri = 'https://example.com/active.bin'
    const waitingUri = 'https://example.com/waiting.bin'
    const stoppedUri = 'https://example.com/stopped.bin'

    state.tasks.value = {
      active: [task('active', 'active', activeUri)],
      waiting: [task('waiting', 'waiting', waitingUri)],
      stopped: [task('stopped', 'complete', stoppedUri)],
    }

    expect(isDuplicateUri('', { allUris: state.allUris.value })).toBe(false)
    expect(isDuplicateUri('   ', { allUris: state.allUris.value })).toBe(false)
    expect(isDuplicateUri(` ${activeUri} `, { allUris: state.allUris.value })).toBe(true)
    expect(isDuplicateUri(`${activeUri}?x=1`, { allUris: state.allUris.value })).toBe(false)
    expect(state.allUris.value.has(activeUri)).toBe(true)
    expect(state.allUris.value.has(waitingUri)).toBe(true)
    expect(state.allUris.value.has(stoppedUri)).toBe(true)
    expect([...state.allUris.value].sort()).toEqual([activeUri, stoppedUri, waitingUri].sort())
  })

  it('keeps stopped URI membership when only active/waiting references change', () => {
    const state = setupState()
    const stopped = [task('stopped', 'complete', 'https://example.com/stopped.bin')]

    state.tasks.value = {
      active: [task('active-1', 'active', 'https://example.com/active-1.bin')],
      waiting: [task('waiting-1', 'waiting', 'https://example.com/waiting-1.bin')],
      stopped,
    }

    expect(state.allUris.value.has('https://example.com/stopped.bin')).toBe(true)

    state.tasks.value = {
      active: [task('active-2', 'active', 'https://example.com/active-2.bin')],
      waiting: [task('waiting-2', 'waiting', 'https://example.com/waiting-2.bin')],
      stopped,
    }

    expect(state.allUris.value.has('https://example.com/stopped.bin')).toBe(true)
    expect(state.allUris.value.has('https://example.com/active-1.bin')).toBe(false)
    expect(state.allUris.value.has('https://example.com/active-2.bin')).toBe(true)
  })

  it('drops removed URIs after full replacement and optimistic removal', async () => {
    const state = setupState()
    const actions = setupActions(state)
    const oldUri = 'https://example.com/old.bin'
    const currentUri = 'https://example.com/current.bin'

    state.tasks.value = {
      active: [task('old', 'active', oldUri)],
      waiting: [],
      stopped: [],
    }
    expect(state.allUris.value.has(oldUri)).toBe(true)

    mockGetTasks.mockResolvedValue({
      active: [task('current', 'active', currentUri)],
      waiting: [],
      stopped: [],
    } as unknown as { active: Task[]; waiting: Task[]; stopped: Task[] })

    await actions.fetchTasks()
    expect(state.allUris.value.has(oldUri)).toBe(false)
    expect(state.allUris.value.has(currentUri)).toBe(true)

    mockRemoveTask.mockResolvedValue(undefined)
    await actions.remove('current', false)
    expect(state.allUris.value.has(currentUri)).toBe(false)
  })

  it('sees URI metadata that arrives after a Lite task', async () => {
    vi.useFakeTimers()
    try {
      const state = setupState()
      const actions = setupActions(state)
      const events = setupEvents(state, actions, {} as TaskPolling)
      const enrichedUri = 'https://example.com/enriched.bin'

      state.tasks.value.active = [task('lite', 'active')]
      mockGetTaskMetadata.mockResolvedValue({
        lite: task('lite', 'active', enrichedUri),
      } as Record<string, Task>)

      await events.handleTaskDelta({
        type: 'progress',
        gid: 'lite',
        payload: { completedLength: '750' },
      })
      await vi.runAllTimersAsync()

      expect(state.allUris.value.has(enrichedUri)).toBe(true)
      expect(isDuplicateUri(` ${enrichedUri} `, { allUris: state.allUris.value })).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('remains correct after active to stopped, waiting to active, and stopped removal flows', async () => {
    const state = setupState()
    const actions = setupActions(state)
    const events = setupEvents(state, actions, {} as TaskPolling)
    const activeUri = 'https://example.com/active.bin'
    const waitingUri = 'https://example.com/waiting.bin'
    const stoppedUri = 'https://example.com/stopped.bin'

    state.tasks.value = {
      active: [task('active', 'active', activeUri)],
      waiting: [task('waiting', 'waiting', waitingUri)],
      stopped: [task('stopped', 'complete', stoppedUri)],
    }

    await events.handleTaskDelta({ type: 'complete', gid: 'active' })
    expect(state.allUris.value.has(activeUri)).toBe(true)
    expect(state.tasks.value.stopped.some(item => item.gid === 'active')).toBe(true)

    events.handleTaskMove({ gid: 'waiting', from: 'waiting', to: 'active', task: {} })
    expect(state.allUris.value.has(waitingUri)).toBe(true)
    expect(state.tasks.value.active.some(item => item.gid === 'waiting')).toBe(true)

    mockRemoveTask.mockResolvedValue(undefined)
    await actions.remove('stopped', false)
    expect(state.allUris.value.has(stoppedUri)).toBe(false)
    expect(state.allUris.value.has(activeUri)).toBe(true)
    expect(state.allUris.value.has(waitingUri)).toBe(true)
  })
})
