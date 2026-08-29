import { sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import { isBootReady } from '../background/bootState'
import { configState } from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'
import { wsClient } from '../background/wsClient'
import { getCookiesForUrl } from '../background/cookieCapture'
import { getDownloadPageUrl } from '../background/refererCapture'
import type {
  DownloadHandoffMessage,
  DownloadInterceptedMessage,
  DownloadResponse,
  InterceptedReply,
} from '../utils/messaging'
import type { InterceptionContext, InterceptionDecision } from './LinkGrabberResponse'
import { t } from '../lib/i18n'

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
  // Windows executables / installers
  'application/x-msdos-program',
  'application/x-msdownload',
  'application/x-msi',
  'application/vnd.microsoft.portable-executable',
  'application/x-dosexec',
  'application/x-ms-dos-executable',
  'application/x-ms-ne-executable',
  // Linux packages
  'application/x-debian-package',
  'application/x-redhat-package-manager',
  'application/x-rpm',
  // Android
  'application/vnd.android.package-archive',
  // macOS
  'application/x-apple-diskimage',
  'application/x-dmg',
])

/**
 * Shared interception pipeline. Subclasses implement the browser-specific
 * event registration (Firefox webRequestBlocking / Chrome downloads API path B)
 * and the SW-restart recovery strategy.
 */
export abstract class DownloadLinkInterceptor {
  // Serializes sendDownloadRequest calls. wsClient already serializes and
  // prefers request_id; this chain keeps interceptor callers from racing
  // the download-kind FIFO fallback when an ack omits id.
  private sendChain: Promise<DownloadResponse | void> = Promise.resolve()

  /**
   * Synchronous decision. Firefox webRequestBlocking needs a synchronous
   * return, so all async work (cookie/referer capture) is deferred to
   * constructDownloadRequest.
   */
  shouldIntercept(ctx: InterceptionContext): InterceptionDecision {
    if (!isBootReady()) return 'pass'
    if (!configState.autoCapture) return 'pass'
    if (!connectionState.interceptionEnabled) return 'pass'

    const scheme = getScheme(ctx.url)
    if (scheme && NON_HTTP_SCHEMES.has(scheme)) return 'pass'
    if (scheme !== 'http:' && scheme !== 'https:') return 'pass'

    const mime = ctx.mimeType
    const types = configState.registeredFileTypes
    const hasStrictWhitelist = types.length > 0
    const ext = getEffectiveExtension(ctx)
    const extMatches = matchesRegisteredFileTypes(ext)

    // 1. Content-Disposition: attachment — a server-declared download.
    //    With a strict whitelist, only intercept whitelist hits; on a miss,
    //    still allow MIME fallback for extensionless CDN URLs (e.g. attachment
    //    + application/zip with no filename ext). Without a whitelist,
    //    intercept all attachments.
    if (hasAttachmentDisposition(ctx.contentDisposition)) {
      if (!hasStrictWhitelist) return 'intercept'
      if (extMatches) return 'intercept'
      if (!ext && mime && isDownloadMimeType(mime)) return 'intercept'
      return 'pass'
    }

    // 2. inline disposition is a page-embedded resource, not a download.
    //    Evaluated before the whitelist so an explicit inline never gets
    //    pulled in just because the extension is registered.
    if (getDispositionType(ctx.contentDisposition) === 'inline') return 'pass'

    // 3. Strict whitelist hit — override weak/misconfigured MIME (empty,
    //    text/plain, installer types), but never cancel NON_DOWNLOAD page
    //    or resource types other than text/plain (the classic misconfig).
    if (
      hasStrictWhitelist &&
      extMatches &&
      !(mime && NON_DOWNLOAD_MIME_TYPES.has(mime) && mime !== 'text/plain')
    ) {
      return 'intercept'
    }

    // 4. Non-download MIME types (text/html, text/css, etc.).
    if (mime && NON_DOWNLOAD_MIME_TYPES.has(mime)) return 'pass'

    // 5. No strict whitelist: only intercept clear download MIME (or empty
    //    MIME). Without this gate, Firefox main_frame would false-capture
    //    SVG/XML and other non-page, non-download types.
    if (!hasStrictWhitelist) {
      if (mime && !isDownloadMimeType(mime)) return 'pass'
      return 'intercept'
    }

    // 6. Strict whitelist, ext didn't match. Allow MIME-based fallback only
    //    when the file truly has no extension (e.g. an extension-less CDN
    //    URL serving application/zip). If there IS an extension, respect the
    //    user's whitelist — they chose not to include it.
    if (!ext && mime && isDownloadMimeType(mime)) return 'intercept'

    return 'pass'
  }

