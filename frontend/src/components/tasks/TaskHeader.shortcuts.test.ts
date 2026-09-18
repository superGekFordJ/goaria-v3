import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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

describe('TaskHeader shortcut hints and platform differentiation', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    vi.resetModules()
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('renders default shortcut tips with Ctrl on non-Mac platform', async () => {
    vi.doMock('@wailsio/runtime', () => ({
      System: {
        IsMac: () => false,
      },
    }))

    const { default: TaskHeader } = await import('./TaskHeader.vue')
    const wrapper = mount(TaskHeader, { attachTo: document.body })

    const kbdElements = wrapper.findAll('kbd')
    const kbdTexts = kbdElements.map(k => k.text())

    expect(kbdTexts).toContain('Enter')
    expect(kbdTexts).toContain('Ctrl+V')
    expect(kbdTexts).toContain('Ctrl+A')
    expect(wrapper.text()).toContain('taskHeader.quickAdd')
    expect(wrapper.text()).toContain('taskHeader.pasteLink')
    expect(wrapper.text()).toContain('taskHeader.selectAllTasks')
  })

  it('renders shortcut tips with Cmd on Mac platform', async () => {
    vi.doMock('@wailsio/runtime', () => ({
      System: {
        IsMac: () => true,
      },
    }))

    const { default: TaskHeader } = await import('./TaskHeader.vue')
    const wrapper = mount(TaskHeader, { attachTo: document.body })

    const kbdElements = wrapper.findAll('kbd')
    const kbdTexts = kbdElements.map(k => k.text())

    expect(kbdTexts).toContain('Enter')
    expect(kbdTexts).toContain('Cmd+V')
    expect(kbdTexts).toContain('Cmd+A')
    expect(kbdTexts).not.toContain('Ctrl+V')
    expect(kbdTexts).not.toContain('Ctrl+A')
    expect(wrapper.text()).toContain('taskHeader.selectAllTasks')
  })

  it('shows Cmd+Enter in multiline mode on Mac and handles Meta+Enter keydown', async () => {
    vi.doMock('@wailsio/runtime', () => ({
      System: {
        IsMac: () => true,
      },
    }))

    storeMocks.taskStore.batchAddUri.mockResolvedValue({
      succeeded: ['https://example.com/a'],
      duplicates: [],
      errors: {},
      groups: [],
    })

    const { default: TaskHeader } = await import('./TaskHeader.vue')
    const wrapper = mount(TaskHeader, { attachTo: document.body })

    // Paste multiline text
    await wrapper.find('input').trigger('paste', {
      clipboardData: {
        getData: () => 'https://example.com/a\nhttps://example.com/b',
      },
    })
    vi.advanceTimersByTime(300)
    await wrapper.vm.$nextTick()

    // Should be in multiline mode
    const kbdElements = wrapper.findAll('kbd')
    const kbdTexts = kbdElements.map(k => k.text())
    expect(kbdTexts).toContain('Cmd+Enter')
    expect(kbdTexts).toContain('Esc')

    // Trigger Meta+Enter on textarea
    const textarea = wrapper.find('textarea')
    await textarea.trigger('keydown', { key: 'Enter', metaKey: true })
    await wrapper.vm.$nextTick()

    expect(storeMocks.taskStore.batchAddUri).toHaveBeenCalledWith([
      'https://example.com/a',
      'https://example.com/b',
    ])
  })
})
