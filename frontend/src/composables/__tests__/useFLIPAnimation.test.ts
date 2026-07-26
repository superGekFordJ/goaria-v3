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

  it('treats undefined keys as entering from container bottom', () => {
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

    // 'b' enters from container height (600) -> deltaY = 600, animated.
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

  it('treats play() without capture() as all-entering and animates', () => {
    const container = makeContainer()
    const containerRef = ref<HTMLElement | null>(container)
    const { play } = useFLIPAnimation(containerRef)

    const els = setupRows(container, [
      { key: 'a', top: 0 },
      { key: 'b', top: 100 },
    ])

    play()

    // All keys undefined -> entering from bottom -> animated.
    expect(els[0].style.transform).not.toBe('')
    expect(els[1].style.transform).not.toBe('')
  })
})
