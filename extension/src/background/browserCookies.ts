import { parseHTTPURLHost } from './canonicalHost'

export type BrowserCookieInput = {
  name?: unknown
  value?: unknown
  domain?: unknown
  path?: unknown
  secure?: unknown
  hostOnly?: unknown
  storeId?: unknown
  partitionKey?: unknown
  httpOnly?: unknown
  sameSite?: unknown
  expirationDate?: unknown
  session?: unknown
}

export type WireBrowserCookie = {
  name: string
  value: string
  domain: string
  path: string
  secure: boolean
  host_only: boolean
}

export type MapBrowserCookieResult =
  | { cookie: WireBrowserCookie }
  | { skip: true }
  | { error: string }

export type CollectCookiesResult =
  | { cookies: WireBrowserCookie[] }
  | { error: string }

export function isBrowserCookieInput(value: unknown): value is BrowserCookieInput {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function mapBrowserCookie(input: BrowserCookieInput): MapBrowserCookieResult {
  if (hasPartitionKey(input)) {
    return { skip: true }
  }
  if (typeof input.hostOnly !== 'boolean') {
    return { error: 'hostOnly is required' }
  }
  if (typeof input.secure !== 'boolean') {
    return { error: 'secure is required' }
  }
  if (typeof input.name !== 'string' || input.name === '') {
    return { error: 'name must be a non-empty string' }
  }
  if (typeof input.value !== 'string') {
    return { error: 'value must be a string' }
  }
  const domain = typeof input.domain === 'string' ? input.domain : ''
  const path = typeof input.path === 'string' ? input.path : '/'
  return {
    cookie: {
      name: input.name,
      value: input.value,
      domain,
      path,
      secure: input.secure,
      host_only: input.hostOnly,
    },
  }
}

export function hasPartitionKey(input: { partitionKey?: unknown }): boolean {
  const pk = input.partitionKey
  if (pk == null || pk === '') {
    return false
  }
  if (typeof pk === 'object' && !Array.isArray(pk) && Object.keys(pk).length === 0) {
    return false
  }
  return true
}

export async function collectStructuredCookies(
  url: string,
  storeId: string,
  getAll: (details: { url: string; storeId: string }) => Promise<unknown[]>,
): Promise<CollectCookiesResult> {
  if (typeof storeId !== 'string' || storeId.trim() === '') {
    return { error: 'storeId is required' }
  }
  if (parseHTTPURLHost(url) === undefined) {
    return { error: 'url is not a valid http(s) host' }
  }
  const trimmedStoreId = storeId.trim()
  try {
    const cookies = await getAll({ url, storeId: trimmedStoreId })
    const mapped: WireBrowserCookie[] = []
    for (const cookie of cookies) {
      if (!isBrowserCookieInput(cookie)) {
        return { error: 'malformed cookie' }
      }
      const result = mapBrowserCookie(cookie)
      if ('error' in result) {
        return { error: result.error }
      }
      if ('skip' in result) {
        continue
      }
      mapped.push(result.cookie)
    }
    return { cookies: mapped }
  } catch {
    return { error: 'cookies.getAll failed' }
  }
}
