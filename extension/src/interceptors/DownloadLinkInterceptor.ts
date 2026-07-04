import { sendMessage } from 'webext-bridge/background'
import { configState } from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'
import { wsClient } from '../background/wsClient'
import { getCookiesForUrl } from '../background/cookieCapture'
import { getDownloadPageUrl } from '../background/refererCapture'
import type {
  DownloadHandoffMessage,
  DownloadInterceptedMessage,
  DownloadResponse,
} from '../utils/messaging'
import type { InterceptionContext, InterceptionDecision } from './LinkGrabberResponse'

// MIME types that are page/resource content, not downloads. Interception skips
// these unless a Content-Disposition: attachment header overrides the type.
const NON_DOWNLOAD_MIME_TYPES = new Set([
  'text/html',
  'text/plain',
  'application/xhtml+xml',
  'application/json',
  'text/css',
  'application/javascript',
  'text/javascript',
  'application/x-mpegurl',
  'application/vnd.apple.mpegurl',
])

// URL schemes the backend cannot handle (non-HTTP). Interception is skipped.
const NON_HTTP_SCHEMES = new Set(['data:', 'blob:', 'javascript:', 'ftp:', 'file:', 'about:'])

// MIME types that are explicit download payloads (not page/resource content).
// Hoisted to module level so each isDownloadMimeType call reuses one Set.
const DOWNLOAD_MIME_TYPES = new Set([
  'application/octet-stream',
  'application/zip',
  'application/x-gzip',
  'application/gzip',
  'application/pdf',
  'application/x-rar-compressed',
  'application/x-tar',
  'application/x-7z-compressed',
  'application/x-bzip2',
  'application/x-xz',
  'application/msword',
  'application/vnd.ms-excel',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation',
])

/**
 * Shared interception pipeline. Subclasses implement the browser-specific
 * event registration (Firefox webRequestBlocking / Chrome downloads API path B)
 * and the SW-restart recovery strategy.
 */
export abstract class DownloadLinkInterceptor {
  // Serializes sendDownloadRequest calls. The WS client correlates acks by
  // FIFO order and assumes a single in-flight request, so concurrent sends
  // would mismatch acks. This chain ensures only one request is in flight.
  private sendChain: Promise<DownloadResponse | void> = Promise.resolve()

  /**
   * Synchronous decision. Firefox webRequestBlocking needs a synchronous
   * return, so all async work (cookie/referer capture) is deferred to
   * constructDownloadRequest.
   */
  shouldIntercept(ctx: InterceptionContext): InterceptionDecision {
    if (!configState.autoCapture) return 'pass'
    if (!connectionState.interceptionEnabled) return 'pass'

    const scheme = getScheme(ctx.url)
    if (scheme && NON_HTTP_SCHEMES.has(scheme)) return 'pass'
    if (scheme !== 'http:' && scheme !== 'https:') return 'pass'

    // Content-Disposition: attachment wins regardless of MIME type.
    if (hasAttachmentDisposition(ctx.contentDisposition)) {
      if (!matchesRegisteredFileTypes(ctx.url)) return 'pass'
      return 'intercept'
    }
    // inline disposition is a page-embedded resource, not a download.
    if (getDispositionType(ctx.contentDisposition) === 'inline') return 'pass'

    const mime = ctx.mimeType
    if (mime && NON_DOWNLOAD_MIME_TYPES.has(mime)) return 'pass'
    if (mime && !isDownloadMimeType(mime)) return 'pass'

    if (!matchesRegisteredFileTypes(ctx.url)) return 'pass'
    return 'intercept'
  }

  /**
   * Build the DownloadHandoffMessage: capture cookies + download page URL and
   * assemble the camelCase message the WS client converts to snake_case JSON.
   */
  async constructDownloadRequest(
    ctx: InterceptionContext,
  ): Promise<DownloadHandoffMessage> {
    const [cookies, downloadPage] = await Promise.all([
      getCookiesForUrl(ctx.url),
      getDownloadPageUrl({
        tabId: ctx.tabId,
        referrer: ctx.referrer,
        initiator: ctx.initiator,
        originUrl: ctx.originUrl,
      }),
    ])
    // When the size is already known, skip the backend HEAD probe to avoid
    // burning a presigned-CDN signature on an extra request.
    const skipHeadProbe = ctx.fileSize > 0
    return {
      url: ctx.url,
      headers: cookies,
      fileSize: ctx.fileSize,
      skipHeadProbe,
      dedupKey: '',
      filename: ctx.filename,
      downloadPage,
    }
  }

