import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import StaticGlassPanel from './StaticGlassPanel.vue'

const uiStoreMock = vi.hoisted(() => ({
  effects: 'full',
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => uiStoreMock,
}))

vi.mock('../../composables/useLiquidGlass', () => ({
  getStaticGlassFilterId: () => 'static-glass-refraction',
}))

describe('StaticGlassPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    uiStoreMock.effects = 'full'
  })

  it('renders correctly with default props', () => {
    const wrapper = mount(StaticGlassPanel)
    expect(wrapper.exists()).toBe(true)
    expect(wrapper.element.tagName.toLowerCase()).toBe('div')
  })

  it('applies disabled attribute and removes interactive classes when disabled', () => {
    const wrapper = mount(StaticGlassPanel, {
      props: {
        as: 'button',
        interactive: true,
        disabled: true,
      },
    })

    // 1. Root element should be a button
    expect(wrapper.element.tagName.toLowerCase()).toBe('button')

    // 2. Native disabled attribute MUST fall through to the root element.
    // This blocks CSS :hover pseudo-classes natively on disabled form elements.
    expect(wrapper.attributes('disabled')).toBeDefined()

    // 3. Since it is disabled, interactive hover classes like cursor-pointer should NOT be present.
    expect(wrapper.classes()).not.toContain('cursor-pointer')
    expect(wrapper.classes()).not.toContain('hover:scale-[1.01]')
    expect(wrapper.classes()).not.toContain('active:scale-[0.99]')
  })

  it('preserves hover capability when interactive and not disabled', () => {
    const wrapper = mount(StaticGlassPanel, {
      props: {
        as: 'button',
        interactive: true,
        disabled: false,
      },
    })

    expect(wrapper.attributes('disabled')).toBeUndefined()
    expect(wrapper.classes()).toContain('cursor-pointer')
    expect(wrapper.classes()).toContain('hover:scale-[1.01]')
  })
})
