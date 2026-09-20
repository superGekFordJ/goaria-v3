/**
 * Helper to detect whether the host webpage is dark or light.
 * Prioritizes computed background luminance, then explicit theme markers,
 * and falls back to system prefers-color-scheme.
 */

export function parseRgbLuminance(colorStr: string): number | null {
  if (!colorStr) return null
  const match = colorStr.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)(?:,\s*([\d.]+))?\)/i)
  if (!match) return null
  const r = Number(match[1])
  const g = Number(match[2])
  const b = Number(match[3])
  const a = match[4] !== undefined ? Number(match[4]) : 1
  if (a < 0.1) return null // Transparent or near-transparent
  return 0.299 * r + 0.587 * g + 0.114 * b
}

export function detectPageDarkness(): boolean {
  if (typeof document === 'undefined') return true

  // 1. Explicit theme markers on documentElement or body
  const docEl = document.documentElement
  if (docEl) {
    const dataTheme = docEl.getAttribute('data-theme') || docEl.getAttribute('data-color-mode')
    if (dataTheme === 'dark') return true
    if (dataTheme === 'light') return false
    if (docEl.classList.contains('dark') || docEl.classList.contains('theme-dark')) return true
    if (docEl.classList.contains('light') || docEl.classList.contains('theme-light')) return false
  }

  // 2. Computed background color of body / html
  if (typeof window !== 'undefined' && typeof window.getComputedStyle === 'function') {
    try {
      if (document.body) {
        const lum = parseRgbLuminance(window.getComputedStyle(document.body).backgroundColor)
        if (lum !== null) return lum < 128
      }
      if (docEl) {
        const lum = parseRgbLuminance(window.getComputedStyle(docEl).backgroundColor)
        if (lum !== null) return lum < 128
      }
    } catch {
      // Fallback on security/context errors
    }
  }

  // 3. Fallback to browser/OS preference
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    return window.matchMedia('(prefers-color-scheme: dark)').matches
  }

  return true
}
