import browser from 'webextension-polyfill'

/**
 * Resolve the download page URL to use as the Referer source. The value is
 * stored in DownloadRequest.download_page; the Go backend synthesizes a
 * "Referer: <downloadPage>" header via ensureRefererHeader only when the
 * extension-supplied headers do not already contain a Referer.
 *
 * Priority: referrer > initiator > originUrl > tab URL. The first non-empty
 * value wins. Extension-internal pages are never used as the download page.
 */
export async function getDownloadPageUrl(params: {
  tabId?: number
  referrer?: string
  initiator?: string
  originUrl?: string
}): Promise<string> {
  if (params.referrer && !isExtensionUrl(params.referrer)) return params.referrer
  if (params.initiator && !isExtensionUrl(params.initiator)) return params.initiator
  if (params.originUrl && !isExtensionUrl(params.originUrl)) return params.originUrl
  if (params.tabId !== undefined && params.tabId >= 0) {
    try {
      const tab = await browser.tabs.get(params.tabId)
      if (tab.url && !isExtensionUrl(tab.url)) return tab.url
    } catch {
      // tab closed or inaccessible — fall through to empty
    }
  }
  return ''
}

function isExtensionUrl(url: string): boolean {
  return url.startsWith('chrome-extension://') || url.startsWith('moz-extension://')
}
