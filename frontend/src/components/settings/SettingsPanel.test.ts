import { defineComponent, h, nextTick, reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AppConfig } from '../../../bindings/goaria-v3/internal/config/models.js'
import { SaveConfigResult } from '../../../bindings/goaria-v3/internal/wailsapp/models.js'
import SettingsPanel from './SettingsPanel.vue'

vi.mock('vue-i18n', async importOriginal => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

function sampleConfig(overrides: Partial<AppConfig> = {}): AppConfig {
  return new AppConfig({
    rpc_port: '16800',
    rpc_secret: '',
    download_dir: '/downloads',
    max_connections: '16',
    max_concurrent_downloads: '5',
    user_agent: 'GoAria-Test/1.0',
    show_history: true,
    window_transparency: 'none',
    smart_thread_mode: true,
    min_thread_life: 5,
    close_to_tray: false,
    convergence_interval: 0,
    extension_enabled: true,
    extension_ws_port: 16801,
    extension_secret: 'managed',
    ...overrides,
  })
}

const storeMock = reactive({
  settings: sampleConfig(),
  isHydrated: false,
  isLoading: false,
  hydrateFailed: false,
  needsAppRestart: false,
  updateConfig: vi.fn(),
  applyCanonicalConfig: vi.fn((snapshot: AppConfig) => {
    Object.assign(storeMock.settings, snapshot)
  }),
  noteAppRestartRequired: vi.fn(() => {
    storeMock.needsAppRestart = true
  }),
  restartApp: vi.fn(),
  pickDirectory: vi.fn(),
  fetchConfig: vi.fn(),
})

vi.mock('../../stores/config', () => ({
  useConfigStore: () => storeMock,
}))

const TransitionStub = defineComponent({
  name: 'Transition',
  setup(_, { slots }) {
    return () => slots.default?.()
  },
})

const RPCStub = defineComponent({
  name: 'RPCSection',
  emits: ['change', 'update:port', 'update:secret'],
  setup(_, { emit }) {
    return () =>
      h('button', { class: 'rpc-change', onClick: () => emit('change') }, 'rpc')
  },
})

const UAStub = defineComponent({
  name: 'UASection',
  props: { modelValue: { type: String, default: '' } },
  emits: ['change', 'update:modelValue'],
  setup(props, { emit }) {
    return () =>
      h('div', [
        h('span', { class: 'ua-value' }, props.modelValue),
        h(
          'button',
          {
            class: 'ua-change',
            onClick: () => {
              emit('update:modelValue', 'edited-ua')
              emit('change')
            },
          },
          'ua',
        ),
      ])
  },
})

const PerformanceStub = defineComponent({
  name: 'PerformanceSection',
  props: {
    connections: { type: String, default: '' },
    concurrentDownloads: { type: String, default: '' },
    connectionOptions: { type: Array, default: () => [] },
    smartThreadMode: { type: Boolean, default: false },
  },
  emits: ['change', 'update:connections', 'update:concurrentDownloads', 'update:smartThreadMode'],
  setup(props, { emit }) {
    return () =>
      h('div', [
        h('span', { class: 'conn-display' }, String(props.connections)),
        h('span', { class: 'conn-options' }, (props.connectionOptions as string[]).join(',')),
        h(
          'button',
          {
            class: 'concurrent-change',
            onClick: () => {
              emit('update:concurrentDownloads', '99')
              emit('change')
            },
          },
          'concurrent',
        ),
      ])
  },
})

const DownloadStub = defineComponent({
  name: 'DownloadSection',
  props: { modelValue: { type: String, default: '' } },
  emits: ['pick'],
  setup(props, { emit }) {
    return () =>
      h('div', [
        h('span', { class: 'dir-display' }, props.modelValue),
        h('button', { class: 'dir-pick', onClick: () => emit('pick') }, 'pick'),
      ])
  },
})

const AdvancedStub = defineComponent({
  name: 'AdvancedSection',
  props: { transparency: { type: String, default: 'none' }, showHistory: { type: Boolean, default: false } },
  emits: ['change', 'update:transparency', 'update:showHistory'],
  setup(_, { emit }) {
    return () =>
      h('div', [
        h(
          'button',
          {
            class: 'transparency-change',
            onClick: () => {
              emit('update:transparency', 'mica')
              emit('change')
            },
          },
          'appearance',
        ),
        h(
          'button',
          {
            class: 'history-change',
            onClick: () => {
              emit('update:showHistory', false)
              emit('change')
            },
          },
          'history',
        ),
      ])
  },
})

const IndependentStub = defineComponent({
  name: 'IndependentSection',
  setup() {
    return () => h('button', { class: 'independent-section', type: 'button' }, 'independent')
  },
})

function mountPanel() {
  return mount(SettingsPanel, {
    global: {
      stubs: {
        Transition: TransitionStub,
        DownloadSection: DownloadStub,
        RPCSection: RPCStub,
        PerformanceSection: PerformanceStub,
        UASection: UAStub,
        AppearanceSection: IndependentStub,
        AdvancedSection: AdvancedStub,
        ExtensionSection: IndependentStub,
        UpdateSection: IndependentStub,
      },
    },
  })
}

async function flushHydration() {
  await nextTick()
  await nextTick()
}

describe('SettingsPanel', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    storeMock.isHydrated = false
    storeMock.isLoading = false
    storeMock.hydrateFailed = false
    storeMock.needsAppRestart = false
    storeMock.settings = sampleConfig()
    storeMock.updateConfig.mockReset()
    storeMock.applyCanonicalConfig.mockClear()
    storeMock.noteAppRestartRequired.mockClear()
    storeMock.restartApp.mockReset()
    storeMock.pickDirectory.mockReset()
    storeMock.fetchConfig.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('does not autosave before backend hydration even after 100ms', async () => {
    const wrapper = mountPanel()
    await wrapper.find('.rpc-change').trigger('click')
    await vi.advanceTimersByTimeAsync(200)
    expect(storeMock.updateConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows backend 64 after hydration and keeps it on unrelated edits', async () => {
    const wrapper = mountPanel()
    storeMock.settings = sampleConfig({ max_connections: '64' })
    storeMock.isHydrated = true
    await flushHydration()
    expect(wrapper.find('.conn-display').text()).toBe('64')
    expect(wrapper.find('.conn-options').text()).toBe('1,4,8,16,24,32')

    storeMock.updateConfig.mockResolvedValue(
      new SaveConfigResult({
        success: true,
        config: sampleConfig({ max_connections: '64', user_agent: 'edited-ua' }),
      }),
    )
    await wrapper.find('.ua-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    const sent = storeMock.updateConfig.mock.calls[0][0] as AppConfig
    expect(sent.max_connections).toBe('64')
    wrapper.unmount()
  })

  it('ignores a stale completion while a newer edit is debounced', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()

    let resolveA: (value: SaveConfigResult) => void = () => undefined
    storeMock.updateConfig.mockImplementationOnce(
      () =>
        new Promise<SaveConfigResult>(resolve => {
          resolveA = resolve
        }),
    )
    await wrapper.find('.ua-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)

    storeMock.updateConfig.mockImplementationOnce(async (candidate: AppConfig) => {
      return new SaveConfigResult({ success: true, config: candidate })
    })
    await wrapper.find('.transparency-change').trigger('click')

    resolveA(
      new SaveConfigResult({
        success: true,
        config: sampleConfig({ user_agent: 'stale-A', window_transparency: 'none' }),
      }),
    )
    await flushPromises()
    expect(storeMock.settings.user_agent).not.toBe('stale-A')
    expect(wrapper.text()).not.toContain('settings.saved')

    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(storeMock.settings.window_transparency).toBe('mica')
    wrapper.unmount()
  })

  it('shows error not saved on business failure and syncs rollback config', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()
    const rolled = sampleConfig({ user_agent: 'rolled-back' })
    storeMock.updateConfig.mockResolvedValue(
      new SaveConfigResult({ success: false, config: rolled, error_code: 'config_persist_failed' }),
    )
    await wrapper.find('.ua-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(wrapper.text()).toContain('settings.errors.persistFailed')
    expect(wrapper.text()).not.toContain('settings.saveFailed')
    expect(wrapper.text()).not.toContain('settings.saved')
    expect(storeMock.settings.user_agent).toBe('rolled-back')
    expect(wrapper.find('.ua-value').text()).toBe('rolled-back')
    wrapper.unmount()
  })

  it('keeps the user edit on transport failure', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()
    storeMock.updateConfig.mockRejectedValue(new Error('ipc'))
    await wrapper.find('.ua-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(wrapper.find('.ua-value').text()).toBe('edited-ua')
    expect(wrapper.text()).toContain('settings.saveFailed')
    expect(wrapper.text()).not.toContain('settings.saved')
    wrapper.unmount()
  })

  it('syncs canonical backend values after a successful save', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()
    storeMock.updateConfig.mockResolvedValue(
      new SaveConfigResult({
        success: true,
        config: sampleConfig({ max_concurrent_downloads: '5' }),
      }),
    )
    await wrapper.find('.concurrent-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(storeMock.settings.max_concurrent_downloads).toBe('5')
    expect(wrapper.text()).toContain('settings.saved')
    wrapper.unmount()
  })

  it('does not save when directory picker is cancelled', async () => {
    storeMock.isHydrated = true
    storeMock.pickDirectory.mockResolvedValue(null)
    const wrapper = mountPanel()
    await flushHydration()
    await wrapper.find('.dir-pick').trigger('click')
    await flushPromises()
    expect(storeMock.updateConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('triggers one debounced save after picking a directory', async () => {
    storeMock.isHydrated = true
    storeMock.pickDirectory.mockResolvedValue('/new-dir')
    storeMock.updateConfig.mockResolvedValue(
      new SaveConfigResult({ success: true, config: sampleConfig({ download_dir: '/new-dir' }) }),
    )
    const wrapper = mountPanel()
    await flushHydration()
    await wrapper.find('.dir-pick').trigger('click')
    await flushPromises()
    expect(storeMock.updateConfig).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(storeMock.updateConfig).toHaveBeenCalledTimes(1)
    expect((storeMock.updateConfig.mock.calls[0][0] as AppConfig).download_dir).toBe('/new-dir')
    wrapper.unmount()
  })

  it('ignores late responses after unmount', async () => {
    storeMock.isHydrated = true
    let resolveSave: (value: SaveConfigResult) => void = () => undefined
    storeMock.updateConfig.mockImplementation(
      () =>
        new Promise<SaveConfigResult>(resolve => {
          resolveSave = resolve
        }),
    )
    const wrapper = mountPanel()
    await flushHydration()
    await wrapper.find('.ua-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    wrapper.unmount()
    resolveSave(new SaveConfigResult({ success: true, config: sampleConfig({ user_agent: 'late' }) }))
    await flushPromises()
    expect(storeMock.settings.user_agent).not.toBe('late')
  })

  it('shows load error, persisted preview, and blocks saves when hydration failed', async () => {
    storeMock.settings = sampleConfig({ user_agent: 'from-storage' })
    storeMock.isHydrated = false
    storeMock.isLoading = false
    storeMock.hydrateFailed = true
    const wrapper = mountPanel()
    await flushHydration()
    expect(wrapper.text()).toContain('settings.loadFailed')
    expect(wrapper.text()).toContain('settings.retry')
    expect(wrapper.find('.ua-value').text()).toBe('from-storage')
    expect(wrapper.find('fieldset').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('.independent-section').length).toBeGreaterThan(0)
    expect(wrapper.find('fieldset .independent-section').exists()).toBe(false)
    await wrapper.find('.rpc-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    expect(storeMock.updateConfig).not.toHaveBeenCalled()
    await wrapper.find('.retry-hydrate').trigger('click')
    expect(storeMock.fetchConfig).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('keeps a session restart latch after a later non-restart save', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()
    storeMock.updateConfig.mockResolvedValue(
      new SaveConfigResult({
        success: true,
        requires_app_restart: true,
        config: sampleConfig({ window_transparency: 'mica' }),
      }),
    )
    await wrapper.find('.transparency-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(storeMock.noteAppRestartRequired).toHaveBeenCalled()
    expect(wrapper.text()).toContain('settings.requiresAppRestart')
    expect(wrapper.find('.restart-now').exists()).toBe(true)

    storeMock.updateConfig.mockResolvedValue(
      new SaveConfigResult({
        success: true,
        requires_app_restart: false,
        config: sampleConfig({ window_transparency: 'mica', show_history: false }),
      }),
    )
    await wrapper.find('.history-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(wrapper.text()).toContain('settings.requiresAppRestart')
    expect(wrapper.text()).toContain('settings.saved')
    wrapper.unmount()
  })

  it('latches restart from a stale in-flight save while a newer edit completes', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()

    let resolveA: (value: SaveConfigResult) => void = () => undefined
    storeMock.updateConfig.mockImplementationOnce(
      () =>
        new Promise<SaveConfigResult>(resolve => {
          resolveA = resolve
        }),
    )
    await wrapper.find('.transparency-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)

    storeMock.updateConfig.mockImplementationOnce(async (candidate: AppConfig) => {
      return new SaveConfigResult({ success: true, requires_app_restart: false, config: candidate })
    })
    await wrapper.find('.ua-change').trigger('click')

    resolveA(
      new SaveConfigResult({
        success: true,
        requires_app_restart: true,
        config: sampleConfig({ user_agent: 'stale-A', window_transparency: 'mica' }),
      }),
    )
    await flushPromises()
    expect(storeMock.noteAppRestartRequired).toHaveBeenCalled()
    expect(storeMock.settings.user_agent).not.toBe('stale-A')

    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(wrapper.text()).toContain('settings.requiresAppRestart')
    wrapper.unmount()
  })

  it('maps known error codes to i18n keys without backend strings', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()
    storeMock.updateConfig.mockResolvedValue(
      new SaveConfigResult({
        success: false,
        config: sampleConfig(),
        error_code: 'download_dir_unavailable',
        message: 'Download directory is unavailable.',
      }),
    )
    await wrapper.find('.ua-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(wrapper.text()).toContain('settings.errors.downloadDirUnavailable')
    expect(wrapper.text()).not.toContain('Download directory is unavailable.')
    wrapper.unmount()
  })

  it('treats a successful save without config as an error', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()
    storeMock.updateConfig.mockResolvedValue({ success: true, config: null } as unknown as SaveConfigResult)
    await wrapper.find('.ua-change').trigger('click')
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(wrapper.text()).toContain('settings.saveFailed')
    expect(storeMock.applyCanonicalConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('ignores a directory pick that resolves after unmount', async () => {
    storeMock.isHydrated = true
    let resolvePick: (value: string | null) => void = () => undefined
    storeMock.pickDirectory.mockImplementation(
      () =>
        new Promise<string | null>(resolve => {
          resolvePick = resolve
        }),
    )
    const wrapper = mountPanel()
    await flushHydration()
    const pending = wrapper.find('.dir-pick').trigger('click')
    wrapper.unmount()
    resolvePick('/late-dir')
    await pending
    await flushPromises()
    expect(storeMock.updateConfig).not.toHaveBeenCalled()
  })

  it('keeps the load banner and disables retry while a fetch is in flight', async () => {
    storeMock.isHydrated = false
    storeMock.isLoading = true
    storeMock.hydrateFailed = true
    const wrapper = mountPanel()
    await flushHydration()
    expect(wrapper.text()).toContain('settings.loadFailed')
    expect(wrapper.find('.retry-hydrate').attributes('disabled')).toBeDefined()
    await wrapper.find('.retry-hydrate').trigger('click')
    expect(storeMock.fetchConfig).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('unlocks config editors after a successful retry hydration', async () => {
    storeMock.isHydrated = false
    storeMock.hydrateFailed = true
    const wrapper = mountPanel()
    await flushHydration()
    expect(wrapper.find('fieldset').attributes('disabled')).toBeDefined()
    storeMock.isHydrated = true
    storeMock.hydrateFailed = false
    await flushHydration()
    expect(wrapper.find('fieldset').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })
})
