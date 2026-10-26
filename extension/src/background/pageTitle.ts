// Page titles are page-controlled input: markup or `document.title = ...`
// can embed newlines or run past the wire limit, while the title is only a
// best-effort naming hint. Normalizing at collection keeps a hostile title
// from vetoing the handoff; the backend's strict check stays as
// defense-in-depth.
export const PAGE_TITLE_MAX_BYTES = 1024

function utf8ByteLength(codePoint: number): number {
  if (codePoint < 0x80) return 1
  if (codePoint < 0x800) return 2
  if (codePoint < 0x10000) return 3
  return 4
}

export function sanitizePageTitle(raw: unknown): string | undefined {
  if (typeof raw !== 'string') return undefined
  const cleaned = raw.replace(/[\r\n\0]/g, '').trim()
  if (!cleaned) return undefined
  let bytes = 0
  let out = ''
  // Iterate code points so a multi-byte character is never split at the cap.
  for (const ch of cleaned) {
    const need = utf8ByteLength(ch.codePointAt(0) ?? 0)
    if (bytes + need > PAGE_TITLE_MAX_BYTES) break
    out += ch
    bytes += need
  }
  return out || undefined
}
