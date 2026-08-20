import browser from 'webextension-polyfill'
import { collectStructuredCookies, type CollectCookiesResult } from './browserCookies'
import { pickCookieStoreId, type CookieStoreHint } from './cookieStoreId'

export { pickCookieStoreId, type CookieStoreHint } from './cookieStoreId'

/**
 * Collect cookies for a URL (including HttpOnly) via the browser.cookies API
 * and fold them into a single "Cookie" header line matching the aria2
 * "name: value" header format expected by the Go DownloadRequest.headers field.
 *
 * Returns [] when there are no cookies or the API throws — an empty Cookie
 * header is omitted to avoid sending a malformed request to servers that treat
 * an empty Cookie as suspicious.
 */
export async function getCookiesForUrl(url: string): Promise<string[]> {
  try {
    const cookies = await browser.cookies.getAll({ url })
    if (cookies.length === 0) return []
    // Sort by name for a stable cookie string so duplicate detection on the
    // backend is not confused by ordering variance.
    cookies.sort((a, b) => a.name.localeCompare(b.name))
    const cookieStr = cookies.map(c => `${c.name}=${c.value}`).join('; ')
    return [`Cookie: ${cookieStr}`]
  } catch {
    // cookies.getAll may reject in incognito split mode or without the cookies
    // permission. Don't block interception — the backend may still succeed.
    return []
  }
}

export async function getStructuredCookiesForUrl(
  url: string,
  storeId: string,
): Promise<CollectCookiesResult> {
  return collectStructuredCookies(url, storeId, details => browser.cookies.getAll(details))
}

export async function resolveCookieStoreIdForTab(
  tab: browser.Tabs.Tab,
): Promise<string | undefined> {
  if (typeof tab.id !== 'number') return undefined
  const fromTab = pickCookieStoreId(tab.id, (tab as { cookieStoreId?: unknown }).cookieStoreId, [])
  if (fromTab) return fromTab
  try {
    const stores = (await browser.cookies.getAllCookieStores()) as CookieStoreHint[]
    return pickCookieStoreId(tab.id, undefined, stores)
  } catch {
    return undefined
  }
}
