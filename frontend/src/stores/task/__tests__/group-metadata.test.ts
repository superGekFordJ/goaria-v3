import { beforeEach, describe, expect, it, vi } from 'vitest'
import { setupState } from '../state'
import { setupActions } from '../actions'
import { setupEvents } from '../events'
import {
  applyMetadataFromCache,
  cacheMetadata,
  clearMetadataCache,
  getMetadataCacheSize,
} from '../metadata'
import {
  buildVisibleTaskGroupHints,
  cloneTaskGroupMetadata,
  getTaskGroupHint,
  isTaskGroupEqual,
} from '../grouping'
import type { TaskPolling } from '../polling'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'

vi.mock('../../../../bindings/goaria-v3/internal/wailsapp/app.js', () => ({
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

const group = {
  id: 'dg-opaque-1',
  kind: 'batch',
  name: 'Batch 2026-05-07 dg-opaque-1',
  folder_name: 'Batch 2026-05-07 dg-opaque-1',
  dir: '/downloads/Batch 2026-05-07 dg-opaque-1',
  item_count: 5,
  created_at: 1770000000,
}

describe('task group metadata helpers and retention', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    clearMetadataCache()
  })

  it('returns null for ungrouped tasks and normalizes grouped task hints', () => {
    expect(getTaskGroupHint(mockTask('plain'))).toBeNull()

    const hint = getTaskGroupHint(mockTask('grouped', { download_group: group }))
    expect(hint).toMatchObject({
      groupKey: 'dg-opaque-1',
      folderLabel: 'Batch 2026-05-07 dg-opaque-1',
      folderPath: '/downloads/Batch 2026-05-07 dg-opaque-1',
      totalCount: 5,
      isAutoFoldered: true,
    })
  })

  it('caches and reapplies group metadata independently of file metadata', () => {
    cacheMetadata(mockTask('lite-group', { files: [], dir: '', download_group: group }))
    expect(getMetadataCacheSize()).toBe(1)

    const enriched = applyMetadataFromCache(mockTask('lite-group', { files: [], dir: '' }))
    expect(enriched.download_group?.id).toBe(group.id)
    expect(enriched.files).toHaveLength(0)
  })

  it('preserves download_group name_status through clone equality and metadata cache updates', () => {
    const pendingGroup = { ...group, name_status: 'pending' }
    const stableGroup = { ...group, name: 'Stable batch name', name_status: 'stable' }

    expect(
      cloneTaskGroupMetadata(mockTask('name-status', { download_group: pendingGroup }))
        ?.name_status,
    ).toBe('pending')
    expect(isTaskGroupEqual(pendingGroup, { ...pendingGroup })).toBe(true)
    expect(isTaskGroupEqual(pendingGroup, stableGroup)).toBe(false)

    cacheMetadata(mockTask('name-status', { files: [], dir: '', download_group: pendingGroup }))
    const pendingEnriched = applyMetadataFromCache(
      mockTask('name-status', { files: [], dir: '', download_group: undefined }),
    )
    expect(pendingEnriched.download_group?.name_status).toBe('pending')

    cacheMetadata(mockTask('name-status', { files: [], dir: '', download_group: stableGroup }))
    const stableEnriched = applyMetadataFromCache(
      mockTask('name-status', { files: [], dir: '', download_group: undefined }),
    )
    expect(stableEnriched.download_group?.name).toBe('Stable batch name')
    expect(stableEnriched.download_group?.name_status).toBe('stable')
  })

  it('preserves cached group metadata when applying a Lite payload', () => {
    cacheMetadata(mockTask('full', { download_group: group }))

    const lite = mockTask('full', {
      files: [],
      dir: '',
      totalLength: '0',
      download_group: undefined,
    })
    const enriched = applyMetadataFromCache(lite)

    expect(enriched.files[0].path).toBe('/downloads/full.bin')
    expect(enriched.download_group?.id).toBe(group.id)
  })

  it('preserves group metadata through metadata recovery and sparse event merge', async () => {
    const state = setupState()
    const actions = setupActions(state)
    const events = setupEvents(state, actions, {} as TaskPolling)

    state.tasks.value.active = [mockTask('gid-rich', { download_group: group })]

    await events.handleTaskDelta({
      type: 'add',
      gid: 'gid-rich',
      payload: {
        gid: 'gid-rich',
        status: 'active',
        files: [],
        dir: '',
        totalLength: '0',
      },
    })

    expect(state.tasks.value.active[0].download_group?.id).toBe(group.id)

    events.handleTaskMove({
      gid: 'gid-rich',
      from: 'active',
      to: 'waiting',
      task: { gid: 'gid-rich', status: 'waiting', files: [], dir: '' },
    })

    expect(state.tasks.value.waiting[0].download_group?.id).toBe(group.id)
  })

  it('builds visible group hints with visible counts and ordinals', () => {
    const hints = buildVisibleTaskGroupHints([
      mockTask('a', { download_group: group }),
      mockTask('b', { download_group: group }),
      mockTask('c'),
    ])

    expect(hints.get('a')).toMatchObject({ visibleCount: 2, ordinal: 1, totalCount: 5 })
    expect(hints.get('b')).toMatchObject({ visibleCount: 2, ordinal: 2, totalCount: 5 })
    expect(hints.has('c')).toBe(false)
  })

  it('keeps task and group selections in separate reactive sets', () => {
    const state = setupState()
    const actions = setupActions(state)

    actions.toggleSelect('gid-one')
    actions.toggleSelectGroup('dg-one')

    expect(state.getSelectedGids.value).toEqual(['gid-one'])
    expect(state.getSelectedGroupKeys.value).toEqual(['dg-one'])
    expect(state.selectedTaskCount.value).toBe(1)
    expect(state.selectedGroupCount.value).toBe(1)
    expect(state.selectedCount.value).toBe(2)
    expect(state.isSelected('dg-one')).toBe(false)
    expect(state.isGroupSelected('dg-one')).toBe(true)

    actions.clearSelectedGroup('dg-one')
    expect(state.getSelectedGids.value).toEqual(['gid-one'])
    expect(state.getSelectedGroupKeys.value).toEqual([])

    actions.selectAll(['gid-two'], ['dg-two'])
    expect(state.getSelectedGids.value).toEqual(['gid-two'])
    expect(state.getSelectedGroupKeys.value).toEqual(['dg-two'])

    actions.clearSelection()
    expect(state.getSelectedGids.value).toEqual([])
    expect(state.getSelectedGroupKeys.value).toEqual([])
  })
})
