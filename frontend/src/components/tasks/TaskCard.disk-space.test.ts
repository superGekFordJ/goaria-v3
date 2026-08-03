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

  it('shows disk-space label when errorCode is 9', () => {
    const wrapper = mount(TaskCard, {
      props: { task: mockTask() },
    })

    expect(wrapper.text()).toContain('taskCard.insufficientDiskSpace')
    expect(wrapper.text()).not.toContain('taskCard.error')
    expect(wrapper.findAll('button')).toHaveLength(3)
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
