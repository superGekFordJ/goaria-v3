import { mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import BatchActionBar from './BatchActionBar.vue'

const storeMocks = vi.hoisted(() => ({
  taskStore: {
    selectedCount: 0,
    getSelectedGids: [] as string[],
    getSelectedGroupKeys: [] as string[],
    batchPause: vi.fn().mockResolvedValue(undefined),
    batchResume: vi.fn().mockResolvedValue(undefined),
    clearSelection: vi.fn(),
  },
  downloadGroupStore: {
    pauseGroup: vi.fn().mockResolvedValue(null),
    resumeGroup: vi.fn().mockResolvedValue(null),
  },
}))

const taskStore = reactive(storeMocks.taskStore)
const downloadGroupStore = reactive(storeMocks.downloadGroupStore)

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
  }),
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => taskStore,
}))

vi.mock('../../stores/downloadGroups', () => ({
  useDownloadGroupStore: () => downloadGroupStore,
}))

describe('BatchActionBar group selection', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    taskStore.selectedCount = 3
    taskStore.getSelectedGids = ['gid-one']
    taskStore.getSelectedGroupKeys = ['dg-one', 'dg-two']
  })

  it('BatchActionBar pauses and resumes mixed task and group selections by type', async () => {
    const wrapper = mount(BatchActionBar)
    const buttons = wrapper.findAll('.batch-btn-icon')

    await buttons[0]?.trigger('click')
    await Promise.resolve()
    await Promise.resolve()

    expect(taskStore.batchPause).toHaveBeenCalledWith(['gid-one'])
    expect(downloadGroupStore.pauseGroup).toHaveBeenCalledWith('dg-one')
    expect(downloadGroupStore.pauseGroup).toHaveBeenCalledWith('dg-two')
    expect(taskStore.clearSelection).not.toHaveBeenCalled()

    await buttons[1]?.trigger('click')
    await Promise.resolve()
    await Promise.resolve()

    expect(taskStore.batchResume).toHaveBeenCalledWith(['gid-one'])
    expect(downloadGroupStore.resumeGroup).toHaveBeenCalledWith('dg-one')
    expect(downloadGroupStore.resumeGroup).toHaveBeenCalledWith('dg-two')
    expect(taskStore.clearSelection).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('skips task batch APIs when only groups are selected', async () => {
    taskStore.selectedCount = 1
    taskStore.getSelectedGids = []
    taskStore.getSelectedGroupKeys = ['dg-only']
    const wrapper = mount(BatchActionBar)

    await wrapper.findAll('.batch-btn-icon')[0]?.trigger('click')
    await Promise.resolve()
    await Promise.resolve()

    expect(taskStore.batchPause).not.toHaveBeenCalled()
    expect(downloadGroupStore.pauseGroup).toHaveBeenCalledWith('dg-only')
    wrapper.unmount()
  })
})
