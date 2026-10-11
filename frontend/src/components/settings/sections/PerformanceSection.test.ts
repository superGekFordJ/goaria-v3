import { defineComponent, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import PerformanceSection from './PerformanceSection.vue'

vi.mock('vue-i18n', async importOriginal => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const TransitionStub = defineComponent({
  name: 'Transition',
  setup(_, { slots }) {
    return () => slots.default?.()
  },
})

const presets = ['1', '4', '8', '16', '24', '32']

function mountSection(
  connections = '16',
  overrides: { concurrentDownloads?: string; smartThreadMode?: boolean } = {},
) {
  return mount(PerformanceSection, {
    props: {
      connections,
      concurrentDownloads: overrides.concurrentDownloads ?? '5',
      connectionOptions: presets,
      smartThreadMode: overrides.smartThreadMode ?? true,
    },
    global: {
      stubs: { Transition: TransitionStub },
    },
  })
}

function connectionsTrigger(wrapper: ReturnType<typeof mountSection>) {
  return wrapper.findAll('button').find(b => b.text().includes('performance.threads'))
}

function menuConnectionValues(wrapper: ReturnType<typeof mountSection>) {
  return wrapper
    .findAll('button')
    .filter(b => b.classes().includes('p-3') && b.text().includes('performance.threads'))
    .map(b => b.text().trim().split(/\s+/)[0])
}

describe('PerformanceSection', () => {
  it('keeps preset order unchanged', async () => {
    const wrapper = mountSection()
    await connectionsTrigger(wrapper)?.trigger('click')
    await nextTick()
    expect(menuConnectionValues(wrapper)).toEqual(presets)
    wrapper.unmount()
  })

  it.each(['64', '128', '256'])(
    'shows custom %s in the trigger and menu as checked',
    async value => {
      const wrapper = mountSection(value)
      const trigger = connectionsTrigger(wrapper)
      expect(trigger?.text()).toContain(value)
      await trigger?.trigger('click')
      await nextTick()
      const menuItems = wrapper
        .findAll('button')
        .filter(b => b.classes().includes('p-3') && b.text().includes('performance.threads'))
      const custom = menuItems.find(
        b => b.text().includes(`${value} `) || b.text().startsWith(value),
      )
      expect(custom?.text()).toContain(value)
      expect(custom?.classes().join(' ')).toContain('bg-[var(--neon-primary)]/10')
      wrapper.unmount()
    },
  )

  it('emits the selected preset once', async () => {
    const wrapper = mountSection()
    await connectionsTrigger(wrapper)?.trigger('click')
    await nextTick()
    const eight = wrapper
      .findAll('button')
      .find(b => /^\s*8\s/.test(b.text()) || b.text().startsWith('8 '))
    await eight?.trigger('click')
    expect(wrapper.emitted('update:connections')?.[0]).toEqual(['8'])
    expect(wrapper.emitted('change')).toHaveLength(1)
    wrapper.unmount()
  })

  it('shows custom value in menu and ignores invalid input', async () => {
    const wrapper = mountSection('64')
    await connectionsTrigger(wrapper)?.trigger('click')
    await nextTick()
    expect(menuConnectionValues(wrapper)).toEqual([...presets, '64'])
    wrapper.unmount()

    const empty = mountSection('')
    await connectionsTrigger(empty)?.trigger('click')
    await nextTick()
    const labels = empty.findAll('button').map(b => b.text())
    expect(labels.some(text => text.includes('NaN'))).toBe(false)
    expect(menuConnectionValues(empty)).toEqual(presets)
    empty.unmount()

    const garbage = mountSection('64abc')
    await connectionsTrigger(garbage)?.trigger('click')
    await nextTick()
    expect(menuConnectionValues(garbage)).toEqual(presets)
    garbage.unmount()
  })

  it('sets concurrent input max to 32', () => {
    const wrapper = mountSection()
    expect(wrapper.find('input[type="number"]').attributes('max')).toBe('32')
    wrapper.unmount()
  })

  it('renders the tooltip through an i18n key without duplicate native title', () => {
    const wrapper = mountSection()
    expect(wrapper.text()).toContain('performance.surgeExclusiveTooltip')
    expect(wrapper.text()).not.toContain('Aria2 is still limited')
    expect(wrapper.find('[title]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('exposes the smart thread toggle as a switch', () => {
    const wrapper = mountSection()
    expect(wrapper.find('[role="switch"]').attributes('aria-checked')).toBe('true')
    wrapper.unmount()
  })

  it('locks the concurrent input under smart mode and keeps the raw value', async () => {
    const wrapper = mountSection('16', { concurrentDownloads: '8', smartThreadMode: true })
    const input = wrapper.find('input[type="number"]')
    expect(input.attributes('disabled')).toBeDefined()
    expect((input.element as HTMLInputElement).value).toBe('8')
    expect(input.attributes('min')).toBe('1')
    expect(input.attributes('max')).toBe('32')

    // A manually dispatched input event must not emit either
    const el = input.element as HTMLInputElement
    el.value = '12'
    await input.trigger('input')
    expect(wrapper.emitted('update:concurrentDownloads')).toBeUndefined()
    expect(wrapper.emitted('change')).toBeUndefined()

    expect(wrapper.text()).toContain('performance.concurrentManagedHint')
    expect(wrapper.text()).toContain('performance.concurrentRestartHint')
    wrapper.unmount()
  })

  it('edits the same raw value and still shows the restart hint when smart mode is off', async () => {
    const wrapper = mountSection('16', { concurrentDownloads: '8', smartThreadMode: false })
    const input = wrapper.find('input[type="number"]')
    expect(input.attributes('disabled')).toBeUndefined()
    expect((input.element as HTMLInputElement).value).toBe('8')

    const el = input.element as HTMLInputElement
    el.value = '8'
    await input.trigger('input')
    expect(wrapper.emitted('update:concurrentDownloads')).toEqual([['8']])
    expect(wrapper.emitted('change')).toHaveLength(1)

    expect(wrapper.text()).not.toContain('performance.concurrentManagedHint')
    expect(wrapper.text()).toContain('performance.concurrentRestartHint')
    wrapper.unmount()
  })
})
