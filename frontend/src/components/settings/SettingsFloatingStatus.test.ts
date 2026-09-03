import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import SettingsFloatingStatus from './SettingsFloatingStatus.vue'

vi.mock('vue-i18n', async importOriginal => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

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

describe('SettingsFloatingStatus', () => {
  it('does not render when visible is false', () => {
    const wrapper = mount(SettingsFloatingStatus, {
      props: {
        visible: false,
        status: 'saving',
        errorKey: 'settings.saveFailed',
      },
    })
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(false)
  })

  it('does not render when status is idle even if visible is true', () => {
    const wrapper = mount(SettingsFloatingStatus, {
      props: {
        visible: true,
        status: 'idle',
        errorKey: 'settings.saveFailed',
      },
    })
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(false)
  })

  it('renders saving state when visible and saving', () => {
    const wrapper = mount(SettingsFloatingStatus, {
      props: {
        visible: true,
        status: 'saving',
        errorKey: 'settings.saveFailed',
      },
    })
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('settings.saving')
  })

  it('renders saved state when visible and saved', () => {
    const wrapper = mount(SettingsFloatingStatus, {
      props: {
        visible: true,
        status: 'saved',
        errorKey: 'settings.saveFailed',
      },
    })
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('settings.saved')
  })

  it('renders error state with localized key when visible and error', () => {
    const wrapper = mount(SettingsFloatingStatus, {
      props: {
        visible: true,
        status: 'error',
        errorKey: 'settings.errors.persistFailed',
      },
    })
    expect(wrapper.find('[data-testid="floating-save-status"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('settings.errors.persistFailed')
  })
})
