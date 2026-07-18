import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ExtensionPairingModal from './ExtensionPairingModal.vue'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const copyPairUrlMock = vi.hoisted(() => vi.fn().mockResolvedValue(true))

vi.mock('../../stores/extension', () => ({
  useExtensionStore: () => ({
    copyPairUrl: copyPairUrlMock,
  }),
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

const TeleportStub = defineComponent({
  name: 'Teleport',
  setup(_, { slots }) {
    return () => slots.default?.()
  },
})

describe('ExtensionPairingModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    copyPairUrlMock.mockResolvedValue(true)
  })

  const mountModal = (props: Partial<{ show: boolean; url: string; regenerating: boolean }> = {}) => {
    return mount(ExtensionPairingModal, {
      props: {
        show: true,
        url: 'http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc123',
        regenerating: false,
        ...props,
      },
      global: {
        stubs: {
          LiquidGlassPanel: StubPanel,
          Transition: TransitionStub,
          Teleport: TeleportStub,
        },
      },
    })
  }

  it('renders the URL with font-mono-data when show=true', () => {
    const wrapper = mountModal()
    const urlDisplay = wrapper.find('.font-mono-data')
    expect(urlDisplay.exists()).toBe(true)
    expect(urlDisplay.text()).toContain('http://127.0.0.1:16810')
  })

  it('does not render when show=false', () => {
    const wrapper = mountModal({ show: false })
    expect(wrapper.find('.glass-panel-solid').exists()).toBe(false)
  })

  it('Copy button calls copyPairUrl and emits copied', async () => {
    const wrapper = mountModal()
    const buttons = wrapper.findAll('button')
    const copyButton = buttons.find((b) => b.text().includes('extension.modal.copy'))
    expect(copyButton).toBeDefined()
    await copyButton!.trigger('click')
    expect(copyPairUrlMock).toHaveBeenCalledOnce()
    expect(wrapper.emitted('copied')).toBeTruthy()
  })

  it('Regenerate button emits regenerate', async () => {
    const wrapper = mountModal()
    const buttons = wrapper.findAll('button')
    const regenButton = buttons.find((b) => b.text().includes('extension.modal.regenerate'))
    expect(regenButton).toBeDefined()
    await regenButton!.trigger('click')
    expect(wrapper.emitted('regenerate')).toBeTruthy()
  })

  it('Regenerate button shows regenerating text and is disabled when regenerating=true', () => {
    const wrapper = mountModal({ regenerating: true })
    const buttons = wrapper.findAll('button')
    const regenButton = buttons.find((b) => b.text().includes('extension.modal.regenerating'))
    expect(regenButton).toBeDefined()
    expect(regenButton!.attributes('disabled')).toBeDefined()
  })

  it('stale notice appears after timeout', async () => {
    vi.useFakeTimers()
    const wrapper = mount(ExtensionPairingModal, {
      props: {
        show: false,
        url: 'http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc123',
        regenerating: false,
      },
      global: {
        stubs: {
          LiquidGlassPanel: StubPanel,
          Transition: TransitionStub,
          Teleport: TeleportStub,
        },
      },
    })
    expect(wrapper.text()).not.toContain('extension.modal.staleNotice')
    await wrapper.setProps({ show: true })
    vi.advanceTimersByTime(5 * 60 * 1000)
    await wrapper.vm.$nextTick()
    expect(wrapper.text()).toContain('extension.modal.staleNotice')
    vi.useRealTimers()
  })

  it('close button emits close', async () => {
    const wrapper = mountModal()
    const buttons = wrapper.findAll('button')
    const closeButton = buttons.find((b) => b.text().includes('extension.modal.close'))
    expect(closeButton).toBeDefined()
    await closeButton!.trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
    expect(wrapper.emitted('update:show')).toBeTruthy()
    expect(wrapper.emitted('update:show')![0]).toEqual([false])
  })

  it('backdrop click emits close', async () => {
    const wrapper = mountModal()
    const overlay = wrapper.find('.fixed.inset-0')
    expect(overlay.exists()).toBe(true)
    await overlay.trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
    expect(wrapper.emitted('update:show')).toBeTruthy()
    expect(wrapper.emitted('update:show')![0]).toEqual([false])
  })
})
