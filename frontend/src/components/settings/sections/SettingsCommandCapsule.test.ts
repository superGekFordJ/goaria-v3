import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FolderOpen, Palette } from '@lucide/vue'
import SettingsCommandCapsule from './SettingsCommandCapsule.vue'
import capsuleSource from './SettingsCommandCapsule.vue?raw'
import { compileStyle, parse } from 'vue/compiler-sfc'

vi.mock('vue-i18n', async importOriginal => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('../../../stores/ui', () => ({
  useUIStore: () => ({
    effectsTier: 'full',
    effectsLevel: 100,
  }),
}))

vi.mock('../../../composables/useLiquidGlass', () => ({
  useLiquidGlass: () => ({ filterId: { value: 'lg-test' } }),
  getStaticGlassFilterId: () => 'static-glass-filter',
}))

const sections = [
  { id: 'download', labelKey: 'download.title', icon: FolderOpen },
  { id: 'appearance', labelKey: 'appearance.title', icon: Palette },
]
const wrappers: ReturnType<typeof mount>[] = []

function mountCapsule(status: 'idle' | 'loading' | 'saving' | 'saved' | 'error' = 'idle') {
  const wrapper = mount(SettingsCommandCapsule, {
    attachTo: document.body,
    props: {
      sections,
      activeSection: 'download',
      floating: false,
      status,
      errorKey: 'settings.errors.persistFailed',
    },
  })
  wrappers.push(wrapper)
  return wrapper
}

afterEach(() => {
  wrappers.splice(0).forEach(wrapper => wrapper.unmount())
})

describe('SettingsCommandCapsule', () => {
  it('scopes compiled motion to the capsule and preserves morphing in reduced modes', () => {
    const { descriptor } = parse(capsuleSource)
    const result = compileStyle({
      source: descriptor.styles[0].content,
      filename: 'SettingsCommandCapsule.vue',
      id: 'data-v-capsule',
      scoped: true,
    })
    expect(result.errors).toEqual([])
    expect(result.code).toContain(
      "[data-effects-glow='breathe'] .capsule-ready-dot[data-v-capsule]",
    )
    expect(result.code).not.toMatch(/\[data-effects[^\]]*\]\s*\{/)
    expect(result.code).toContain('width 0.4s cubic-bezier(0.34, 1.56, 0.64, 1)')
    expect(result.code).toContain('height 0.18s cubic-bezier(0.4, 0, 1, 1)')
    expect(result.code).toContain('animation: capsule-expand-capsule 0.4s')
    expect(result.code).not.toMatch(/opacity\s*:/)
    const disabledSelectors: string[] = []
    result.rawResult?.root.walkRules(rule => {
      rule.walkDecls('animation', declaration => {
        if (declaration.value === 'none') disabledSelectors.push(rule.selector)
      })
    })
    expect(disabledSelectors.length).toBeGreaterThan(0)
    expect(disabledSelectors.every(selector => selector.includes('.capsule-ready-dot'))).toBe(true)
    expect(result.code).not.toMatch(
      /\.command-capsule(?:\.is-open)?\[data-v-capsule\]\s*\{[^{}]*transition:\s*none/,
    )
  })

  it('is navigable immediately while docked and idle', async () => {
    const wrapper = mountCapsule()
    const trigger = wrapper.get('[data-testid="settings-navigation-toggle"]')
    expect(trigger.text()).toContain('download.title')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    await trigger.trigger('click')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('nav').isVisible()).toBe(true)
    expect(wrapper.findAll('[data-section-link]')).toHaveLength(2)
    expect(wrapper.get('[data-section-link="download"]').attributes('aria-current')).toBe(
      'location',
    )
  })

  it('navigates, collapses and keeps the same glass surface mounted', async () => {
    const wrapper = mountCapsule()
    const glass = wrapper.get('.command-capsule').element
    await wrapper.get('[data-testid="settings-navigation-toggle"]').trigger('click')
    expect(wrapper.get('.command-capsule').classes()).toContain('is-open')
    await wrapper.get('[data-section-link="appearance"]').trigger('click')
    expect(wrapper.emitted('navigate')).toEqual([['appearance']])
    expect(
      wrapper.get('[data-testid="settings-navigation-toggle"]').attributes('aria-expanded'),
    ).toBe('false')
    expect(wrapper.get('.command-capsule').classes()).not.toContain('is-open')
    expect(wrapper.get('nav').isVisible()).toBe(false)
    expect(wrapper.get('.command-capsule').element).toBe(glass)
  })

  it.each([
    ['loading', 'settings.navigation.loading'],
    ['saving', 'settings.saving'],
    ['saved', 'settings.saved'],
    ['error', 'settings.errors.persistFailed'],
  ] as const)('announces %s without disabling navigation', async (status, message) => {
    const wrapper = mountCapsule(status)
    expect(wrapper.get('[role="status"]').text()).toBe(message)
    if (status === 'saving' || status === 'loading') {
      expect(wrapper.find('.capsule-status .animate-spin').exists()).toBe(true)
    }
    await wrapper.get('[data-testid="settings-navigation-toggle"]').trigger('click')
    expect(wrapper.get('nav').isVisible()).toBe(true)
    if (status === 'error') expect(wrapper.get('.capsule-error').text()).toBe(message)
    await wrapper.setProps({ status: 'idle', activeSection: 'appearance' })
    expect(wrapper.get('nav').isVisible()).toBe(true)
    await wrapper.get('[data-testid="settings-navigation-toggle"]').trigger('click')
    expect(wrapper.get('[data-testid="settings-navigation-toggle"]').text()).toContain(
      'appearance.title',
    )
  })

  it('dismisses with Escape and restores trigger focus', async () => {
    const wrapper = mountCapsule()
    const trigger = wrapper.get<HTMLButtonElement>('[data-testid="settings-navigation-toggle"]')
    await trigger.trigger('keydown', { key: 'ArrowDown' })
    expect(document.activeElement).toBe(wrapper.get('[data-section-link="download"]').element)
    await wrapper.get('[data-section-link="download"]').trigger('keydown', { key: 'ArrowRight' })
    expect(document.activeElement).toBe(wrapper.get('[data-section-link="appearance"]').element)
    await wrapper.get('[data-section-link="appearance"]').trigger('keydown', { key: 'Escape' })
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(trigger.element)
  })

  it('closes on outside pointer and focus without stealing focus', async () => {
    const wrapper = mountCapsule()
    const trigger = wrapper.get('[data-testid="settings-navigation-toggle"]')
    await trigger.trigger('click')
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await wrapper.vm.$nextTick()
    expect(trigger.attributes('aria-expanded')).toBe('false')
    await trigger.trigger('click')
    document.body.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    await wrapper.vm.$nextTick()
    expect(trigger.attributes('aria-expanded')).toBe('false')
  })

  it('preserves navigation while detaching, redocking and changing active section', async () => {
    const wrapper = mountCapsule()
    await wrapper.get('[data-testid="settings-navigation-toggle"]').trigger('click')
    await wrapper.setProps({ floating: true, activeSection: 'appearance' })
    expect(wrapper.get('[data-docking]').attributes('data-docking')).toBe('floating')
    expect(wrapper.get('[data-section-link="appearance"]').attributes('aria-current')).toBe(
      'location',
    )
    await wrapper.setProps({ floating: false })
    expect(wrapper.get('[data-docking]').attributes('data-docking')).toBe('docked')
    expect(wrapper.get('nav').isVisible()).toBe(true)
  })
})
