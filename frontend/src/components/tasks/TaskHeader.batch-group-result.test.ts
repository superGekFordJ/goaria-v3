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
  useDownloadGroupStore: () => ({
    groups: [],
    fetchGroups: vi.fn(),
    addPlaceholdersFromDownloadGroups: vi.fn(),
  }),
}))

function mountHeader() {
  return mount(TaskHeader, { attachTo: document.body })
}

async function switchToMultiline(wrapper: ReturnType<typeof mountHeader>, text: string) {
  await wrapper.find('input').trigger('paste', {
    clipboardData: {
      getData: () => text,
    },
  })
  vi.advanceTimersByTime(300)
  await wrapper.vm.$nextTick()
}

function addButton(wrapper: ReturnType<typeof mountHeader>) {
  const buttons = wrapper.findAll('button')
  return buttons[buttons.length - 1]
}

async function flushPromises() {
  await Promise.resolve()
  await Promise.resolve()
}

describe('TaskHeader batch group result UI', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    storeMocks.taskStore.allUris = new Set<string>()
    storeMocks.taskStore.batchAddUri.mockResolvedValue({
      succeeded: [],
      duplicates: [],
      errors: {},
      groups: [],
    })
    storeMocks.uiStore.pendingPasteUri = ''
    storeMocks.uiStore.pendingPasteUris = []
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps the pre-submit multiline row counts-only for five valid links', async () => {
    const wrapper = mountHeader()

    await switchToMultiline(
      wrapper,
      [
        'https://example.invalid/1',
        'https://example.invalid/2',
        'https://example.invalid/3',
        'https://example.invalid/4',
        'https://example.invalid/5',
      ].join('\n'),
    )

    const text = wrapper.text()
    expect(text).toContain('taskHeader.validLinks {"count":5}')
    expect(text).not.toContain('taskHeader.batchGroupCreated')
    expect(text).not.toContain('taskHeader.batchGroupsCreated')

    wrapper.unmount()
  })

  it('renders backend-confirmed single-group summary and keeps failed urls', async () => {
    storeMocks.taskStore.batchAddUri.mockResolvedValue({
      succeeded: ['https://example.invalid/1'],
      duplicates: ['https://example.invalid/2'],
      errors: { 'https://example.invalid/fail': 'failed' },
      groups: [
        {
          id: 'dg-header',
          kind: 'batch',
          name: 'Batch 2026-05-07 dg-header',
          folder_name: 'Batch 2026-05-07 dg-header',
          dir: '/downloads/Batch 2026-05-07 dg-header',
          item_count: 5,
          created_at: 1770000000,
        },
      ],
    })
    const wrapper = mountHeader()

    await switchToMultiline(
      wrapper,
      [
        'https://example.invalid/1',
        'https://example.invalid/2',
        'https://example.invalid/3',
        'https://example.invalid/4',
        'https://example.invalid/fail',
      ].join('\n'),
    )

    await addButton(wrapper).trigger('click')
    await flushPromises()
    await wrapper.vm.$nextTick()

    const text = wrapper.text()
    expect(text).toContain('taskHeader.batchGroupCreated {"count":5}')
    expect(text).toContain('taskHeader.batchGroupFolder {"folder":"Batch 2026-05-07 dg-header"}')
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe(
      'https://example.invalid/fail',
    )

    wrapper.unmount()
  })

  it('renders no group summary when backend returns empty groups', async () => {
    storeMocks.taskStore.batchAddUri.mockResolvedValue({
      succeeded: ['https://example.invalid/1'],
      duplicates: ['https://example.invalid/2'],
      errors: {},
      groups: [],
    })
    const wrapper = mountHeader()

    await switchToMultiline(
      wrapper,
      ['https://example.invalid/1', 'https://example.invalid/2'].join('\n'),
    )

    await addButton(wrapper).trigger('click')
    await flushPromises()
    await wrapper.vm.$nextTick()

    const text = wrapper.text()
    expect(text).toContain('taskHeader.batchSucceeded {"count":1}')
    expect(text).not.toContain('taskHeader.batchGroupCreated')
    expect(text).not.toContain('taskHeader.batchGroupsCreated')

    wrapper.unmount()
  })
})
