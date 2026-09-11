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

export const PRISM_BTN_TEXT_DARK = '#0c0c0f'
export const PRISM_BTN_TEXT_LIGHT = '#16161b'
export const PRISM_BTN_TEXT_WHITE = '#ffffff'

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
  0.519, 0.519, 0.519, 0.519, 0.537, 0.72, 0.755, 0.79, 0.799, 0.795, 0.791,
  0.788, 0.783, 0.794, 0.794, 0.794, 0.773, 0.775, 0.776, 0.778, 0.779, 0.781,
  0.783, 0.76, 0.715, 0.52, 0.519, 0.519, 0.519, 0.519, 0.519, 0.519, 0.519,
  0.519, 0.519, 0.519,
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

/**
 * Relative luminance (WCAG Y) of an OKLCH color after sRGB gamut mapping.
 * Out-of-gamut chroma is reduced toward the gamut hull — approximating the
 * CSS Color 4 mapping — so Y tracks what the engine actually renders.
 */
export function oklchRelativeLuminance(L: number, C: number, hue: number): number {
  const h = normaliseHue(hue)
  const hr = (h * Math.PI) / 180
  let lo = 0
  let hi = C
  if (!inSrgbGamut(oklabToLinearSrgb(L, hi * Math.cos(hr), hi * Math.sin(hr)))) {
    for (let i = 0; i < 24; i++) {
      const mid = (lo + hi) / 2
      if (inSrgbGamut(oklabToLinearSrgb(L, mid * Math.cos(hr), mid * Math.sin(hr)))) {
        lo = mid
      } else {
        hi = mid
      }
    }
  }
  const [r, g, b] = oklabToLinearSrgb(L, lo * Math.cos(hr), lo * Math.sin(hr))
  return 0.2126729 * r + 0.7151522 * g + 0.072175 * b
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
 */
export function prismButtonText(hue: number, theme: 'light' | 'dark'): string {
  const L = prismFillLightness(hue, theme)
  const C = theme === 'dark' ? PRISM_DARK.C : PRISM_LIGHT_FILL_C
  const y1 = oklchRelativeLuminance(L, C, hue)
  const y2 = oklchRelativeLuminance(L, C, hue + PRISM_ACCENT_HUE_OFFSET)
  const ink = theme === 'dark' ? PRISM_BTN_TEXT_DARK : PRISM_BTN_TEXT_LIGHT
  const inkY = srgbRelativeLuminance(ink)
  const whiteY = srgbRelativeLuminance(PRISM_BTN_TEXT_WHITE)
  const inkMin = Math.min(wcagContrastRatio(inkY, y1), wcagContrastRatio(inkY, y2))
  const whiteMin = Math.min(wcagContrastRatio(whiteY, y1), wcagContrastRatio(whiteY, y2))
  return inkMin >= whiteMin ? ink : PRISM_BTN_TEXT_WHITE
}