  /**
   * Build the DownloadHandoffMessage: capture cookies + download page URL and
   * assemble the camelCase message the WS client converts to snake_case JSON.
   */
  async constructDownloadRequest(ctx: InterceptionContext): Promise<DownloadHandoffMessage> {
    const cookies =
      ctx.incognito === true
        ? []
        : await getCookiesForUrl(ctx.url)
    const downloadPage = await getDownloadPageUrl({
      tabId: ctx.tabId,
      referrer: ctx.referrer,
      initiator: ctx.initiator,
      originUrl: ctx.originUrl,
    })
    // When the size is already known, skip the backend HEAD probe to avoid
    // burning a presigned-CDN signature on an extra request.
    const skipHeadProbe = ctx.fileSize > 0
    return {
      url: ctx.url,
      finalUrl: ctx.finalUrl ?? '',
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
    void this.deliverIntercepted(ctx, msg)
  }

  private async deliverIntercepted(
    ctx: InterceptionContext,
    msg: DownloadInterceptedMessage,
  ): Promise<void> {
    const tabId = await this.resolveTabId(ctx)
    if (tabId < 0) {
      showInterceptedNotification(msg)
      return
    }
    try {
      const sendPromise = sendMessage(
        'download:intercepted',
        msg,
        `content-script@${tabId}`,
      ) as Promise<InterceptedReply | undefined>

      const reply = await Promise.race([
        sendPromise,
        new Promise<undefined>((_, reject) =>
          setTimeout(() => reject(new Error('sendMessage timeout')), 1000),
        ),
      ])
      if (reply !== 'shown') showInterceptedNotification(msg)
    } catch {
      // No content script in this tab (e.g. chrome:// pages), timed out or it threw.
      showInterceptedNotification(msg)
    }
  }

  /** Resolve the tab to notify. Chrome's downloads API leaves tabId at -1. */
  private async resolveTabId(ctx: InterceptionContext): Promise<number> {
    if (ctx.tabId >= 0) {
      try {
        const tab = await browser.tabs.get(ctx.tabId)
        if (tab) return ctx.tabId
      } catch {
        // Tab no longer exists.
      }
    }
    try {
      const [tab] = await browser.tabs.query({ active: true, currentWindow: true })
      return tab?.id ?? -1
    } catch {
      return -1
    }
  }

  /** Register browser-specific event listeners. */
  abstract register(): void

  /** Recover pending decisions after a service worker restart. */
  abstract recoverPendingDecisions(): Promise<void>
}

// --- shared helpers ---

// Degraded feedback when the in-page toast is unavailable. Runs in the
// background where the notifications API is available.
export function showInterceptedNotification(msg: DownloadInterceptedMessage): void {
  try {
    void browser.notifications.create({
      type: 'basic',
      iconUrl: browser.runtime.getURL('icons/icon-128.png'),
      title: msg.success ? t('interceptor_notif_title_taken') : t('interceptor_notif_title_failed'),
      message: msg.error ? `${msg.filename}\n${msg.error}` : msg.filename || msg.url,
    })
  } catch {
    // notifications API unavailable; nothing more we can do.
  }
}

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

function matchesRegisteredFileTypes(ext: string): boolean {
  const types = configState.registeredFileTypes
  if (types.length === 0) return true
  if (!ext) return false
  return types.some(t => t.toLowerCase() === ext)
}

// Extension priority: CD/browser filename basename → finalUrl path → url path.
// Only a non-empty ext short-circuits; trailing-dot basenames ("file.exe.")
// and extensionless CD names ("download") fall through to URL sources.
// Chrome may supply an OS path in item.filename (dots in directory names), so
// take the basename before reading the extension.
function getEffectiveExtension(ctx: InterceptionContext): string {
  if (ctx.filename) {
    const ext = extensionOfBasename(basenameOfPath(ctx.filename))
    if (ext) return ext
  }
  if (ctx.finalUrl) {
    const ext = getUrlExtension(ctx.finalUrl)
    if (ext) return ext
  }
  return getUrlExtension(ctx.url)
}

function basenameOfPath(path: string): string {
  const slash = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return slash >= 0 ? path.slice(slash + 1) : path
}

function extensionOfBasename(base: string): string {
  const dot = base.lastIndexOf('.')
  if (dot <= 0) return ''
  return base.slice(dot + 1).toLowerCase()
}

function getUrlExtension(url: string): string {
  try {
    const u = new URL(url)
    const last = u.pathname.split('/').pop()
    if (!last) return ''
    return extensionOfBasename(last)
  } catch {
    return ''
  }
}
