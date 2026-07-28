import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import LiquidGlassPanel from './LiquidGlassPanel.vue'

const uiStoreMock = vi.hoisted(() => ({
  effectsTier: 'full',
  effectsLevel: 100,
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => uiStoreMock,
}))

vi.mock('../../composables/useLiquidGlass', () => ({
  useLiquidGlass: () => ({ filterId: { value: 'lg-test' } }),
  getStaticGlassFilterId: () => 'static-glass-filter',
}))

describe('LiquidGlassPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    uiStoreMock.effectsTier = 'full'
    uiStoreMock.effectsLevel = 100
  })

  it('renders correctly with default props', () => {
    const wrapper = mount(LiquidGlassPanel)
    expect(wrapper.exists()).toBe(true)
    // Default is div
    expect(wrapper.element.tagName.toLowerCase()).toBe('div')
  })

  it('applies disabled attribute and removes interactive classes when disabled', () => {
    const wrapper = mount(LiquidGlassPanel, {
      props: {
        as: 'button',
        interactive: true,
        disabled: true,
        hoverEffect: 'all',
      },
    })

    // 1. Root element should be a button
    expect(wrapper.element.tagName.toLowerCase()).toBe('button')

    // 2. Native disabled attribute MUST fall through to the root element.
    // This is the core mechanism that prevents CSS :hover states from triggering.
    expect(wrapper.attributes('disabled')).toBeDefined()

    // 3. Since it is disabled, interactive classes like cursor-pointer should NOT be present.
    expect(wrapper.classes()).not.toContain('cursor-pointer')

    // 4. In full effects mode, the interactive inner glow layer should be stripped out.
    // The inner glow layer has 'group-hover/liquid:opacity-100'
    const html = wrapper.html()
    expect(html).not.toContain('group-hover/liquid:opacity-100')
  })

  it('preserves hover capability when interactive and not disabled', () => {
    const wrapper = mount(LiquidGlassPanel, {
      props: {
        as: 'button',
        interactive: true,
        disabled: false,
        hoverEffect: 'all',
      },
    })

    // Root should not have disabled attribute
    expect(wrapper.attributes('disabled')).toBeUndefined()

    // Should have cursor pointer
    expect(wrapper.classes()).toContain('cursor-pointer')

    // Inner glow layer should be present in the DOM
    const innerGlow = wrapper.find('.group-hover\\/liquid\\:opacity-100')
    expect(innerGlow.exists()).toBe(true)
  })
})
