import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Task } from '../../../bindings/goaria-v3/internal/rpc/models'

const storeMocks = vi.hoisted(() => {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  const { ref: vueRef } = require('vue')
  return {
    taskStore: {
      allUris: new Set<string>(),
      addUri: vi.fn(),
      batchAddUri: vi.fn(),
      activeTasks: vueRef([] as Task[]),
      waitingTasks: vueRef([] as Task[]),
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
  }
})

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
    storeMocks.taskStore.activeTasks.value = []
    storeMocks.taskStore.waitingTasks.value = []
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('renders only Paste tip when no input and no tasks on non-Mac platform', async () => {
    vi.doMock('@wailsio/runtime', () => ({
      System: {
        IsMac: () => false,
      },
    }))

    const { default: TaskHeader } = await import('./TaskHeader.vue')
    const wrapper = mount(TaskHeader, { attachTo: document.body })

    const kbdTexts = wrapper.findAll('kbd').map(k => k.text())

    expect(kbdTexts).toContain('Ctrl+V')
    expect(kbdTexts).not.toContain('Ctrl+A')
    expect(kbdTexts).not.toContain('Enter')
    expect(wrapper.text()).toContain('taskHeader.pasteLink')
    expect(wrapper.text()).not.toContain('taskHeader.selectAllTasks')
    expect(wrapper.text()).not.toContain('taskHeader.quickAdd')
  })

  it('renders Paste and Select All tips when no input but tasks exist on non-Mac platform', async () => {
    vi.doMock('@wailsio/runtime', () => ({
      System: {
        IsMac: () => false,
      },
    }))
    storeMocks.taskStore.activeTasks.value = [{ gid: 'test-gid-1' } as unknown as Task]

    const { default: TaskHeader } = await import('./TaskHeader.vue')
    const wrapper = mount(TaskHeader, { attachTo: document.body })

    const kbdTexts = wrapper.findAll('kbd').map(k => k.text())

    expect(kbdTexts).toContain('Ctrl+V')
    expect(kbdTexts).toContain('Ctrl+A')
    expect(kbdTexts).not.toContain('Enter')
    expect(wrapper.text()).toContain('taskHeader.pasteLink')
    expect(wrapper.text()).toContain('taskHeader.selectAllTasks')
    expect(wrapper.text()).not.toContain('taskHeader.quickAdd')
  })

  it('renders Quick Add tip only when input is focused and URL is present on non-Mac platform', async () => {
    vi.doMock('@wailsio/runtime', () => ({
      System: {
        IsMac: () => false,
      },
    }))
    storeMocks.taskStore.activeTasks.value = [{ gid: 'test-gid-1' } as unknown as Task]

    const { default: TaskHeader } = await import('./TaskHeader.vue')
    const wrapper = mount(TaskHeader, { attachTo: document.body })

    const input = wrapper.find('input')
    await input.setValue('https://example.com/test.zip')
    await wrapper.vm.$nextTick()

    // When not focused, shows task tips rather than Enter
    let kbdTexts = wrapper.findAll('kbd').map(k => k.text())
    expect(kbdTexts).toContain('Ctrl+V')
    expect(kbdTexts).toContain('Ctrl+A')
    expect(kbdTexts).not.toContain('Enter')

    // Focus input -> switches to Enter Quick Add
    await input.trigger('focus')
    await wrapper.vm.$nextTick()
    kbdTexts = wrapper.findAll('kbd').map(k => k.text())
    expect(kbdTexts).toContain('Enter')
    expect(kbdTexts).not.toContain('Ctrl+V')
    expect(kbdTexts).not.toContain('Ctrl+A')
    expect(wrapper.text()).toContain('taskHeader.quickAdd')
    expect(wrapper.text()).not.toContain('taskHeader.pasteLink')
    expect(wrapper.text()).not.toContain('taskHeader.selectAllTasks')

    // Blur input -> switches back to task tips
    await input.trigger('blur')
    await wrapper.vm.$nextTick()
    kbdTexts = wrapper.findAll('kbd').map(k => k.text())
    expect(kbdTexts).toContain('Ctrl+V')
    expect(kbdTexts).toContain('Ctrl+A')
    expect(kbdTexts).not.toContain('Enter')
  })

  it('renders Mac shortcut tips (Cmd+V, Cmd+A) according to task and focus context', async () => {
    vi.doMock('@wailsio/runtime', () => ({
      System: {
        IsMac: () => true,
      },
    }))

    const { default: TaskHeader } = await import('./TaskHeader.vue')
    const wrapper = mount(TaskHeader, { attachTo: document.body })

    // 1. Empty input + no tasks
    let kbdTexts = wrapper.findAll('kbd').map(k => k.text())
    expect(kbdTexts).toContain('Cmd+V')
    expect(kbdTexts).not.toContain('Cmd+A')
    expect(kbdTexts).not.toContain('Ctrl+V')
    expect(kbdTexts).not.toContain('Enter')

    // 2. Empty input + with tasks
    storeMocks.taskStore.waitingTasks.value = [{ gid: 'waiting-1' } as unknown as Task]
    await wrapper.vm.$nextTick()
    kbdTexts = wrapper.findAll('kbd').map(k => k.text())
    expect(kbdTexts).toContain('Cmd+V')
    expect(kbdTexts).toContain('Cmd+A')
    expect(kbdTexts).not.toContain('Cmd+Enter')
    expect(kbdTexts).not.toContain('Ctrl+A')

    // 3. With input and focused
    const input = wrapper.find('input')
    await input.setValue('https://example.com/mac.dmg')
    await input.trigger('focus')
    await wrapper.vm.$nextTick()
    kbdTexts = wrapper.findAll('kbd').map(k => k.text())
    expect(kbdTexts).toContain('Enter')
    expect(kbdTexts).not.toContain('Cmd+V')
    expect(kbdTexts).not.toContain('Cmd+A')
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
