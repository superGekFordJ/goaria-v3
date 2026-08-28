import { hasPartitionKey } from './browserCookies'
import { isSchemefulSameSite } from './domSameSite'

export const MAX_DIRECT_HEADER_LINE_BYTES = 4096
export const DOM_COOKIE_CONCURRENCY = 4
const MAX_COOKIES_PER_URL = 64

export type RawDomCookie = {
  name?: unknown
  value?: unknown
  secure?: unknown
  sameSite?: unknown
  partitionKey?: unknown
}

export type CookieGetAll = (details: { url: string; storeId: string }) => Promise<unknown[]>

function sameSiteToken(raw: unknown): string {
  if (typeof raw !== 'string') return 'unspecified'
  return raw.trim().toLowerCase().replace(/-/g, '_')
}

function includeBySameSite(
  cookie: RawDomCookie,
  sourceHref: string,
  targetHref: string,
): boolean {
  const token = sameSiteToken(cookie.sameSite)
  if (token === 'none' || token === 'no_restriction') {
    return cookie.secure === true && targetHref.toLowerCase().startsWith('https:')
  }
  return isSchemefulSameSite(sourceHref, targetHref)
}

function hasUnsafeCookieChars(raw: string): boolean {
  for (const ch of raw) {
    const code = ch.charCodeAt(0)
    if (code < 32 || code === 127 || ch === ';' || ch === ',') return true
  }
  return false
}

function cookiePair(cookie: RawDomCookie): { name: string; value: string } | undefined {
  if (typeof cookie.name !== 'string' || cookie.name === '') return undefined
  if (typeof cookie.value !== 'string') return undefined
  if (hasUnsafeCookieChars(cookie.name) || hasUnsafeCookieChars(cookie.value)) return undefined
  return { name: cookie.name, value: cookie.value }
}

function serializeCookieLine(pairs: Array<{ name: string; value: string }>): string | undefined {
  if (pairs.length === 0) return undefined
  const sorted = [...pairs].sort((a, b) => a.name.localeCompare(b.name))
  const line = `Cookie: ${sorted.map(p => `${p.name}=${p.value}`).join('; ')}`
  try {
    if (new TextEncoder().encode(line).length > MAX_DIRECT_HEADER_LINE_BYTES) return undefined
  } catch {
    return undefined
  }
  return line
}

export function cookieHeaderForItem(
  cookies: unknown[],
  sourceHref: string,
  targetHref: string,
  maxCookies: number = MAX_COOKIES_PER_URL,
): string | undefined {
  const pairs: Array<{ name: string; value: string }> = []
  for (const raw of cookies) {
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) continue
    const cookie = raw as RawDomCookie
    if (hasPartitionKey(cookie)) continue
    if (!includeBySameSite(cookie, sourceHref, targetHref)) continue
    const pair = cookiePair(cookie)
    if (!pair) continue
    pairs.push(pair)
  }
  return serializeCookieLine(pairs.slice(0, Math.max(0, maxCookies)))
}

async function getAllSafe(
  getAll: CookieGetAll,
  url: string,
  storeId: string,
): Promise<unknown[]> {
  try {
    return await getAll({ url, storeId })
  } catch {
    return []
  }
}

export async function collectCookieHeadersForUrls(opts: {
  urls: string[]
  sourceHref: string
  storeId: string | undefined
  storeUnproven: boolean
  getAll: CookieGetAll
  concurrency?: number
}): Promise<Array<string | undefined>> {
  if (opts.storeUnproven || typeof opts.storeId !== 'string' || opts.storeId.trim() === '') {
    return opts.urls.map(() => undefined)
  }
  const storeId = opts.storeId.trim()
  const limit = Math.max(1, opts.concurrency ?? DOM_COOKIE_CONCURRENCY)
  const out: Array<string | undefined> = new Array(opts.urls.length)
  let next = 0
  const worker = async (): Promise<void> => {
    while (next < opts.urls.length) {
      const i = next
      next += 1
      const url = opts.urls[i]
      if (!url) {
        out[i] = undefined
        continue
      }
      const cookies = await getAllSafe(opts.getAll, url, storeId)
      out[i] = cookieHeaderForItem(cookies, opts.sourceHref, url)
    }
  }
  const n = Math.min(limit, Math.max(1, opts.urls.length))
  await Promise.all(Array.from({ length: n }, () => worker()))
  return out
}
