export const EXTRACTOR_SESSION_PREFIX = 'exs_'
export const EXTRACTOR_IGNORE_PREFIX = 'exi_'
export const EXTRACTOR_NOTIF_PREFIX = 'exn_'
export const EXTRACTOR_LEASE_MS = 5 * 60 * 1000
export const EXTRACTOR_ACK_LEASE_MS = EXTRACTOR_LEASE_MS - 30_000
export const EXTRACTOR_MAX_SESSION_ITEMS = 128
export const EXTRACTOR_PICKER_WINDOW = 80
export const EXTRACTOR_FOLDER_MAX_RUNES = 100
export const DISPLAY_FILENAME_MAX_CHARS = 200

function isUnsafeDisplayChar(code: number): boolean {
  if (code <= 31 || code === 127) return true
  if (code >= 0x80 && code <= 0x9f) return true
  if (code === 0x3c || code === 0x3e || code === 0x22 || code === 0x27 || code === 0x60) return true
  if (code === 0x200e || code === 0x200f) return true
  if (code >= 0x202a && code <= 0x202e) return true
  if (code >= 0x2066 && code <= 0x2069) return true
  return false
}

export function sanitizeDisplayFilename(raw: unknown): string | undefined {
  if (typeof raw !== 'string') return undefined
  let text = ''
  for (const ch of raw) {
    if (isUnsafeDisplayChar(ch.charCodeAt(0))) continue
    text += ch
  }
  text = text.trim()
  if (!text) return undefined
  if (text.length > DISPLAY_FILENAME_MAX_CHARS) text = text.slice(0, DISPLAY_FILENAME_MAX_CHARS)
  return text
}
