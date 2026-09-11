// Bounded in-memory Browser Header Grant model. Values captured here are
// ephemeral authorization objects: memory-only, never logged, never sent to
// storage, Content Script, or Popup. Keep this module free of
// webextension-polyfill, svelte stores, and wsClient so it stays pure.
import { parseHTTPURLHost } from './canonicalHost'

export const HEADER_GRANT_MAX_CANDIDATES = 16
export const HEADER_GRANT_OBSERVE_WINDOW_MS = 60_000
export const HEADER_GRANT_TTL_MS = 60_000
export const HEADER_GRANT_MAX_PER_RESOLVE = 8
export const HEADER_GRANT_MAX_HEADERS = 8
export const HEADER_GRANT_MAX_NAME_BYTES = 128
export const HEADER_GRANT_MAX_VALUE_BYTES = 4096
export const HEADER_GRANT_MAX_AGGREGATE_VALUE_BYTES = 8192
export const HEADER_GRANT_MAX_SOURCE_ORIGIN_BYTES = 512
export const HEADER_GRANT_MAX_TARGET_URL_BYTES = 4096

export type WireBrowserHeader = {
  name: string
  value: string
}

// Wire DTO — must stay string-equal with internal/extension/protocol.go.
export type WireBrowserHeaderGrant = {
  source_origin: string
  target_url: string
  method: 'GET' | 'HEAD'
  captured_at_unix_ms: number
  expires_at_unix_ms: number
  headers: WireBrowserHeader[]
}

function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).length
}

// canonicalSourceOrigin returns the canonical http(s) origin for a page URL or
// bare origin: lower-cased host, default port dropped, no path/query/fragment.
export function canonicalSourceOrigin(raw: string): string | undefined {
  if (typeof raw !== 'string' || raw === '') return undefined
  if (parseHTTPURLHost(raw) === undefined) return undefined
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return undefined
  }
  const origin = parsed.origin
  if (origin === 'null' || origin === '') return undefined
  if (utf8Bytes(origin) > HEADER_GRANT_MAX_SOURCE_ORIGIN_BYTES) return undefined
  return origin
}

// canonicalGrantTargetUrl returns the canonical observed HTTPS target: exact
// origin + escaped path + query. Fragments and userinfo are rejected outright.
export function canonicalGrantTargetUrl(raw: string): string | undefined {
  if (typeof raw !== 'string' || raw === '') return undefined
  if (utf8Bytes(raw) > HEADER_GRANT_MAX_TARGET_URL_BYTES) return undefined
  if (raw.includes('#')) return undefined
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return undefined
  }
  if (parsed.protocol !== 'https:') return undefined
  if (parsed.username !== '' || parsed.password !== '') return undefined
  if (parsed.hostname === '') return undefined
  const out = parsed.origin + parsed.pathname + parsed.search
  if (utf8Bytes(out) > HEADER_GRANT_MAX_TARGET_URL_BYTES) return undefined
  return out
}

const HEADER_NAME_TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/

const DENIED_X_EXACT = new Set([
  'x-real-ip',
  'x-client-ip',
  'x-host',
  'x-original-url',
  'x-rewrite-url',
  'x-method-override',
])

const DENIED_X_PREFIXES = ['x-forwarded-', 'x-http-method', 'x-proxy-', 'x-goaria-']

// isEligibleHeaderName is the capture allowlist: authorization plus business
// x-* names, minus routing/proxy/method-override/reserved families. Denied
// names are simply not eligible — they are skipped, never copied.
function isEligibleHeaderName(lowerName: string): boolean {
  if (lowerName === 'authorization') return true
  if (!lowerName.startsWith('x-')) return false
  if (!HEADER_NAME_TOKEN.test(lowerName)) return false
  if (DENIED_X_EXACT.has(lowerName)) return false
  for (const prefix of DENIED_X_PREFIXES) {
    if (lowerName.startsWith(prefix)) return false
  }
  return true
}

function isValidGrantHeaderValue(value: string): boolean {
  if (value === '' || value !== value.trim()) return false
  if (value.includes('\r') || value.includes('\n') || value.includes('\0')) return false
  return utf8Bytes(value) <= HEADER_GRANT_MAX_VALUE_BYTES
}

type ObservedHeaderItem = {
  name?: unknown
  value?: unknown
  binaryValue?: unknown
}

