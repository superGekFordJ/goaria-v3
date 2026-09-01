import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TaskCard from './TaskCard.vue'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'

const storeMocks = vi.hoisted(() => ({
  taskStore: {
    isSelected: vi.fn().mockReturnValue(false),
    toggleSelect: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    openTaskFolder: vi.fn(),
  },
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
  }),
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => storeMocks.taskStore,
}))

function mockTask(overrides: Partial<Task> = {}): Task {
  return {
    gid: 'sg_unknown_test',
    status: 'active',
    totalLength: '0',
    completedLength: '7520000',
    downloadSpeed: '512000',
    dir: '/downloads',
    files: [{ path: '/downloads/stream.zip', uris: [] }],
    ...overrides,
  } as Task
}

describe('TaskCard unknown-size handling', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders active unknown task with -- total, --% percentage, -- ETA, and indeterminate bar', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'active',
          totalLength: '0',
          completedLength: '7520000',
          downloadSpeed: '512000',
        }),
      },
    })

    const text = wrapper.text()
    expect(text).toContain('7.17 MB')
    expect(text).toContain('/')
    expect(text).toContain('--')
    expect(text).toContain('--%')
    expect(text).not.toContain('0 B')
    expect(text).not.toContain('0.0%')

    const pbar = wrapper.find('[role="progressbar"]')
    expect(pbar.exists()).toBe(true)
    expect(pbar.attributes('aria-valuenow')).toBeUndefined()

    const indet = wrapper.find('.progress-bar-indeterminate')
    expect(indet.exists()).toBe(true)
    expect(indet.classes()).not.toContain('opacity-50')

    const determinate = wrapper.find('.progress-bar-fill')
    expect(determinate.exists()).toBe(false)
  })

  it('renders paused and waiting unknown task with static dim unknown bar', () => {
    const wrapperPaused = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'paused',
          totalLength: '0',
          completedLength: '1048576',
          downloadSpeed: '0',
        }),
      },
    })

    expect(wrapperPaused.text()).toContain('1.00 MB')
    expect(wrapperPaused.text()).toContain('--%')
    const pausedIndet = wrapperPaused.find('.progress-bar-indeterminate')
    expect(pausedIndet.exists()).toBe(true)
    expect(pausedIndet.classes()).toContain('opacity-50')

    const wrapperWaiting = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'waiting',
          totalLength: '0',
          completedLength: '1048576',
          downloadSpeed: '0',
        }),
      },
    })
    const waitingIndet = wrapperWaiting.find('.progress-bar-indeterminate')
    expect(waitingIndet.exists()).toBe(true)
    expect(waitingIndet.classes()).toContain('opacity-50')
  })

  it('renders complete unknown task with single final size and 100.0% without progress bar', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'complete',
          totalLength: '0',
          completedLength: '7520000',
          downloadSpeed: '0',
        }),
      },
    })

    const text = wrapper.text()
    expect(text).toContain('7.17 MB')
    expect(text).not.toContain('7.17 MB /')
    expect(text).toContain('100.0%')

    const pbar = wrapper.find('[role="progressbar"]')
    expect(pbar.exists()).toBe(false)
  })

  it('renders complete 0/0 task with 0 B and 100.0%', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'complete',
          totalLength: '0',
          completedLength: '0',
          downloadSpeed: '0',
        }),
      },
    })

    const text = wrapper.text()
    expect(text).toContain('0 B')
    expect(text).toContain('100.0%')
  })

  it('renders error unknown task with --% and not 100%', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'error',
          totalLength: '0',
          completedLength: '500000',
          downloadSpeed: '0',
        }),
      },
    })

    const text = wrapper.text()
    expect(text).toContain('--%')
    expect(text).not.toContain('100.0%')
    expect(text).not.toContain('100%')
  })

  it('preserves determinate rendering and ARIA values for known tasks', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'active',
          totalLength: '10000000',
          completedLength: '5000000',
          downloadSpeed: '1000000',
        }),
      },
    })

    const text = wrapper.text()
    expect(text).toContain('4.77 MB')
    expect(text).toContain('9.54 MB')
    expect(text).toContain('50.0%')

    const pbar = wrapper.find('[role="progressbar"]')
    expect(pbar.exists()).toBe(true)
    expect(pbar.attributes('aria-valuenow')).toBe('50')

    const determinate = wrapper.find('.progress-bar-fill')
    expect(determinate.exists()).toBe(true)
    expect(wrapper.find('.progress-bar-indeterminate').exists()).toBe(false)
  })

  it('safely handles malformed, negative, and infinite values without rendering NaN or Infinity', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'active',
          totalLength: 'NaN',
          completedLength: '-500',
          downloadSpeed: 'Infinity',
        }),
      },
    })

    const text = wrapper.text()
    expect(text).not.toContain('NaN')
    expect(text).not.toContain('Infinity')
    expect(text).not.toContain('undefined')
    expect(text).toContain('0 B')
    expect(text).toContain('--%')
  })

  it('smoothly transitions from active unknown to active known without flash', async () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'active',
          totalLength: '0',
          completedLength: '5000000',
          downloadSpeed: '1000000',
        }),
      },
    })

    expect(wrapper.find('.progress-bar-indeterminate').exists()).toBe(true)

    // Discover total size mid-transfer
    await wrapper.setProps({
      task: mockTask({
        status: 'active',
        totalLength: '10000000',
        completedLength: '5000000',
        downloadSpeed: '1000000',
      }),
    })

    expect(wrapper.find('.progress-bar-indeterminate').exists()).toBe(false)
    const fill = wrapper.find('.progress-bar-fill')
    expect(fill.exists()).toBe(true)
    expect(wrapper.text()).toContain('50.0%')
  })

  it('smoothly transitions from active unknown to complete unknown on same component', async () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          status: 'active',
          totalLength: '0',
          completedLength: '7520000',
          downloadSpeed: '1000000',
        }),
      },
    })

    expect(wrapper.find('.progress-bar-indeterminate').exists()).toBe(true)

    // Complete event arrives
    await wrapper.setProps({
      task: mockTask({
        status: 'complete',
        totalLength: '7520000',
        completedLength: '7520000',
        downloadSpeed: '0',
      }),
    })

    expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('100.0%')
    expect(wrapper.text()).toContain('7.17 MB')
  })
})
