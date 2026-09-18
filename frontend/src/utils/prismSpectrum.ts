/**
 * Prism Spectrum Engine — OKLCH color math for the continuous-hue skin (Seat 7).
 *
 * Two guardrails keep the free-form hue wheel safe:
 * - Perceptual uniformity: OKLCH fixes perceived lightness per role, so dragging
 *   the wheel never shifts apparent brightness (HSL's L is a coordinate, not
 *   perceived lightness — yellows flare while blues sink).
 * - MacAdam-aware dual scale (light mode): a fully saturated yellow is
 *   inherently high-luminance — darkening it turns it olive. So fills split by
 *   branch: naturally-deep hues (red/blue/violet/magenta) hold L≈0.51–0.54 with
 *   white text; naturally-bright hues (orange/yellow/green/cyan) hold
 *   L≈0.67–0.71 with dark ink text. Plateaus snap at branch boundaries so the
 *   fill never crosses the Y∈(0.18,0.22) dead zone where neither text clears 4.5:1.
 *
 * `prismSpectrum.ts` is the single source for the constants consumed by the
 * `[data-skin='prism']` blocks in styles/theme/skins.css — keep them in sync.
 */

export const PRISM_DARK = { L: 0.78, C: 0.17 } as const
export const PRISM_LIGHT_INK = { L: 0.46, C: 0.14 } as const
export const PRISM_LIGHT_FILL_C = 0.19

/**
 * Three curated chroma stops (晶艳 · 澄光 · 烟岚). Each tone scales the base
 * family chroma while reusing the same engineered lightness plateaus: WCAG
 * contrast is dominated by L so the guardrails hold, and lowering chroma can
 * never leave the sRGB gamut. `mist` bottoms out at ~0.45× to keep the hue
 * wheel legible — below C≈0.06 hues collapse into indistinguishable grays.
 */
export type PrismTone = 'vivid' | 'lucid' | 'mist'
export const PRISM_TONES = ['vivid', 'lucid', 'mist'] as const
export const DEFAULT_PRISM_TONE: PrismTone = 'vivid'
export const PRISM_TONE_CHROMA_SCALE: Record<PrismTone, number> = {
  vivid: 1,
  lucid: 0.7,
  mist: 0.45,
} as const

export function normalisePrismTone(tone: unknown): PrismTone {
  return tone === 'vivid' || tone === 'lucid' || tone === 'mist' ? tone : DEFAULT_PRISM_TONE
}

/** Effective per-family chroma after tone scaling. */
export function prismChroma(
  theme: 'light' | 'dark',
  tone: PrismTone,
): { ink: number; fill: number } {
  const s = PRISM_TONE_CHROMA_SCALE[tone]
  return theme === 'dark'
    ? { ink: PRISM_DARK.C * s, fill: PRISM_DARK.C * s }
    : { ink: PRISM_LIGHT_INK.C * s, fill: PRISM_LIGHT_FILL_C * s }
}

/** Saturation percent for the hsl() fallback declarations, scaled by tone. */
export const PRISM_HSL_S = 85
export function prismHslSaturation(tone: PrismTone): string {
  return `${Math.round(PRISM_HSL_S * PRISM_TONE_CHROMA_SCALE[tone])}%`
}

export const PRISM_BTN_TEXT_WHITE = '#ffffff'

/**
 * Deep chromatic ink (同系深墨色) for text on bright fills: keeps the accent's
 * hue family at near-black depth instead of neutral dead black. Chroma is NOT
 * scaled by tone — the softer the fill, the more the ink must stay visibly of
 * the hue family, like calligraphy ink on muted paper. L=0.26 (Y≈0.018) gives
 * the sRGB gamut enough room for reds/violets to show tint while holding
 * ~8:1+ contrast on every bright-branch fill. Chroma is gamut-mapped before
 * hex encoding, so the result is always a plain sRGB color usable anywhere.
 */
export const PRISM_INK_L = 0.26
export const PRISM_INK_C = 0.06

/** Gradient hue offset between --skin-accent-from and --skin-accent-to. */
export const PRISM_ACCENT_HUE_OFFSET = 35

/**
 * Light-mode fill lightness plateaus @10° (index = round(hue/10) % 36).
 * Deep-branch hues (reds/ambers 0–40°, blues/violets/magentas 250–350°) are
 * solved for Y≈0.14 → rich fills with white text. Bright-branch hues
 * (50–240°) ride each hue's chroma-peak lightness — the point where sRGB
 * allows maximum saturation — capped at Y≤0.50 so fills keep ~1.5:1 presence
 * on Platinum Slate instead of washing out. Nearest-step lookup: plateaus
 * snap at branch boundaries rather than interpolating through the
 * Y∈(0.18,0.22) dead zone where neither text clears 4.5:1.
 */
const FILL_L_TABLE = [
  0.519, 0.519, 0.519, 0.519, 0.537, 0.72, 0.755, 0.79, 0.799, 0.795, 0.791, 0.788, 0.783, 0.794,
  0.794, 0.794, 0.773, 0.775, 0.776, 0.778, 0.779, 0.781, 0.783, 0.76, 0.715, 0.52, 0.519, 0.519,
  0.519, 0.519, 0.519, 0.519, 0.519, 0.519, 0.519, 0.519,
] as const