// projectObservedHeaders folds raw webRequest headers into the grant header
// list. A malformed eligible header rejects the whole observation; a request
// with no eligible headers produces 'none' (no grant, not an error).
function projectObservedHeaders(
  items: readonly unknown[] | undefined,
): { headers: WireBrowserHeader[] } | 'reject' | 'none' {
  if (!Array.isArray(items)) return 'none'
  const out: WireBrowserHeader[] = []
  const seen = new Set<string>()
  let aggregate = 0
  for (const raw of items) {
    if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) continue
    const item = raw as ObservedHeaderItem
    if (typeof item.name !== 'string') continue
    const name = item.name.toLowerCase()
    if (!isEligibleHeaderName(name)) continue
    if (utf8Bytes(name) > HEADER_GRANT_MAX_NAME_BYTES) return 'reject'
    if (seen.has(name)) return 'reject'
    if (typeof item.value !== 'string' || !isValidGrantHeaderValue(item.value)) return 'reject'
    aggregate += utf8Bytes(item.value)
    if (aggregate > HEADER_GRANT_MAX_AGGREGATE_VALUE_BYTES) return 'reject'
    seen.add(name)
    out.push({ name, value: item.value })
  }
  if (out.length === 0) return 'none'
  if (out.length > HEADER_GRANT_MAX_HEADERS) return 'reject'
  out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  return { headers: out }
}

function isSafePositiveInt(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

// projectBrowserHeaderGrants re-validates a caller-supplied grant list for the
// outbound resolve allowlist. One malformed item rejects the whole list.
export function projectBrowserHeaderGrants(
  value: unknown,
): WireBrowserHeaderGrant[] | undefined {
  if (!Array.isArray(value) || value.length === 0 || value.length > HEADER_GRANT_MAX_PER_RESOLVE) {
    return undefined
  }
  const out: WireBrowserHeaderGrant[] = []
  const scopes = new Set<string>()
  for (const item of value) {
    const grant = projectBrowserHeaderGrant(item)
    if (!grant) return undefined
    const scope = `${grant.method} ${grant.target_url}`
    if (scopes.has(scope)) return undefined
    scopes.add(scope)
    out.push(grant)
  }
  return out
}

const GRANT_KEYS = ['captured_at_unix_ms', 'expires_at_unix_ms', 'headers', 'method', 'source_origin', 'target_url']

function projectBrowserHeaderGrant(value: unknown): WireBrowserHeaderGrant | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return undefined
  const rec = value as Record<string, unknown>
  const keys = Object.keys(rec).sort()
  if (keys.length !== GRANT_KEYS.length || !GRANT_KEYS.every((key, i) => keys[i] === key)) {
    return undefined
  }
  if (typeof rec.source_origin !== 'string') return undefined
  const sourceOrigin = canonicalSourceOrigin(rec.source_origin)
  if (sourceOrigin === undefined || sourceOrigin !== rec.source_origin) return undefined
  if (typeof rec.target_url !== 'string') return undefined
  const targetUrl = canonicalGrantTargetUrl(rec.target_url)
  if (targetUrl === undefined || targetUrl !== rec.target_url) return undefined
  if (rec.method !== 'GET' && rec.method !== 'HEAD') return undefined
  if (!isSafePositiveInt(rec.captured_at_unix_ms) || !isSafePositiveInt(rec.expires_at_unix_ms)) {
    return undefined
  }
  const captured = rec.captured_at_unix_ms
  const expires = rec.expires_at_unix_ms
  if (expires <= captured || expires - captured > HEADER_GRANT_TTL_MS) return undefined
  const headers = projectWireHeaders(rec.headers)
  if (headers === undefined) return undefined
  return {
    source_origin: sourceOrigin,
    target_url: targetUrl,
    method: rec.method,
    captured_at_unix_ms: captured,
    expires_at_unix_ms: expires,
    headers,
  }
}

