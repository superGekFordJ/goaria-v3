import browser from 'webextension-polyfill'
import {
  DownloadLinkInterceptor,
  extractFilename,
  extractMimeType,
} from './DownloadLinkInterceptor'
import type { InterceptionContext } from './LinkGrabberResponse'

/**
 * Firefox MV3 interceptor. Uses webRequest.onHeadersReceived with the
 * "blocking" + "responseHeaders" options (Firefox retains webRequestBlocking
 * in MV3) to synchronously cancel the request before the browser starts the
 * download, then asynchronously hands the URL off to GoAria.
 *
 * The cancel decision is synchronous so there is no race between the browser
 * starting the download and the backend accepting the handoff. Cookie/referer
 * capture and the WS request happen after the callback returns.
 *
 * Original-URL capture: Firefox's onHeadersReceived fires for every hop in
 * a redirect chain, including 3xx responses (Mozilla bug 1448599). At the
 * final 2xx, details.url is the post-redirect CDN URL, not the original
 * openlist URL. To match Chrome's contract (url = original, finalUrl = CDN),
 * onBeforeRequest records the initial request URL keyed by requestId at the
 * start of the chain; onHeadersReceived looks it up and uses it as ctx.url
 * while details.url (CDN) becomes ctx.finalUrl. Redirect hops are bypassed
 * (a 3xx has no body), so interception only happens at the final 2xx where
 * Content-Length is still read, keeping the skipHeadProbe optimization intact.
 */
export class FirefoxBlockingInterceptor extends DownloadLinkInterceptor {
  // requestId → original request URL captured at onBeforeRequest. Firefox's
  // requestId is stable across redirect hops for one logical request, so the
  // final onHeadersReceived can recover the pre-redirect URL. Entries are
  // looked up (not deleted) in onHeadersReceived — because it fires per hop
  // including 3xx, deleting at a redirect hop would let the next onBeforeRequest
  // re-capture the CDN URL. Cleanup is handled by onCompleted/onErrorOccurred
  // (cancel triggers onErrorOccurred); a max-size cap guards against unbounded
  // growth if a listener miss leaves entries stranded (e.g. SW restart).
  private originalUrls = new Map<string, string>()
  private static readonly MAX_ORIGINAL_URLS = 512

  register(): void {
    browser.webRequest.onBeforeRequest.addListener(this.onBeforeRequest, {
      urls: ['<all_urls>'],
      types: ['main_frame', 'sub_frame'],
    })
    browser.webRequest.onHeadersReceived.addListener(
      this.onHeadersReceived,
      { urls: ['<all_urls>'], types: ['main_frame', 'sub_frame'] },
      ['blocking', 'responseHeaders'],
    )
    // Safety-net cleanup: remove stranded entries when the request completes
    // or errors without reaching onHeadersReceived (e.g. cancelled, aborted,
    // or a non-download navigation that never triggers shouldIntercept).
    browser.webRequest.onCompleted.addListener(this.onRequestFinished, {
      urls: ['<all_urls>'],
      types: ['main_frame', 'sub_frame'],
    })
    browser.webRequest.onErrorOccurred.addListener(this.onRequestFinished, {
      urls: ['<all_urls>'],
      types: ['main_frame', 'sub_frame'],
    })
  }

  private onBeforeRequest = (details: browser.WebRequest.OnBeforeRequestDetailsType): void => {
    // onBeforeRequest fires for every hop in a redirect chain (not just the
    // initial request), but requestId stays constant. Only capture the FIRST
    // URL — the pre-redirect original — so redirect targets don't overwrite it.
    if (!this.originalUrls.has(details.requestId)) {
      // Guard against unbounded growth if cleanup listeners miss (SW restart,
      // listener race). Evicting the oldest entry keeps the map bounded; the
      // evicted request simply falls back to details.url at onHeadersReceived.
      // Done only when inserting a new entry, so an already-present requestId
      // (redirect hop) does not evict an unrelated entry.
      if (this.originalUrls.size >= FirefoxBlockingInterceptor.MAX_ORIGINAL_URLS) {
        const firstKey = this.originalUrls.keys().next().value
        if (firstKey !== undefined) this.originalUrls.delete(firstKey)
      }
      this.originalUrls.set(details.requestId, details.url)
    }
  }

  private onRequestFinished = (
    details:
      browser.WebRequest.OnCompletedDetailsType | browser.WebRequest.OnErrorOccurredDetailsType,
  ): void => {
    this.originalUrls.delete(details.requestId)
  }

  // Firefox webRequestBlocking path has no pause/cancel state machine, so
  // there is nothing to recover after a SW restart.
  async recoverPendingDecisions(): Promise<void> {
    /* no-op */
  }

