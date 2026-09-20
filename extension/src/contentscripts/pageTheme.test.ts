import { describe, it, expect } from 'vitest'
import { parseRgbLuminance } from './pageTheme'

describe('pageTheme', () => {
  describe('parseRgbLuminance', () => {
    it('calculates luminance for black', () => {
      expect(parseRgbLuminance('rgb(0, 0, 0)')).toBe(0)
    })

    it('calculates luminance for white', () => {
      expect(parseRgbLuminance('rgb(255, 255, 255)')).toBe(255)
    })

    it('identifies dark navy as dark luminance', () => {
      const lum = parseRgbLuminance('rgb(11, 19, 41)')
      expect(lum).not.toBeNull()
      expect(lum!).toBeLessThan(128)
    })

    it('identifies light gray as light luminance', () => {
      const lum = parseRgbLuminance('rgb(240, 240, 240)')
      expect(lum).not.toBeNull()
      expect(lum!).toBeGreaterThanOrEqual(128)
    })

    it('returns null for transparent rgba', () => {
      expect(parseRgbLuminance('rgba(0, 0, 0, 0)')).toBeNull()
      expect(parseRgbLuminance('rgba(255, 255, 255, 0.05)')).toBeNull()
    })

    it('handles non-color strings gracefully', () => {
      expect(parseRgbLuminance('transparent')).toBeNull()
      expect(parseRgbLuminance('')).toBeNull()
      expect(parseRgbLuminance('inherit')).toBeNull()
    })
  })
})
