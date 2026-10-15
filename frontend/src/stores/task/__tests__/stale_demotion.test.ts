// Regression tests for stale-delta demotion: queued deltas apply in commit
// order behind authoritative task:move events, so a delta committed before
// a resume must never fold a live row back to waiting.
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { ref, computed } from 'vue'
import { setupEvents } from '../events'
import { setupActions } from '../actions'
import { clearMetadataCache } from '../metadata'
import type { Task } from '../../../../bindings/goaria-v3/internal/rpc/models'
import type { TaskState } from '../state'
import type { TaskActions } from '../actions'
import type { TaskPolling } from '../polling'

const downloadGroupMocks = vi.hoisted(() => ({
  scheduleAutoSyncImmediate: vi.fn(),
}))

vi.mock('../../downloadGroups', () => ({
  useDownloadGroupStore: () => downloadGroupMocks,
}))

vi.mock('../../../../bindings/goaria-v3/internal/wailsapp/app.js', () => ({
  UpdateTrayState: vi.fn(),
  GetTasks: vi.fn(),
  GetTaskMetadata: vi.fn().mockResolvedValue({}),
}))

import { GetTasks } from '../../../../bindings/goaria-v3/internal/wailsapp/app.js'

const mockGetTasks = vi.mocked(GetTasks)

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
  const tasks = ref<Record<string, Task[]>>({ active: [], waiting: [], stopped: [] })
  return {
    tasks,
    selectedGids: ref<Set<string>>(new Set()),
    selectedGroupKeys: ref<Set<string>>(new Set()),
    pollingEnabled: ref(false),
    pollingContextEnabled: ref(false),
    isFetching: ref(false),
    isWindowVisible: ref(true),
    preferredInterval: ref(1000),
    consecutiveErrors: ref(0),
    activeTasks: computed(() => tasks.value.active || []),
    waitingTasks: computed(() => tasks.value.waiting || []),
    stoppedTasks: computed(() => tasks.value.stopped || []),
    allTasksCount: computed(() => 0),
    allUris: computed(() => new Set<string>()),
    selectedTaskCount: computed(() => 0),
    selectedGroupCount: computed(() => 0),
    selectedCount: computed(() => 0),
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

describe('stale-delta demotion', () => {
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

  // A pause delta committed at T0 sits in the 50ms debounce queue while a
  // newer resume commit lands and emits its move immediately (w→a). When the
  // stale pause delta flushes after the move, it must not fold the live row
  // back to waiting — commit order, not flush order, decides the row's list.
  it('a pause delta flushed after the resume move does not demote the live row', async () => {
    const task = mockTask('sg_stale-pause', { status: 'active' })
    state.tasks.value.active = [task]

    // 1. Real pause lands: authoritative move a→w (row now paused-in-waiting).
    events.handleTaskMove({
      gid: 'sg_stale-pause',
      from: 'active',
      to: 'waiting',
      task: { gid: 'sg_stale-pause', status: 'paused', downloadSpeed: '0' },
    })
    expect(state.tasks.value.waiting[0]?.status).toBe('paused')

    // 2. Resume commits <50ms later: authoritative move w→a lands FIRST.
    events.handleTaskMove({
      gid: 'sg_stale-pause',
      from: 'waiting',
      to: 'active',
      task: { gid: 'sg_stale-pause', status: 'active' },
    })
    expect(state.tasks.value.active).toHaveLength(1)

    // 3. The queued pause delta flushes — it was committed before the resume.
    await events.handleTaskDelta({
      type: 'pause',
      gid: 'sg_stale-pause',
      payload: { gid: 'sg_stale-pause', status: 'paused', downloadSpeed: '0' },
    })

    expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_stale-pause'])
    expect(state.tasks.value.waiting).toHaveLength(0)
  })

  // Same invariant through the status-less pause arm, which would fold any
  // live row to waiting/paused unconditionally.
  it('a status-less pause delta does not demote a row already moved to active', async () => {
    const task = mockTask('sg_stale-pause2', { status: 'active' })
    state.tasks.value.active = [task]

    events.handleTaskMove({
      gid: 'sg_stale-pause2',
      from: 'active',
      to: 'waiting',
      task: { gid: 'sg_stale-pause2', status: 'paused', downloadSpeed: '0' },
    })
    events.handleTaskMove({
      gid: 'sg_stale-pause2',
      from: 'waiting',
      to: 'active',
      task: { gid: 'sg_stale-pause2', status: 'active' },
    })

    await events.handleTaskDelta({ type: 'pause', gid: 'sg_stale-pause2' })

    expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_stale-pause2'])
    expect(state.tasks.value.waiting).toHaveLength(0)
  })

  // Why demotion must not happen: sg_ progress deltas carry no status and
  // only patch rows already in active — a wrongly demoted row is never
  // healed by the highest-frequency channel.
  it('a row kept active through a stale pause keeps receiving progress patches', async () => {
    const task = mockTask('sg_stale-heal', { status: 'active' })
    state.tasks.value.active = [task]

    events.handleTaskMove({
      gid: 'sg_stale-heal',
      from: 'active',
      to: 'waiting',
      task: { gid: 'sg_stale-heal', status: 'paused', downloadSpeed: '0' },
    })
    events.handleTaskMove({
      gid: 'sg_stale-heal',
      from: 'waiting',
      to: 'active',
      task: { gid: 'sg_stale-heal', status: 'active' },
    })
    await events.handleTaskDelta({
      type: 'pause',
      gid: 'sg_stale-heal',
      payload: { gid: 'sg_stale-heal', status: 'paused', downloadSpeed: '0' },
    })

    // Engine is actually downloading: progress deltas keep arriving.
    await events.handleTaskDelta({
      type: 'progress',
      gid: 'sg_stale-heal',
      payload: { completedLength: '900', downloadSpeed: '5000', totalLength: '1000' },
    })

    expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_stale-heal'])
    expect(state.tasks.value.active[0]?.downloadSpeed).toBe('5000')
    expect(state.tasks.value.waiting).toHaveLength(0)
  })

  // Guarded deltas recover via a read (recoverViaRead → GetTasks); a read
  // whose snapshot predates an interleaved move must not overwrite it on
  // resolve. Uses the real actions.fetchTasks so GetTasks is actually awaited.
  it('a fetchTasks snapshot taken before a move does not regress it', async () => {
    const realActions = setupActions(state)
    const realEvents = setupEvents(state, realActions, {} as TaskPolling)

    const paused = mockTask('sg_stale-snap', { status: 'paused', downloadSpeed: '0' })
    state.tasks.value.waiting = [paused]

    let resolveGetTasks!: (v: { active: Task[]; waiting: Task[]; stopped: Task[] }) => void
    mockGetTasks.mockReturnValue(
      new Promise(resolve => {
        resolveGetTasks = resolve
      }) as never,
    )

    // A status-less resume delta forces the real fetchTasks path.
    const pending = realEvents.handleTaskDelta({ type: 'resume', gid: 'sg_stale-snap' })
    await Promise.resolve() // let the handler reach the await
    expect(mockGetTasks).toHaveBeenCalledTimes(1)

    // While the read is in flight, the authoritative move w→a lands.
    realEvents.handleTaskMove({
      gid: 'sg_stale-snap',
      from: 'waiting',
      to: 'active',
      task: { gid: 'sg_stale-snap', status: 'active' },
    })
    expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_stale-snap'])

    // Snapshot resolves with pre-move truth (gid still waiting).
    resolveGetTasks({ active: [], waiting: [paused], stopped: [] })
    await pending

    expect(state.tasks.value.active.map(t => t.gid)).toEqual(['sg_stale-snap'])
    expect(state.tasks.value.waiting).toHaveLength(0)
  })
})
