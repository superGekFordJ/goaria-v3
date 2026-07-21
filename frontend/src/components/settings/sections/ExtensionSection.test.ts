import { mount } from '@vue/test-utils'
import { defineComponent, reactive } from 'vue'
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
  requestUnpair: vi.fn(),
  cancelUnpair: vi.fn(),
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
  showUnpairConfirm: false,
  ...storeFns,
})

vi.mock('../../../stores/extension', () => ({
  useExtensionStore: () => storeMock,
}))

vi.mock('../../../utils/clipboard', () => ({
  clearClipboardIfMatches: vi.fn().mockResolvedValue(undefined),
}))

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
    storeMock.showUnpairConfirm = false
  })

  const mountSection = () =>
    mount(ExtensionSection, {
      global: {
        stubs: {
          Transition: TransitionStub,
        },
      },
    })

  it('does not render the pairing stage when pairingPanelOpen is false', () => {
    const wrapper = mountSection()
    expect(wrapper.find('[data-testid="pairing-stage-pairing"]').exists()).toBe(false)
  })

  it('renders the pairing stage with font-mono-data URL when pairingPanelOpen is true', () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc123'
    const wrapper = mountSection()
    const urlInput = wrapper.find('[data-testid="pairing-url-input"]')
    expect(urlInput.exists()).toBe(true)
    expect(urlInput.attributes('value')).toContain('http://127.0.0.1:16810')
  })

  it('URL input is readonly', () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const urlInput = wrapper.find('[data-testid="pairing-url-input"]')
    expect(urlInput.attributes('readonly')).toBeDefined()
  })

  it('Copy button calls copyPairUrl', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const copyButton = wrapper.find('[data-testid="pairing-copy-btn"]')
    expect(copyButton.exists()).toBe(true)
    await copyButton.trigger('click')
    expect(storeMock.copyPairUrl).toHaveBeenCalledOnce()
  })

  it('Open in Browser button calls openInBrowser with pairUrl', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const openButton = wrapper.find('[data-testid="pairing-open-btn"]')
    expect(openButton.exists()).toBe(true)
    await openButton.trigger('click')
    expect(storeMock.openInBrowser).toHaveBeenCalledWith('http://127.0.0.1:16810/pair')
  })

  it('Regenerate button calls regenerate', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const regenButton = wrapper.find('[data-testid="pairing-regenerate-btn"]')
    expect(regenButton.exists()).toBe(true)
    await regenButton.trigger('click')
    expect(storeMock.regenerate).toHaveBeenCalledOnce()
  })

  it('Close button sets pairingPanelOpen to false and clears pairUrl', async () => {
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    const wrapper = mountSection()
    const closeButton = wrapper.find('[data-testid="pairing-close-btn"]')
    expect(closeButton.exists()).toBe(true)
    await closeButton.trigger('click')
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
    expect(wrapper.find('[data-testid="pairing-stage-pairing"]').exists()).toBe(true)
    storeMock.pairingPanelOpen = false
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="pairing-stage-pairing"]').exists()).toBe(false)
  })

  it('shows idle stage when closed and pairing stage when open', async () => {
    const wrapper = mountSection()
    expect(wrapper.find('[data-testid="pairing-stage-idle"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="pairing-stage-pairing"]').exists()).toBe(false)
    storeMock.pairingPanelOpen = true
    storeMock.pairUrl = 'http://127.0.0.1:16810/pair'
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="pairing-stage-idle"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pairing-stage-pairing"]').exists()).toBe(true)
  })

  it('Unpair button calls requestUnpair instead of unpair directly', async () => {
    storeMock.paired = true
    const wrapper = mountSection()
    const unpairBtn = wrapper.find('[data-testid="unpair-btn"]')
    expect(unpairBtn.exists()).toBe(true)
    await unpairBtn.trigger('click')
    expect(storeMock.requestUnpair).toHaveBeenCalledOnce()
    expect(storeMock.unpair).not.toHaveBeenCalled()
  })

  it('in-place confirmation UI renders when showUnpairConfirm is true', async () => {
    storeMock.paired = true
    storeMock.showUnpairConfirm = true
    const wrapper = mountSection()
    const confirm = wrapper.find('[data-testid="unpair-confirm"]')
    expect(confirm.exists()).toBe(true)
    expect(wrapper.text()).toContain('extension.unpairConfirm.message')
  })

  it('Confirm button calls unpair', async () => {
    storeMock.paired = true
    storeMock.showUnpairConfirm = true
    const wrapper = mountSection()
    const confirmBtn = wrapper.find('[data-testid="unpair-confirm-btn"]')
    expect(confirmBtn.exists()).toBe(true)
    await confirmBtn.trigger('click')
    expect(storeMock.unpair).toHaveBeenCalledOnce()
  })

  it('Cancel button calls cancelUnpair', async () => {
    storeMock.paired = true
    storeMock.showUnpairConfirm = true
    const wrapper = mountSection()
    const cancelBtn = wrapper.find('[data-testid="unpair-cancel-btn"]')
    expect(cancelBtn.exists()).toBe(true)
    await cancelBtn.trigger('click')
    expect(storeMock.cancelUnpair).toHaveBeenCalledOnce()
  })
})
