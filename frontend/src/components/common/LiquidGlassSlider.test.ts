import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import LiquidGlassSlider from './LiquidGlassSlider.vue'

// happy-dom does not implement Canvas 2D `getContext('2d')` (returns null), so
// the slider's SDF displacement-map builder crashes on mount. Stub the minimal
// Canvas 2D surface the component actually touches: getContext / createImageData
// / putImageData / toDataURL. Canvas work is purely visual and not asserted.
let getContextSpy: ReturnType<typeof vi.spyOn> | undefined
let toDataURLSpy: ReturnType<typeof vi.spyOn> | undefined

beforeEach(() => {
  getContextSpy = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(() => {
    const ctx = {
      createImageData: (w: number, h: number): ImageData =>
        ({ data: new Uint8ClampedArray(w * h * 4) }) as ImageData,
      putImageData: () => {},
    }
    return ctx as unknown as CanvasRenderingContext2D
  })
  toDataURLSpy = vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/png;base64,')
})

afterEach(() => {
  getContextSpy?.mockRestore()
  toDataURLSpy?.mockRestore()
})

describe('LiquidGlassSlider', () => {
  it('renders correctly', () => {
    const wrapper = mount(LiquidGlassSlider, {
      props: {
        modelValue: 50,
        min: 0,
        max: 100,
        step: 1,
        ariaLabel: 'Test Slider',
        ariaValuetext: 'Test Value'
      }
    })
    
    expect(wrapper.exists()).toBe(true)
    const root = wrapper.find('.lgs')
    expect(root.attributes('aria-valuenow')).toBe('50')
    expect(root.attributes('aria-label')).toBe('Test Slider')
    expect(root.attributes('aria-valuetext')).toBe('Test Value')
  })

  it('handles keyboard navigation correctly', async () => {
    const wrapper = mount(LiquidGlassSlider, {
      props: {
        modelValue: 50,
        min: 0,
        max: 100,
        step: 2
      }
    })

    const slider = wrapper.find('.lgs')
    
    // ArrowRight (+step)
    await slider.trigger('keydown', { key: 'ArrowRight' })
    expect(wrapper.emitted('update:modelValue')![0]).toEqual([52])

    // ArrowLeft (-step)
    await slider.trigger('keydown', { key: 'ArrowLeft' })
    expect(wrapper.emitted('update:modelValue')![1]).toEqual([48])

    // PageUp (+step * 10)
    await slider.trigger('keydown', { key: 'PageUp' })
    expect(wrapper.emitted('update:modelValue')![2]).toEqual([70])

    // PageDown (-step * 10)
    await slider.trigger('keydown', { key: 'PageDown' })
    expect(wrapper.emitted('update:modelValue')![3]).toEqual([30])

    // Home (min)
    await slider.trigger('keydown', { key: 'Home' })
    expect(wrapper.emitted('update:modelValue')![4]).toEqual([0])

    // End (max)
    await slider.trigger('keydown', { key: 'End' })
    expect(wrapper.emitted('update:modelValue')![5]).toEqual([100])
  })

  it('clamps values within bounds', async () => {
    const wrapper = mount(LiquidGlassSlider, {
      props: {
        modelValue: 95,
        min: 0,
        max: 100,
        step: 10
      }
    })

    const slider = wrapper.find('.lgs')
    
    // ArrowRight (+10 from 95 should clamp to 100)
    await slider.trigger('keydown', { key: 'ArrowRight' })
    expect(wrapper.emitted('update:modelValue')![0]).toEqual([100])
    
    // PageDown from near min (should clamp to 0)
    await wrapper.setProps({ modelValue: 5 })
    await slider.trigger('keydown', { key: 'PageDown' })
    expect(wrapper.emitted('update:modelValue')![1]).toEqual([0])
  })
})
