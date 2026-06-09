import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import TaskHeader from './TaskHeader.vue'
import type { BatchAddResult } from '../../../bindings/goaria-v3/internal/tasks/models'

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

describe('TaskHeader batch group hints', () => {
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

  it('shows generic pre-submit hint with duplicate and invalid counts for qualifying multiline batches', async () => {
    storeMocks.taskStore.allUris = new Set(['https://example.invalid/2'])
    const wrapper = mountHeader()

    await pasteMultiline(
      wrapper,
      [
        'https://example.invalid/1',
        'https://example.invalid/2',
        'https://example.invalid/3',
        'https://example.invalid/4',
        'https://example.invalid/5',
        'https://example.invalid/6',
        'not-a-url',
      ].join('\n'),
    )

    const text = wrapper.text()
    expect(text).toContain('taskHeader.validLinks {"count":5}')
    expect(text).toContain('taskHeader.duplicateLinks {"count":1}')
    expect(text).toContain('taskHeader.invalidLinks {"count":1}')
    expect(text).toContain('taskHeader.autoFolderHintWithCount {"count":5}')
    wrapper.unmount()
  })

  it('does not show pre-submit hint for non-qualifying or duplicate-only input', async () => {
    storeMocks.taskStore.allUris = new Set([
      'https://example.invalid/1',
      'https://example.invalid/2',
      'https://example.invalid/3',
      'https://example.invalid/4',
      'https://example.invalid/5',
    ])
    const wrapper = mountHeader()

    await pasteMultiline(
      wrapper,
      [
        'https://example.invalid/1',
        'https://example.invalid/2',
        'https://example.invalid/3',
        'https://example.invalid/4',
        'https://example.invalid/5',
      ].join('\n'),
    )

    expect(wrapper.text()).toContain('taskHeader.duplicateLinks {"count":5}')
    expect(wrapper.text()).not.toContain('taskHeader.autoFolderHintWithCount')

    wrapper.unmount()
  })

  it('does not show pre-submit hint when repeated same URL lines are below unique addable threshold', async () => {
    const wrapper = mountHeader()

    await pasteMultiline(
      wrapper,
      [
        'https://example.invalid/repeated',
        'https://example.invalid/repeated',
        'https://example.invalid/repeated',
        'https://example.invalid/repeated',
        'https://example.invalid/repeated',
      ].join('\n'),
    )

    const text = wrapper.text()
    expect(text).toContain('taskHeader.validLinks {"count":5}')
    expect(text).not.toContain('taskHeader.autoFolderHintWithCount')

    await wrapper.findAll('button')[1].trigger('click')
    expect(storeMocks.taskStore.batchAddUri).toHaveBeenCalledWith([
      'https://example.invalid/repeated',
      'https://example.invalid/repeated',
      'https://example.invalid/repeated',
      'https://example.invalid/repeated',
      'https://example.invalid/repeated',
    ])

    wrapper.unmount()
  })

  it('shows post-submit hint from backend group result and keeps failed urls in textarea', async () => {
    const result: Partial<BatchAddResult> = {
      succeeded: ['https://example.invalid/1'],
      duplicates: ['https://example.invalid/2'],
      errors: { 'https://example.invalid/fail': 'failed' },
      groups: [
        {
          id: 'dg-opaque-result',
          kind: 'batch',
          name: 'Batch 2026-05-07 dg-opaque-result',
          folder_name: 'Batch 2026-05-07 dg-opaque-result',
          dir: '/downloads/Batch 2026-05-07 dg-opaque-result',
          item_count: 5,
          created_at: 1770000000,
        },
      ],
    }
    storeMocks.taskStore.batchAddUri.mockResolvedValue(result)
    const wrapper = mountHeader()

    await pasteMultiline(
      wrapper,
      [
        'https://example.invalid/1',
        'https://example.invalid/2',
        'https://example.invalid/3',
        'https://example.invalid/4',
        'https://example.invalid/fail',
      ].join('\n'),
    )

    await wrapper.findAll('button')[1].trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('taskHeader.batchGroupCreated {"count":5}')
    expect(wrapper.text()).toContain(
      'taskHeader.batchGroupFolder {"folder":"Batch 2026-05-07 dg-opaque-result"}',
    )
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe(
      'https://example.invalid/fail',
    )

    wrapper.unmount()
  })

  it('TaskHeader batch add registers placeholders from BatchAddResult groups and refetches groups', async () => {
    const groups = [
      {
        id: 'dg-placeholder',
        kind: 'batch',
        name: 'Batch dg-placeholder',
        folder_name: 'Batch dg-placeholder',
        dir: '/downloads/dg-placeholder',
        item_count: 5,
        created_at: 1770000000,
      },
    ]
    storeMocks.taskStore.batchAddUri.mockResolvedValue({
      succeeded: ['https://example.invalid/1'],
      duplicates: [],
      errors: {},
      groups,
    })
    const wrapper = mountHeader()

    await pasteMultiline(
      wrapper,
      [
        'https://example.invalid/1',
        'https://example.invalid/2',
        'https://example.invalid/3',
        'https://example.invalid/4',
        'https://example.invalid/5',
      ].join('\n'),
    )

    await wrapper.findAll('button')[1].trigger('click')
    await wrapper.vm.$nextTick()

    expect(storeMocks.downloadGroupStore.addPlaceholdersFromDownloadGroups).toHaveBeenCalledWith(
      groups,
      'batch-add',
    )
    expect(storeMocks.downloadGroupStore.fetchGroups).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('TaskHeader AddUri success refetches groups without creating a placeholder from string result', async () => {
    storeMocks.taskStore.addUri.mockResolvedValue('success')
    const wrapper = mountHeader()
    const input = wrapper.find('input')

    await input.setValue('https://example.invalid/single')
    await input.trigger('keyup.enter')
    await wrapper.vm.$nextTick()

    expect(storeMocks.downloadGroupStore.fetchGroups).toHaveBeenCalled()
    expect(storeMocks.downloadGroupStore.addPlaceholdersFromDownloadGroups).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
