export const MAX_DIRECT_URL_BYTES = 4096
export const MAX_DIRECT_DOWNLOAD_PAGE_BYTES = 2048

function hasCRLFOrNUL(raw: string): boolean {
  return raw.includes('\r') || raw.includes('\n') || raw.includes('\0')
}

function hasSpaceOrControl(raw: string): boolean {
  for (const ch of raw) {
    const code = ch.codePointAt(0) ?? 0
    if (code <= 32 || code === 127) return true
    if (code >= 0x80 && code <= 0x9f) return true
    if (/\s/u.test(ch)) return true
  }
  return false
}

function stripFragment(raw: string): string {
  const hash = raw.indexOf('#')
  return hash === -1 ? raw : raw.slice(0, hash)
}

function pathAndQueryAfterHost(noHash: string): { path: string; query?: string } | undefined {
  const sep = noHash.indexOf('://')
  if (sep < 0) return undefined
  const after = noHash.slice(sep + 3)
  const slash = after.indexOf('/')
  const qmark = after.indexOf('?')
  if (slash < 0 && qmark < 0) return { path: '' }
  if (qmark >= 0 && (slash < 0 || qmark < slash)) {
    return { path: '', query: after.slice(qmark + 1) }
  }
  const rest = after.slice(slash)
  const qi = rest.indexOf('?')
  if (qi < 0) return { path: rest }
  return { path: rest.slice(0, qi), query: rest.slice(qi + 1) }
}

/** Align with Go canonicalizeDirectURL: http(s), no userinfo, strip fragment, keep raw query. */
export function canonicalizeDirectURL(
  raw: string,
  maxBytes: number = MAX_DIRECT_URL_BYTES,
): string | undefined {
  if (typeof raw !== 'string' || raw === '' || raw.length > maxBytes) return undefined
  if (hasCRLFOrNUL(raw) || hasSpaceOrControl(raw)) return undefined
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return undefined
  }
  const scheme = parsed.protocol.replace(/:$/, '').toLowerCase()
  if (scheme !== 'http' && scheme !== 'https') return undefined
  if (parsed.username !== '' || parsed.password !== '') return undefined
  const host = parsed.host.toLowerCase()
  if (host === '') return undefined
  const parts = pathAndQueryAfterHost(stripFragment(raw))
  if (!parts) return undefined
  let out = `${scheme}://${host}${parts.path}`
  if (parts.query !== undefined) out += `?${parts.query}`
  if (out.length > maxBytes) return undefined
  return out
}

// Suffix check aligned with Go isHLSManifestURL: http(s) only, last path
// suffix, case-insensitive, ignores query/fragment. Percent-escapes are
// decoded when safe so ".m3u%38" matches like the backend; malformed escapes
// fall back to the raw path.
function urlPathHasSuffix(raw: string, suffix: string): boolean {
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return false
  }
  const scheme = parsed.protocol.replace(/:$/, '').toLowerCase()
  if (scheme !== 'http' && scheme !== 'https') return false
  if (parsed.host === '') return false
  let path = parsed.pathname
  try {
    path = decodeURIComponent(path)
  } catch {
    // keep the raw path — a malformed escape simply cannot gain a suffix
  }
  return path.toLowerCase().endsWith(suffix)
}

/** HLS manifest: HTTP(S) URL path ending in .m3u8 (case-insensitive). */
export function urlPathIsM3u8Playlist(url: string): boolean {
  return urlPathHasSuffix(url, '.m3u8')
}

/** Ordinary playlist: HTTP(S) URL path ending in .m3u (not HLS). */
export function urlPathIsM3uPlaylist(url: string): boolean {
  return urlPathHasSuffix(url, '.m3u')
}
