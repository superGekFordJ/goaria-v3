import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick, reactive } from 'vue'
import {
  useDownloadGroupStore,
  DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS,
  DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES,
  DOWNLOAD_GROUP_PENDING_NAME_REFETCH_MS,
  DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS,
} from '../downloadGroups'
import {
  buildDownloadGroupTaskAutoSyncSignature,
  buildInlineTaskListEntries,
  isTerminalDownloadGroupCard,
} from '../downloadGroups'
import {
  snapshotGroupResumeHoldGids,
  succeededOperationItemGids,
} from '../downloadGroups/utils'
import type {
  DownloadGroupCard,
  DownloadGroupDetailEnvelope,
  DownloadGroupListEnvelope,
  DownloadGroupOperationResult,
} from '../../../bindings/goaria-v3/internal/downloadgroups/models'
import type { DownloadGroup, Task } from '../../../bindings/goaria-v3/internal/rpc/models'

const bindingMocks = vi.hoisted(() => ({
  GetDownloadGroups: vi.fn(),
  GetDownloadGroupDetail: vi.fn(),
  PauseDownloadGroup: vi.fn(),
  ResumeDownloadGroup: vi.fn(),
  RemoveDownloadGroup: vi.fn(),
  OpenDownloadGroupFolder: vi.fn(),
  PauseTask: vi.fn(),
  ResumeTask: vi.fn(),
  RemoveTask: vi.fn(),
  BatchPause: vi.fn(),
  BatchResume: vi.fn(),
  BatchRemove: vi.fn(),
  OpenFolder: vi.fn(),
}))

vi.mock('../../../bindings/goaria-v3/internal/wailsapp/app.js', () => bindingMocks)

const taskStoreMock = vi.hoisted(() => ({
  __state: null as null | { activeTasks: Task[]; waitingTasks: Task[]; stoppedTasks: Task[] },
  get activeTasks() {
    return this.__state?.activeTasks ?? []
  },
  set activeTasks(value: Task[]) {
    if (this.__state) this.__state.activeTasks = value
  },
  get waitingTasks() {
    return this.__state?.waitingTasks ?? []
  },
  set waitingTasks(value: Task[]) {
    if (this.__state) this.__state.waitingTasks = value
  },
  get stoppedTasks() {
    return this.__state?.stoppedTasks ?? []
  },
  set stoppedTasks(value: Task[]) {
    if (this.__state) this.__state.stoppedTasks = value
  },
  fetchTasks: vi.fn().mockResolvedValue(undefined),
  runHeldResume: vi.fn(
    async (
      _gids: string[],
      getEngineOkGids: () => Promise<string[]>,
      _options?: { recoverSnapshot?: boolean },
    ) => {
      await getEngineOkGids()
    },
  ),
  clearSelection: vi.fn(),
  clearSelectedGroup: vi.fn(),
  selectedGids: new Set<string>(),
  getSelectedGids: ['gid-selected-one', 'gid-selected-two'],
}))

vi.mock('../task', () => ({
  useTaskStore: () => taskStoreMock,
}))

function group(id: string, itemCount = 2): DownloadGroup {
  return {
    id,
    kind: 'batch',
    name: `Batch ${id}`,
    folder_name: `Folder ${id}`,
    dir: `/downloads/${id}`,
    item_count: itemCount,
    created_at: 1770000000,
  } as DownloadGroup
}

function counts() {
  return {
    expected: 2,
    resolved: 2,
    missing: 0,
    active: 1,
    waiting: 0,
    paused: 0,
    complete: 1,
    error: 0,
    history_only: 0,
  }
}

function card(id: string, patch: Partial<DownloadGroupCard> = {}): DownloadGroupCard {
  return {
    group_key: id,
    download_group: group(id),
    kind: 'batch',
    display_name: `Batch ${id}`,
    fallback_name: `Fallback ${id}`,
    name_status: 'fallback',
    status: 'active',
    degraded: false,
    warnings: [],
    counts: counts(),
    total_length: '2000',
    completed_length: '1000',
    download_speed: '25',
    progress: 0.5,
    created_at: 1770000000,
    updated_at: 1770000100,
    folder_label: `Folder ${id}`,
    folder_path_hint: `/downloads/${id}`,
    has_folder: true,
    ...patch,
  } as DownloadGroupCard
}

function envelope(cards: DownloadGroupCard[] = []): DownloadGroupListEnvelope {
  return {
    groups: cards,
    updated_at: 1770000200,
    degraded: false,
    warnings: [],
  } as DownloadGroupListEnvelope
}

function operationResult(
  patch: Partial<DownloadGroupOperationResult> = {},
): DownloadGroupOperationResult {
  return {
    group_key: 'dg-op',
    action: 'pause',
    ok: true,
    found: true,
    noop: false,
    total_targets: 1,
    succeeded: 1,
    skipped: 0,
    failed: 0,
    items: [{ gid: 'gid-op', status: 'succeeded', code: 'paused', message: 'backend detail' }],
    warnings: [],
    refresh: { tasks: false, groups: false, detail: false },
    updated_at: 1770000500,
    ...patch,
  } as DownloadGroupOperationResult
}

function task(gid: string, status: string): Task {
  return {
    gid,
    status,
    totalLength: '100',
    completedLength: status === 'complete' ? '100' : '10',
    downloadSpeed: '1',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: `/downloads/${gid}.bin`, uris: [] }],
  } as Task
}

