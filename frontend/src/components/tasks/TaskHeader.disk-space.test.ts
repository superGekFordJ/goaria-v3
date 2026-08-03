import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import TaskHeader from './TaskHeader.vue'

const storeMocks = vi.hoisted(() => ({
  taskStore: {
    allUris: new Set<string>(),
    addUri: vi.fn(),
    batchAddUri: vi.fn(),
  },
  uiStore: {
    pendingPasteUri: '',
    pendingPasteUris: [] as string[],
    consumePendingPasteUri: vi.fn(),
    consumePendingPasteUris: vi.fn(),
  },
  downloadGroupStore: {
    addPlaceholdersFromDownloadGroups: vi.fn(),
    fetchGroups: vi.fn(),
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

vi.mock('../../stores/ui', () => ({
  useUIStore: () => storeMocks.uiStore,
}))

vi.mock('../../stores/downloadGroups', () => ({
  useDownloadGroupStore: () => storeMocks.downloadGroupStore,
}))

function mountHeader() {
  return mount(TaskHeader, {
    attachTo: document.body,
  })
}

async function pasteMultiline(wrapper: ReturnType<typeof mountHeader>, text: string) {
  await wrapper.find('input').trigger('paste', {
    clipboardData: {
      getData: () => text,
    },
  })
  vi.advanceTimersByTime(300)
  await wrapper.vm.$nextTick()
}

describe('TaskHeader insufficient disk space', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    storeMocks.taskStore.allUris = new Set<string>()
    storeMocks.taskStore.batchAddUri.mockResolvedValue({
      succeeded: [],
      duplicates: [],
      errors: {},
    })
    storeMocks.taskStore.addUri.mockResolvedValue('success')
    storeMocks.downloadGroupStore.fetchGroups.mockResolvedValue(null)
    storeMocks.uiStore.pendingPasteUri = ''
    storeMocks.uiStore.pendingPasteUris = []
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows disk i18n for single-add insufficient disk space', async () => {
    storeMocks.taskStore.addUri.mockResolvedValue('insufficient disk space')
    const wrapper = mountHeader()

    await wrapper.find('input').setValue('https://example.invalid/big.bin')
    await wrapper.findAll('button')[0].trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('taskHeader.insufficientDiskSpace')
    expect(wrapper.text()).not.toContain('taskHeader.addFailed')
    wrapper.unmount()
  })

  it('shows batch disk i18n when errors include insufficient disk space', async () => {
    storeMocks.taskStore.batchAddUri.mockResolvedValue({
      succeeded: ['https://example.invalid/ok.bin'],
      duplicates: [],
      errors: {
        'https://example.invalid/fail.bin': 'insufficient disk space',
      },
    })
    const wrapper = mountHeader()

    await pasteMultiline(
      wrapper,
      ['https://example.invalid/ok.bin', 'https://example.invalid/fail.bin'].join('\n'),
    )
    await wrapper.findAll('button')[1].trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('taskHeader.insufficientDiskSpaceBatch {"count":1}')
    expect(wrapper.text()).toContain('taskHeader.batchSucceeded {"count":1}')
    wrapper.unmount()
  })
})
