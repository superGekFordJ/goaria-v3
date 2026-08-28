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

export function urlPathIsM3uPlaylist(url: string): boolean {
  try {
    const path = new URL(url).pathname.toLowerCase()
    return path.endsWith('.m3u8') || path.endsWith('.m3u')
  } catch {
    return false
  }
}
