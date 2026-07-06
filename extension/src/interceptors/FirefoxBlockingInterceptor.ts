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
 */
export class FirefoxBlockingInterceptor extends DownloadLinkInterceptor {
  register(): void {
    browser.webRequest.onHeadersReceived.addListener(
      this.onHeadersReceived,
      { urls: ['<all_urls>'], types: ['main_frame', 'sub_frame'] },
      ['blocking', 'responseHeaders'],
    )
  }

  // Firefox webRequestBlocking path has no pause/cancel state machine, so
  // there is nothing to recover after a SW restart.
  async recoverPendingDecisions(): Promise<void> {
    /* no-op */
  }

  private onHeadersReceived = (
    details: browser.WebRequest.OnHeadersReceivedDetailsType,
  ): browser.WebRequest.BlockingResponse => {
    const ctx = this.buildContext(details)
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
      browser.tabs.remove(ctx.tabId).catch(() => undefined)
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
    return {
      url: details.url,
      tabId: details.tabId,
      mimeType: extractMimeType(contentType),
      contentDisposition,
      fileSize: contentLength,
      filename: extractFilename(contentDisposition, details.url),
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
