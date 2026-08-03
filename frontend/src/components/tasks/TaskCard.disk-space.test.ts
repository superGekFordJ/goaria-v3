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
    gid: 'sg_disk',
    status: 'error',
    totalLength: '1000',
    completedLength: '250',
    downloadSpeed: '0',
    errorCode: '9',
    errorMessage: 'insufficient disk space',
    dir: '/downloads',
    files: [{ path: '/downloads/file.bin', uris: [] }],
    ...overrides,
  } as Task
}

describe('TaskCard insufficient disk space', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('shows disk-space label when errorCode is 9', async () => {
    const wrapper = mount(TaskCard, {
      props: { task: mockTask() },
    })

    expect(wrapper.text()).toContain('taskCard.insufficientDiskSpace')
    expect(wrapper.text()).not.toContain('taskCard.error')
    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(3)

    await buttons[0].trigger('click')
    expect(storeMocks.taskStore.resume).toHaveBeenCalledWith('sg_disk')
    await buttons[1].trigger('click')
    expect(storeMocks.taskStore.openTaskFolder).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows disk-space label from message when errorCode is empty', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          errorCode: '',
          errorMessage: 'write failed: insufficient disk space',
        }),
      },
    })

    expect(wrapper.text()).toContain('taskCard.insufficientDiskSpace')
    expect(wrapper.text()).not.toContain('taskCard.error')
    wrapper.unmount()
  })

  it('keeps generic error label for other error codes', () => {
    const wrapper = mount(TaskCard, {
      props: {
        task: mockTask({
          errorCode: '1',
          errorMessage: 'network timeout',
        }),
      },
    })

    expect(wrapper.text()).toContain('taskCard.error')
    expect(wrapper.text()).not.toContain('taskCard.insufficientDiskSpace')
    wrapper.unmount()
  })
})
