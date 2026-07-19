import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TaskCard from './TaskCard.vue'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
import type { TaskGroupHint } from '../../stores/task/grouping'

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
    gid: 'gid-card',
    status: 'active',
    totalLength: '1000',
    completedLength: '250',
    downloadSpeed: '100',
    errorCode: '',
    errorMessage: '',
    dir: '/downloads',
    files: [{ path: '/downloads/file.bin', uris: [] }],
    ...overrides,
  } as Task
}

const groupHint: TaskGroupHint = {
  groupKey: 'dg-card',
  folderLabel: 'Batch 2026-05-07 dg-card',
  folderPath: '/downloads/Batch 2026-05-07 dg-card',
  totalCount: 5,
  visibleCount: 2,
  ordinal: 1,
  isAutoFoldered: true,
}

describe('TaskCard group hint chip', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders a compact grouped chip with folder hint and mono count', () => {
    const wrapper = mount(TaskCard, {
      props: { task: mockTask(), groupHint },
    })

    const chip = wrapper.find('.task-group-chip')
    expect(chip.exists()).toBe(true)
    expect(chip.text()).toContain('taskCard.groupFolder {"folder":"Batch 2026-05-07 dg-card"}')
    expect(chip.text()).toContain('taskCard.groupOrdinal {"index":1,"count":5}')
    expect(chip.find('.font-mono-data').exists()).toBe(true)
    expect(chip.attributes('aria-label')).toContain('taskCard.groupHintLabel')

    expect(wrapper.find('input.task-checkbox').exists()).toBe(true)
    expect(wrapper.findAll('button')).toHaveLength(3)

    wrapper.unmount()
  })

  it('renders no group chip for ungrouped cards while keeping actions', () => {
    const wrapper = mount(TaskCard, {
      props: { task: mockTask(), groupHint: null },
    })

    expect(wrapper.find('.task-group-chip').exists()).toBe(false)
    expect(wrapper.find('input.task-checkbox').exists()).toBe(true)
    expect(wrapper.findAll('button')).toHaveLength(3)

    wrapper.unmount()
  })
})
