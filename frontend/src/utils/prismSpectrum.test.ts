import { describe, expect, it } from 'vitest'
import {
  normalisePrismTone,
  oklchRelativeLuminance,
  prismButtonText,
  prismChroma,
  prismChromaticInk,
  prismFillLightness,
  prismHslSaturation,
  srgbRelativeLuminance,
  wcagContrastRatio,
  PRISM_ACCENT_HUE_OFFSET,
  PRISM_BTN_TEXT_WHITE,
  PRISM_DARK,
  PRISM_LIGHT_INK,
  PRISM_TONES,
  type PrismTone,
} from './prismSpectrum'

const SLATE_LIGHT = srgbRelativeLuminance('#e2e8f0') // Platinum Slate surface
const OBSIDIAN_DARK = srgbRelativeLuminance('#15151b') // dark card over app bg

function fillEndYs(hue: number, theme: 'light' | 'dark', tone: PrismTone) {
  const L = prismFillLightness(hue, theme)
  const C = prismChroma(theme, tone).fill
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

describe('prismTone chroma stops', () => {
  it('orders chroma vivid > lucid > mist for both families and themes', () => {
    for (const theme of ['light', 'dark'] as const) {
      const v = prismChroma(theme, 'vivid')
      const l = prismChroma(theme, 'lucid')
      const m = prismChroma(theme, 'mist')
      expect(v.ink).toBeGreaterThan(l.ink)
      expect(l.ink).toBeGreaterThan(m.ink)
      expect(v.fill).toBeGreaterThan(l.fill)
      expect(l.fill).toBeGreaterThan(m.fill)
    }
    // mist keeps hues legible — never collapses toward the gray dead zone
    expect(prismChroma('light', 'mist').fill).toBeGreaterThan(0.06)
  })

  it('normalisePrismTone falls back to vivid on unknown values', () => {
    expect(normalisePrismTone('vivid')).toBe('vivid')
    expect(normalisePrismTone('lucid')).toBe('lucid')
    expect(normalisePrismTone('mist')).toBe('mist')
    expect(normalisePrismTone('neon')).toBe('vivid')
    expect(normalisePrismTone(undefined)).toBe('vivid')
    expect(normalisePrismTone(42)).toBe('vivid')
  })

  it('scales the hsl fallback saturation with tone', () => {
    expect(prismHslSaturation('vivid')).toBe('85%')
    expect(prismHslSaturation('lucid')).toBe('59%')
    expect(prismHslSaturation('mist')).toBe('38%')
  })
})

describe('prismChromaticInk', () => {
  it('stays deep enough for strong contrast on bright fills', () => {
    for (let h = 0; h < 360; h += 15) {
      expect(srgbRelativeLuminance(prismChromaticInk(h))).toBeLessThan(0.04)
    }
  })

  it('tints toward the accent hue family instead of neutral black', () => {
    const inks = new Set([0, 30, 60, 90, 120, 150, 180, 210, 240, 270, 300, 330].map(prismChromaticInk))
    // every hue family yields a distinct ink — not a shared near-black
    expect(inks.size).toBe(12)
    expect(prismChromaticInk(0)).toMatch(/^#[0-9a-f]{6}$/)
  })
})

describe('prismButtonText', () => {
  it('returns white on the deep branch and chromatic ink on the bright branch', () => {
    for (const tone of PRISM_TONES) {
      expect(prismButtonText(265, 'light', tone)).toBe(PRISM_BTN_TEXT_WHITE) // deep blue
      expect(prismButtonText(95, 'light', tone)).toBe(prismChromaticInk(95)) // golden yellow
      expect(prismButtonText(280, 'dark', tone)).toBe(prismChromaticInk(280))
    }
  })
})

describe('guardrail invariants — full hue wheel sweep, all tones', () => {
  it('light ink tokens keep ≥4.5:1 text contrast on Platinum Slate', () => {
    for (const tone of PRISM_TONES) {
      const C = prismChroma('light', tone).ink
      for (let h = 0; h < 360; h++) {
        const y = oklchRelativeLuminance(PRISM_LIGHT_INK.L, C, h)
        expect(wcagContrastRatio(y, SLATE_LIGHT)).toBeGreaterThanOrEqual(4.5)
      }
    }
  })

  it('light button text keeps ≥4.5:1 on both gradient ends', () => {
    for (const tone of PRISM_TONES) {
      for (let h = 0; h < 360; h++) {
        const text = prismButtonText(h, 'light', tone)
        const textY = srgbRelativeLuminance(text)
        for (const endY of fillEndYs(h, 'light', tone)) {
          expect(wcagContrastRatio(textY, endY)).toBeGreaterThanOrEqual(4.5)
        }
      }
    }
  })

  it('dark primary keeps ≥4.5:1 as text on Smoked Obsidian', () => {
    for (const tone of PRISM_TONES) {
      const C = prismChroma('dark', tone).ink
      for (let h = 0; h < 360; h++) {
        const y = oklchRelativeLuminance(PRISM_DARK.L, C, h)
        expect(wcagContrastRatio(y, OBSIDIAN_DARK)).toBeGreaterThanOrEqual(4.5)
      }
    }
  })

  it('dark button text keeps ≥4.5:1 on both gradient ends', () => {
    for (const tone of PRISM_TONES) {
      for (let h = 0; h < 360; h++) {
        const text = prismButtonText(h, 'dark', tone)
        const textY = srgbRelativeLuminance(text)
        for (const endY of fillEndYs(h, 'dark', tone)) {
          expect(wcagContrastRatio(textY, endY)).toBeGreaterThanOrEqual(4.5)
        }
      }
    }
  })
})
