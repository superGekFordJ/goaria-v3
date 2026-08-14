import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ref } from 'vue'
import { useFLIPAnimation } from '../useFLIPAnimation'

function mockRect(top: number, height = 100): DOMRect {
  return {
    top,
    height,
    bottom: top + height,
    left: 0,
    right: 100,
    width: 100,
    x: 0,
    y: top,
    toJSON: () => ({}),
  } as DOMRect
}

function makeContainer(): HTMLElement {
  const container = document.createElement('div')
  vi.spyOn(container, 'getBoundingClientRect').mockReturnValue(mockRect(0, 600))
  return container
}

function makeRow(key: string, top: number): HTMLElement {
  const el = document.createElement('div')
  el.setAttribute('data-entry-key', key)
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue(mockRect(top, 100))
  return el
}

function setupRows(
  container: HTMLElement,
  rows: Array<{ key: string; top: number }>,
): HTMLElement[] {
  const els = rows.map(r => makeRow(r.key, r.top))
  els.forEach(el => container.appendChild(el))
  vi.spyOn(container, 'querySelectorAll').mockReturnValue(els as unknown as NodeListOf<HTMLElement>)
  return els
}

function setRowTop(el: HTMLElement, top: number): void {
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue(mockRect(top, 100))
}

function invertDy(el: HTMLElement): number | null {
  const match = el.style.transform.match(/translateY\((-?\d+(?:\.\d+)?)px\)/)
  return match ? Number(match[1]) : null
}

function translateY(el: HTMLElement): number {
  return invertDy(el) ?? 0
}

function makeLayoutRow(key: string, layoutTop: number, height = 100): HTMLElement {
  const el = document.createElement('div')
  el.setAttribute('data-entry-key', key)
  el.dataset.layoutTop = String(layoutTop)
  vi.spyOn(el, 'getBoundingClientRect').mockImplementation(() => {
    const top = Number(el.dataset.layoutTop) + translateY(el)
    return mockRect(top, height)
  })
  return el
}

function installLayoutRows(
  container: HTMLElement,
  rows: Array<{ key: string; layoutTop: number }>,
): HTMLElement[] {
  const els = rows.map(r => makeLayoutRow(r.key, r.layoutTop))
  els.forEach(el => container.appendChild(el))
  vi.spyOn(container, 'querySelectorAll').mockReturnValue(els as unknown as NodeListOf<HTMLElement>)
  return els
}

function holdAnimationFrames(): FrameRequestCallback[] {
  const queue: FrameRequestCallback[] = []
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
    queue.push(cb)
    return queue.length
  })
  return queue
}