  /** Serialize and send a download request through the WS client. */
  requestAddDownload(req: DownloadHandoffMessage): Promise<DownloadResponse> {
    const next = this.sendChain.then(() => wsClient.sendDownloadRequest(req))
    // Keep the chain alive even if a request rejects, so a failure doesn't
    // block subsequent downloads in the queue.
    this.sendChain = next.catch(() => undefined)
    return next
  }

  /** Notify the content script of an interception outcome (best-effort). */
  notifyIntercepted(ctx: InterceptionContext, success: boolean, error?: string): void {
    const msg: DownloadInterceptedMessage = {
      url: ctx.url,
      filename: ctx.filename,
      success,
      error,
    }
    // Content script may not be injected on some pages; ignore send failures.
    void sendMessage('download:intercepted', msg, 'content-script').catch(() => undefined)
  }

  /** Register browser-specific event listeners. */
  abstract register(): void

  /** Recover pending decisions after a service worker restart. */
  abstract recoverPendingDecisions(): Promise<void>
}

// --- shared helpers ---

export function getScheme(url: string): string {
  const idx = url.indexOf(':')
  if (idx === -1) return ''
  return url.slice(0, idx + 1).toLowerCase()
}

export function extractMimeType(contentType: string): string {
  if (!contentType) return ''
  const semi = contentType.indexOf(';')
  const base = semi === -1 ? contentType : contentType.slice(0, semi)
  return base.trim().toLowerCase()
}

export function extractFilename(contentDisposition: string, url: string): string {
  if (contentDisposition) {
    // RFC 6266 filename* (RFC 5987 ext-value: charset'language'value).
    const star = /filename\*=(?:[^']*'[^']*')?([^;]+)/i.exec(contentDisposition)
    if (star?.[1]) {
      return decodeStarFilename(star[1].trim())
    }
    const plain = /filename="?([^";]+)"?/i.exec(contentDisposition)
    if (plain?.[1]) return plain[1].trim()
  }
  try {
    const u = new URL(url)
    const last = u.pathname.split('/').pop()
    return last ? decodeURIComponent(last) : ''
  } catch {
    return ''
  }
}

function decodeStarFilename(raw: string): string {
  // filename* may be percent-encoded.
  try {
    return decodeURIComponent(raw)
  } catch {
    return raw
  }
}

export function hasAttachmentDisposition(contentDisposition: string): boolean {
  return getDispositionType(contentDisposition) === 'attachment'
}

/** Extract the disposition type token (first token before ';'), lower-cased. */
export function getDispositionType(contentDisposition: string): string {
  if (!contentDisposition) return ''
  const semi = contentDisposition.indexOf(';')
  const base = semi === -1 ? contentDisposition : contentDisposition.slice(0, semi)
  return base.trim().toLowerCase()
}

export function isDownloadMimeType(mimeType: string): boolean {
  if (!mimeType) return false
  if (NON_DOWNLOAD_MIME_TYPES.has(mimeType)) return false
  if (mimeType.startsWith('video/')) return true
  if (mimeType.startsWith('audio/')) return true
  if (mimeType.startsWith('image/') && mimeType !== 'image/svg+xml') return true
  return DOWNLOAD_MIME_TYPES.has(mimeType)
}

function matchesRegisteredFileTypes(url: string): boolean {
  const types = configState.registeredFileTypes
  if (types.length === 0) return true
  const ext = getUrlExtension(url)
  if (!ext) return false
  return types.some((t) => t.toLowerCase() === ext)
}

function getUrlExtension(url: string): string {
  try {
    const u = new URL(url)
    const last = u.pathname.split('/').pop()
    if (!last) return ''
    const dot = last.lastIndexOf('.')
    if (dot === -1) return ''
    return last.slice(dot + 1).toLowerCase()
  } catch {
    return ''
  }
}
