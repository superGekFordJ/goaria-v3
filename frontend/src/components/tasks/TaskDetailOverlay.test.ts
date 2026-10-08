import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, reactive, ref } from 'vue'
import TaskDetailOverlay from './TaskDetailOverlay.vue'
import TaskChunkMap from './TaskChunkMap.vue'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
import type { TaskDetailState } from '../../stores/task/detail'

const mocks = vi.hoisted(() => ({
  store: null as null | {
    activeTasks: Task[]
    isWindowVisible: boolean
    taskDetail: TaskDetailState | null
    fetchTaskDetail: ReturnType<typeof vi.fn>
    pause: ReturnType<typeof vi.fn>
    resume: ReturnType<typeof vi.fn>
  },
  copy: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
    locale: ref('en'),
  }),
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => mocks.store,
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => ({ prismHue: 280, prismTone: 'vivid' }),
}))

vi.mock('../../utils/clipboard', () => ({
  copyToClipboard: mocks.copy,
}))

const NOW_SEC = Math.floor(Date.now() / 1000)

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    gid: 'sg_detail',
    status: 'active',
    totalLength: '1000',
    completedLength: '250',
    downloadSpeed: '2048',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: '/downloads/file.bin', uris: [{ uri: 'https://t0.example.com/f', status: 'used' }] }],
    ...overrides,
  } as Task
}

function ready(task: Task, detail: Record<string, unknown>): TaskDetailState {
  return {
    gid: task.gid,
    status: task.status,
    phase: 'ready',
    found: true,
    detail: { gid: task.gid, ...detail } as TaskDetailState['detail'],
  }
}

let cardEl: HTMLElement
let wrapper: VueWrapper | null = null

// live defaults to "active", mirroring the card's own judgment for store tasks.
function mountOverlay(
  task: Task,
  opts: { live?: boolean; detail?: TaskDetailState | null; focusOnMount?: boolean } = {},
) {
  mocks.store!.taskDetail = opts.detail === undefined ? null : opts.detail
  wrapper = mount(TaskDetailOverlay, {
    props: { task, live: opts.live ?? task.status === 'active', focusOnMount: opts.focusOnMount },
    attachTo: cardEl,
    global: { stubs: { TaskChunkMap: true } },
  })
  return wrapper
}

const cellKinds = (w: VueWrapper) => w.findAll('[data-cell]').map(c => c.attributes('data-cell'))
const isRevealed = (w: VueWrapper) => w.get('[data-detail-open]').attributes('data-detail-reveal') !== undefined
const CHUNKS = {
  chunk_states: [2, 2, 1, 0, 0],
  chunk_count: 5,
  chunk_size: 4 * 1024 * 1024,
  chunk_progress: [4 * 1024 * 1024, 4 * 1024 * 1024, 1024 * 1024, 0, 0],
}