function projectWireHeaders(value: unknown): WireBrowserHeader[] | undefined {
  if (!Array.isArray(value) || value.length === 0 || value.length > HEADER_GRANT_MAX_HEADERS) {
    return undefined
  }
  const out: WireBrowserHeader[] = []
  const seen = new Set<string>()
  let aggregate = 0
  for (const raw of value) {
    if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) return undefined
    const keys = Object.keys(raw as Record<string, unknown>)
    if (keys.length !== 2) return undefined
    const rec = raw as Record<string, unknown>
    if (typeof rec.name !== 'string' || typeof rec.value !== 'string') return undefined
    const name = rec.name
    if (name !== name.toLowerCase() || !isEligibleHeaderName(name)) return undefined
    if (utf8Bytes(name) > HEADER_GRANT_MAX_NAME_BYTES) return undefined
    if (seen.has(name)) return undefined
    if (!isValidGrantHeaderValue(rec.value)) return undefined
    aggregate += utf8Bytes(rec.value)
    if (aggregate > HEADER_GRANT_MAX_AGGREGATE_VALUE_BYTES) return undefined
    seen.add(name)
    out.push({ name, value: rec.value })
  }
  out.sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  return out
}

type GrantCandidate = {
  pageToken: string
  sourceOrigin: string
  generation: number
  incognito: boolean
  cookieStoreId: string | undefined
  expiresAt: number
  grants: Map<string, WireBrowserHeaderGrant>
}

export type HeaderGrantStoreDeps = {
  isFirefoxTarget: () => boolean
  isGenerationCurrent: (generation: number) => boolean
  now?: () => number
}

export type ArmHeaderCandidateInput = {
  tabId: number
  pageToken: string
  sourceUrl: string
  generation: number
  incognito: unknown
  cookieStoreId?: unknown
}

export type HeaderObserveInput = {
  tabId: number
  type: unknown
  method: unknown
  url: unknown
  headers?: readonly unknown[]
  initiator?: unknown
  originUrl?: unknown
  documentUrl?: unknown
  incognito?: unknown
  cookieStoreId?: unknown
}

export type TakeHeaderGrantsInput = {
  tabId: number
  pageToken: string
  incognito?: unknown
  cookieStoreId?: unknown
}

