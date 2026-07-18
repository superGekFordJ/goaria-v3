import { mount } from '@vue/test-utils'
import { defineComponent, h, reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ExtensionSection from './ExtensionSection.vue'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        const suffix = params ? ` ${JSON.stringify(params)}` : ''
        return `${key}${suffix}`
      },
    }),
  }
})

const storeFns = vi.hoisted(() => ({
  subscribeToEvents: vi.fn(),
  unsubscribeFromEvents: vi.fn(),
  refreshStatus: vi.fn(),
  pair: vi.fn(),
  unpair: vi.fn(),
  regenerate: vi.fn(),
  openInBrowser: vi.fn(),
  copyPairUrl: vi.fn().mockResolvedValue(true),
}))

const storeMock = reactive({
  status: 'listening' as 'disconnected' | 'listening' | 'paired',
  wsPort: 16801,
  connectedClients: 0,
  paired: false,
  pairing: false,
  pairUrl: '',
  pairingPanelOpen: false,
  regenerating: false,
  authFailedNotice: false,
  unpairRotatedNotice: false,
  ...storeFns,
})

vi.mock('../../../stores/extension', () => ({
  useExtensionStore: () => storeMock,
}))

vi.mock('../../../utils/clipboard', () => ({
  clearClipboardIfMatches: vi.fn().mockResolvedValue(undefined),
}))

const StubPanel = defineComponent({
  name: 'LiquidGlassPanel',
  props: {
    as: { type: String, default: 'div' },
    interactive: { type: Boolean, default: false },
    disabled: { type: Boolean, default: false },
  },
  setup(props, { slots }) {
    return () => h(props.as, { disabled: props.disabled || undefined }, slots.default?.())
  },
})

const TransitionStub = defineComponent({
  name: 'Transition',
  setup(_, { slots }) {
    return () => slots.default?.()
  },
})

describe('ExtensionSection inline pairing panel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    storeMock.status = 'listening'
    storeMock.wsPort = 16801
    storeMock.connectedClients = 0
    storeMock.paired = false
    storeMock.pairing = false
    storeMock.pairUrl = ''
    storeMock.pairingPanelOpen = false
    storeMock.regenerating = false
    storeMock.authFailedNotice = false
    storeMock.unpairRotatedNotice = false
  })

  const mountSection = () =>
    mount(ExtensionSection, {
      global: {
        stubs: {
          LiquidGlassPanel: StubPanel,
          Transition: TransitionStub,
        },
      },
    })

  it('does not render the pairing panel when pairingPanelOpen is false', () => {
    const wrapper = mountSection()
    expect(wrapper.find('.glass-panel-solid').exists()).toBe(false)
  })

  it('renders the pairing panel with font-mono-data URL when pairingPanelOpen is true', () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc123'
    const wrapper = mountSection()
    const urlDisplay = wrapper.find('.glass-panel-solid .font-mono-data')
    expect(urlDisplay.exists()).toBe(true)
    expect(urlDisplay.text()).toContain('http://127.0.0.1:16810')
  })

  it('Copy button calls copyPairUrl', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const buttons = wrapper.findAll('button')
    const copyButton = buttons.find((b) => b.text().includes('extension.modal.copy'))
    expect(copyButton).toBeDefined()
    await copyButton!.trigger('click')
    expect(storeMock.copyPairUrl).toHaveBeenCalledOnce()
  })

  it('Open in Browser button calls openInBrowser with pairUrl', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const buttons = wrapper.findAll('button')
    const openButton = buttons.find((b) => b.text().includes('extension.modal.openInBrowser'))
    expect(openButton).toBeDefined()
    await openButton!.trigger('click')
    expect(storeMock.openInBrowser).toHaveBeenCalledWith('http://127.0.0.1:16810/pair')
  })

  it('Regenerate button calls regenerate', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const buttons = wrapper.findAll('button')
    const regenButton = buttons.find((b) => b.text().includes('extension.modal.regenerate'))
    expect(regenButton).toBeDefined()
    await regenButton!.trigger('click')
    expect(storeMock.regenerate).toHaveBeenCalledOnce()
  })

  it('Close button sets pairingPanelOpen to false and clears pairUrl', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const buttons = wrapper.findAll('button')
    const closeButton = buttons.find((b) => b.text().includes('extension.modal.close'))
    expect(closeButton).toBeDefined()
    await closeButton!.trigger('click')
    expect(storeMock.pairingPanelOpen).toBe(false)
    expect(storeMock.pairUrl).toBe('')
  })

  it('stale notice appears after timeout', async () => {
    vi.useFakeTimers()
    const wrapper = mountSection()
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).not.toContain('extension.modal.staleNotice')
    vi.advanceTimersByTime(5 * 60 * 1000)
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('extension.modal.staleNotice')
    vi.useRealTimers()
  })

  it('auto-collapses when pairingPanelOpen becomes false', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    expect(wrapper.find('.glass-panel-solid').exists()).toBe(true)
    storeMock.pairingPanelOpen = false
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.glass-panel-solid').exists()).toBe(false)
  })
})
