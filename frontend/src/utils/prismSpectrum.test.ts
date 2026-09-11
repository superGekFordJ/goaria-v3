import { describe, expect, it } from 'vitest'
import {
  oklchRelativeLuminance,
  prismButtonText,
  prismFillLightness,
  srgbRelativeLuminance,
  wcagContrastRatio,
  PRISM_ACCENT_HUE_OFFSET,
  PRISM_BTN_TEXT_DARK,
  PRISM_BTN_TEXT_LIGHT,
  PRISM_BTN_TEXT_WHITE,
  PRISM_DARK,
  PRISM_LIGHT_FILL_C,
  PRISM_LIGHT_INK,
} from './prismSpectrum'

const SLATE_LIGHT = srgbRelativeLuminance('#e2e8f0') // Platinum Slate surface
const OBSIDIAN_DARK = srgbRelativeLuminance('#15151b') // dark card over app bg

function fillEndYs(hue: number, theme: 'light' | 'dark') {
  const L = prismFillLightness(hue, theme)
  const C = theme === 'dark' ? PRISM_DARK.C : PRISM_LIGHT_FILL_C
  return [
    oklchRelativeLuminance(L, C, hue),
    oklchRelativeLuminance(L, C, hue + PRISM_ACCENT_HUE_OFFSET),
  ]
}

describe('prismSpectrum color math', () => {
  it('maps achromatic OKLCH endpoints to luminance 0 and 1', () => {
    expect(oklchRelativeLuminance(0, 0, 0)).toBeCloseTo(0, 5)
    expect(oklchRelativeLuminance(1, 0, 200)).toBeCloseTo(1, 2)
  })

  it('keeps srgbRelativeLuminance on known anchors', () => {
    expect(srgbRelativeLuminance('#000000')).toBeCloseTo(0, 5)
    expect(srgbRelativeLuminance('#ffffff')).toBeCloseTo(1, 5)
  })

  it('wcagContrastRatio is symmetric and bounded below by 1', () => {
    expect(wcagContrastRatio(0.5, 0.5)).toBeCloseTo(1, 5)
    expect(wcagContrastRatio(1, 0)).toBeCloseTo(21, 0)
  })
})

describe('prismFillLightness', () => {
  it('returns the uniform dark-mode ruler', () => {
    for (let h = 0; h < 360; h += 17) {
      expect(prismFillLightness(h, 'dark')).toBe(PRISM_DARK.L)
    }
  })

  it('keeps light-mode fills on the two engineered plateaus', () => {
    for (let h = 0; h < 360; h++) {
      const L = prismFillLightness(h, 'light')
      expect(L).toBeGreaterThanOrEqual(0.5)
      expect(L).toBeLessThanOrEqual(0.82)
    }
    // both branches are exercised: deep hues stay dark, bright hues stay light
    expect(prismFillLightness(265, 'light')).toBeLessThan(0.56) // blue → deep branch
    expect(prismFillLightness(95, 'light')).toBeGreaterThan(0.7) // yellow → bright branch
  })
})

describe('prismButtonText', () => {
  it('returns white on the deep branch and dark ink on the bright branch', () => {
    expect(prismButtonText(265, 'light')).toBe(PRISM_BTN_TEXT_WHITE) // deep blue
    expect(prismButtonText(95, 'light')).toBe(PRISM_BTN_TEXT_LIGHT) // golden yellow
    expect(prismButtonText(280, 'dark')).toBe(PRISM_BTN_TEXT_DARK)
  })
})

describe('guardrail invariants — full hue wheel sweep', () => {
  it('light ink tokens keep ≥4.5:1 text contrast on Platinum Slate', () => {
    for (let h = 0; h < 360; h++) {
      const y = oklchRelativeLuminance(PRISM_LIGHT_INK.L, PRISM_LIGHT_INK.C, h)
      expect(wcagContrastRatio(y, SLATE_LIGHT)).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('light button text keeps ≥4.5:1 on both gradient ends', () => {
    for (let h = 0; h < 360; h++) {
      const text = prismButtonText(h, 'light')
      const textY = srgbRelativeLuminance(text)
      for (const endY of fillEndYs(h, 'light')) {
        expect(wcagContrastRatio(textY, endY)).toBeGreaterThanOrEqual(4.5)
      }
    }
  })

  it('dark primary keeps ≥4.5:1 as text on Smoked Obsidian', () => {
    for (let h = 0; h < 360; h++) {
      const y = oklchRelativeLuminance(PRISM_DARK.L, PRISM_DARK.C, h)
      expect(wcagContrastRatio(y, OBSIDIAN_DARK)).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('dark button text keeps ≥4.5:1 on both gradient ends', () => {
    for (let h = 0; h < 360; h++) {
      const text = prismButtonText(h, 'dark')
      const textY = srgbRelativeLuminance(text)
      for (const endY of fillEndYs(h, 'dark')) {
        expect(wcagContrastRatio(textY, endY)).toBeGreaterThanOrEqual(4.5)
      }
    }
  })
})
