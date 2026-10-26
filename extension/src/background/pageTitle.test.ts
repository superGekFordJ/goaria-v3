import { describe, expect, it } from 'vitest'
import { PAGE_TITLE_MAX_BYTES, sanitizePageTitle } from './pageTitle'

describe('sanitizePageTitle', () => {
  it('passes a plain title through untouched', () => {
    expect(sanitizePageTitle('Live Show')).toBe('Live Show')
    expect(sanitizePageTitle('café €uro')).toBe('café €uro')
  })

  it('returns undefined for non-strings and titles that clean to nothing', () => {
    for (const raw of [undefined, null, 42, ['x'], {}]) {
      expect(sanitizePageTitle(raw)).toBeUndefined()
    }
    expect(sanitizePageTitle('')).toBeUndefined()
    expect(sanitizePageTitle('   ')).toBeUndefined()
    expect(sanitizePageTitle('\r\n\0')).toBeUndefined()
  })

  it('strips exactly the characters the backend rejects', () => {
    expect(sanitizePageTitle('a\nb')).toBe('ab')
    expect(sanitizePageTitle('x\ry')).toBe('xy')
    expect(sanitizePageTitle('p\0q')).toBe('pq')
    expect(sanitizePageTitle('\n  Foo\nBar  \n')).toBe('FooBar')
    // Interior characters the backend accepts (tab) survive.
    expect(sanitizePageTitle('a\tb')).toBe('a\tb')
  })

  it('truncates single-byte titles at the byte cap', () => {
    const out = sanitizePageTitle('x'.repeat(PAGE_TITLE_MAX_BYTES + 500))
    expect(out).toBe('x'.repeat(PAGE_TITLE_MAX_BYTES))
  })

  it('truncates on a character boundary instead of splitting multibyte UTF-8', () => {
    // 341 * 3-byte chars = 1023 bytes; the 342nd char would exceed the cap.
    const out = sanitizePageTitle('€'.repeat(600))
    expect(out).toBe('€'.repeat(341))
    expect(new TextEncoder().encode(out!).length).toBeLessThanOrEqual(PAGE_TITLE_MAX_BYTES)
    expect(out).not.toContain('�')
    // A char straddling the cap is dropped whole, not half-written.
    expect(sanitizePageTitle('a'.repeat(PAGE_TITLE_MAX_BYTES - 2) + '€')).toBe(
      'a'.repeat(PAGE_TITLE_MAX_BYTES - 2),
    )
  })
})
