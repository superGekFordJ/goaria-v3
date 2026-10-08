import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, reactive, ref } from 'vue'
import TaskChunkMap from './TaskChunkMap.vue'

const mocks = vi.hoisted(() => ({
  ui: null as null | { prismHue: number; prismTone: string },
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      const suffix = params ? ` ${JSON.stringify(params)}` : ''
      return `${key}${suffix}`
    },
    locale: ref('en'),
  }),
}))

vi.mock('../../stores/ui', () => ({
  useUIStore: () => mocks.ui,
}))

// --- canvas / observer / frame stubs (happy-dom has no 2D context or layout) ---

interface FakeCtx {
  setTransform: ReturnType<typeof vi.fn>
  clearRect: ReturnType<typeof vi.fn>
  beginPath: ReturnType<typeof vi.fn>
  roundRect: ReturnType<typeof vi.fn>
  rect: ReturnType<typeof vi.fn>
  fill: ReturnType<typeof vi.fn>
  fillRect: ReturnType<typeof vi.fn>
  globalAlpha: number
  styles: string[]
  // Each shape call tagged with the fillStyle active at that moment, so a
  // test can count how many capsules a given state produced.
  drawCalls: Array<{ fn: string; style: string }>
  fillStyle: string
}

function makeCtx(): FakeCtx {
  const styles: string[] = []
  const drawCalls: Array<{ fn: string; style: string }> = []
  let current = ''
  const tag = (fn: string) => vi.fn(() => drawCalls.push({ fn, style: current }))
  return {
    setTransform: vi.fn(),
    clearRect: vi.fn(),
    beginPath: vi.fn(),
    roundRect: tag('roundRect'),
    rect: tag('rect'),
    fill: vi.fn(),
    fillRect: tag('fillRect'),
    globalAlpha: 1,
    styles,
    drawCalls,
    set fillStyle(value: string) {
      current = value
      styles.push(value)
    },
    get fillStyle() {
      return current
    },
  }
}

const TOKEN_COLORS: Record<string, string> = {
  'var(--app-text-subtle)': 'rgb(1, 1, 1)',
  'var(--neon-primary)': 'rgb(2, 2, 2)',
  'var(--status-complete)': 'rgb(3, 3, 3)',
  'var(--status-paused)': 'rgb(4, 4, 4)',
  'var(--app-text)': 'rgb(5, 5, 5)',
}

class FakeResizeObserver {
  static instances: FakeResizeObserver[] = []
  observe = vi.fn()
  disconnect = vi.fn()
  constructor(public callback: (entries: ResizeObserverEntry[]) => void) {
    FakeResizeObserver.instances.push(this)
  }
  resize(width: number, height: number, device?: { inlineSize: number; blockSize: number }) {
    this.callback([
      {
        contentRect: { width, height },
        devicePixelContentBoxSize: device ? [device] : undefined,
      } as unknown as ResizeObserverEntry,
    ])
  }
}

let contexts: Map<HTMLCanvasElement, FakeCtx>
let contextAvailable: boolean
let frames: Array<FrameRequestCallback | null>
let wrapper: VueWrapper | null = null

function setDpr(value: number) {
  Object.defineProperty(window, 'devicePixelRatio', { value, configurable: true })
}

function flushFrames() {
  const pending = frames
  frames = []
  pending.forEach(cb => cb?.(0))
}

const pendingFrames = () => frames.filter(Boolean).length

function mountMap(props: Record<string, unknown> = {}, attach = true) {
  wrapper = mount(TaskChunkMap, {
    props: { states: [2, 2, 1, 0], ...props },
    attachTo: attach ? document.body : undefined,
  })
  return wrapper
}

function canvases(w: VueWrapper) {
  return w.findAll('canvas').map(c => c.element as HTMLCanvasElement)
}

const observer = () => FakeResizeObserver.instances[FakeResizeObserver.instances.length - 1]

