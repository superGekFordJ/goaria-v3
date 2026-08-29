// Shared interception-pipeline types used by the abstract base class and the
// browser-specific subclasses. Kept in a standalone module to avoid circular
// imports between the base class and the concrete interceptors.

/** Outcome of the synchronous decision step. */
export type InterceptionDecision = 'intercept' | 'pass'

/**
 * Context describing a single candidate download. Populated by the
 * browser-specific listener (Firefox webRequest or Chrome downloads API) and
 * consumed by the shared decision + request-construction logic.
 */
export type InterceptionContext = {
  url: string
  /** Post-redirect absolute URL (Chrome item.finalUrl). Empty when not available. */
  finalUrl?: string
  tabId: number
  /** Content-Type with parameters stripped, lower-cased (e.g. "text/html"). */
  mimeType: string
  /** Raw Content-Disposition header value, or '' when absent. */
  contentDisposition: string
  /** Best-known total bytes; 0 means unknown. */
  fileSize: number
  /** Filename from Content-Disposition or URL path; '' when unknown. */
  filename: string
  /** Download page URL used as the Referer source. */
  referrer: string
  /**
   * Request initiator URL (Firefox webRequest). When the initiator is an
   * extension page, the referrer field alone would mask the real originUrl;
   * passing both separately lets getDownloadPageUrl check each independently.
   */
  initiator?: string
  /** Origin URL (Firefox webRequest only), used as a Referer fallback. */
  originUrl?: string
  /** Chrome DownloadItem.incognito; Firefox webRequest `incognito === true`. */
  incognito?: boolean
  /** Firefox webRequest cookie store; omitted when empty. Chrome does not set this. */
  cookieStoreId?: string
  /** Firefox webRequest document URL; omitted when empty. */
  documentUrl?: string
  /** Firefox webRequest frame id. */
  frameId?: number
}
