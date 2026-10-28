import { defineComponent, h, nextTick, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  preloadDisplacementMap,
  useLiquidGlass,
  warmupLiquidGlassPipeline,
} from '../useLiquidGlass'

let getContextSpy: ReturnType<typeof vi.spyOn> | undefined
let toBlobSpy: ReturnType<typeof vi.spyOn> | undefined
let createObjectURLSpy: ReturnType<typeof vi.spyOn> | undefined
let revokeObjectURLSpy: ReturnType<typeof vi.spyOn> | undefined
let lastImageData: ImageData | null = null
let roCallbacks: Array<() => void> = []

beforeEach(() => {
  roCallbacks = []
  lastImageData = null
  vi.stubGlobal('navigator', {
    userAgent: 'Mozilla/5.0 Chrome/126.0.0.0 Safari/537.36',
    userAgentData: { brands: [{ brand: 'Chromium' }] },
  })
  vi.stubGlobal(
    'ResizeObserver',
    class {
      private cb: () => void
      constructor(cb: () => void) {
        this.cb = cb
        roCallbacks.push(cb)
      }
      observe() {
        this.cb()
      }
      disconnect() {}
    },
  )
  getContextSpy = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(() => {
    const ctx = {
      createImageData: (w: number, h: number): ImageData => {
        lastImageData = {
          width: w,
          height: h,
          data: new Uint8ClampedArray(w * h * 4),
          colorSpace: 'srgb',
        } as ImageData
        return lastImageData
      },
      putImageData: () => {},
    }
    return ctx as unknown as CanvasRenderingContext2D
  })
  toBlobSpy = vi
    .spyOn(HTMLCanvasElement.prototype, 'toBlob')
    .mockImplementation((cb: BlobCallback) => {
      cb(new Blob(['png'], { type: 'image/png' }))
    })
  let blobId = 0
  createObjectURLSpy = vi
    .spyOn(URL, 'createObjectURL')
    .mockImplementation(() => `blob:lg-test-${++blobId}`)
  revokeObjectURLSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  vi.spyOn(HTMLImageElement.prototype, 'decode').mockResolvedValue(undefined)
})

afterEach(() => {
  getContextSpy?.mockRestore()
  toBlobSpy?.mockRestore()
  createObjectURLSpy?.mockRestore()
  revokeObjectURLSpy?.mockRestore()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('useLiquidGlass', () => {
  it('generates a 4-way symmetric SDF displacement map with neutral center', async () => {
    const dispose = preloadDisplacementMap(64, 40, 12, 10)
    await flushPromises()
    expect(lastImageData).not.toBeNull()
    expect(getContextSpy).toHaveBeenCalledWith('2d', { willReadFrequently: true })
    const img = lastImageData!
    expect(img.width).toBe(64)
    expect(img.height).toBe(40)

    const pixel = (x: number, y: number) => {
      const i = (y * img.width + x) * 4
      return [img.data[i], img.data[i + 1], img.data[i + 2], img.data[i + 3]]
    }

    // Center pixel is neutral (128, 128, 0, 255)
    expect(pixel(32, 20)).toEqual([128, 128, 0, 255])

    // 4-way symmetry inside the corner bezel band: top-left vs top-right, bottom-left, bottom-right
    const [rTL, gTL, bTL, aTL] = pixel(5, 5)
    const [rTR, gTR] = pixel(58, 5)
    const [rBL, gBL] = pixel(5, 34)
    const [rBR, gBR] = pixel(58, 34)

    expect(bTL).toBe(0)
    expect(aTL).toBe(255)
    expect(rTL).toBeGreaterThan(128)
    expect(rTL + rTR).toBe(255)
    expect(gTL).toBe(gTR)
    expect(rTL).toBe(rBL)
    expect(gTL + gBL).toBe(255)
    expect(rBR).toBe(rTR)
    expect(gBR).toBe(gBL)

    dispose()
  })

  it('binds pre-warmed geometry synchronously inside ResizeObserver without re-decoding', async () => {
    const dispose = preloadDisplacementMap(164, 34, 17, 17)
    await flushPromises()
    const decodeSpy = vi.spyOn(HTMLImageElement.prototype, 'decode')
    decodeSpy.mockClear()

    const Host = defineComponent({
      setup() {
        const layerRef = ref<HTMLElement | null>(null)
        const { filterId } = useLiquidGlass(layerRef)
        return () =>
          h('div', {
            ref: layerRef,
            'data-filter-id': filterId.value,
            style: { borderTopLeftRadius: '17px' },
          })
      },
    })

    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(
      () => new DOMRect(0, 0, 164, 34),
    )

    const wrapper = mount(Host, { attachTo: document.body })
    await nextTick()

    // Filter ID is exposed in the same tick without waiting for Image.decode()
    expect(wrapper.attributes('data-filter-id')).toMatch(/^lgf-\d+$/)
    expect(decodeSpy).not.toHaveBeenCalled()

    wrapper.unmount()
    dispose()
  })

  it('places the warmup shader probe inside the viewport rather than offscreen', async () => {
    warmupLiquidGlassPipeline()
    await flushPromises()
    const probe = Array.from(document.body.querySelectorAll('div')).find(el =>
      el.innerHTML.includes('#lg-warmup-probe'),
    )
    expect(probe).toBeDefined()
    expect(probe?.style.right).toBe('0px')
    expect(probe?.style.bottom).toBe('0px')
    expect(probe?.style.top).not.toBe('-9999px')
  })
})
