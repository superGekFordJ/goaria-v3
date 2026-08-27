import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { AppConfig } from '../../../bindings/goaria-v3/internal/config/models.js'
import { SaveConfigResult } from '../../../bindings/goaria-v3/internal/wailsapp/models.js'

const bindingMocks = vi.hoisted(() => ({
  GetConfig: vi.fn(),
  GetAria2Connected: vi.fn(),
  SaveConfig: vi.fn(),
  SelectDirectory: vi.fn(),
}))

vi.mock('../../../bindings/goaria-v3/internal/wailsapp/app.js', () => bindingMocks)

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

describe('useConfigStore', () => {
  beforeEach(async () => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    localStorage.clear()
  })

  it('stays unhydrated until GetConfig succeeds', async () => {
    let resolveConfig: (value: AppConfig) => void = () => undefined
    bindingMocks.GetConfig.mockReturnValue(
      new Promise<AppConfig>(resolve => {
        resolveConfig = resolve
      }),
    )
    const { useConfigStore } = await import('../config')
    const store = useConfigStore()
    const pending = store.fetchConfig()
    expect(store.isHydrated).toBe(false)
    const backend = sampleConfig({ max_connections: '64' })
    resolveConfig(backend)
    await pending
    expect(store.isHydrated).toBe(true)
    expect(store.settings.max_connections).toBe('64')
  })

  it('failed GetConfig does not hydrate or wipe persisted settings', async () => {
    const { useConfigStore } = await import('../config')
    const store = useConfigStore()
    store.settings.user_agent = 'kept-from-storage'
    bindingMocks.GetConfig.mockRejectedValue(new Error('offline'))
    await store.fetchConfig()
    expect(store.isHydrated).toBe(false)
    expect(store.settings.user_agent).toBe('kept-from-storage')
  })

  it('does not mutate the caller snapshot while awaiting SaveConfig', async () => {
    const { useConfigStore } = await import('../config')
    const store = useConfigStore()
    let captured: AppConfig | undefined
    bindingMocks.SaveConfig.mockImplementation(async (cfg: AppConfig) => {
      await Promise.resolve()
      captured = cfg
      return new SaveConfigResult({ success: true, config: cfg })
    })
    const candidate = sampleConfig({ user_agent: 'original' })
    const pending = store.updateConfig(candidate)
    candidate.user_agent = 'mutated-after-submit'
    await pending
    expect(captured?.user_agent).toBe('original')
  })

  it('does not implicitly write settings on success or failure', async () => {
    const { useConfigStore } = await import('../config')
    const store = useConfigStore()
    store.settings.max_connections = '16'
    const canonical = sampleConfig({ max_connections: '5' })
    bindingMocks.SaveConfig.mockResolvedValue(
      new SaveConfigResult({ success: true, config: canonical }),
    )
    await store.updateConfig(sampleConfig({ max_concurrent_downloads: '99' }))
    expect(store.settings.max_connections).toBe('16')
    store.applyCanonicalConfig(canonical)
    expect(store.settings.max_connections).toBe('5')

    bindingMocks.SaveConfig.mockResolvedValue(
      new SaveConfigResult({ success: false, config: sampleConfig({ max_connections: '16' }) }),
    )
    await store.updateConfig(sampleConfig({ max_connections: '64' }))
    expect(store.settings.max_connections).toBe('5')
  })

  it('rethrows transport failures and resets the inflight counter', async () => {
    const { useConfigStore } = await import('../config')
    const store = useConfigStore()
    bindingMocks.SaveConfig.mockRejectedValue(new Error('ipc down'))
    await expect(store.updateConfig(sampleConfig())).rejects.toThrow('ipc down')
    expect(store.isSaving).toBe(false)
  })

  it('keeps isSaving true while a second request is still in flight', async () => {
    const { useConfigStore } = await import('../config')
    const store = useConfigStore()
    let releaseFirst: () => void = () => undefined
    let releaseSecond: () => void = () => undefined
    const first = new Promise<SaveConfigResult>(resolve => {
      releaseFirst = () => resolve(new SaveConfigResult({ success: true, config: sampleConfig() }))
    })
    const second = new Promise<SaveConfigResult>(resolve => {
      releaseSecond = () => resolve(new SaveConfigResult({ success: true, config: sampleConfig() }))
    })
    bindingMocks.SaveConfig.mockReturnValueOnce(first).mockReturnValueOnce(second)
    const p1 = store.updateConfig(sampleConfig({ user_agent: 'a' }))
    const p2 = store.updateConfig(sampleConfig({ user_agent: 'b' }))
    expect(store.isSaving).toBe(true)
    releaseFirst()
    await p1
    expect(store.isSaving).toBe(true)
    releaseSecond()
    await p2
    expect(store.isSaving).toBe(false)
  })

  it('pickDirectory does not mutate settings', async () => {
    const { useConfigStore } = await import('../config')
    const store = useConfigStore()
    store.settings.download_dir = '/old'
    bindingMocks.SelectDirectory.mockResolvedValue('/picked')
    const path = await store.pickDirectory()
    expect(path).toBe('/picked')
    expect(store.settings.download_dir).toBe('/old')
  })
})