function groupedTask(
  gid: string,
  groupId: string,
  patch: Partial<Task> = {},
  groupPatch: Partial<DownloadGroup> = {},
): Task {
  return {
    ...task(gid, patch.status ?? 'active'),
    ...patch,
    download_group: { ...group(groupId), ...groupPatch, id: groupId },
  } as Task
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

describe('download group store', () => {
  beforeEach(() => {
    vi.useRealTimers()
    setActivePinia(createPinia())
    vi.clearAllMocks()
    taskStoreMock.fetchTasks.mockResolvedValue(undefined)
    taskStoreMock.runHeldResume.mockImplementation(
      async (
        _gids: string[],
        getEngineOkGids: () => Promise<string[]>,
        _options?: { recoverSnapshot?: boolean },
      ) => {
        await getEngineOkGids()
      },
    )
    taskStoreMock.clearSelection.mockClear()
    taskStoreMock.clearSelectedGroup.mockClear()
    taskStoreMock.selectedGids = new Set(['gid-selected-one', 'gid-selected-two'])
    taskStoreMock.getSelectedGids = ['gid-selected-one', 'gid-selected-two']
    taskStoreMock.__state = reactive({ activeTasks: [], waitingTasks: [], stoppedTasks: [] })
  })

  it('fetchGroups merges backend cards and preserves unchanged card identity', async () => {
    const firstCard = card('dg-one')
    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(envelope([firstCard]))
    const store = useDownloadGroupStore()

    await store.fetchGroups()
    const preserved = store.backendCards[0]

    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(
      envelope([{ ...firstCard } as DownloadGroupCard]),
    )
    await store.fetchGroups()

    expect(store.backendCards[0]).toBe(preserved)
  })

  it('buildDownloadGroupTaskAutoSyncSignature changes for grouped progress speed status and list moves', () => {
    const base = groupedTask('gid-one', 'dg-signature', {
      completedLength: '10',
      totalLength: '100',
      downloadSpeed: '1',
      status: 'active',
    })
    const original = buildDownloadGroupTaskAutoSyncSignature([base], [], [])
    const progressChanged = buildDownloadGroupTaskAutoSyncSignature(
      [{ ...base, completedLength: '20', downloadSpeed: '4' }],
      [],
      [],
    )
    const statusChanged = buildDownloadGroupTaskAutoSyncSignature(
      [{ ...base, status: 'waiting', errorCode: '3' }],
      [],
      [],
    )
    const moved = buildDownloadGroupTaskAutoSyncSignature([], [base], [])
    const namingChanged = buildDownloadGroupTaskAutoSyncSignature(
      [
        groupedTask(
          'gid-one',
          'dg-signature',
          { completedLength: '10', totalLength: '100', downloadSpeed: '1', status: 'active' },
          { name_status: 'stable', name: 'Stable Name', folder_name: 'Folder Name' },
        ),
      ],
      [],
      [],
    )

    expect(progressChanged).not.toBe(original)
    expect(statusChanged).not.toBe(original)
    expect(moved).not.toBe(original)
    expect(namingChanged).not.toBe(original)
  })

  it('buildDownloadGroupTaskAutoSyncSignature ignores ungrouped task progress changes', () => {
    const original = buildDownloadGroupTaskAutoSyncSignature(
      [task('gid-ungrouped', 'active')],
      [],
      [],
    )
    const changed = buildDownloadGroupTaskAutoSyncSignature(
      [{ ...task('gid-ungrouped', 'active'), completedLength: '99', downloadSpeed: '42' }],
      [],
      [],
    )

    expect(original).toBe('')
    expect(changed).toBe(original)
  })

  it('fetchGroups reconciles placeholders by opaque group_key', async () => {
    const store = useDownloadGroupStore()
    store.addPlaceholdersFromDownloadGroups([group('dg-reconcile')], 'batch-add')
    expect(store.placeholders).toHaveLength(1)

    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(envelope([card('dg-reconcile')]))
    await store.fetchGroups()

    expect(store.placeholders).toHaveLength(0)
    expect(store.masterItems).toHaveLength(1)
    expect(store.masterItems[0]?.type).toBe('backend')
  })

  it('fetchGroups ignores out-of-order stale responses and preserves loading state', async () => {
    const first = deferred<DownloadGroupListEnvelope>()
    const second = deferred<DownloadGroupListEnvelope>()
    bindingMocks.GetDownloadGroups.mockReturnValueOnce(first.promise).mockReturnValueOnce(
      second.promise,
    )
    const store = useDownloadGroupStore()

    const firstFetch = store.fetchGroups()
    expect(store.isLoading).toBe(true)
    const secondFetch = store.fetchGroups({ silent: true, reason: 'task-signature' })

    second.resolve(envelope([card('dg-new')]))
    await secondFetch
    expect(store.backendCards.map(cardItem => cardItem.group_key)).toEqual(['dg-new'])
    expect(store.isLoading).toBe(true)

    first.resolve(envelope([card('dg-stale')]))
    await firstFetch
    expect(store.backendCards.map(cardItem => cardItem.group_key)).toEqual(['dg-new'])
    expect(store.isLoading).toBe(false)
  })

  it('addPlaceholdersFromDownloadGroups dedupes keys and ignores malformed groups', () => {
    const store = useDownloadGroupStore()
    store.addPlaceholdersFromDownloadGroups([
      group('dg-valid'),
      group('dg-valid'),
      { ...group(''), id: '   ' } as DownloadGroup,
    ])

    expect(store.placeholders).toHaveLength(1)
    expect(store.placeholders[0]?.group_key).toBe('dg-valid')
  })

  it('pruneExpiredPlaceholders removes placeholders after 15 seconds', () => {
    vi.useFakeTimers()
    vi.setSystemTime(1_000)
    const store = useDownloadGroupStore()
    store.addPlaceholdersFromDownloadGroups([group('dg-expire')])

    store.pruneExpiredPlaceholders(1_000 + DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS)

    expect(store.placeholders).toHaveLength(0)
  })

  it('placeholder timer automatically prunes expired placeholders after the TTL', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(2_000)
    const store = useDownloadGroupStore()
    store.addPlaceholdersFromDownloadGroups([group('dg-auto-expire')])

    expect(store.placeholders).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS)
    expect(store.placeholders).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(1)
    expect(store.placeholders).toHaveLength(0)
    store.$dispose()
  })

  it('placeholder timer is rescheduled for the earliest remaining expiry', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(10_000)
    const store = useDownloadGroupStore()
    store.addPlaceholdersFromDownloadGroups([group('dg-first')])

    vi.setSystemTime(11_000)
    store.addPlaceholdersFromDownloadGroups([group('dg-second')])

    await vi.advanceTimersByTimeAsync(DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS)

    expect(store.placeholders.map(placeholder => placeholder.group_key)).toEqual(['dg-second'])
    store.$dispose()
  })

  it('fetchGroupDetail stores backend split tasks without flattening membership', async () => {
    const detail: DownloadGroupDetailEnvelope = {
      group_key: 'dg-detail',
      found: true,
      group: card('dg-detail'),
      tasks: {
        active: [task('gid-active', 'active')],
        waiting: [task('gid-waiting', 'waiting')],
        stopped: [task('gid-stopped', 'complete')],
      },
      updated_at: 1770000300,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope
    bindingMocks.GetDownloadGroupDetail.mockResolvedValueOnce(detail)
    const store = useDownloadGroupStore()

    await store.fetchGroupDetail(' dg-detail ')

    expect(bindingMocks.GetDownloadGroupDetail).toHaveBeenCalledWith('dg-detail')
    expect(store.currentDetail?.tasks.active[0]?.gid).toBe('gid-active')
    expect(store.currentDetail?.tasks.waiting[0]?.gid).toBe('gid-waiting')
    expect(store.currentDetail?.tasks.stopped[0]?.gid).toBe('gid-stopped')
  })

  it('fetchGroupDetail keeps degraded not-found envelopes for unknown groups', async () => {
    const notFound: DownloadGroupDetailEnvelope = {
      group_key: 'missing',
      found: false,
      group: card('missing', { status: 'unknown', degraded: true }),
      tasks: { active: [], waiting: [], stopped: [] },
      updated_at: 1770000300,
      degraded: true,
      warnings: [{ code: 'group_not_found', severity: 'warning' }],
    } as DownloadGroupDetailEnvelope
    bindingMocks.GetDownloadGroupDetail.mockResolvedValueOnce(notFound)
    const store = useDownloadGroupStore()

    await store.fetchGroupDetail('missing')

    expect(store.currentDetail?.found).toBe(false)
    expect(store.currentDetail?.degraded).toBe(true)
    expect(store.currentDetail?.tasks.active).toHaveLength(0)
  })

  it('fetchGroupDetail clears previous detail while loading another key and replaces failures with empty degraded state', async () => {
    const store = useDownloadGroupStore()
    bindingMocks.GetDownloadGroupDetail.mockResolvedValueOnce({
      group_key: 'dg-first',
      found: true,
      group: card('dg-first'),
      tasks: { active: [task('gid-first', 'active')], waiting: [], stopped: [] },
      updated_at: 1770000300,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)

    await store.fetchGroupDetail('dg-first')
    expect(store.currentDetail?.tasks.active[0]?.gid).toBe('gid-first')

    bindingMocks.GetDownloadGroupDetail.mockRejectedValueOnce(new Error('detail failed'))
    await store.fetchGroupDetail('dg-second')

    expect(store.currentDetail?.group_key).toBe('dg-second')
    expect(store.currentDetail?.found).toBe(false)
    expect(store.currentDetail?.tasks.active).toHaveLength(0)
    expect(store.detailError).toBe('detail failed')
  })

  it('fetchGroupDetail ignores out-of-order responses and keeps the newest selected detail', async () => {
    const deferredFirst = deferred<DownloadGroupDetailEnvelope>()
    const deferredSecond = deferred<DownloadGroupDetailEnvelope>()
    bindingMocks.GetDownloadGroupDetail.mockReturnValueOnce(
      deferredFirst.promise,
    ).mockReturnValueOnce(deferredSecond.promise)
    const store = useDownloadGroupStore()

    const firstFetch = store.fetchGroupDetail('dg-first')
    const secondFetch = store.fetchGroupDetail('dg-second')

    deferredSecond.resolve({
      group_key: 'dg-second',
      found: true,
      group: card('dg-second'),
      tasks: { active: [task('gid-second', 'active')], waiting: [], stopped: [] },
      updated_at: 1770000400,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)
    await secondFetch

    expect(store.currentDetail?.group_key).toBe('dg-second')
    expect(store.currentDetail?.tasks.active[0]?.gid).toBe('gid-second')
    expect(store.isDetailLoading).toBe(false)

    deferredFirst.resolve({
      group_key: 'dg-first',
      found: true,
      group: card('dg-first'),
      tasks: { active: [task('gid-first-stale', 'active')], waiting: [], stopped: [] },
      updated_at: 1770000300,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)
    await firstFetch

    expect(store.currentDetail?.group_key).toBe('dg-second')
    expect(store.currentDetail?.tasks.active[0]?.gid).toBe('gid-second')
    expect(store.detailError).toBeNull()
  })

  it('fetchGroupDetail ignores out-of-order failures from stale requests', async () => {
    const deferredFirst = deferred<DownloadGroupDetailEnvelope>()
    const deferredSecond = deferred<DownloadGroupDetailEnvelope>()
    bindingMocks.GetDownloadGroupDetail.mockReturnValueOnce(
      deferredFirst.promise,
    ).mockReturnValueOnce(deferredSecond.promise)
    const store = useDownloadGroupStore()

    const firstFetch = store.fetchGroupDetail('dg-first')
    const secondFetch = store.fetchGroupDetail('dg-second')
    deferredSecond.resolve({
      group_key: 'dg-second',
      found: true,
      group: card('dg-second'),
      tasks: { active: [task('gid-second', 'active')], waiting: [], stopped: [] },
      updated_at: 1770000400,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)
    await secondFetch

    deferredFirst.reject(new Error('stale failure'))
    await firstFetch

    expect(store.currentDetail?.group_key).toBe('dg-second')
    expect(store.currentDetail?.tasks.active[0]?.gid).toBe('gid-second')
    expect(store.detailError).toBeNull()
  })

  it('silent fetchGroupDetail failure preserves the current detail without loading flicker', async () => {
    const store = useDownloadGroupStore()
    store.currentDetailKey = 'dg-current'
    store.currentDetail = {
      group_key: 'dg-current',
      found: true,
      group: card('dg-current'),
      tasks: { active: [task('gid-current', 'active')], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope
    store.isDetailLoading = false
    bindingMocks.GetDownloadGroupDetail.mockRejectedValueOnce(new Error('transient failure'))

    await store.fetchGroupDetail('dg-current', { silent: true, reason: 'task-signature' })

    expect(store.currentDetail?.tasks.active[0]?.gid).toBe('gid-current')
    expect(store.detailError).toBeNull()
    expect(store.isDetailLoading).toBe(false)
  })

  it('syncAfterSnapshot refetches group list and selected detail', async () => {
    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(envelope([card('dg-selected')]))
    bindingMocks.GetDownloadGroupDetail.mockResolvedValueOnce({
      group_key: 'dg-selected',
      found: true,
      group: card('dg-selected'),
      tasks: { active: [], waiting: [], stopped: [] },
      updated_at: 1770000300,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)
    const store = useDownloadGroupStore()

    await store.syncAfterSnapshot('dg-selected')
    await nextTick()

    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(1)
    expect(bindingMocks.GetDownloadGroupDetail).toHaveBeenCalledWith('dg-selected')
  })

  it('startAutoSync debounces grouped task signature changes into one silent group refetch', async () => {
    vi.useFakeTimers()
    bindingMocks.GetDownloadGroups.mockResolvedValue(envelope([card('dg-auto')]))
    const store = useDownloadGroupStore()
    store.startAutoSync()

    taskStoreMock.activeTasks = [groupedTask('gid-auto', 'dg-auto', { completedLength: '10' })]
    await nextTick()
    taskStoreMock.activeTasks = [groupedTask('gid-auto', 'dg-auto', { completedLength: '20' })]
    await nextTick()

    await vi.advanceTimersByTimeAsync(DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS - 1)
    expect(bindingMocks.GetDownloadGroups).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1)

    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(1)
    expect(store.isLoading).toBe(false)
    store.$dispose()
  })

  it('startAutoSync sees an explicit backend metadata transition to grouped and silently refetches once', async () => {
    vi.useFakeTimers()
    bindingMocks.GetDownloadGroups.mockResolvedValue(envelope([card('dg-explicit')]))
    taskStoreMock.activeTasks = [task('gid-explicit', 'active')]
    const store = useDownloadGroupStore()
    store.startAutoSync()

    taskStoreMock.activeTasks = [
      { ...task('gid-explicit', 'active'), download_group: group('dg-explicit') },
    ]
    await nextTick()

    await vi.advanceTimersByTimeAsync(DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS)

    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(1)
    expect(store.isLoading).toBe(false)
    store.$dispose()
  })

  it('auto sync silently refreshes current detail when grouped task progress changes', async () => {
    vi.useFakeTimers()
    bindingMocks.GetDownloadGroups.mockResolvedValue(envelope([card('dg-detail-auto')]))
    bindingMocks.GetDownloadGroupDetail.mockResolvedValue({
      group_key: 'dg-detail-auto',
      found: true,
      group: card('dg-detail-auto', { progress: 0.75, download_speed: '20' }),
      tasks: { active: [task('gid-detail-auto', 'active')], waiting: [], stopped: [] },
      updated_at: 2,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)
    const store = useDownloadGroupStore()
    store.currentDetailKey = 'dg-detail-auto'
    store.currentDetail = {
      group_key: 'dg-detail-auto',
      found: true,
      group: card('dg-detail-auto', { progress: 0.1, download_speed: '1' }),
      tasks: { active: [task('gid-detail-old', 'active')], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope
    store.startAutoSync()

    taskStoreMock.activeTasks = [
      groupedTask('gid-detail-auto', 'dg-detail-auto', {
        completedLength: '75',
        downloadSpeed: '20',
      }),
    ]
    await nextTick()
    await vi.advanceTimersByTimeAsync(DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS)

    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(1)
    expect(bindingMocks.GetDownloadGroupDetail).toHaveBeenCalledWith('dg-detail-auto')
    expect(store.currentDetail?.group.progress).toBe(0.75)
    expect(store.isDetailLoading).toBe(false)
    store.$dispose()
  })

  it('pending name schedules delayed silent refetch with a bounded retry cap', async () => {
    vi.useFakeTimers()
    const pendingCard = card('dg-pending-name', {
      name_status: 'pending',
      warnings: [{ code: 'name_pending', severity: 'info' }],
    })
    bindingMocks.GetDownloadGroups.mockResolvedValue(envelope([pendingCard]))
    const store = useDownloadGroupStore()
    store.startAutoSync()

    await store.fetchGroups({ silent: true, reason: 'test' })

    for (let index = 0; index < DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES + 2; index += 1) {
      await vi.advanceTimersByTimeAsync(DOWNLOAD_GROUP_PENDING_NAME_REFETCH_MS)
      await Promise.resolve()
    }

    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(
      1 + DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES,
    )
    store.$dispose()
  })

  it('stopAutoSync clears watchers debounce timers and pending-name timers', async () => {
    vi.useFakeTimers()
    bindingMocks.GetDownloadGroups.mockResolvedValue(
      envelope([
        card('dg-stop', {
          name_status: 'pending',
          warnings: [{ code: 'name_pending', severity: 'info' }],
        }),
      ]),
    )
    const store = useDownloadGroupStore()
    store.startAutoSync()
    await store.fetchGroups({ silent: true, reason: 'test' })
    taskStoreMock.activeTasks = [groupedTask('gid-stop', 'dg-stop', { completedLength: '1' })]
    await nextTick()

    store.stopAutoSync()
    taskStoreMock.activeTasks = [groupedTask('gid-stop', 'dg-stop', { completedLength: '2' })]
    await nextTick()
    await vi.advanceTimersByTimeAsync(
      DOWNLOAD_GROUP_PENDING_NAME_REFETCH_MS + DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS,
    )

    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(1)
    expect(store.isAutoSyncActive).toBe(false)
  })

  it('pauseGroup calls PauseDownloadGroup with opaque group_key and applies tasks groups detail refresh hints', async () => {
    const store = useDownloadGroupStore()
    store.currentDetailKey = 'dg-op'
    bindingMocks.PauseDownloadGroup.mockResolvedValueOnce(
      operationResult({ refresh: { tasks: true, groups: true, detail: true } }),
    )
    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(envelope([card('dg-op')]))
    bindingMocks.GetDownloadGroupDetail.mockResolvedValueOnce({
      group_key: 'dg-op',
      found: true,
      group: card('dg-op'),
      tasks: { active: [], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)

    await store.pauseGroup(' dg-op ')

    expect(bindingMocks.PauseDownloadGroup).toHaveBeenCalledWith('dg-op')
    expect(taskStoreMock.fetchTasks).toHaveBeenCalledTimes(1)
    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(1)
    expect(bindingMocks.GetDownloadGroupDetail).toHaveBeenCalledWith('dg-op')
  })

  it('resumeGroup records partial failure notice without exposing raw item messages', async () => {
    const store = useDownloadGroupStore()
    bindingMocks.ResumeDownloadGroup.mockResolvedValueOnce(
      operationResult({
        action: 'resume',
        ok: false,
        succeeded: 1,
        skipped: 1,
        failed: 1,
        items: [
          { gid: 'gid-ok', status: 'succeeded', code: 'resumed' },
          { gid: 'gid-skip', status: 'skipped', code: 'not_paused', message: 'backend skip' },
          { gid: 'gid-fail', status: 'failed', code: 'rpc_error', message: 'backend failure' },
        ],
        warnings: [{ code: 'partial_failure', severity: 'warning', count: 1 }],
      }),
    )

    await store.resumeGroup('dg-op')

    expect(store.operationNotice?.severity).toBe('warning')
    expect(store.operationNotice?.code).toBe('partial_failure')
    expect(
      store.operationNotice?.result.items?.some(item => item.message === 'backend failure'),
    ).toBe(true)
  })

  it('noop degraded operation warnings remain warning severity instead of info', async () => {
    const store = useDownloadGroupStore()
    bindingMocks.PauseDownloadGroup.mockResolvedValueOnce(
      operationResult({
        ok: true,
        noop: true,
        succeeded: 0,
        skipped: 2,
        failed: 0,
        items: [
          { gid: 'gid-stale', status: 'skipped', code: 'stale_member' },
          { gid: 'gid-history', status: 'skipped', code: 'history_only' },
        ],
        warnings: [
          { code: 'no_actionable_members', severity: 'info', count: 1 },
          { code: 'stale_group', severity: 'warning', count: 1 },
          { code: 'missing_metadata', severity: 'warning', count: 1 },
        ],
      }),
    )

    await store.pauseGroup('dg-stale-noop')

    expect(store.operationNotice?.noop).toBe(true)
    expect(store.operationNotice?.severity).toBe('warning')
  })

  it('operation notice primary code and chips exclude read-model warnings', async () => {
    const store = useDownloadGroupStore()
    bindingMocks.PauseDownloadGroup.mockResolvedValueOnce(
      operationResult({
        ok: true,
        noop: true,
        succeeded: 0,
        skipped: 1,
        failed: 0,
        items: [{ gid: 'gid-history', status: 'skipped', code: 'history_only' }],
        warnings: [
          { code: 'stale_group', severity: 'warning', count: 1 },
          { code: 'missing_metadata', severity: 'warning', count: 1 },
          { code: 'name_pending', severity: 'info', count: 1 },
        ],
      }),
    )

    await store.pauseGroup('dg-read-warnings')

    expect(store.operationNotice?.code).toBe('history_only')
    expect(store.operationNotice?.severity).toBe('info')
  })

  it('operation detail refresh rechecks current detail key after awaited task and group refreshes', async () => {
    const store = useDownloadGroupStore()
    store.currentDetailKey = 'dg-original'
    store.currentDetail = {
      group_key: 'dg-original',
      found: true,
      group: card('dg-original'),
      tasks: { active: [task('gid-original', 'active')], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope
    bindingMocks.PauseDownloadGroup.mockResolvedValueOnce(
      operationResult({
        group_key: 'dg-original',
        refresh: { tasks: true, groups: true, detail: true },
      }),
    )
    taskStoreMock.fetchTasks.mockImplementationOnce(async () => {
      store.currentDetailKey = 'dg-other'
      return undefined
    })
    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(envelope([card('dg-original')]))

    await store.pauseGroup('dg-original')

    expect(taskStoreMock.fetchTasks).toHaveBeenCalledTimes(1)
    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(1)
    expect(bindingMocks.GetDownloadGroupDetail).not.toHaveBeenCalledWith('dg-original')
    expect(store.currentDetailKey).toBe('dg-other')
  })

  it('removeGroup calls RemoveDownloadGroup with delete_files and clears stale detail state', async () => {
    const store = useDownloadGroupStore()
    store.currentDetailKey = 'dg-remove'
    store.currentDetail = {
      group_key: 'dg-remove',
      found: true,
      group: card('dg-remove'),
      tasks: { active: [], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope
    bindingMocks.RemoveDownloadGroup.mockResolvedValueOnce(
      operationResult({
        group_key: 'dg-remove',
        action: 'remove',
        refresh: { tasks: true, groups: true, detail: true },
      }),
    )
    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(envelope([]))

    await store.removeGroup('dg-remove', true)

    expect(bindingMocks.RemoveDownloadGroup).toHaveBeenCalledWith('dg-remove', true)
    expect(store.currentDetailKey).toBeNull()
    expect(store.currentDetail).toBeNull()
    expect(store.operationNotice).toBeNull()
  })

  it('removeGroup clears group key and group member tasks from selection in taskStore', async () => {
    const store = useDownloadGroupStore()
    bindingMocks.RemoveDownloadGroup.mockResolvedValueOnce(
      operationResult({
        group_key: 'dg-remove',
        action: 'remove',
        refresh: { tasks: true, groups: true, detail: true },
      }),
    )
    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(envelope([]))

    const memberTask = groupedTask('gid-selected-one', 'dg-remove')
    taskStoreMock.activeTasks = [memberTask]
    taskStoreMock.selectedGids = new Set(['gid-selected-one', 'gid-selected-two'])

    await store.removeGroup('dg-remove', true)

    expect(taskStoreMock.clearSelectedGroup).toHaveBeenCalledWith('dg-remove')
    expect(taskStoreMock.selectedGids.has('gid-selected-one')).toBe(false)
    expect(taskStoreMock.selectedGids.has('gid-selected-two')).toBe(true)
  })

  it('openGroupFolder handles redacted failure results without task-level IPC', async () => {
    const store = useDownloadGroupStore()
    bindingMocks.OpenDownloadGroupFolder.mockResolvedValueOnce(
      operationResult({
        action: 'open_folder',
        ok: false,
        failed: 1,
        succeeded: 0,
        items: [{ status: 'failed', code: 'folder_unsafe', message: 'backend redacted' }],
      }),
    )

    await store.openGroupFolder('dg-folder')

    expect(bindingMocks.OpenDownloadGroupFolder).toHaveBeenCalledWith('dg-folder')
    expect(bindingMocks.OpenFolder).not.toHaveBeenCalled()
    expect(store.operationNotice?.severity).toBe('error')
  })

  it('group operations ignore selected task GIDs and never call task-level action bindings', async () => {
    const store = useDownloadGroupStore()
    taskStoreMock.getSelectedGids = ['gid-selected']
    bindingMocks.PauseDownloadGroup.mockResolvedValueOnce(operationResult())

    await store.pauseGroup('dg-boundary')

    expect(bindingMocks.PauseDownloadGroup).toHaveBeenCalledWith('dg-boundary')
    expect(bindingMocks.PauseDownloadGroup).not.toHaveBeenCalledWith('gid-selected')
    expect(bindingMocks.PauseTask).not.toHaveBeenCalled()
    expect(bindingMocks.BatchPause).not.toHaveBeenCalled()
    expect(bindingMocks.RemoveTask).not.toHaveBeenCalled()
    expect(bindingMocks.BatchRemove).not.toHaveBeenCalled()

    bindingMocks.ResumeDownloadGroup.mockResolvedValueOnce(operationResult({ action: 'resume' }))
    await store.resumeGroup('dg-boundary')

    expect(bindingMocks.ResumeDownloadGroup).toHaveBeenCalledWith('dg-boundary')
    expect(bindingMocks.BatchResume).not.toHaveBeenCalled()
    expect(bindingMocks.ResumeTask).not.toHaveBeenCalled()
  })

  it('cloneDownloadGroup preserves generated name_status for placeholders', () => {
    const store = useDownloadGroupStore()
    store.addPlaceholdersFromDownloadGroups([{ ...group('dg-name'), name_status: 'pending' }])

    expect(store.placeholders[0]?.download_group.name_status).toBe('pending')
  })

  it('buildInlineTaskListEntries inserts a group card at the first member position and hides member rows', () => {
    const groupCard = card('dg-inline')
    const entries = buildInlineTaskListEntries({
      tab: 'downloads',
      tasks: [
        task('gid-before', 'active'),
        { ...task('gid-member-a', 'active'), download_group: group('dg-inline') },
        { ...task('gid-member-b', 'active'), download_group: group('dg-inline') },
        task('gid-after', 'active'),
      ],
      groupItems: [{ type: 'backend', group_key: 'dg-inline', card: groupCard }],
    })

    expect(entries.map(entry => entry.key)).toEqual([
      'task:gid-before',
      'group:dg-inline',
      'task:gid-after',
    ])
    expect(entries.some(entry => entry.key === 'task:gid-member-a')).toBe(false)
    expect(entries.some(entry => entry.key === 'task:gid-member-b')).toBe(false)
  })

  it('buildInlineTaskListEntries keeps mixed or live groups in Downloads and terminal complete/error groups in Completed', () => {
    const liveGroup = card('dg-live', {
      status: 'active',
      counts: { ...counts(), active: 1, complete: 1 },
    })
    const terminalComplete = card('dg-complete', {
      status: 'complete',
      counts: { ...counts(), active: 0, waiting: 0, paused: 0, complete: 2, error: 0 },
    })
    const terminalError = card('dg-error', {
      status: 'error',
      counts: { ...counts(), active: 0, waiting: 0, paused: 0, complete: 1, error: 1 },
    })
    const placeholderGroup = group('dg-placeholder')
    const groupItems = [
      {
        type: 'placeholder' as const,
        group_key: 'dg-placeholder',
        placeholder: {
          group_key: 'dg-placeholder',
          download_group: placeholderGroup,
          created_at: 10,
          expires_at: 20,
          source: 'batch-add' as const,
        },
      },
      { type: 'backend' as const, group_key: 'dg-live', card: liveGroup },
      { type: 'backend' as const, group_key: 'dg-complete', card: terminalComplete },
      { type: 'backend' as const, group_key: 'dg-error', card: terminalError },
    ]

    const downloads = buildInlineTaskListEntries({ tab: 'downloads', tasks: [], groupItems })
    const completed = buildInlineTaskListEntries({ tab: 'stopped', tasks: [], groupItems })

    expect(isTerminalDownloadGroupCard(liveGroup)).toBe(false)
    expect(downloads.map(entry => entry.key)).toEqual(['group:dg-placeholder', 'group:dg-live'])
    expect(completed.map(entry => entry.key)).toEqual(['group:dg-complete', 'group:dg-error'])
  })

  it('buildInlineTaskListEntries filters Completed groups by group text or hidden member filename', () => {
    const terminalGroup = card('dg-search', {
      display_name: 'Collection Alpha',
      status: 'complete',
      counts: { ...counts(), active: 0, waiting: 0, paused: 0, complete: 2 },
    })
    const entries = buildInlineTaskListEntries({
      tab: 'stopped',
      searchQuery: 'hidden-file',
      tasks: [
        {
          ...task('gid-hidden', 'complete'),
          files: [{ path: '/downloads/hidden-file.bin', uris: [] }],
          download_group: group('dg-search'),
        },
      ],
      groupItems: [{ type: 'backend', group_key: 'dg-search', card: terminalGroup }],
    })

    expect(entries.map(entry => entry.key)).toEqual(['group:dg-search'])
  })

  it('buildInlineTaskListEntries suppresses known group members across tabs even when the card is not emitted', () => {
    const liveGroup = card('dg-live-cross-tab', {
      status: 'active',
      counts: { ...counts(), active: 1, waiting: 0, paused: 0, complete: 1 },
    })
    const entries = buildInlineTaskListEntries({
      tab: 'stopped',
      tasks: [
        { ...task('gid-completed-member', 'complete'), download_group: group('dg-live-cross-tab') },
        task('gid-plain-completed', 'complete'),
      ],
      groupItems: [{ type: 'backend', group_key: 'dg-live-cross-tab', card: liveGroup }],
    })

    expect(entries.map(entry => entry.key)).toEqual(['task:gid-plain-completed'])
  })

  it('scheduleAutoSyncImmediate triggers fetchGroups within a microtask and coalesces rapid calls', async () => {
    const firstCard = card('dg-immediate')
    bindingMocks.GetDownloadGroups.mockResolvedValue(envelope([firstCard]))
    const store = useDownloadGroupStore()
    store.startAutoSync()

    store.scheduleAutoSyncImmediate('pause-delta')
    store.scheduleAutoSyncImmediate('resume-delta')
    store.scheduleAutoSyncImmediate('pause-delta')

    expect(bindingMocks.GetDownloadGroups).not.toHaveBeenCalled()

    await Promise.resolve()

    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalledTimes(1)

    store.stopAutoSync()
  })

  it('resumeGroup snapshots paused waiting GIDs before ResumeDownloadGroup and does not send member GIDs', async () => {
    taskStoreMock.waitingTasks = [
      groupedTask('gid-paused-a', 'dg-op', { status: 'paused' }),
      groupedTask('gid-queued', 'dg-op', { status: 'waiting' }),
      groupedTask('gid-other', 'dg-other', { status: 'paused' }),
      groupedTask('gid-paused-b', 'dg-op', { status: 'paused' }),
    ]
    taskStoreMock.activeTasks = [groupedTask('gid-active', 'dg-op', { status: 'active' })]
    taskStoreMock.stoppedTasks = [groupedTask('gid-complete', 'dg-op', { status: 'complete' })]
    const ipc = deferred<DownloadGroupOperationResult>()
    bindingMocks.ResumeDownloadGroup.mockReturnValueOnce(ipc.promise)

    const store = useDownloadGroupStore()
    const resumePromise = store.resumeGroup('dg-op')
    await Promise.resolve()

    expect(taskStoreMock.runHeldResume).toHaveBeenCalledTimes(1)
    expect(taskStoreMock.runHeldResume.mock.calls[0][0]).toEqual(['gid-paused-a', 'gid-paused-b'])
    expect(taskStoreMock.runHeldResume.mock.calls[0][2]).toEqual({ recoverSnapshot: false })
    expect(bindingMocks.ResumeDownloadGroup).toHaveBeenCalledWith('dg-op')
    expect(bindingMocks.ResumeDownloadGroup.mock.calls[0]).toHaveLength(1)
    expect(taskStoreMock.fetchTasks).not.toHaveBeenCalled()

    ipc.resolve(
      operationResult({
        action: 'resume',
        refresh: { tasks: true, groups: false, detail: false },
      }),
    )
    await resumePromise
  })

  it('resumeGroup unions open-detail paused waiting GIDs after task-store order', async () => {
    taskStoreMock.waitingTasks = [groupedTask('gid-paused-a', 'dg-op', { status: 'paused' })]
    bindingMocks.ResumeDownloadGroup.mockResolvedValueOnce(
      operationResult({ action: 'resume' }),
    )
    const store = useDownloadGroupStore()
    store.currentDetailKey = 'dg-op'
    store.currentDetail = {
      group_key: 'dg-op',
      found: true,
      group: card('dg-op'),
      tasks: {
        active: [],
        waiting: [
          groupedTask('gid-paused-a', 'dg-op', { status: 'paused' }),
          groupedTask('gid-detail-extra', 'dg-op', { status: 'paused' }),
        ],
        stopped: [],
      },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope

    await store.resumeGroup('dg-op')

    expect(taskStoreMock.runHeldResume.mock.calls[0][0]).toEqual([
      'gid-paused-a',
      'gid-detail-extra',
    ])
  })

  it('resumeGroup confirms only succeeded item GIDs and still records partial-failure notices', async () => {
    const engineOk: string[][] = []
    taskStoreMock.runHeldResume.mockImplementation(
      async (
        _gids: string[],
        getEngineOkGids: () => Promise<string[]>,
        _options?: { recoverSnapshot?: boolean },
      ) => {
        engineOk.push(await getEngineOkGids())
      },
    )
    const store = useDownloadGroupStore()
    bindingMocks.ResumeDownloadGroup.mockResolvedValueOnce(
      operationResult({
        action: 'resume',
        ok: false,
        succeeded: 1,
        skipped: 1,
        failed: 1,
        items: [
          { gid: 'gid-ok', status: 'succeeded', code: 'resumed' },
          { gid: 'gid-skip', status: 'skipped', code: 'not_paused', message: 'backend skip' },
          { gid: 'gid-fail', status: 'failed', code: 'rpc_error', message: 'backend failure' },
        ],
        warnings: [{ code: 'partial_failure', severity: 'warning', count: 1 }],
      }),
    )

    await store.resumeGroup('dg-op')

    expect(engineOk).toEqual([['gid-ok']])
    expect(store.operationNotice?.severity).toBe('warning')
    expect(store.operationNotice?.code).toBe('partial_failure')
  })

  it('resumeGroup clears via recoverSnapshot false and fetches tasks only after the hold returns', async () => {
    const order: string[] = []
    const ipc = deferred<DownloadGroupOperationResult>()
    bindingMocks.ResumeDownloadGroup.mockReturnValueOnce(ipc.promise)
    taskStoreMock.runHeldResume.mockImplementation(
      async (
        _gids: string[],
        getEngineOkGids: () => Promise<string[]>,
        _options?: { recoverSnapshot?: boolean },
      ) => {
        order.push('held-start')
        await getEngineOkGids()
        order.push('held-end')
      },
    )
    taskStoreMock.fetchTasks.mockImplementation(async () => {
      order.push('fetchTasks')
    })
    const store = useDownloadGroupStore()
    const resumePromise = store.resumeGroup('dg-op')
    await Promise.resolve()

    expect(order).toEqual(['held-start'])
    expect(taskStoreMock.fetchTasks).not.toHaveBeenCalled()
    expect(taskStoreMock.runHeldResume.mock.calls[0][2]).toEqual({ recoverSnapshot: false })

    ipc.resolve(
      operationResult({
        action: 'resume',
        refresh: { tasks: true, groups: false, detail: false },
      }),
    )
    await resumePromise

    expect(order).toEqual(['held-start', 'held-end', 'fetchTasks'])
    expect(taskStoreMock.fetchTasks).toHaveBeenCalledTimes(1)
  })

  it('resumeGroup still calls ResumeDownloadGroup when the local snapshot is empty', async () => {
    bindingMocks.ResumeDownloadGroup.mockResolvedValueOnce(
      operationResult({
        action: 'resume',
        ok: true,
        noop: true,
        succeeded: 0,
        skipped: 1,
        failed: 0,
        items: [{ gid: 'gid-active', status: 'skipped', code: 'already_active' }],
        refresh: { tasks: true, groups: false, detail: false },
      }),
    )
    const store = useDownloadGroupStore()

    await store.resumeGroup('dg-op')

    expect(taskStoreMock.runHeldResume).toHaveBeenCalledWith(
      [],
      expect.any(Function),
      { recoverSnapshot: false },
    )
    expect(bindingMocks.ResumeDownloadGroup).toHaveBeenCalledWith('dg-op')
    expect(taskStoreMock.fetchTasks).toHaveBeenCalledTimes(1)
  })

  it('resumeGroup records a rejected result and fetches once when ResumeDownloadGroup throws', async () => {
    bindingMocks.ResumeDownloadGroup.mockRejectedValueOnce(new Error('resume ipc failed'))
    bindingMocks.GetDownloadGroups.mockResolvedValueOnce(envelope([card('dg-op')]))
    bindingMocks.GetDownloadGroupDetail.mockResolvedValueOnce({
      group_key: 'dg-op',
      found: true,
      group: card('dg-op'),
      tasks: { active: [], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)
    const store = useDownloadGroupStore()
    store.currentDetailKey = 'dg-op'

    await store.resumeGroup('dg-op')

    expect(store.lastOperationResult?.ok).toBe(false)
    expect(store.lastOperationResult?.items?.[0]?.code).toBe('rpc_error')
    expect(taskStoreMock.fetchTasks).toHaveBeenCalledTimes(1)
  })

  it('runAutoSync skips group and detail fetches while a group resume is in flight', async () => {
    bindingMocks.GetDownloadGroups.mockResolvedValue(envelope([card('dg-op')]))
    bindingMocks.GetDownloadGroupDetail.mockResolvedValue({
      group_key: 'dg-op',
      found: true,
      group: card('dg-op'),
      tasks: { active: [], waiting: [], stopped: [] },
      updated_at: 1,
      degraded: false,
      warnings: [],
    } as DownloadGroupDetailEnvelope)
    const ipc = deferred<DownloadGroupOperationResult>()
    bindingMocks.ResumeDownloadGroup.mockReturnValueOnce(ipc.promise)
    const store = useDownloadGroupStore()
    store.currentDetailKey = 'dg-op'
    store.startAutoSync()

    const resumePromise = store.resumeGroup('dg-op')
    await Promise.resolve()
    expect(store.isGroupOperationBusy('dg-op', 'resume')).toBe(true)

    store.scheduleAutoSyncImmediate('resume-delta')
    await Promise.resolve()

    expect(bindingMocks.GetDownloadGroups).not.toHaveBeenCalled()
    expect(bindingMocks.GetDownloadGroupDetail).not.toHaveBeenCalled()

    ipc.resolve(
      operationResult({
        action: 'resume',
        refresh: { tasks: true, groups: true, detail: true },
      }),
    )
    await resumePromise

    expect(bindingMocks.GetDownloadGroups).toHaveBeenCalled()
    expect(bindingMocks.GetDownloadGroupDetail).toHaveBeenCalledWith('dg-op')

    store.stopAutoSync()
  })

  it('resumeGroup sequential calls hold each group snapshot separately', async () => {
    taskStoreMock.waitingTasks = [
      groupedTask('gid-a1', 'dg-a', { status: 'paused' }),
      groupedTask('gid-b1', 'dg-b', { status: 'paused' }),
      groupedTask('gid-a2', 'dg-a', { status: 'paused' }),
    ]
    bindingMocks.ResumeDownloadGroup.mockResolvedValueOnce(
      operationResult({
        action: 'resume',
        group_key: 'dg-a',
        refresh: { tasks: true, groups: false, detail: false },
      }),
    )
    bindingMocks.ResumeDownloadGroup.mockResolvedValueOnce(
      operationResult({
        action: 'resume',
        group_key: 'dg-b',
        refresh: { tasks: true, groups: false, detail: false },
      }),
    )
    const store = useDownloadGroupStore()

    await store.resumeGroup('dg-a')
    await store.resumeGroup('dg-b')

    expect(taskStoreMock.runHeldResume.mock.calls[0][0]).toEqual(['gid-a1', 'gid-a2'])
    expect(taskStoreMock.runHeldResume.mock.calls[1][0]).toEqual(['gid-b1'])
    expect(bindingMocks.ResumeDownloadGroup.mock.calls.map(call => call[0])).toEqual([
      'dg-a',
      'dg-b',
    ])
  })
})

describe('group resume hold snapshot helpers', () => {
  it('keeps paused waiting members in waiting order and ignores other statuses and groups', () => {
    expect(
      snapshotGroupResumeHoldGids('dg-op', [
        groupedTask('gid-paused-a', 'dg-op', { status: 'paused' }),
        groupedTask('gid-active', 'dg-op', { status: 'active' }),
        groupedTask('gid-queued', 'dg-op', { status: 'waiting' }),
        groupedTask('gid-other', 'dg-other', { status: 'paused' }),
        groupedTask('gid-paused-b', 'dg-op', { status: 'paused' }),
        groupedTask('gid-complete', 'dg-op', { status: 'complete' }),
      ]),
    ).toEqual(['gid-paused-a', 'gid-paused-b'])
  })

  it('appends open-detail paused waiting GIDs after task-store order', () => {
    expect(
      snapshotGroupResumeHoldGids(
        'dg-op',
        [groupedTask('gid-paused-a', 'dg-op', { status: 'paused' })],
        'dg-op',
        [
          groupedTask('gid-paused-a', 'dg-op', { status: 'paused' }),
          groupedTask('gid-detail-extra', 'dg-op', { status: 'paused' }),
          groupedTask('gid-detail-queued', 'dg-op', { status: 'waiting' }),
        ],
      ),
    ).toEqual(['gid-paused-a', 'gid-detail-extra'])
  })

  it('returns an empty snapshot for empty inputs', () => {
    expect(snapshotGroupResumeHoldGids('dg-op', [])).toEqual([])
    expect(snapshotGroupResumeHoldGids('dg-op', undefined)).toEqual([])
  })

  it('maps only succeeded items with a non-empty gid', () => {
    expect(
      succeededOperationItemGids(
        operationResult({
          items: [
            { gid: 'gid-ok', status: 'succeeded', code: 'resumed' },
            { gid: 'gid-skip', status: 'skipped', code: 'not_paused' },
            { gid: 'gid-fail', status: 'failed', code: 'rpc_error' },
            { gid: '', status: 'succeeded', code: 'resumed' },
            { status: 'succeeded', code: 'resumed' },
          ],
        }),
      ),
    ).toEqual(['gid-ok'])
  })
})
