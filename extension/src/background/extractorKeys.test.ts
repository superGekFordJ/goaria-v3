import { describe, expect, it } from 'vitest'
import { sanitizeDisplayFilename } from './extractorKeys'

describe('sanitizeDisplayFilename', () => {
  it('strips C0 controls and caps length', () => {
    expect(sanitizeDisplayFilename('clip.bin')).toBe('clip.bin')
    expect(sanitizeDisplayFilename('a\r\nb')).toBe('ab')
    expect(sanitizeDisplayFilename('x'.repeat(250))?.length).toBe(200)
    expect(sanitizeDisplayFilename('   ')).toBeUndefined()
  })

  it('strips C1 controls and bidi format characters', () => {
    expect(sanitizeDisplayFilename(`clip\u0081.bin`)).toBe('clip.bin')
    expect(sanitizeDisplayFilename(`a\u202Eexe.txt`)).toBe('aexe.txt')
    expect(sanitizeDisplayFilename(`a\u2066b\u2069c`)).toBe('abc')
    expect(sanitizeDisplayFilename(`a\u200Fb`)).toBe('ab')
  })

  it('strips HTML metacharacters from display names', () => {
    expect(sanitizeDisplayFilename(`<img src=x>.bin`)).toBe('img src=x.bin')
    expect(sanitizeDisplayFilename(`a"b'c\`d`)).toBe('abcd')
  })
})
