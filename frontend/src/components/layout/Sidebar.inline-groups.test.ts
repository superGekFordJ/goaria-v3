import { mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Sidebar from './Sidebar.vue'

const rawStoreMocks = vi.hoisted(() => ({
  uiStore: {
    activeTab: 'downloads' as 'downloads' | 'stopped' | 'settings',
    setActiveTab: vi.fn((tab: 'downloads' | 'stopped' | 'settings') => {
      rawStoreMocks.uiStore.activeTab = tab
    }),
  },
  taskStore: {
    activeTasks: [{ gid: 'gid-hidden-a', downloadSpeed: '10' }],
    waitingTasks: [{ gid: 'gid-hidden-b', downloadSpeed: '0' }],
    stoppedTasks: [{ gid: 'gid-hidden-c', downloadSpeed: '0' }],
  },
  downloadGroupStore: {
    inlineDownloadsCount: 1,
    inlineCompletedCount: 2,
    visibleGroupCount: 99,
  },
  configStore: {
    settings: { rpc_port: 6800 },
    aria2Connected: true,
  },
}))

const storeMocks = {
  uiStore: reactive(rawStoreMocks.uiStore),
  taskStore: reactive(rawStoreMocks.taskStore),
  downloadGroupStore: reactive(rawStoreMocks.downloadGroupStore),
  configStore: reactive(rawStoreMocks.configStore),
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => storeMocks.uiStore,
}))

vi.mock('../../stores/task', () => ({
  useTaskStore: () => storeMocks.taskStore,
}))

vi.mock('../../stores/downloadGroups', () => ({
  useDownloadGroupStore: () => storeMocks.downloadGroupStore,
}))

vi.mock('../../stores/config', () => ({
  useConfigStore: () => storeMocks.configStore,
}))

vi.mock('../common/ThemeIcon.vue', () => ({
  default: { name: 'ThemeIcon', template: '<div data-test="theme-icon" />' },
}))

describe('Sidebar inline groups', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMocks.uiStore.activeTab = 'downloads'
    storeMocks.downloadGroupStore.inlineDownloadsCount = 1
    storeMocks.downloadGroupStore.inlineCompletedCount = 2
    storeMocks.downloadGroupStore.visibleGroupCount = 99
  })

  it('Sidebar renders no Groups nav and counts inline entries after group replacement', async () => {
    const wrapper = mount(Sidebar)

    expect(wrapper.text()).toContain('sidebar.inProgress')
    expect(wrapper.text()).toContain('sidebar.completed')
    expect(wrapper.text()).toContain('sidebar.settings')
    expect(wrapper.text()).not.toContain('downloadGroups.navLabel')
    expect(wrapper.text()).not.toContain('99')
    expect(wrapper.text()).toContain('1')
    expect(wrapper.text()).toContain('2')

    const buttons = wrapper.findAll('button')
    await buttons[1]?.trigger('click')
    expect(storeMocks.uiStore.setActiveTab).toHaveBeenCalledWith('stopped')
    expect(storeMocks.uiStore.setActiveTab).not.toHaveBeenCalledWith('groups')

    // Connected state asserts port
    expect(wrapper.text()).toContain('6800')
    expect(wrapper.text()).not.toContain('sidebar.aria2Offline')

    // Disconnected state asserts offline text
    storeMocks.configStore.aria2Connected = false
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('sidebar.aria2Offline')

    wrapper.unmount()
  })
})