function normaliseHue(hue: number): number {
  return ((hue % 360) + 360) % 360
}

/** OKLab → linear sRGB (Ottosson matrices). Channels may be out of gamut. */
function oklabToLinearSrgb(L: number, a: number, b: number): [number, number, number] {
  const l_ = L + 0.3963377774 * a + 0.2158037573 * b
  const m_ = L - 0.1055613458 * a - 0.0638541729 * b
  const s_ = L - 0.0894841775 * a - 1.291485548 * b
  const l = l_ ** 3
  const m = m_ ** 3
  const s = s_ ** 3
  return [
    +4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ]
}

function inSrgbGamut([r, g, b]: [number, number, number]): boolean {
  const e = 1e-4
  return r >= -e && r <= 1 + e && g >= -e && g <= 1 + e && b >= -e && b <= 1 + e
}

/** Largest chroma ≤C that lands inside the sRGB gamut at (L, hue). */
function srgbGamutMapChroma(L: number, C: number, hue: number): number {
  const h = normaliseHue(hue)
  const hr = (h * Math.PI) / 180
  let lo = 0
  let hi = C
  if (inSrgbGamut(oklabToLinearSrgb(L, hi * Math.cos(hr), hi * Math.sin(hr)))) {
    return C
  }
  for (let i = 0; i < 24; i++) {
    const mid = (lo + hi) / 2
    if (inSrgbGamut(oklabToLinearSrgb(L, mid * Math.cos(hr), mid * Math.sin(hr)))) {
      lo = mid
    } else {
      hi = mid
    }
  }
  return lo
}

/**
 * Relative luminance (WCAG Y) of an OKLCH color after sRGB gamut mapping.
 * Out-of-gamut chroma is reduced toward the gamut hull — approximating the
 * CSS Color 4 mapping — so Y tracks what the engine actually renders.
 */
export function oklchRelativeLuminance(L: number, C: number, hue: number): number {
  const h = normaliseHue(hue)
  const hr = (h * Math.PI) / 180
  const mapped = srgbGamutMapChroma(L, C, h)
  const [r, g, b] = oklabToLinearSrgb(L, mapped * Math.cos(hr), mapped * Math.sin(hr))
  return 0.2126729 * r + 0.7151522 * g + 0.072175 * b
}

function linearSrgbToByte(v: number): number {
  const c = Math.min(1, Math.max(0, v))
  return Math.round((c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055) * 255)
}

/**
 * The chromatic ink for a given hue, as a #rrggbb string. Uses the
 * accent gradient's mid-hue (+17.5°) so the ink centers between both ends.
 */
export function prismChromaticInk(hue: number): string {
  const h = normaliseHue(hue + PRISM_ACCENT_HUE_OFFSET / 2)
  const hr = (h * Math.PI) / 180
  const C = srgbGamutMapChroma(PRISM_INK_L, PRISM_INK_C, h)
  const [r, g, b] = oklabToLinearSrgb(PRISM_INK_L, C * Math.cos(hr), C * Math.sin(hr))
  return `#${[r, g, b].map(v => linearSrgbToByte(v).toString(16).padStart(2, '0')).join('')}`
}

/** WCAG relative luminance of a #rrggbb color. */
export function srgbRelativeLuminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16)
  const lin = (v: number) => {
    const s = (v & 255) / 255
    return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126729 * lin(n >> 16) + 0.7151522 * lin(n >> 8) + 0.072175 * lin(n)
}

export function wcagContrastRatio(y1: number, y2: number): number {
  return (Math.max(y1, y2) + 0.05) / (Math.min(y1, y2) + 0.05)
}

/**
 * Lightness fed to --prism-fill-l. Dark mode is a single perceptual ruler;
 * light mode looks up the branch-engineered plateau table.
 */
export function prismFillLightness(hue: number, theme: 'light' | 'dark'): number {
  if (theme === 'dark') return PRISM_DARK.L
  const i = Math.round(normaliseHue(hue) / 10) % 36
  return FILL_L_TABLE[i]
}

/**
 * Text color for elements sitting on the accent gradient (--neon-btn-text).
 * Picks the candidate with the best worst-end WCAG contrast across the
 * accent-from/accent-to gradient, so the choice survives the +35° hue span.
 * The dark candidate is a chromatic ink in the accent's hue family, not dead
 * black — on bright fills it reads as carved from the same stone.
 */
export function prismButtonText(
  hue: number,
  theme: 'light' | 'dark',
  tone: PrismTone = DEFAULT_PRISM_TONE,
): string {
  const L = prismFillLightness(hue, theme)
  const C = prismChroma(theme, tone).fill
  const y1 = oklchRelativeLuminance(L, C, hue)
  const y2 = oklchRelativeLuminance(L, C, hue + PRISM_ACCENT_HUE_OFFSET)
  const ink = prismChromaticInk(hue)
  const inkY = srgbRelativeLuminance(ink)
  const whiteY = srgbRelativeLuminance(PRISM_BTN_TEXT_WHITE)
  const inkMin = Math.min(wcagContrastRatio(inkY, y1), wcagContrastRatio(inkY, y2))
  const whiteMin = Math.min(wcagContrastRatio(whiteY, y1), wcagContrastRatio(whiteY, y2))
  return inkMin >= whiteMin ? ink : PRISM_BTN_TEXT_WHITE
}