describe('useFLIPAnimation', () => {
  let rafMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    vi.useFakeTimers()
    rafMock = vi.fn(cb => {
      cb(0)
      return 0
    })
    vi.stubGlobal('requestAnimationFrame', rafMock)
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('computes deltaY and animates only elements above threshold', () => {
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const els = setupRows(container, [
      { key: 'a', top: 0 },
      { key: 'b', top: 100 },
      { key: 'c', top: 200 },
    ])

    capture()

    // Move 'a' down 50px (major), 'b' stays, 'c' moves 1px (below threshold).
    setRowTop(els[0], 50)
    setRowTop(els[1], 100)
    setRowTop(els[2], 201)

    play()

    expect(els[0].style.transform).not.toBe('')
    expect(els[0].style.transition).not.toBe('')
    expect(els[1].style.transform).toBe('')
    expect(els[2].style.transform).toBe('')
  })

  it('early-exits when no element moved and none are entering', () => {
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const els = setupRows(container, [
      { key: 'a', top: 0 },
      { key: 'b', top: 100 },
    ])

    capture()
    // No position changes.
    play()

    expect(els[0].style.transform).toBe('')
    expect(els[0].style.transition).toBe('')
    expect(els[0].style.zIndex).toBe('')
    expect(els[1].style.transform).toBe('')
  })

  it('generation counter prevents stale cleanup from clobbering a newer cycle', () => {
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const els = setupRows(container, [{ key: 'a', top: 0 }])
    const el = els[0]

    capture()
    setRowTop(el, 50)
    play()

    // Cycle 2 before cycle 1 cleanup fires.
    setRowTop(el, 100)
    play()

    // Cycle 2's transform must still be set — cycle 1's stale cleanup bailed via generation guard.
    expect(el.style.transform).not.toBe('')

    // Advance past fallback timeout; cycle 1's timer should bail via generation guard.
    vi.advanceTimersByTime(700)

    // Cycle 2 styles should be cleaned up by its own fallback timer.
    expect(el.style.transform).toBe('')
    expect(el.style.transition).toBe('')
  })

  it('treats undefined keys as entering from above their final position', () => {
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const els = setupRows(container, [{ key: 'a', top: 0 }])
    capture()

    // Add a new element not present in lastRects.
    const newEl = makeRow('b', 0)
    container.appendChild(newEl)
    vi.spyOn(container, 'querySelectorAll').mockReturnValue([
      els[0],
      newEl,
    ] as unknown as NodeListOf<HTMLElement>)

    play()

    // 'b' enters from above its final position (newTop - height = -100) -> deltaY = -100, animated.
    expect(newEl.style.transform).not.toBe('')
    expect(newEl.style.transition).not.toBe('')
  })

  it('restores all inline styles after a full FLIP cycle', () => {
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const els = setupRows(container, [{ key: 'a', top: 0 }])
    const el = els[0]

    capture()
    setRowTop(el, 60) // major move
    play()

    // Trigger fallback cleanup.
    vi.advanceTimersByTime(700)

    expect(el.style.transform).toBe('')
    expect(el.style.transition).toBe('')
    expect(el.style.zIndex).toBe('')
    expect(el.style.position).toBe('')
    expect(el.style.filter).toBe('')
    expect(el.style.boxShadow).toBe('')
    expect(el.style.borderRadius).toBe('')
    expect(el.style.animationDelay).toBe('')
    expect(el.classList.contains('animate-spring-in')).toBe(false)
  })

  it('no-ops when the container ref is null', () => {
    const containerRef = ref<HTMLElement | null>(null)
    const { capture, play, clear } = useFLIPAnimation(containerRef)

    expect(() => {
      capture()
      play()
      clear()
    }).not.toThrow()
  })

  it('after cleanup, a later below-threshold cycle keeps animation none', () => {
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const els = setupRows(container, [{ key: 'a', top: 0 }])
    const el = els[0]
    el.classList.add('animate-spring-in')

    capture()
    setRowTop(el, 100)
    play()
    vi.advanceTimersByTime(700)

    expect(el.style.animation).toBe('none')
    expect(el.classList.contains('animate-spring-in')).toBe(false)

    capture()
    setRowTop(el, 101)
    play()

    expect(invertDy(el)).toBeNull()
    expect(el.style.animation).toBe('none')
    expect(el.classList.contains('animate-spring-in')).toBe(false)
  })

  it('treats play() without capture() as all-entering from above and animates', () => {
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { play } = useFLIPAnimation(containerRef)

    const els = setupRows(container, [
      { key: 'a', top: 0 },
      { key: 'b', top: 100 },
    ])

    play()

    // All keys undefined -> entering from above their final positions -> animated.
    expect(els[0].style.transform).not.toBe('')
    expect(els[1].style.transform).not.toBe('')
  })

  it('new key with Last at top inverts -height; existing yielder also -height', () => {
    holdAnimationFrames()
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    setupRows(container, [{ key: 'a', top: 0 }])
    capture()

    const [enterer, yielder] = setupRows(container, [
      { key: 'b', top: 0 },
      { key: 'a', top: 100 },
    ])
    play()

    expect(invertDy(enterer)).toBe(-100)
    expect(invertDy(yielder)).toBe(-100)
  })

  it('unfiltered capture after the new key is already at the old top is clone (invert ~0)', () => {
    holdAnimationFrames()
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const [enterer, yielder] = setupRows(container, [
      { key: 'b', top: 0 },
      { key: 'a', top: 100 },
    ])
    capture()
    play()

    expect(invertDy(enterer)).toBeNull()
    expect(invertDy(yielder)).toBeNull()
  })

  it('capture restricted to previous keys: new key already at old top still enters from-above', () => {
    holdAnimationFrames()
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const [enterer, yielder] = setupRows(container, [
      { key: 'b', top: 0 },
      { key: 'a', top: 100 },
    ])
    capture(['a'])
    play()

    expect(invertDy(enterer)).toBe(-100)
    expect(invertDy(yielder)).toBeNull()
  })

  it('consecutive prepends with Last at top: every enterer inverts -height', () => {
    holdAnimationFrames()
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const keys = ['seed']
    setupRows(container, [{ key: 'seed', top: 0 }])

    const enteringDy: Array<{ key: string; dy: number | null }> = []
    for (let n = 1; n <= 4; n++) {
      capture()
      const next = `n${n}`
      keys.unshift(next)
      const els = setupRows(
        container,
        keys.map((key, index) => ({ key, top: index * 100 })),
      )
      play()
      enteringDy.push({ key: next, dy: invertDy(els[0]) })
    }

    expect(enteringDy).toEqual([
      { key: 'n1', dy: -100 },
      { key: 'n2', dy: -100 },
      { key: 'n3', dy: -100 },
      { key: 'n4', dy: -100 },
    ])
  })

  it('new key with Last at bottom still inverts -height into the bottom slot', () => {
    holdAnimationFrames()
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    setupRows(container, [
      { key: 'a', top: 0 },
      { key: 'b', top: 100 },
    ])
    capture()

    const bottomTop = 400
    const [a, b, c] = setupRows(container, [
      { key: 'a', top: 0 },
      { key: 'b', top: 100 },
      { key: 'c', top: bottomTop },
    ])
    play()

    expect(invertDy(c)).toBe(-100)
    expect(invertDy(a)).toBeNull()
    expect(invertDy(b)).toBeNull()
  })

  it('key already in lastRects at the bottom, Last at top: positive MOVE deltaY', () => {
    holdAnimationFrames()
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { capture, play } = useFLIPAnimation(containerRef)

    const bottomTop = 300
    setupRows(container, [
      { key: 'a', top: 0 },
      { key: 'b', top: 100 },
      { key: 'c', top: 200 },
      { key: 'd', top: bottomTop },
    ])
    capture()

    const [d] = setupRows(container, [
      { key: 'd', top: 0 },
      { key: 'a', top: 100 },
      { key: 'b', top: 200 },
      { key: 'c', top: 300 },
    ])
    play()

    expect(invertDy(d)).toBe(bottomTop)
    expect(invertDy(d)).toBeGreaterThan(0)
  })

  it('leftover Invert capture with previousKeys: new key at old top still enters from-above', () => {
    holdAnimationFrames()
    const container = makeContainer()
    const { capture, play } = useFLIPAnimation(ref(container))

    installLayoutRows(container, [{ key: 'a', layoutTop: 0 }])
    capture()
    const [b, a] = installLayoutRows(container, [
      { key: 'b', layoutTop: 0 },
      { key: 'a', layoutTop: 100 },
    ])
    play()

    b.dataset.layoutTop = '100'
    a.dataset.layoutTop = '200'
    const [c] = installLayoutRows(container, [{ key: 'c', layoutTop: 0 }])
    vi.spyOn(container, 'querySelectorAll').mockReturnValue(
      [c, b, a] as unknown as NodeListOf<HTMLElement>,
    )

    capture(['b', 'a'])
    play()

    expect(invertDy(c)).toBe(-100)
    expect(invertDy(b)).toBe(-100)
  })

  it('same-key second play during leftover Invert replays yielder -height', () => {
    holdAnimationFrames()
    const container = makeContainer()
    const { capture, play } = useFLIPAnimation(ref(container))

    installLayoutRows(container, [
      { key: 'a', layoutTop: 0 },
      { key: 'b', layoutTop: 100 },
    ])
    capture()
    const [enterer, yielder] = installLayoutRows(container, [
      { key: 'c', layoutTop: 0 },
      { key: 'a', layoutTop: 100 },
      { key: 'b', layoutTop: 200 },
    ])
    play()
    expect(invertDy(enterer)).toBe(-100)
    expect(invertDy(yielder)).toBe(-100)

    capture()
    play()

    expect(invertDy(yielder)).toBe(-100)
    expect(invertDy(enterer)).toBe(-100)
  })
})
