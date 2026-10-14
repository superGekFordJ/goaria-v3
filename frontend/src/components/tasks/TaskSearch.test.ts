import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import TaskSearch from './TaskSearch.vue'

vi.mock('vue-i18n', () => ({
  createI18n: () => ({
    global: {
      t: (key: string) => key,
    },
  }),
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => ({
    effectsLevel: 'full',
    windowTransparent: false,
  }),
}))

describe('TaskSearch component', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('renders search input with placeholder and stable right buffer slot', () => {
    const wrapper = mount(TaskSearch, {
      props: {
        modelValue: '',
      },
      attachTo: document.body,
    })

    const input = wrapper.find('input')
    expect(input.exists()).toBe(true)
    expect(input.attributes('placeholder')).toBe('taskSearch.placeholder')

    // Right buffer slot exists to maintain straight safety zone
    const bufferSlot = wrapper.find('.w-8.h-8')
    expect(bufferSlot.exists()).toBe(true)

    // Clear button is not rendered when empty
    const clearButton = wrapper.find('button[aria-label="taskSearch.clear"]')
    expect(clearButton.exists()).toBe(false)
  })

  it('renders clear button within buffer slot when query is present and clears on click', async () => {
    const wrapper = mount(TaskSearch, {
      props: {
        modelValue: 'ubuntu-iso',
        'onUpdate:modelValue': (val: string) => wrapper.setProps({ modelValue: val }),
      },
      attachTo: document.body,
    })

    const clearButton = wrapper.find('button[aria-label="taskSearch.clear"]')
    expect(clearButton.exists()).toBe(true)

    // Clear button should be inside the stable buffer slot
    const bufferSlot = wrapper.find('.w-8.h-8')
    expect(bufferSlot.find('button').exists()).toBe(true)

    await clearButton.trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.props('modelValue')).toBe('')
  })

  it('focuses input when clicking the search icon', async () => {
    const wrapper = mount(TaskSearch, {
      props: {
        modelValue: '',
      },
      attachTo: document.body,
    })

    const inputEl = wrapper.find('input').element as HTMLInputElement
    const focusSpy = vi.spyOn(inputEl, 'focus')

    const iconContainer = wrapper.find('.cursor-text')
    await iconContainer.trigger('click')

    expect(focusSpy).toHaveBeenCalled()
  })
})