describe('TaskChunkMap', () => {
  beforeEach(() => {
    mocks.ui = reactive({ prismHue: 280, prismTone: 'vivid' })
    contexts = new Map()
    contextAvailable = true
    frames = []
    FakeResizeObserver.instances = []
    setDpr(1)
    vi.stubGlobal('ResizeObserver', FakeResizeObserver)
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => frames.push(cb))
    vi.stubGlobal('cancelAnimationFrame', (id: number) => {
      frames[id - 1] = null
    })
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(function (
      this: HTMLCanvasElement,
    ) {
      if (!contextAvailable) return null
      let ctx = contexts.get(this)
      if (!ctx) {
        ctx = makeCtx()
        contexts.set(this, ctx)
      }
      return ctx as unknown as CanvasRenderingContext2D
    } as unknown as HTMLCanvasElement['getContext'])
    vi.spyOn(window, 'getComputedStyle').mockImplementation(
      (el: Element) =>
        ({ color: TOKEN_COLORS[(el as HTMLElement).style.color] ?? '' }) as CSSStyleDeclaration,
    )
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = null
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
    document.documentElement.removeAttribute('data-theme')
    document.documentElement.removeAttribute('data-skin')
  })

  describe('accessibility and tooltip', () => {
    it('exposes count and chunk size through title and aria-label', () => {
      const w = mountMap({ count: 4096, chunkSize: 4 * 1024 * 1024 })
      const root = w.get('[data-chunk-map]')
      const expected = 'taskDetail.chunkMap {"count":"4,096","size":"4.00 MB"}'
      expect(root.attributes('role')).toBe('img')
      expect(root.attributes('title')).toBe(expected)
      expect(root.attributes('aria-label')).toBe(expected)
      expect(root.attributes('tabindex')).toBeUndefined()
      expect(w.findAll('canvas').every(c => c.attributes('aria-hidden') === 'true')).toBe(true)
    })

    it('falls back to the array length and a size-less label', () => {
      const w = mountMap({ states: [0, 1, 2] })
      expect(w.get('[data-chunk-map]').attributes('title')).toBe(
        'taskDetail.chunkMapNoSize {"count":"3"}',
      )
      expect(w.text()).toBe('')
    })

    it('marks the frozen state', async () => {
      const w = mountMap()
      expect(w.get('[data-chunk-map]').attributes('data-frozen')).toBeUndefined()
      await w.setProps({ frozen: true })
      expect(w.get('[data-chunk-map]').attributes('data-frozen')).toBe('')
    })
  })

  describe('backing store and scale', () => {
    it.each([
      [1, 168, 38, 168, 38],
      [1.25, 168, 38, 210, 48],
      [1.5, 168, 38, 252, 57],
    ])('dpr %s: canvas = round(css x dpr) and context scaled by dpr', (dpr, w, h, dw, dh) => {
      setDpr(dpr)
      const wrap = mountMap()
      observer().resize(w, h)
      flushFrames()
      for (const canvas of canvases(wrap)) {
        expect(canvas.width).toBe(dw)
        expect(canvas.height).toBe(dh)
        expect(contexts.get(canvas)!.setTransform).toHaveBeenLastCalledWith(dpr, 0, 0, dpr, 0, 0)
      }
      expect(contexts.get(canvases(wrap)[0])!.roundRect).toHaveBeenCalled()
    })

    it('prefers the device-pixel content box when the observer provides it', () => {
      setDpr(1.25)
      const wrap = mountMap()
      observer().resize(168, 38, { inlineSize: 211, blockSize: 47 })
      flushFrames()
      expect(canvases(wrap)[0].width).toBe(211)
      expect(canvases(wrap)[0].height).toBe(47)
    })

    it('redraws when the display scale changes without a resize', () => {
      const listeners: Array<() => void> = []
      vi.stubGlobal('matchMedia', () => ({
        addEventListener: (_: string, cb: () => void) => listeners.push(cb),
        removeEventListener: vi.fn(),
      }))
      const wrap = mountMap()
      observer().resize(168, 38)
      flushFrames()
      setDpr(1.5)
      listeners[listeners.length - 1]()
      expect(pendingFrames()).toBe(1)
      flushFrames()
      expect(canvases(wrap)[0].width).toBe(252)
    })

    it('stays silent with a zero-size box or no 2D context', () => {
      const wrap = mountMap()
      observer().resize(0, 0)
      flushFrames()
      expect(contexts.size).toBe(0)

      contextAvailable = false
      observer().resize(168, 38)
      expect(() => flushFrames()).not.toThrow()
      expect(canvases(wrap)[0].width).toBe(168)
    })
  })

  describe('redraw scheduling', () => {
    it('coalesces several changes in one tick into a single frame', async () => {
      const w = mountMap()
      observer().resize(168, 38)
      await w.setProps({ states: [1, 1, 0], frozen: true })
      expect(pendingFrames()).toBe(1)
      flushFrames()
      expect(contexts.get(canvases(w)[0])!.clearRect).toHaveBeenCalledTimes(1)
    })

    it('does not repaint when the data reference is unchanged', async () => {
      const states = [2, 1, 0]
      const w = mountMap({ states })
      observer().resize(168, 38)
      flushFrames()
      await w.setProps({ states, count: 3 })
      expect(pendingFrames()).toBe(0)
      await w.setProps({ states: [...states] })
      expect(pendingFrames()).toBe(1)
    })

    it('re-resolves colors after a theme or skin attribute change', async () => {
      mountMap()
      observer().resize(168, 38)
      flushFrames()
      const reads = vi.mocked(window.getComputedStyle).mock.calls.length
      flushFrames()
      expect(vi.mocked(window.getComputedStyle).mock.calls.length).toBe(reads)

      document.documentElement.setAttribute('data-skin', 'surge')
      await new Promise(resolve => setTimeout(resolve, 0))
      expect(pendingFrames()).toBe(1)
      flushFrames()
      expect(vi.mocked(window.getComputedStyle).mock.calls.length).toBeGreaterThan(reads)
    })

    it('re-resolves colors when the prism hue moves', async () => {
      mountMap()
      observer().resize(168, 38)
      flushFrames()
      mocks.ui!.prismHue = 120
      await nextTick()
      expect(pendingFrames()).toBe(1)
    })

    it('cancels the pending frame and disconnects observers on unmount', () => {
      const disconnect = vi.spyOn(MutationObserver.prototype, 'disconnect')
      const w = mountMap()
      const ro = observer()
      ro.resize(168, 38)
      expect(pendingFrames()).toBe(1)
      w.unmount()
      wrapper = null
      expect(pendingFrames()).toBe(0)
      expect(ro.disconnect).toHaveBeenCalled()
      expect(disconnect).toHaveBeenCalled()
    })

    it('a copy mounted in a detached tree never observes or draws', async () => {
      const w = mountMap({}, false)
      expect(FakeResizeObserver.instances).toHaveLength(0)
      await w.setProps({ states: [1] })
      expect(pendingFrames()).toBe(0)
    })
  })

  describe('byte progress track', () => {
    const DOWNLOADING_COLOR = 'rgb(2, 2, 2)'
    // One downloading capsule per bucket on the base layer, tagged by the
    // downloading fillStyle that was active while the shapes were recorded.
    const downloadingCapsules = (w: VueWrapper) =>
      contexts
        .get(canvases(w)[0])!
        .drawCalls.filter(c => c.fn === 'roundRect' && c.style === DOWNLOADING_COLOR).length

    it('a partial chunk lights only the buckets its bytes reach, incl. one straddling frontier', () => {
      const props = { states: [2, 1, 0, 0], chunkSize: 100, totalSize: 400 }
      const w = mountMap(props)
      observer().resize(168, 38)
      flushFrames()
      const discrete = downloadingCapsules(w)
      w.unmount()

      // chunk1 has 55/100B: the byte track paints [100,155) as complete and
      // keeps only the bucket straddling 155 downloading, while the discrete
      // track marks the whole chunk's bucket span.
      const w2 = mountMap({ ...props, progress: [100, 55, 0, 0] })
      observer().resize(168, 38)
      flushFrames()
      const byteLevel = downloadingCapsules(w2)

      expect(discrete).toBeGreaterThan(0)
      expect(byteLevel).toBeGreaterThan(0)
      expect(byteLevel).toBeLessThan(discrete)
    })

    it('unusable geometry falls back to the discrete track', () => {
      const props = { states: [2, 1, 0, 0], chunkSize: 100 }
      const w = mountMap(props)
      observer().resize(168, 38)
      flushFrames()
      const discrete = downloadingCapsules(w)
      w.unmount()

      // totalSize unknown → the byte track declines; the outcome must match
      // the discrete rendering capsule for capsule.
      const w2 = mountMap({ ...props, progress: [100, 50, 0, 0] })
      observer().resize(168, 38)
      flushFrames()
      expect(downloadingCapsules(w2)).toBe(discrete)
    })

    it('a new progress or totalSize reference schedules a repaint', async () => {
      const progress = [100, 50, 0, 0]
      const w = mountMap({ states: [2, 1, 0, 0], chunkSize: 100, totalSize: 400, progress })
      observer().resize(168, 38)
      flushFrames()
      await w.setProps({ progress })
      expect(pendingFrames()).toBe(0)
      await w.setProps({ progress: [...progress] })
      expect(pendingFrames()).toBe(1)
      flushFrames()
      await w.setProps({ totalSize: 800 })
      expect(pendingFrames()).toBe(1)
    })
  })

  describe('palette', () => {
    it('paints every state from its token and the frontier on the glow layer', () => {
      const w = mountMap({ states: [0, 2, 1] })
      observer().resize(168, 38)
      flushFrames()
      const [base, glow] = canvases(w).map(c => contexts.get(c)!)
      expect(base.styles).toEqual(['rgb(1, 1, 1)', 'rgb(3, 3, 3)', 'rgb(2, 2, 2)'])
      expect(glow.styles.every(s => s === 'rgb(2, 2, 2)' || s === 'rgb(5, 5, 5)')).toBe(true)
      expect(glow.fillRect).toHaveBeenCalled()
    })

    it('frozen paints the whole map in the paused tone without a hot core', async () => {
      const w = mountMap({ states: [0, 2, 1], frozen: true })
      observer().resize(168, 38)
      flushFrames()
      const [base, glow] = canvases(w).map(c => contexts.get(c)!)
      expect(new Set([...base.styles, ...glow.styles])).toEqual(new Set(['rgb(4, 4, 4)']))
      expect(glow.fillRect).not.toHaveBeenCalled()
    })

    it('light theme drops the hot core', () => {
      document.documentElement.setAttribute('data-theme', 'light')
      const w = mountMap({ states: [1, 1] })
      observer().resize(168, 38)
      flushFrames()
      expect(contexts.get(canvases(w)[1])!.fillRect).not.toHaveBeenCalled()
    })

    it('does not draw when a token cannot be resolved', () => {
      vi.mocked(window.getComputedStyle).mockImplementation(
        () => ({ color: '' }) as CSSStyleDeclaration,
      )
      const w = mountMap()
      observer().resize(168, 38)
      flushFrames()
      expect(contexts.get(canvases(w)[0])!.roundRect).not.toHaveBeenCalled()
    })
  })
})
