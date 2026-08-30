import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  supportsUrlBackdropFilter,
  _resetSupportsUrlBackdropFilterForTest,
  buildDisplacementMap,
  buildFilterDataUri,
  getStaticGlassFilterUrl,
  GLASS_PRESETS,
} from './glassMaterial'

describe('glassMaterial supportsUrlBackdropFilter', () => {
  const originalNavigator = globalThis.navigator

  beforeEach(() => {
    _resetSupportsUrlBackdropFilterForTest(null)
  })

  afterEach(() => {
    _resetSupportsUrlBackdropFilterForTest(null)
    Object.defineProperty(globalThis, 'navigator', {
      value: originalNavigator,
      configurable: true,
      writable: true,
    })
  })

  it('detects Chromium through userAgentData brands', () => {
    Object.defineProperty(globalThis, 'navigator', {
      value: {
        userAgentData: {
          brands: [{ brand: 'Chromium', version: '120' }, { brand: 'Google Chrome', version: '120' }],
        },
        userAgent: 'Mozilla/5.0 Chrome/120.0.0.0 Safari/537.36',
      },
      configurable: true,
      writable: true,
    })
    expect(supportsUrlBackdropFilter()).toBe(true)
  })

  it('detects non-Chromium through userAgentData brands', () => {
    Object.defineProperty(globalThis, 'navigator', {
      value: {
        userAgentData: {
          brands: [{ brand: 'SomeOtherBrowser', version: '1.0' }],
        },
        userAgent: 'Mozilla/5.0',
      },
      configurable: true,
      writable: true,
    })
    expect(supportsUrlBackdropFilter()).toBe(false)
  })

  it('detects Chrome UA as supported when userAgentData is missing', () => {
    Object.defineProperty(globalThis, 'navigator', {
      value: {
        userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36',
      },
      configurable: true,
      writable: true,
    })
    expect(supportsUrlBackdropFilter()).toBe(true)
  })

  it('detects Edge UA as supported when userAgentData is missing', () => {
    Object.defineProperty(globalThis, 'navigator', {
      value: {
        userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36 Edg/128.0.0.0',
      },
      configurable: true,
      writable: true,
    })
    expect(supportsUrlBackdropFilter()).toBe(true)
  })

  it('detects Firefox UA as unsupported', () => {
    Object.defineProperty(globalThis, 'navigator', {
      value: {
        userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0',
      },
      configurable: true,
      writable: true,
    })
    expect(supportsUrlBackdropFilter()).toBe(false)
  })

  it('detects Safari / WebKit UA as unsupported', () => {
    Object.defineProperty(globalThis, 'navigator', {
      value: {
        userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15',
      },
      configurable: true,
      writable: true,
    })
    expect(supportsUrlBackdropFilter()).toBe(false)
  })
})

describe('glassMaterial builders', () => {
  beforeEach(() => {
    // Mock canvas for Node environment
    const mockContext = {
      createImageData: (w: number, h: number) => ({ data: new Uint8ClampedArray(w * h * 4) }),
      putImageData: vi.fn(),
    }
    const mockCanvas = {
      width: 0,
      height: 0,
      getContext: vi.fn().mockReturnValue(mockContext),
      toDataURL: vi.fn().mockReturnValue('data:image/png;base64,mockCanvasData'),
    }
    vi.stubGlobal('document', {
      createElement: vi.fn((tag: string) => {
        if (tag === 'canvas') return mockCanvas
        return {}
      }),
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('buildDisplacementMap produces data URL', () => {
    const dataUri = buildDisplacementMap(100, 50, 12, 10, 1)
    expect(dataUri).toBe('data:image/png;base64,mockCanvasData')
  })

  it('buildFilterDataUri produces SVG filter data URI', () => {
    const filterUri = buildFilterDataUri(100, 50, 12, GLASS_PRESETS.clear)
    expect(filterUri).toMatch(/^url\('data:image\/svg\+xml;charset=utf-8,.*#f'\)$/)
  })

  it('getStaticGlassFilterUrl returns static glass filter URI', () => {
    const staticUrl = getStaticGlassFilterUrl()
    expect(staticUrl).toMatch(/^url\('data:image\/svg\+xml;charset=utf-8,.*#static-glass-refraction'\)$/)
  })
})
