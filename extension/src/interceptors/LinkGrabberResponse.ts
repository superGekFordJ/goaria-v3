// Shared interception-pipeline types used by the abstract base class and the
// browser-specific subclasses. Kept in a standalone module to avoid circular
// imports between the base class and the concrete interceptors.

/** Outcome of the synchronous decision step. */
export type InterceptionDecision = 'intercept' | 'pass' | 'too_small'

/**
 * Context describing a single candidate download. Populated by the
 * browser-specific listener (Firefox webRequest or Chrome downloads API) and
 * consumed by the shared decision + request-construction logic.
 */
export type InterceptionContext = {
  url: string
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
}