describe('TaskDetailOverlay', () => {
  beforeEach(() => {
    mocks.store = reactive({
      activeTasks: [] as Task[],
      isWindowVisible: true,
      taskDetail: null as TaskDetailState | null,
      fetchTaskDetail: vi.fn().mockResolvedValue(undefined),
      pause: vi.fn(),
      resume: vi.fn(),
    })
    mocks.copy.mockReset().mockResolvedValue(true)
    cardEl = document.createElement('div')
    cardEl.className = 'task-card'
    document.body.appendChild(cardEl)
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = null
    cardEl.remove()
    vi.useRealTimers()
  })

  describe('cell matrix', () => {
    it('active live sg_: two rows of source, saveLocation, connections, engine, added-at', () => {
      const task = makeTask({ threads: '4' } as Partial<Task>)
      const w = mountOverlay(task, {
        detail: ready(task, { peak_speed: 4096, added_at: NOW_SEC - 10 }),
      })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation', 'connections', 'engine', 'addedAt'])
      expect(w.get('[data-cell="connections"]').text()).toContain('4')
      expect(w.get('[data-cell="engine"]').text()).toContain('Surge')
      expect(w.get('[data-cell="addedAt"]').text()).toContain('taskDetail.justNow')
    })

    it('active live sg_ without real thread telemetry: no connections cell', () => {
      const task = makeTask()
      const w = mountOverlay(task, { detail: ready(task, { added_at: NOW_SEC - 10 }) })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation', 'engine', 'addedAt'])
    })

    it('active live aria2: never shows connections', () => {
      const task = makeTask({ gid: 'ar_x', threads: '8' } as Partial<Task>)
      const w = mountOverlay(task, { detail: ready(task, { added_at: NOW_SEC - 10 }) })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation', 'engine', 'addedAt'])
      expect(w.get('[data-cell="engine"]').text()).toContain('Aria2')
    })

    it('active live lays facts out in the unified 4-column grid', () => {
      const task = makeTask({ threads: '4' } as Partial<Task>)
      const w = mountOverlay(task, { detail: ready(task, { added_at: NOW_SEC - 10 }) })
      const facts = w.get('[data-detail-facts]')
      expect(facts.classes()).toContain('grid')
      expect(facts.classes()).toContain('grid-cols-4')
      expect(w.get('[data-cell="source"]').classes()).toContain('col-span-2')
      expect(w.get('[data-cell="saveLocation"]').classes()).toContain('col-span-2')
      expect(w.get('[data-cell="engine"]').classes()).toContain('col-span-1')
    })

    it('active snapshot (not live): progress back in the grid, no live-only cells', () => {
      const task = makeTask({ threads: '4' } as Partial<Task>)
      const w = mountOverlay(task, {
        live: false,
        detail: ready(task, { peak_speed: 4096, added_at: NOW_SEC - 10 }),
      })
      expect(cellKinds(w)).toEqual(['downloaded', 'source', 'engine', 'addedAt'])
      expect(w.get('[data-cell="downloaded"]').text()).toContain('250.00 B / 1000.00 B')
      expect(w.get('[data-detail-facts]').classes()).toContain('grid-cols-4')
      expect(w.get('[data-cell="downloaded"]').classes()).toContain('col-span-2')
    })

    it('waiting', () => {
      const task = makeTask({ status: 'waiting' })
      const w = mountOverlay(task, { detail: ready(task, { added_at: NOW_SEC - 7200 }) })
      expect(cellKinds(w)).toEqual(['downloaded', 'source', 'saveLocation', 'engine', 'addedAt'])
    })

    it('paused never shows peak, even when the DTO has it', () => {
      const task = makeTask({ status: 'paused' })
      const w = mountOverlay(task, { detail: ready(task, { peak_speed: 10, added_at: NOW_SEC }) })
      expect(cellKinds(w)).toEqual(['downloaded', 'source', 'saveLocation', 'engine', 'addedAt'])
    })

    it('error with info: explanation row, then downloaded and source; save location drops', () => {
      const task = makeTask({ status: 'error', errorCode: '24', errorMessage: 'HTTP 401 raw' })
      const w = mountOverlay(task, { detail: ready(task, {}) })
      expect(cellKinds(w)).toEqual(['error', 'downloaded', 'source'])
      expect(w.get('[data-cell="downloaded"]').classes()).toContain('col-span-2')
      const row = w.get('[data-cell="error"]')
      expect(row.text()).toContain('taskDetail.errors.authFailed')
      expect(row.text()).toContain('HTTP 401 raw')
    })

    it('error without code or message hides the error row', () => {
      const task = makeTask({ status: 'error' })
      const w = mountOverlay(task, { detail: ready(task, {}) })
      expect(cellKinds(w)).toEqual(['downloaded', 'source', 'saveLocation'])
    })

    it('error with only a code shows just the explanation line', () => {
      const task = makeTask({ status: 'error', errorCode: '19' })
      const w = mountOverlay(task, { detail: ready(task, {}) })
      const row = w.get('[data-cell="error"]')
      expect(row.text()).toContain('taskDetail.errors.dnsFailed')
      expect(row.find('.font-mono-data').exists()).toBe(false)
    })

    it('complete', () => {
      const task = makeTask({ status: 'complete', completedLength: '1000' })
      const w = mountOverlay(task, {
        detail: ready(task, { time_taken_ms: 65_000, avg_speed: 2048, peak_speed: 4096, completed_at: NOW_SEC - 30 }),
      })
      expect(cellKinds(w)).toEqual(['timeTaken', 'avgSpeed', 'peak', 'completedAt', 'source', 'saveLocation'])
      expect(w.get('[data-cell="timeTaken"]').text()).toContain('1m 5s')
    })
  })

  describe('T1 phases', () => {
    const complete = () => makeTask({ status: 'complete' })

    it('pending shows fixed placeholders', () => {
      const w = mountOverlay(complete(), { detail: null })
      expect(w.findAll('[data-detail-placeholder]')).toHaveLength(4)
      expect(cellKinds(w)).toEqual(['timeTaken', 'avgSpeed', 'peak', 'completedAt', 'source', 'saveLocation'])
    })

    it('data for another status is treated as pending', () => {
      const task = complete()
      const w = mountOverlay(task, { detail: { ...ready(task, { time_taken_ms: 1 }), status: 'active' } })
      expect(w.findAll('[data-detail-placeholder]')).toHaveLength(4)
    })

    it('failed hides T1 cells', () => {
      const task = complete()
      const w = mountOverlay(task, { detail: { gid: task.gid, status: 'complete', phase: 'failed' } })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation'])
    })

    it('found:false hides T1 cells', () => {
      const task = complete()
      const w = mountOverlay(task, { detail: { gid: task.gid, status: 'complete', phase: 'ready', found: false } })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation'])
    })

    it('ready without values hides T1 cells (no 0 fillers)', () => {
      const task = complete()
      const w = mountOverlay(task, { detail: ready(task, {}) })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation'])
      expect(w.text()).not.toContain('0 B/s')
    })

    it('source prefers detail uris and falls back to T0 uris', () => {
      const task = complete()
      const w = mountOverlay(task, { detail: ready(task, { uris: ['https://d.example.com/f', 'https://m.example.com/f'] }) })
      const src = w.get('[data-cell="source"]')
      expect(src.text()).toContain('d.example.com')
      expect(src.text()).toContain('taskDetail.mirrorBadge {"count":1}')
      w.unmount()
      wrapper = null
      const w2 = mountOverlay(task, { detail: null })
      expect(w2.get('[data-cell="source"]').text()).toContain('t0.example.com')
    })
  })

  it.each([
    ['active live', makeTask(), true],
    ['active snapshot', makeTask(), false],
    ['waiting', makeTask({ status: 'waiting' }), false],
    ['paused', makeTask({ status: 'paused' }), false],
    ['error', makeTask({ status: 'error', errorCode: '24' }), false],
    ['complete', makeTask({ status: 'complete' }), false],
  ])('%s: identity strip carries no primary metric', (_label, task, live) => {
    const w = mountOverlay(task, { live, detail: ready(task, {}) })
    expect(w.find('[data-detail-primary]').exists()).toBe(false)
    expect(w.find('.text-neon').exists()).toBe(false)
    expect(w.find('[data-detail-close]').exists()).toBe(true)
  })

  it('primary action follows status', async () => {
    const task = makeTask()
    const w = mountOverlay(task, { detail: ready(task, {}) })
    await w.get('[data-detail-action]').trigger('click')
    expect(mocks.store!.pause).toHaveBeenCalledWith('sg_detail')

    await w.setProps({ task: makeTask({ status: 'complete' }), live: false })
    expect(w.find('[data-detail-action]').exists()).toBe(false)

    await w.setProps({ task: makeTask({ status: 'waiting' }) })
    await w.get('[data-detail-action]').trigger('click')
    expect(mocks.store!.resume).toHaveBeenCalledWith('sg_detail')
  })

  describe('reveal window', () => {
    it.each([
      ['active live', makeTask(), true, true],
      ['active snapshot', makeTask(), false, false],
      ['waiting', makeTask({ status: 'waiting' }), false, false],
      ['paused', makeTask({ status: 'paused' }), false, false],
      ['error', makeTask({ status: 'error' }), false, false],
      ['complete', makeTask({ status: 'complete' }), false, false],
    ])('%s: revealed=%s', (_label, task, live, revealed) => {
      const w = mountOverlay(task, { live, detail: ready(task, {}) })
      expect(isRevealed(w)).toBe(revealed)
    })

    it('a live flag on a non-active task never opens the window', () => {
      const w = mountOverlay(makeTask({ status: 'paused' }), { live: true })
      expect(isRevealed(w)).toBe(false)
    })

    it('the decorative surface is hidden from assistive tech and never takes pointer focus', () => {
      const w = mountOverlay(makeTask())
      const surface = w.get('.task-detail-surface')
      expect(surface.attributes('aria-hidden')).toBe('true')
      expect(surface.findAll('button, a, input')).toHaveLength(0)
    })
  })

  describe('chunk map', () => {
    const chunkMap = (w: VueWrapper) => w.findComponent(TaskChunkMap)

    it('sg_ active with data: shows the map with count, size and byte progress', () => {
      const task = makeTask()
      const w = mountOverlay(task, { detail: ready(task, CHUNKS) })
      expect(chunkMap(w).exists()).toBe(true)
      expect(chunkMap(w).props()).toMatchObject({
        states: CHUNKS.chunk_states,
        count: 5,
        chunkSize: 4 * 1024 * 1024,
        progress: CHUNKS.chunk_progress,
        totalSize: 1000,
        frozen: false,
      })
    })

    it('without chunk_progress the progress prop stays undefined', () => {
      const task = makeTask()
      const noProgress = {
        chunk_states: CHUNKS.chunk_states,
        chunk_count: CHUNKS.chunk_count,
        chunk_size: CHUNKS.chunk_size,
      }
      const w = mountOverlay(task, { detail: ready(task, noProgress) })
      expect(chunkMap(w).props('progress')).toBeUndefined()
      expect(chunkMap(w).props('totalSize')).toBe(1000)
    })

    it('sg_ paused with data: frozen map', () => {
      const task = makeTask({ status: 'paused' })
      const w = mountOverlay(task, { detail: ready(task, CHUNKS) })
      expect(chunkMap(w).props('frozen')).toBe(true)
    })

    it('snapshot (not live) active member with data: map shown without a reveal window', () => {
      const task = makeTask()
      const w = mountOverlay(task, { live: false, detail: ready(task, CHUNKS) })
      expect(chunkMap(w).exists()).toBe(true)
      expect(isRevealed(w)).toBe(false)
    })

    it('aria2 never shows a map; the facts take the full width', () => {
      const task = makeTask({ gid: 'ar_x' })
      const w = mountOverlay(task, { detail: ready(task, CHUNKS) })
      expect(w.find('[data-chunk-slot]').exists()).toBe(false)
      w.unmount()
      wrapper = null
      const pending = mountOverlay(makeTask({ gid: 'ar_y' }), { detail: null })
      expect(pending.find('[data-chunk-slot]').exists()).toBe(false)
    })

    it.each(['waiting', 'error', 'complete'])('%s: no map even with leftover data', status => {
      const task = makeTask({ status })
      const w = mountOverlay(task, { detail: ready(task, CHUNKS) })
      expect(w.find('[data-chunk-slot]').exists()).toBe(false)
    })

    it('failed, found:false and ready without data hide the map', () => {
      const task = makeTask()
      const cases: TaskDetailState[] = [
        { gid: task.gid, status: 'active', phase: 'failed' },
        { gid: task.gid, status: 'active', phase: 'ready', found: false },
        ready(task, {}),
        ready(task, { chunk_states: [] }),
      ]
      for (const detail of cases) {
        const w = mountOverlay(task, { detail })
        expect(w.find('[data-chunk-slot]').exists()).toBe(false)
        w.unmount()
        wrapper = null
      }
    })

    it('first fetch pending: reserves the box, then collapses when no data arrives', async () => {
      const task = makeTask()
      const w = mountOverlay(task, { detail: null })
      expect(w.find('[data-chunk-reserve]').exists()).toBe(true)
      expect(chunkMap(w).exists()).toBe(false)
      mocks.store!.taskDetail = ready(task, {})
      await nextTick()
      expect(w.find('[data-chunk-slot]').exists()).toBe(false)
    })

    it('an empty ready response keeps the last real frame (resume/retry gap)', async () => {
      const task = makeTask()
      const w = mountOverlay(task, { detail: ready(task, CHUNKS) })
      mocks.store!.taskDetail = ready(task, { chunk_states: [] })
      await nextTick()
      expect(chunkMap(w).props('states')).toEqual(CHUNKS.chunk_states)
      mocks.store!.taskDetail = ready(task, {})
      await nextTick()
      expect(chunkMap(w).props('states')).toEqual(CHUNKS.chunk_states)
      expect(w.find('[data-chunk-reserve]').exists()).toBe(false)
    })

    it('a newer non-empty frame replaces the fallback and survives later empties', async () => {
      const task = makeTask()
      const w = mountOverlay(task, { detail: ready(task, CHUNKS) })
      const fresh = [0, 1, 2, 2, 2]
      mocks.store!.taskDetail = ready(task, { chunk_states: fresh })
      await nextTick()
      expect(chunkMap(w).props('states')).toEqual(fresh)
      mocks.store!.taskDetail = ready(task, {})
      await nextTick()
      expect(chunkMap(w).props('states')).toEqual(fresh)
    })

    it('a task that never produced a frame stays hidden through repeated empties', async () => {
      const task = makeTask()
      const w = mountOverlay(task, { detail: null })
      expect(w.find('[data-chunk-reserve]').exists()).toBe(true)
      mocks.store!.taskDetail = ready(task, {})
      await nextTick()
      expect(w.find('[data-chunk-slot]').exists()).toBe(false)
      mocks.store!.taskDetail = ready(task, { chunk_states: [] })
      await nextTick()
      expect(w.find('[data-chunk-slot]').exists()).toBe(false)
    })

    it('a status flip keeps the last frame (recolored) until new data lands', async () => {
      const task = makeTask()
      const w = mountOverlay(task, { detail: ready(task, CHUNKS) })
      await w.setProps({ task: makeTask({ status: 'paused' }), live: false })
      expect(isRevealed(w)).toBe(false)
      expect(w.find('[data-chunk-reserve]').exists()).toBe(false)
      expect(chunkMap(w).props()).toMatchObject({ states: CHUNKS.chunk_states, frozen: true })

      const fresh = [2, 2, 2, 0, 0]
      mocks.store!.taskDetail = ready(makeTask({ status: 'paused' }), { chunk_states: fresh })
      await nextTick()
      expect(chunkMap(w).props('states')).toEqual(fresh)

      await w.setProps({ task: makeTask({ status: 'complete' }) })
      expect(w.find('[data-chunk-slot]').exists()).toBe(false)
    })

    it('a twin in a detached card renders no map and does not fetch', async () => {
      const detached = document.createElement('div')
      detached.className = 'task-card'
      const task = makeTask()
      mocks.store!.taskDetail = ready(task, CHUNKS)
      wrapper = mount(TaskDetailOverlay, {
        props: { task, live: true },
        attachTo: detached,
        global: { stubs: { TaskChunkMap: true } },
      })
      await nextTick()
      expect(wrapper.find('[data-chunk-slot]').exists()).toBe(false)
      expect(mocks.store!.fetchTaskDetail).not.toHaveBeenCalled()
    })
  })

  it('status dot carries the status text only as title / sr-only', () => {
    const w = mountOverlay(makeTask({ status: 'paused' }))
    const dot = w.get('.status-dot')
    expect(dot.classes()).toContain('status-paused')
    expect(dot.attributes('title')).toBe('taskCard.paused')
    expect(w.get('.sr-only').text()).toBe('taskCard.paused')
  })

  it('fetches on mount and refetches on status change', async () => {
    const w = mountOverlay(makeTask({ status: 'paused' }))
    expect(mocks.store!.fetchTaskDetail).toHaveBeenCalledWith('sg_detail', 'paused')
    await w.setProps({ task: makeTask({ status: 'complete' }) })
    expect(mocks.store!.fetchTaskDetail).toHaveBeenLastCalledWith('sg_detail', 'complete')
  })

  it('live overlays refresh every 1s only while active and visible', async () => {
    vi.useFakeTimers()
    mountOverlay(makeTask())
    const calls = () => mocks.store!.fetchTaskDetail.mock.calls.length
    const base = calls()
    vi.advanceTimersByTime(3000)
    expect(calls()).toBe(base + 3)
    mocks.store!.isWindowVisible = false
    await nextTick()
    vi.advanceTimersByTime(10000)
    expect(calls()).toBe(base + 3)
  })

  it('pulls once when a hidden window becomes visible again', async () => {
    vi.useFakeTimers()
    mountOverlay(makeTask(), { live: false })
    const calls = () => mocks.store!.fetchTaskDetail.mock.calls.length
    mocks.store!.isWindowVisible = false
    await nextTick()
    const base = calls()
    mocks.store!.isWindowVisible = true
    await nextTick()
    expect(calls()).toBe(base + 1)
    vi.advanceTimersByTime(5000)
    expect(calls()).toBe(base + 2)
  })

  it('paused overlays do not refetch on visibility restore', async () => {
    vi.useFakeTimers()
    mountOverlay(makeTask({ status: 'paused' }))
    const calls = () => mocks.store!.fetchTaskDetail.mock.calls.length
    mocks.store!.isWindowVisible = false
    await nextTick()
    const base = calls()
    mocks.store!.isWindowVisible = true
    await nextTick()
    expect(calls()).toBe(base)
  })

  it('snapshot (not live) active overlays keep the 5s rhythm', async () => {
    vi.useFakeTimers()
    mountOverlay(makeTask(), { live: false })
    const calls = () => mocks.store!.fetchTaskDetail.mock.calls.length
    const base = calls()
    vi.advanceTimersByTime(4999)
    expect(calls()).toBe(base)
    vi.advanceTimersByTime(1)
    expect(calls()).toBe(base + 1)
  })

  it('paused overlays never poll', async () => {
    vi.useFakeTimers()
    mountOverlay(makeTask({ status: 'paused' }))
    const calls = () => mocks.store!.fetchTaskDetail.mock.calls.length
    const base = calls()
    vi.advanceTimersByTime(15000)
    expect(calls()).toBe(base)
  })

  it('copies the full raw source url and flips the copy label', async () => {
    const task = makeTask({ status: 'complete' })
    const w = mountOverlay(task, { detail: ready(task, { uris: ['https://u:p@d.example.com/f'] }) })
    const btn = w.get('[data-cell="source"] button')
    await btn.trigger('click')
    await Promise.resolve()
    await nextTick()
    expect(mocks.copy).toHaveBeenCalledWith('https://u:p@d.example.com/f')
    expect(w.get('[data-cell="source"] button').attributes('aria-label')).toBe('taskDetail.copied')
  })

  it('a copy resolving after unmount arms no feedback timer', async () => {
    vi.useFakeTimers()
    let resolveCopy!: (ok: boolean) => void
    mocks.copy.mockReturnValue(new Promise<boolean>(r => (resolveCopy = r)))
    const task = makeTask({ status: 'complete' })
    const w = mountOverlay(task, { detail: ready(task, { uris: ['https://u:p@d.example.com/f'] }) })
    await w.get('[data-cell="source"] button').trigger('click')
    w.unmount()
    wrapper = null
    const pendingTimers = vi.getTimerCount()
    resolveCopy(true)
    await nextTick()
    expect(vi.getTimerCount()).toBe(pendingTimers)
  })

  describe('closing and focus', () => {
    it('focuses the close button on an armed mount and reports the claim', () => {
      const w = mountOverlay(makeTask(), { focusOnMount: true })
      expect(document.activeElement).toBe(w.get('[data-detail-close]').element)
      expect(w.emitted('focus-claimed')).toHaveLength(1)
    })

    it('never steals focus on a passive remount (claim already consumed)', () => {
      const first = mountOverlay(makeTask(), { focusOnMount: true })
      expect(document.activeElement).toBe(first.get('[data-detail-close]').element)
      first.unmount()
      wrapper = null
      ;(document.activeElement as HTMLElement | null)?.blur?.()

      const remount = mountOverlay(makeTask())
      expect(document.activeElement).not.toBe(remount.get('[data-detail-close]').element)
      expect(remount.emitted('focus-claimed')).toBeUndefined()
    })

    it('armed mount does not grab focus held outside the card, but still consumes the claim', () => {
      const outside = document.createElement('button')
      document.body.appendChild(outside)
      outside.focus()
      const w = mountOverlay(makeTask(), { focusOnMount: true })
      expect(document.activeElement).toBe(outside)
      expect(w.emitted('focus-claimed')).toHaveLength(1)
      outside.remove()
    })

    it('keyboard activation closes with focus restore, mouse without', async () => {
      const w = mountOverlay(makeTask())
      await w.get('[data-detail-close]').trigger('click', { detail: 0 })
      await w.get('[data-detail-close]').trigger('click', { detail: 1 })
      expect(w.emitted('close')).toEqual([[true], [false]])
    })

    it('outside pointerdown closes; inside does not', () => {
      const w = mountOverlay(makeTask())
      const inside = document.createElement('span')
      cardEl.appendChild(inside)
      inside.dispatchEvent(new Event('pointerdown', { bubbles: true }))
      expect(w.emitted('close')).toBeUndefined()
      document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
      expect(w.emitted('close')).toEqual([[false]])
    })

    it('ignores pointerdown when its card is detached (KeepAlive twin)', () => {
      const w = mountOverlay(makeTask())
      cardEl.remove()
      document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
      expect(w.emitted('close')).toBeUndefined()
    })

    it('removes the document listener and timers on unmount', () => {
      const removeSpy = vi.spyOn(document, 'removeEventListener')
      const w = mountOverlay(makeTask())
      w.unmount()
      wrapper = null
      expect(removeSpy).toHaveBeenCalledWith('pointerdown', expect.any(Function), true)
      removeSpy.mockRestore()
      document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
      expect(w.emitted('close')).toBeUndefined()
    })
  })
})
