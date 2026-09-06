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
  isSaving: false,
  hydrateFailed: false,
  updateConfig: vi.fn(),
  applyCanonicalConfig: vi.fn((snapshot: AppConfig) => {
    Object.assign(storeMock.settings, snapshot)
  }),
  pickDirectory: vi.fn(),
  fetchConfig: vi.fn(),
})

vi.mock('../../stores/config', () => ({
  useConfigStore: () => storeMock,
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => ({
    effectsTier: 'full',
    effectsLevel: 100,
  }),
}))

vi.mock('../../composables/useLiquidGlass', () => ({
  useLiquidGlass: () => ({ filterId: { value: 'lg-test' } }),
  getStaticGlassFilterId: () => 'static-glass-filter',
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
    return () => h('button', { class: 'rpc-change', onClick: () => emit('change') }, 'rpc')
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
  props: {
    transparency: { type: String, default: 'none' },
    showHistory: { type: Boolean, default: false },
  },
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
    attachTo: document.body,
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

function mockNavigationGeometry(wrapper: ReturnType<typeof mountPanel>) {
  const scroller = wrapper.get<HTMLElement>('.overflow-y-auto')
  Object.defineProperties(scroller.element, {
    clientHeight: { configurable: true, value: 500 },
    scrollHeight: { configurable: true, value: 2560 },
  })
  vi.spyOn(scroller.element, 'getBoundingClientRect').mockImplementation(
    () => new DOMRect(0, 0, 760, 500),
  )
  let dockTop = 32
  vi.spyOn(
    wrapper.get<HTMLElement>('.settings-capsule-sentinel').element,
    'getBoundingClientRect',
  ).mockImplementation(() => new DOMRect(298, dockTop - scroller.element.scrollTop, 164, 34))
  wrapper.findAll<HTMLElement>('[data-settings-section]').forEach((section, index) => {
    vi.spyOn(section.element, 'getBoundingClientRect').mockImplementation(
      () => new DOMRect(24, 104 + index * 300 - scroller.element.scrollTop, 672, 280),
    )
  })
  const scrollTo = vi.spyOn(scroller.element, 'scrollTo').mockImplementation(() => undefined)
  return {
    scroller,
    scrollTo,
    setDockTop(value: number) {
      dockTop = value
    },
    async scroll(top: number) {
      scroller.element.scrollTop = top
      await scroller.trigger('scroll')
      await vi.advanceTimersByTimeAsync(20)
    },
  }
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
    storeMock.isSaving = false
    storeMock.hydrateFailed = false
    storeMock.settings = sampleConfig()
    storeMock.updateConfig.mockReset()
    storeMock.applyCanonicalConfig.mockClear()
    storeMock.pickDirectory.mockReset()
    storeMock.fetchConfig.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
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
    resolveSave(
      new SaveConfigResult({ success: true, config: sampleConfig({ user_agent: 'late' }) }),
    )
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
    storeMock.updateConfig.mockResolvedValue({
      success: true,
      config: null,
    } as unknown as SaveConfigResult)
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

  it('keeps navigation visible at the top and while saving, saved or idle', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()

    // 1. Initial state: not scrolled, idle -> docked navigation is available
    expect(wrapper.get('[data-docking]').attributes('data-docking')).toBe('docked')
    expect(wrapper.get('[data-testid="settings-navigation-toggle"]').text()).toContain(
      'download.title',
    )

    // 2. Scroll past header while idle -> floating navigation stays available
    const scroller = wrapper.find('.overflow-y-auto')
    Object.defineProperty(scroller.element, 'scrollTop', {
      value: 100,
      configurable: true,
      writable: true,
    })
    await scroller.trigger('scroll')
    await vi.advanceTimersByTimeAsync(20)
    expect(wrapper.get('[data-docking]').attributes('data-docking')).toBe('floating')

    // 3. Trigger an edit -> status becomes 'saving' -> the same capsule announces it
    storeMock.updateConfig.mockResolvedValue(
      new SaveConfigResult({
        success: true,
        config: sampleConfig({ user_agent: 'edited-ua' }),
      }),
    )
    await wrapper.find('.ua-change').trigger('click')
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="floating-save-status"]').text()).toContain('settings.saving')
    expect(wrapper.find('.capsule-status .animate-spin').exists()).toBe(true)

    // 4. After debounce save completes -> floating capsule shows 'saved'
    await vi.advanceTimersByTimeAsync(800)
    await flushPromises()
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="floating-save-status"]').text()).toContain('settings.saved')

    // 5. After scheduleReset (1500ms) -> saveStatus resets to idle -> navigation remains
    await vi.advanceTimersByTimeAsync(1500)
    await nextTick()
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(true)
    expect(wrapper.find('.capsule-ready-dot').exists()).toBe(true)

    wrapper.unmount()
  })

  it('redocks the same saving capsule when scrolled back to the top', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()

    const scroller = wrapper.find('.overflow-y-auto')
    Object.defineProperty(scroller.element, 'scrollTop', {
      value: 100,
      configurable: true,
      writable: true,
    })
    await scroller.trigger('scroll')

    storeMock.updateConfig.mockImplementation(
      () => new Promise(() => undefined), // pending
    )
    await wrapper.find('.ua-change').trigger('click')
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(true)

    // Scroll back to top
    Object.defineProperty(scroller.element, 'scrollTop', {
      value: 0,
      configurable: true,
      writable: true,
    })
    await scroller.trigger('scroll')
    await vi.advanceTimersByTimeAsync(20)
    expect(wrapper.get('[data-docking]').attributes('data-docking')).toBe('docked')
    expect(wrapper.get('[data-testid="floating-save-status"]').text()).toContain('settings.saving')

    wrapper.unmount()
  })

  it('navigates inside the settings viewport from the initial dock and tracks manual scrolling', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    const geometry = mockNavigationGeometry(wrapper)
    await flushHydration()
    await geometry.scroll(0)
    expect(wrapper.get('[data-docking]').attributes('style')).toContain('--capsule-top: 32px')
    await wrapper.get('[data-testid="settings-navigation-toggle"]').trigger('click')
    expect(wrapper.findAll('[data-section-link]')).toHaveLength(8)
    await wrapper.get('[data-section-link="appearance"]').trigger('click')
    expect(geometry.scrollTo).toHaveBeenCalledWith({ top: 1242, behavior: 'smooth' })
    expect(document.activeElement).toBe(wrapper.get('#settings-section-appearance').element)
    expect(
      wrapper.get('[data-testid="settings-navigation-toggle"]').attributes('aria-expanded'),
    ).toBe('false')
    expect(storeMock.updateConfig).not.toHaveBeenCalled()
    await geometry.scroll(1242)
    expect(wrapper.get('[data-docking]').attributes('data-docking')).toBe('floating')
    expect(wrapper.get('[data-docking]').attributes('style')).toContain('--capsule-top: 12px')
    expect(wrapper.get('[data-testid="settings-navigation-toggle"]').text()).toContain(
      'appearance.title',
    )
    await geometry.scroll(2060)
    expect(wrapper.get('[data-testid="settings-navigation-toggle"]').text()).toContain(
      'settings.navigation.updates',
    )
    await geometry.scroll(0)
    expect(wrapper.get('[data-testid="settings-navigation-toggle"]').text()).toContain(
      'download.title',
    )
    expect(wrapper.get('[data-docking]').attributes('data-docking')).toBe('docked')
    wrapper.unmount()
  })

  it('remeasures docking on resize and disconnects navigation observers on unmount', async () => {
    const callbacks: Array<() => void> = []
    const disconnect = vi.fn()
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(callback: () => void) {
          callbacks.push(callback)
        }
        observe() {}
        disconnect = disconnect
      },
    )
    const wrapper = mountPanel()
    const geometry = mockNavigationGeometry(wrapper)
    geometry.setDockTop(98)
    callbacks.forEach(callback => callback())
    await vi.advanceTimersByTimeAsync(20)
    expect(wrapper.get('[data-docking]').attributes('style')).toContain('--capsule-top: 98px')
    callbacks.forEach(callback => callback())
    wrapper.unmount()
    expect(disconnect).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(20)
  })

  it('keeps navigation available before hydration and respects reduced motion for anchor jumps', async () => {
    const wrapper = mountPanel()
    const geometry = mockNavigationGeometry(wrapper)
    vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: true } as MediaQueryList)
    await wrapper.get('[data-testid="settings-navigation-toggle"]').trigger('click')
    await wrapper.get('[data-section-link="update"]').trigger('click')
    expect(geometry.scrollTo).toHaveBeenCalledWith({ top: 2142, behavior: 'instant' })
    expect(storeMock.updateConfig).not.toHaveBeenCalled()
    expect(wrapper.get('[role="status"]').text()).toBe('settings.navigation.loading')
    wrapper.unmount()
  })

  it('does not render ExtractorSection in generic build', async () => {
    storeMock.isHydrated = true
    const wrapper = mountPanel()
    await flushHydration()

    expect(wrapper.find('[data-testid="load-zip-btn"]').exists()).toBe(false)
    expect(wrapper.find('[data-section-link="extractor"]').exists()).toBe(false)
    expect(wrapper.find('#settings-section-extractor').exists()).toBe(false)
    wrapper.unmount()
  })

  it('renders ExtractorSection when VITE_GOARIA_EXTRACTOR is true', async () => {
    vi.stubEnv('VITE_GOARIA_EXTRACTOR', 'true')
    // Preload ExtractorSection module for async component resolution
    await import('../../features/extractor/ExtractorSection.vue')
    // @ts-expect-error query param is used for Vitest module cache busting
    const { default: TaggedSettingsPanel } = await import('./SettingsPanel.vue?tagged=1')
    storeMock.isHydrated = true
    const wrapper = mount(TaggedSettingsPanel, {
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
    await flushHydration()
    await flushPromises()

    expect(
      wrapper.find('[data-testid="load-zip-btn"]').exists() ||
        wrapper.find('[data-testid="loading-indicator"]').exists() ||
        wrapper.find('[data-testid="unavailable-banner"]').exists(),
    ).toBe(true)

    expect(wrapper.findAll('[data-section-link]')).toHaveLength(9)
    expect(wrapper.get('[data-section-link="extractor"]').attributes('href')).toBe(
      '#settings-section-extractor',
    )
    expect(wrapper.find('#settings-section-extractor').exists()).toBe(true)
    wrapper.unmount()
    vi.unstubAllEnvs()
  })
})