  private onHeadersReceived = (
    details: browser.WebRequest.OnHeadersReceivedDetailsType,
  ): browser.WebRequest.BlockingResponse => {
    // Look up the captured original URL (pre-redirect openlist URL). If the
    // entry is missing (SW restart mid-request, listener added late, or a
    // request that bypassed onBeforeRequest), fall back to details.url — no
    // worse than the pre-fix behavior. Do NOT delete here: onHeadersReceived
    // fires once per hop in a redirect chain, and deleting at the 302 hop
    // would let the next onBeforeRequest (for the CDN hop) re-capture the CDN
    // URL, overwriting the original. Cleanup is handled by
    // onCompleted/onErrorOccurred (cancel triggers onErrorOccurred).
    const originalUrl = this.originalUrls.get(details.requestId)
    // Firefox fires onHeadersReceived for every hop in a redirect chain,
    // including 3xx redirect responses (Mozilla bug 1448599 confirms the 302
    // is delivered with statusCode 302). A 3xx is never the download body: it
    // has no Content-Length and details.url is still the pre-redirect URL, so
    // intercepting here (e.g. a rare 302 carrying Content-Disposition:
    // attachment) would hand GoAria url=finalUrl=openlist — wrong IP scope —
    // and lose the body size. Skip redirect hops; the final 2xx fires
    // onHeadersReceived again with details.url = CDN and the real headers.
    if (details.statusCode >= 300 && details.statusCode < 400) return {}
    const ctx = this.buildContext(details, originalUrl)
    const decision = this.shouldIntercept(ctx)
    if (decision !== 'intercept') return {}
    // Cancel synchronously; the handoff runs after the callback returns.
    void this.handleInterception(ctx)
    // Firefox does not auto-close a tab whose main_frame request is
    // cancelled. Remove the leftover blank tab to avoid content-process
    // accumulation. Only do this for main_frame — a sub_frame interception
    // (e.g. an iframe download link) must not close the parent tab.
    // The tab may already be gone (user-closed), so swallow.
    if (ctx.tabId >= 0 && details.type === 'main_frame') {
      const tabIdToClose = ctx.tabId
      void (async () => {
        try {
          const tab = await browser.tabs.get(tabIdToClose)
          const isBlank =
            !tab.url || tab.url === 'about:blank' || tab.url === ctx.url || tab.url === ctx.finalUrl
          if (isBlank) {
            await browser.tabs.remove(tabIdToClose)
          }
        } catch {
          // Tab already gone or access denied.
        }
      })()
    }
    return { cancel: true }
  }

  private async handleInterception(ctx: InterceptionContext): Promise<void> {
    try {
      const req = await this.constructDownloadRequest(ctx)
      const resp = await this.requestAddDownload(req)
      this.notifyIntercepted(ctx, resp.success, resp.success ? undefined : resp.error)
    } catch (err) {
      // WS dropped between the sync check and the send, or request rejected.
      // The browser download is already cancelled and cannot be restored on
      // the Firefox blocking path — surface the failure to the content script.
      const msg = err instanceof Error ? err.message : String(err)
      this.notifyIntercepted(ctx, false, msg)
    }
  }

  private buildContext(
    details: browser.WebRequest.OnHeadersReceivedDetailsType,
    originalUrl?: string,
  ): InterceptionContext {
    const headers = details.responseHeaders ?? []
    let contentType = ''
    let contentDisposition = ''
    let contentLength = 0
    for (const h of headers) {
      const name = h.name.toLowerCase()
      if (name === 'content-type') contentType = h.value ?? ''
      else if (name === 'content-disposition') contentDisposition = h.value ?? ''
      else if (name === 'content-length') contentLength = parseContentLength(h.value)
    }
    const referrer = details.initiator ?? details.originUrl ?? ''
    // url = the ORIGINAL request URL (openlist), captured at onBeforeRequest.
    // finalUrl = the FINAL response URL (CDN) at onHeadersReceived, i.e.
    // details.url after the redirect chain. This matches Chrome's contract
    // (item.url = original, item.finalUrl = CDN) so GoAria downloads from the
    // original URL (openlist → 302 → freshly-minted CDN token) instead of the
    // stale post-redirect CDN URL whose presigned token is bound to the
    // browser's redirect context. When no original was captured, fall back to
    // details.url for both fields (pre-fix behavior).
    const url = originalUrl ?? details.url
    const finalUrl = details.url
    return {
      url,
      finalUrl,
      tabId: details.tabId,
      mimeType: extractMimeType(contentType),
      contentDisposition,
      fileSize: contentLength,
      filename: extractFilename(contentDisposition, finalUrl || url),
      referrer,
      initiator: details.initiator,
      originUrl: details.originUrl,
    }
  }
}

function parseContentLength(value?: string): number {
  if (!value) return 0
  const n = Number.parseInt(value, 10)
  return Number.isFinite(n) && n > 0 ? n : 0
}
