import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TaskCard from './TaskCard.vue'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'

const storeMocks = vi.hoisted(() => ({
  uiStore: {
    openDetailGid: null as string | null,
    effectsTier: 'balanced',
    openTaskDetail: vi.fn(),
    closeTaskDetail: vi.fn(),
  },
  taskStore: {
    activeTasks: [] as Task[],
    isSelected: vi.fn().mockReturnValue(false),
    toggleSelect: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    openTaskFolder: vi.fn(),
  },
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => storeMocks.uiStore,
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => storeMocks.taskStore,
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
  }),
}))

const TaskDetailOverlayStub = {
  name: 'TaskDetailOverlay',
  props: ['task', 'live'],
  template: '<div data-detail-open :data-stub-live="String(live)"></div>',
}

function mockTask(overrides: Partial<Task> = {}): Task {
  return {
    gid: 'gid-face-test',
    status: 'active',
    totalLength: '1000',
    completedLength: '500',
    downloadSpeed: '250',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: '/downloads/test-file.zip', uris: [] }],
    ...overrides,
  } as Task
}

function mountCard(task: Task) {
  return mount(TaskCard, {
    props: { task },
    global: {
      stubs: {
        TaskDetailOverlay: TaskDetailOverlayStub,
      },
    },
  })
}

describe('TaskCard face splitting & live detail visibility matrix', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMocks.uiStore.openDetailGid = null
    storeMocks.taskStore.activeTasks = []
  })

  it('detail closed: both face and stats remain visible and interactive', () => {
    const task = mockTask({ gid: 'task-closed', status: 'active' })
    storeMocks.taskStore.activeTasks = [task]

    const wrapper = mountCard(task)
    const face = wrapper.get('[data-task-face]')
    const stats = wrapper.get('[data-task-stats]')

    expect(face.classes()).not.toContain('is-detail-hidden')
    expect(face.attributes('aria-hidden')).toBeUndefined()

    expect(stats.classes()).not.toContain('is-detail-hidden')
    expect(stats.attributes('aria-hidden')).toBeUndefined()

    expect(wrapper.find('[data-detail-open]').exists()).toBe(false)
  })

  it('detail open + active-live: face hidden, stats visible, overlay receives live=true', () => {
    const task = mockTask({ gid: 'task-live', status: 'active' })
    storeMocks.uiStore.openDetailGid = task.gid
    storeMocks.taskStore.activeTasks = [task]

    const wrapper = mountCard(task)
    const face = wrapper.get('[data-task-face]')
    const stats = wrapper.get('[data-task-stats]')
    const overlay = wrapper.get('[data-detail-open]')

    expect(face.classes()).toContain('is-detail-hidden')
    expect(face.attributes('aria-hidden')).toBe('true')

    expect(stats.classes()).not.toContain('is-detail-hidden')
    expect(stats.attributes('aria-hidden')).toBeUndefined()

    expect(overlay.attributes('data-stub-live')).toBe('true')
  })

  it('detail open + active non-live (snapshot member not in activeTasks): face hidden, stats hidden, overlay receives live=false', () => {
    const task = mockTask({ gid: 'task-snapshot', status: 'active' })
    storeMocks.uiStore.openDetailGid = task.gid
    // Not present in storeMocks.taskStore.activeTasks
    storeMocks.taskStore.activeTasks = []

    const wrapper = mountCard(task)
    const face = wrapper.get('[data-task-face]')
    const stats = wrapper.get('[data-task-stats]')
    const overlay = wrapper.get('[data-detail-open]')

    expect(face.classes()).toContain('is-detail-hidden')
    expect(face.attributes('aria-hidden')).toBe('true')

    expect(stats.classes()).toContain('is-detail-hidden')
    expect(stats.attributes('aria-hidden')).toBe('true')

    expect(overlay.attributes('data-stub-live')).toBe('false')
  })

  it('detail open + paused: face hidden, stats hidden, overlay receives live=false', () => {
    const task = mockTask({ gid: 'task-paused', status: 'paused' })
    storeMocks.uiStore.openDetailGid = task.gid
    storeMocks.taskStore.activeTasks = []

    const wrapper = mountCard(task)
    const face = wrapper.get('[data-task-face]')
    const stats = wrapper.get('[data-task-stats]')
    const overlay = wrapper.get('[data-detail-open]')

    expect(face.classes()).toContain('is-detail-hidden')
    expect(face.attributes('aria-hidden')).toBe('true')

    expect(stats.classes()).toContain('is-detail-hidden')
    expect(stats.attributes('aria-hidden')).toBe('true')

    expect(overlay.attributes('data-stub-live')).toBe('false')
  })

  it('detail open + complete: face hidden, stats hidden, overlay receives live=false', () => {
    const task = mockTask({ gid: 'task-complete', status: 'complete' })
    storeMocks.uiStore.openDetailGid = task.gid
    storeMocks.taskStore.activeTasks = []

    const wrapper = mountCard(task)
    const face = wrapper.get('[data-task-face]')
    const stats = wrapper.get('[data-task-stats]')
    const overlay = wrapper.get('[data-detail-open]')

    expect(face.classes()).toContain('is-detail-hidden')
    expect(face.attributes('aria-hidden')).toBe('true')

    expect(stats.classes()).toContain('is-detail-hidden')
    expect(stats.attributes('aria-hidden')).toBe('true')

    expect(overlay.attributes('data-stub-live')).toBe('false')
  })

  it('structural separation: stats row is never a descendant of overlay panel', () => {
    const task = mockTask({ gid: 'task-struct', status: 'active' })
    storeMocks.uiStore.openDetailGid = task.gid
    storeMocks.taskStore.activeTasks = [task]

    const wrapper = mountCard(task)
    const statsEl = wrapper.get('[data-task-stats]').element
    const overlayEl = wrapper.get('[data-detail-open]').element

    expect(overlayEl.contains(statsEl)).toBe(false)
    expect(statsEl.contains(overlayEl)).toBe(false)
  })

  it('face and stats both share the task-card-face base transition class', () => {
    const task = mockTask()
    const wrapper = mountCard(task)

    expect(wrapper.get('[data-task-face]').classes()).toContain('task-card-face')
    expect(wrapper.get('[data-task-stats]').classes()).toContain('task-card-face')
  })
})