export function createHeaderGrantStore(deps: HeaderGrantStoreDeps) {
  const now = deps.now ?? (() => Date.now())
  const candidates = new Map<number, GrantCandidate>()

  function candidateLive(candidate: GrantCandidate): boolean {
    return now() < candidate.expiresAt && deps.isGenerationCurrent(candidate.generation)
  }

  function evictDead(): void {
    for (const [tabId, candidate] of candidates) {
      if (!candidateLive(candidate)) candidates.delete(tabId)
    }
  }

  function arm(input: ArmHeaderCandidateInput): boolean {
    evictDead()
    if (!Number.isInteger(input.tabId) || input.tabId < 0) return false
    if (typeof input.pageToken !== 'string' || input.pageToken === '') return false
    if (!Number.isInteger(input.generation) || !deps.isGenerationCurrent(input.generation)) {
      return false
    }
    if (typeof input.incognito !== 'boolean') return false
    const sourceOrigin = canonicalSourceOrigin(input.sourceUrl)
    if (sourceOrigin === undefined) return false
    let cookieStoreId: string | undefined
    if (input.cookieStoreId !== undefined) {
      if (typeof input.cookieStoreId !== 'string' || input.cookieStoreId.trim() === '') {
        return false
      }
      cookieStoreId = input.cookieStoreId
    }
    candidates.delete(input.tabId)
    if (candidates.size >= HEADER_GRANT_MAX_CANDIDATES) {
      const oldest = candidates.keys().next().value
      if (oldest !== undefined) candidates.delete(oldest)
    }
    candidates.set(input.tabId, {
      pageToken: input.pageToken,
      sourceOrigin,
      generation: input.generation,
      incognito: input.incognito,
      cookieStoreId,
      expiresAt: now() + HEADER_GRANT_OBSERVE_WINDOW_MS,
      grants: new Map(),
    })
    return true
  }

  // resolveSourceOrigin folds the browser-provided source signals into the
  // candidate origin. Any present-but-unusable signal or a disagreement
  // between usable fields fails closed.
  function resolveSourceOrigin(candidate: GrantCandidate, event: HeaderObserveInput): boolean {
    if (deps.isFirefoxTarget()) {
      const origins: string[] = []
      for (const raw of [event.originUrl, event.documentUrl]) {
        if (typeof raw !== 'string' || raw === '') continue
        const origin = canonicalSourceOrigin(raw)
        if (origin === undefined) return false
        origins.push(origin)
      }
      if (origins.length === 0) return false
      return origins.every(origin => origin === candidate.sourceOrigin)
    }
    if (typeof event.initiator !== 'string' || event.initiator === '') return false
    return canonicalSourceOrigin(event.initiator) === candidate.sourceOrigin
  }

  function observe(event: HeaderObserveInput): boolean {
    evictDead()
    if (!Number.isInteger(event.tabId) || event.tabId < 0) return false
    const candidate = candidates.get(event.tabId)
    if (!candidate) return false
    if (!candidateLive(candidate)) {
      candidates.delete(event.tabId)
      return false
    }
    if (event.type !== 'xmlhttprequest') return false
    if (event.method !== 'GET' && event.method !== 'HEAD') return false
    if (typeof event.url !== 'string') return false
    const targetUrl = canonicalGrantTargetUrl(event.url)
    if (targetUrl === undefined) return false
    if (!resolveSourceOrigin(candidate, event)) return false
    // `incognito` is a Firefox detail field. Chrome split-mode contexts never
    // see the other profile's events, so a missing field is treated as
    // non-incognito only there; Firefox requires the field to be present.
    const eventIncognito =
      typeof event.incognito === 'boolean'
        ? event.incognito
        : deps.isFirefoxTarget()
          ? undefined
          : false
    if (eventIncognito === undefined || eventIncognito !== candidate.incognito) return false
    if (deps.isFirefoxTarget()) {
      if (typeof candidate.cookieStoreId !== 'string' || candidate.cookieStoreId === '') {
        return false
      }
      if (event.cookieStoreId !== candidate.cookieStoreId) return false
    }
    const projected = projectObservedHeaders(event.headers)
    if (projected === 'none' || projected === 'reject') return false
    const captured = now()
    const key = `${event.method} ${targetUrl}`
    // delete+set moves the refreshed scope to the end so the oldest distinct
    // scope is evicted first when the cap is exceeded.
    candidate.grants.delete(key)
    candidate.grants.set(key, {
      source_origin: candidate.sourceOrigin,
      target_url: targetUrl,
      method: event.method,
      captured_at_unix_ms: captured,
      expires_at_unix_ms: captured + HEADER_GRANT_TTL_MS,
      headers: projected.headers,
    })
    while (candidate.grants.size > HEADER_GRANT_MAX_PER_RESOLVE) {
      const oldest = candidate.grants.keys().next().value
      if (oldest === undefined) break
      candidate.grants.delete(oldest)
    }
    return true
  }

  // take consumes the candidate's live grants for one resolve. Association
  // mismatches and expired candidates drop the whole tab state.
  function take(input: TakeHeaderGrantsInput): WireBrowserHeaderGrant[] {
    evictDead()
    const empty: WireBrowserHeaderGrant[] = []
    if (!Number.isInteger(input.tabId) || input.tabId < 0) return empty
    const candidate = candidates.get(input.tabId)
    if (!candidate) return empty
    const drop = (): WireBrowserHeaderGrant[] => {
      candidates.delete(input.tabId)
      return empty
    }
    if (input.pageToken !== candidate.pageToken) return drop()
    if (input.incognito !== candidate.incognito) return drop()
    if (deps.isFirefoxTarget()) {
      if (typeof candidate.cookieStoreId !== 'string' || candidate.cookieStoreId === '') {
        return drop()
      }
      if (input.cookieStoreId !== candidate.cookieStoreId) return drop()
    }
    const t = now()
    const taken: WireBrowserHeaderGrant[] = []
    for (const [key, grant] of candidate.grants) {
      candidate.grants.delete(key)
      if (grant.expires_at_unix_ms <= t) continue
      taken.push({
        source_origin: grant.source_origin,
        target_url: grant.target_url,
        method: grant.method,
        captured_at_unix_ms: grant.captured_at_unix_ms,
        expires_at_unix_ms: grant.expires_at_unix_ms,
        headers: grant.headers.map(h => ({ name: h.name, value: h.value })),
      })
    }
    return taken.slice(0, HEADER_GRANT_MAX_PER_RESOLVE)
  }

  function clearTab(tabId: number): void {
    candidates.delete(tabId)
  }

  function clearAll(): void {
    candidates.clear()
  }

  return { arm, observe, take, clearTab, clearAll }
}

export type HeaderGrantStore = ReturnType<typeof createHeaderGrantStore>
