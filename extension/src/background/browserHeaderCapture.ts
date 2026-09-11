import browser from 'webextension-polyfill'
import { hasCapability } from './capabilities'
import { connectionState } from '../stores/connection.svelte'
import {
  CAP_EXTRACTOR_HEADER_CONTEXT,
  CAP_EXTRACTOR_RESOLVE,
} from '../stores/config.svelte'
import { isFirefox } from '../utils/extensionInfo'
import { isMatchGenerationCurrent } from './matchSnapshot'
import { pageTokenFromHref } from './pageToken'
import {
  canonicalSourceOrigin,
  createHeaderGrantStore,
  type WireBrowserHeaderGrant,
} from './browserHeaderGrant'

const store = createHeaderGrantStore({
  isFirefoxTarget: isFirefox,
  isGenerationCurrent: isMatchGenerationCurrent,
})

// Capture is usable only while both capabilities are present verbatim on the
// live connection; anything else fails closed.
function headerContextGranted(): boolean {
  const caps = connectionState.capabilities
  return (
    hasCapability(caps, CAP_EXTRACTOR_RESOLVE) &&
    hasCapability(caps, CAP_EXTRACTOR_HEADER_CONTEXT)
  )
}

const OBSERVE_FILTER: browser.WebRequest.RequestFilter = {
  urls: ['https://*/*'],
  types: ['xmlhttprequest'],
}

// Registered synchronously during background init so MV3 cold-wake events are
// not missed; the listener stays inert until every gate passes and never
// blocks or modifies a request.
export function initBrowserHeaderCapture(): void {
  const options: browser.WebRequest.OnSendHeadersOptions[] = isFirefox()
    ? ['requestHeaders']
    : ['requestHeaders', 'extraHeaders']
  browser.webRequest.onSendHeaders.addListener(onSendHeaders, OBSERVE_FILTER, options)
  // Navigation replaces the page identity: drop armed state before any
  // re-detection can arm a new candidate for the tab.
  browser.tabs.onUpdated.addListener((tabId, changeInfo) => {
    if (typeof changeInfo.url === 'string' && changeInfo.url !== '') store.clearTab(tabId)
  })
  browser.tabs.onRemoved.addListener(tabId => store.clearTab(tabId))
}

const onSendHeaders = (details: browser.WebRequest.OnSendHeadersDetailsType): void => {
  if (!headerContextGranted()) return
  store.observe({
    tabId: details.tabId,
    type: details.type,
    method: details.method,
    url: details.url,
    headers: details.requestHeaders,
    initiator: details.initiator,
    originUrl: details.originUrl,
    documentUrl: details.documentUrl,
    incognito: details.incognito,
    cookieStoreId: details.cookieStoreId,
  })
}

// armExtractorHeaderCandidate binds a freshly detected tab to one bounded
// grant candidate. Callers already completed the salted digest, page-token,
// and ignore checks; this re-verifies the live tab after an awaited read so a
// capability or match replacement during the gap cannot arm a stale state.
export async function armExtractorHeaderCandidate(
  tabId: number,
  generation: number,
  tabUrl: string | undefined,
  pageToken: string,
): Promise<void> {
  if (!headerContextGranted() || !isMatchGenerationCurrent(generation)) return
  if (typeof pageToken !== 'string' || pageToken === '') return
  if (typeof tabUrl !== 'string' || canonicalSourceOrigin(tabUrl) === undefined) return
  let tab: browser.Tabs.Tab
  try {
    tab = await browser.tabs.get(tabId)
  } catch {
    return
  }
  if (!headerContextGranted() || !isMatchGenerationCurrent(generation)) return
  const liveToken = await pageTokenFromHref(tab.url ?? '')
  if (liveToken === undefined || liveToken !== pageToken) return
  if (typeof tab.incognito !== 'boolean') return
  const cookieStoreId =
    typeof tab.cookieStoreId === 'string' && tab.cookieStoreId !== ''
      ? tab.cookieStoreId
      : undefined
  store.arm({
    tabId,
    pageToken,
    sourceUrl: tab.url ?? tabUrl,
    generation,
    incognito: tab.incognito,
    cookieStoreId,
  })
}

// takeHeaderGrantsForResolve consumes live grants for one fresh resolve. The
// take is destructive: failed or timed-out sends never restore them.
export function takeHeaderGrantsForResolve(input: {
  tabId: number
  pageToken: string
  incognito: boolean | undefined
  cookieStoreId?: string
}): WireBrowserHeaderGrant[] {
  if (!headerContextGranted()) return []
  return store.take(input)
}

export function clearExtractorHeaderGrants(): void {
  store.clearAll()
}

export function clearExtractorHeaderGrantsForTab(tabId: number): void {
  store.clearTab(tabId)
}
