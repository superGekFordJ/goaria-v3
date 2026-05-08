import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setupState } from '../state'
import { setupActions } from '../actions'
import { setupEvents } from '../events'
import { applyMetadataFromCache, cacheMetadata, clearMetadataCache } from '../metadata'
import { mergeTasks } from '../utils'
import type { TaskPolling } from '../polling'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

vi.mock('../../../../bindings/goaria-v3/app.js', () => ({
  GetTasks: vi.fn(),
  GetActiveTasks: vi.fn(),
  GetStoppedTasks: vi.fn(),
  GetTaskMetadata: vi.fn(),
  AddUri: vi.fn(),
  BatchAddUri: vi.fn(),
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

import { GetActiveTasks, GetTaskMetadata } from '../../../../bindings/goaria-v3/app.js'

const mockGroup = {
  id: 'dg-group-metadata',
  kind: 'batch',
  name: 'Batch 2026-05-07 dg-group-metadata',
  folder_name: 'Batch 2026-05-07 dg-group-metadata',
  dir: '/downloads/Batch 2026-05-07 dg-group-metadata',
  item_count: 5,
  created_at: 1770000000,
}

function mockTask(gid: string, overrides: Partial<Task> = {}): Task {
  return {
    gid,
    status: 'active',
    totalLength: '1000',
    completedLength: '100',
    downloadSpeed: '10',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: `/downloads/${gid}.bin`, uris: [] }],
    ...overrides,
  } as Task
}

function flushPromises() {
  return new Promise<void>(resolve => setTimeout(resolve, 0))
}

describe('download_group metadata retention', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    clearMetadataCache()
    vi.mocked(GetTaskMetadata).mockResolvedValue({} as Record<string, Task>)
  })

  it('caches and reapplies download_group even when files are missing', () => {
    cacheMetadata(mockTask('gid-cache', { files: [], dir: '', download_group: mockGroup }))

    const enriched = applyMetadataFromCache(mockTask('gid-cache', { files: [], dir: '' }))

    expect(enriched.download_group?.id).toBe('dg-group-metadata')
    expect(enriched.files).toHaveLength(0)
  })

  it('preserves richer download_group during polling merge when incoming payload is sparse', () => {
    const oldTask = mockTask('gid-merge', {
      files: [],
      dir: '',
      completedLength: '100',
      download_group: mockGroup,
    })

    const incoming = mockTask('gid-merge', {
      files: [],
      dir: '',
      completedLength: '200',
      download_group: undefined,
    })

    const result = mergeTasks([oldTask], [incoming])

    expect(result.changed).toBe(true)
    expect(result.merged[0].completedLength).toBe('200')
    expect(result.merged[0].download_group?.id).toBe('dg-group-metadata')
  })

  it('applies recovered download_group metadata from GetTaskMetadata', async () => {
    const state = setupState()
    const actions = setupActions(state)

    vi.mocked(GetActiveTasks).mockResolvedValue({
      active: [mockTask('gid-recover', { files: [], dir: '', totalLength: '0' })],
      waiting: [],
    } as unknown as { active: Task[]; waiting: Task[] })
    vi.mocked(GetTaskMetadata).mockResolvedValue({
      'gid-recover': mockTask('gid-recover', {
        files: [{ path: '/downloads/recover.bin', uris: [] }],
        dir: '/downloads',
        totalLength: '2048',
        download_group: mockGroup,
      }),
    } as Record<string, Task>)

    await actions.fetchActiveTasks()
    await flushPromises()

    expect(state.tasks.value.active).toHaveLength(1)
    expect(state.tasks.value.active[0].download_group?.id).toBe('dg-group-metadata')
    expect(state.tasks.value.active[0].files[0].path).toBe('/downloads/recover.bin')
  })

  it('preserves download_group through sparse add and move event merges', async () => {
    const state = setupState()
    const actions = setupActions(state)
    const events = setupEvents(state, actions, {} as TaskPolling)

    state.tasks.value.active = [mockTask('gid-event', { download_group: mockGroup })]

    await events.handleTaskDelta({
      type: 'add',
      gid: 'gid-event',
      payload: {
        gid: 'gid-event',
        status: 'active',
        files: [],
        dir: '',
        totalLength: '0',
        completedLength: '200',
      },
    })

    expect(state.tasks.value.active[0].download_group?.id).toBe('dg-group-metadata')

    events.handleTaskMove({
      gid: 'gid-event',
      from: 'active',
      to: 'waiting',
      task: {
        gid: 'gid-event',
        status: 'waiting',
        files: [],
        dir: '',
      },
    })

    expect(state.tasks.value.waiting).toHaveLength(1)
    expect(state.tasks.value.waiting[0].download_group?.id).toBe('dg-group-metadata')
  })
})
