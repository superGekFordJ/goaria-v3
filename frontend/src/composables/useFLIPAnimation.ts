import type { Ref } from 'vue'

export interface FLIPAnimationOptions {
  keySelector?: string
  keyAttribute?: string
  threshold?: number
  majorMoveThreshold?: number
  duration?: number
  fallbackTimeout?: number
}

export interface FLIPAnimationApi {
  capture: (previousKeys?: readonly string[]) => void
  play: () => void
  clear: () => void
}

const DEFAULT_KEY_SELECTOR = '[data-entry-key]'
const DEFAULT_KEY_ATTRIBUTE = 'data-entry-key'
const DEFAULT_THRESHOLD = 2
const DEFAULT_MAJOR_MOVE_THRESHOLD = 40
const DEFAULT_DURATION = 0.45
const DEFAULT_FALLBACK_TIMEOUT = 600

export function useFLIPAnimation(
  containerRef: Ref<HTMLElement | null>,
  options?: FLIPAnimationOptions,
): FLIPAnimationApi {
  const keySelector = options?.keySelector ?? DEFAULT_KEY_SELECTOR
  const keyAttribute = options?.keyAttribute ?? DEFAULT_KEY_ATTRIBUTE
  const threshold = options?.threshold ?? DEFAULT_THRESHOLD
  const majorMoveThreshold = options?.majorMoveThreshold ?? DEFAULT_MAJOR_MOVE_THRESHOLD
  const duration = options?.duration ?? DEFAULT_DURATION
  const fallbackTimeout = options?.fallbackTimeout ?? DEFAULT_FALLBACK_TIMEOUT

  const lastRects = new Map<string, number>()
  const flipGeneration = new WeakMap<HTMLElement, number>()

  function capture(previousKeys?: readonly string[]): void {
    const container = containerRef.value
    if (!container) return
    lastRects.clear()
    const allowed = previousKeys ? new Set(previousKeys) : null
    const containerRect = container.getBoundingClientRect()
    const elements = container.querySelectorAll<HTMLElement>(keySelector)
    elements.forEach(el => {
      const key = el.getAttribute(keyAttribute)
      if (!key) return
      if (allowed && !allowed.has(key)) return
      const rect = el.getBoundingClientRect()
      lastRects.set(key, rect.top - containerRect.top)
    })
  }

  function clear(): void {
    lastRects.clear()
  }

  function play(): void {
    const container = containerRef.value
    if (!container) return
    const containerRect = container.getBoundingClientRect()
    const elements = container.querySelectorAll<HTMLElement>(keySelector)

    // D2 early-exit: first pass computes deltaY for every element.
    // Entering items (key absent from lastRects) originate one card height above
    // their final position, so they slide down into place — matching the project's
    // prepend-first ordering where new tasks land at the top of the list.
    const deltas: Array<{ el: HTMLElement; deltaY: number; isMajorMove: boolean }> = []
    let hasEntering = false

    elements.forEach(el => {
      const key = el.getAttribute(keyAttribute)
      if (!key) return

      const elRect = el.getBoundingClientRect()
      const newTop = elRect.top - containerRect.top
      const storedOldTop = lastRects.get(key)
      const isEntering = storedOldTop === undefined
      if (isEntering) hasEntering = true
      const oldTop = isEntering ? newTop - elRect.height : (storedOldTop as number)
      const deltaY = oldTop - newTop

      if (Math.abs(deltaY) > threshold) {
        deltas.push({
          el,
          deltaY,
          isMajorMove: Math.abs(deltaY) > majorMoveThreshold,
        })
      }
    })

    // No movement and no genuinely entering items -> skip the play phase entirely.
    if (deltas.length === 0 && !hasEntering) return

    const transitionString = `transform ${duration}s cubic-bezier(0.2, 0.8, 0.2, 1)`

    deltas.forEach(({ el, deltaY, isMajorMove }) => {
      // N1 generation counter: stale cleanup closures bail out when a newer cycle owns the element.
      const gen = (flipGeneration.get(el) ?? 0) + 1
      flipGeneration.set(el, gen)

      el.style.position = 'relative'
      el.style.zIndex = isMajorMove ? '30' : '1'
      el.style.animation = 'none'
      el.style.animationDelay = '0s'
      el.style.transform = `translateY(${deltaY}px) ${isMajorMove ? 'scale(1.02)' : 'scale(1)'}`
      el.style.transition = 'none'

      // Force reflow so the browser registers the starting transform.
      void el.offsetHeight

      requestAnimationFrame(() => {
        el.style.transition = transitionString
        el.style.transform = 'translateY(0px) scale(1)'
      })

      const cleanup = () => {
        if (flipGeneration.get(el) !== gen) return
        el.style.transition = ''
        el.style.transform = ''
        el.style.animation = 'none'
        el.style.zIndex = ''
        el.style.position = ''
        el.style.filter = ''
        el.style.animationDelay = ''
        el.classList.remove('animate-spring-in')
        el.removeEventListener('transitionend', onTransitionEnd)
        clearTimeout(fallbackTimer)
      }
      const onTransitionEnd = (e: TransitionEvent) => {
        if (e.target !== el) return
        cleanup()
      }
      const fallbackTimer = window.setTimeout(cleanup, fallbackTimeout)
      el.addEventListener('transitionend', onTransitionEnd)
    })
  }

  return { capture, play, clear }
}
