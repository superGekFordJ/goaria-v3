import { describe, expect, it } from 'vitest'
import { activeFromRoot, isTrapFocusable, restoreSelector, wrapTabIndex } from './pickerFocus'

describe('wrapTabIndex', () => {
  it('wraps five focusables forward and backward', () => {
    expect(wrapTabIndex(5, 4, false)).toBe(0)
    expect(wrapTabIndex(5, 0, true)).toBe(4)
    expect(wrapTabIndex(5, 2, false)).toBe(3)
    expect(wrapTabIndex(5, 2, true)).toBe(1)
  })

  it('is a no-op for empty or single focusable lists', () => {
    expect(wrapTabIndex(0, 0, false)).toBe(0)
    expect(wrapTabIndex(0, 3, true)).toBe(0)
    expect(wrapTabIndex(1, 0, false)).toBe(0)
    expect(wrapTabIndex(1, 0, true)).toBe(0)
  })
})

describe('restoreSelector', () => {
  it('points at the capsule action', () => {
    expect(restoreSelector).toBe('[data-extractor-capsule-action]')
  })
})

describe('isTrapFocusable', () => {
  it('excludes tabindex=-1 and aria-hidden nodes', () => {
    const attr = (attrs: Record<string, string>) => ({
      getAttribute(name: string) {
        return attrs[name] ?? null
      },
    })
    expect(isTrapFocusable(attr({}))).toBe(true)
    expect(isTrapFocusable(attr({ tabindex: '-1' }))).toBe(false)
    expect(isTrapFocusable(attr({ 'aria-hidden': 'true' }))).toBe(false)
  })
})

describe('activeFromRoot', () => {
  it('prefers the shadow root activeElement over the document fallback', () => {
    const inner = { id: 'checkbox' }
    const host = { id: 'host' }
    expect(activeFromRoot({ activeElement: inner }, host)).toBe(inner)
    expect(activeFromRoot(null, host)).toBe(host)
  })
})
