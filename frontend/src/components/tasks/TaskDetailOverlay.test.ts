import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, reactive, ref } from 'vue'
import TaskDetailOverlay from './TaskDetailOverlay.vue'
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

function mountOverlay(task: Task, opts: { live?: boolean; detail?: TaskDetailState | null } = {}) {
  mocks.store!.activeTasks = opts.live === false ? [] : task.status === 'active' ? [task] : []
  mocks.store!.taskDetail = opts.detail === undefined ? null : opts.detail
  wrapper = mount(TaskDetailOverlay, {
    props: { task, eta: '1m 5s' },
    attachTo: cardEl,
  })
  return wrapper
}

const cellKinds = (w: VueWrapper) => w.findAll('[data-cell]').map(c => c.attributes('data-cell'))

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

  describe('cell selection and two-row packing', () => {
    it('active live: drops the lowest-priority cell that overflows', () => {
      const task = makeTask({ threads: '4' } as Partial<Task>)
      const w = mountOverlay(task, { detail: ready(task, { peak_speed: 4096, added_at: NOW_SEC - 10 }) })
      expect(cellKinds(w)).toEqual(['remaining', 'downloaded', 'peak', 'connections', 'source', 'engine'])
      expect(w.get('[data-cell="remaining"]').text()).toContain('1m 5s')
      expect(w.get('[data-cell="engine"]').text()).toContain('Surge')
    })

    it('active live: a missing peak shifts later cells left so added-at fits', () => {
      const task = makeTask({ threads: '4' } as Partial<Task>)
      const w = mountOverlay(task, { detail: ready(task, { added_at: NOW_SEC - 10 }) })
      expect(cellKinds(w)).toEqual(['remaining', 'downloaded', 'connections', 'source', 'engine', 'addedAt'])
      expect(w.get('[data-cell="addedAt"]').text()).toContain('taskDetail.justNow')
    })

    it('active aria2: never shows connections', () => {
      const task = makeTask({ gid: 'ar_x', threads: '8' } as Partial<Task>)
      const w = mountOverlay(task, { detail: ready(task, {}) })
      expect(cellKinds(w)).not.toContain('connections')
      expect(w.get('[data-cell="engine"]').text()).toContain('Aria2')
    })

    it('waiting', () => {
      const task = makeTask({ status: 'waiting' })
      const w = mountOverlay(task, { detail: ready(task, { added_at: NOW_SEC - 7200 }) })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation', 'engine', 'addedAt'])
    })

    it('paused', () => {
      const task = makeTask({ status: 'paused' })
      const w = mountOverlay(task, { detail: ready(task, { peak_speed: 10, added_at: NOW_SEC }) })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation', 'peak', 'engine', 'addedAt'])
    })

    it('error with info renders the explanation row first', () => {
      const task = makeTask({ status: 'error', errorCode: '24', errorMessage: 'HTTP 401 raw' })
      const w = mountOverlay(task, { detail: ready(task, {}) })
      expect(cellKinds(w)).toEqual(['error', 'source', 'saveLocation'])
      const row = w.get('[data-cell="error"]')
      expect(row.text()).toContain('taskDetail.errors.authFailed')
      expect(row.text()).toContain('HTTP 401 raw')
    })

    it('error without code or message hides the error row', () => {
      const task = makeTask({ status: 'error' })
      const w = mountOverlay(task, { detail: ready(task, {}) })
      expect(cellKinds(w)).toEqual(['source', 'saveLocation'])
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
      expect(src.text()).toContain('+1')
      w.unmount()
      wrapper = null
      const w2 = mountOverlay(task, { detail: null })
      expect(w2.get('[data-cell="source"]').text()).toContain('t0.example.com')
    })
  })

  it('snapshot members hide live-only content', () => {
    const task = makeTask({ threads: '4' } as Partial<Task>)
    const w = mountOverlay(task, { live: false, detail: ready(task, {}) })
    const kinds = cellKinds(w)
    expect(kinds).not.toContain('remaining')
    expect(kinds).not.toContain('connections')
    expect(w.find('[data-detail-primary] .text-neon').exists()).toBe(false)
    expect(w.get('[data-detail-primary]').text()).toContain('250.00 B / 1000.00 B')
  })

  it('primary metric and action follow status', async () => {
    const task = makeTask()
    const w = mountOverlay(task, { detail: ready(task, {}) })
    expect(w.find('[data-detail-primary] .text-neon').exists()).toBe(true)
    await w.get('[data-detail-action]').trigger('click')
    expect(mocks.store!.pause).toHaveBeenCalledWith('sg_detail')

    await w.setProps({ task: makeTask({ status: 'complete' }) })
    expect(w.find('[data-detail-action]').exists()).toBe(false)
    expect(w.get('[data-detail-primary]').text()).toContain('1000.00 B')

    await w.setProps({ task: makeTask({ status: 'waiting' }) })
    await w.get('[data-detail-action]').trigger('click')
    expect(mocks.store!.resume).toHaveBeenCalledWith('sg_detail')
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

  it('silently refreshes every 5s only while active and visible', async () => {
    vi.useFakeTimers()
    mountOverlay(makeTask())
    const calls = () => mocks.store!.fetchTaskDetail.mock.calls.length
    const base = calls()
    vi.advanceTimersByTime(5000)
    expect(calls()).toBe(base + 1)
    mocks.store!.isWindowVisible = false
    await nextTick()
    vi.advanceTimersByTime(15000)
    expect(calls()).toBe(base + 1)
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

  describe('closing and focus', () => {
    it('focuses the close button on mount', () => {
      const w = mountOverlay(makeTask())
      expect(document.activeElement).toBe(w.get('[data-detail-close]').element)
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
