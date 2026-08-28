import {
  canonicalizeDirectURL,
  urlPathIsM3uPlaylist,
} from '../background/domCanonicalUrl'
import { EXTRACTOR_MAX_SESSION_ITEMS, sanitizeDisplayFilename } from '../background/extractorKeys'
import type { DomLinkKind } from '../background/domCatalog'

export type { DomLinkKind }

export const DOM_SCAN_SELECTOR = 'a[href], img, video, audio, source[src]'
export const DOM_SCAN_MAX_VISIT = 10_000
export const DOM_SCAN_MAX_MS = 50

export type DomScanHit = {
  url: string
  kind: DomLinkKind
  filename?: string
  documentPolicy: string
  elementPolicy: string
  relNoreferrer: boolean
}

export type DomScanNode = {
  tagName: string
  href?: string
  currentSrc?: string
  referrerPolicy?: string
  rel?: string
  getAttribute: (name: string) => string | null
  ownerDocument?: { baseURI?: string; referrerPolicy?: string }
}

export type DomScanRoot = {
  querySelectorAll: (selectors: string) => ArrayLike<DomScanNode>
  baseURI?: string
  referrerPolicy?: string
  title?: string
  shadowRoot?: unknown
}

export type CollectDomLinksOpts = {
  now?: () => number
  maxUnique?: number
  maxVisit?: number
  maxMs?: number
  pageHref?: string
}

export type CollectDomLinksResult = {
  items: DomScanHit[]
  truncated: boolean
  title: string
}

function kindOf(tag: string): DomLinkKind | undefined {
  switch (tag) {
    case 'A':
      return 'link'
    case 'IMG':
      return 'image'
    case 'VIDEO':
      return 'video'
    case 'AUDIO':
      return 'audio'
    case 'SOURCE':
      return 'source'
    default:
      return undefined
  }
}

function rawUrlOf(node: DomScanNode, kind: DomLinkKind): string {
  if (kind === 'link') {
    return (node.getAttribute('href') || node.href || '').trim()
  }
  if (kind === 'image' || kind === 'video' || kind === 'audio') {
    const current = typeof node.currentSrc === 'string' ? node.currentSrc.trim() : ''
    if (current) return current
    return (node.getAttribute('src') || '').trim()
  }
  return (node.getAttribute('src') || '').trim()
}

function filenameFromCanonical(url: string): string | undefined {
  try {
    const path = new URL(url).pathname
    const seg = path.split('/').filter(Boolean).pop()
    if (!seg) return undefined
    let decoded = seg
    try {
      decoded = decodeURIComponent(seg)
    } catch {
      decoded = seg
    }
    return sanitizeDisplayFilename(decoded)
  } catch {
    return undefined
  }
}

function relHasNoreferrer(node: DomScanNode): boolean {
  const rel = (node.getAttribute('rel') || node.rel || '').toLowerCase()
  if (!rel) return false
  return rel.split(/\s+/).includes('noreferrer')
}

function elementPolicyOf(node: DomScanNode): string {
  const attr = node.getAttribute('referrerpolicy')
  if (typeof attr === 'string' && attr.trim() !== '') return attr.trim()
  if (typeof node.referrerPolicy === 'string' && node.referrerPolicy.trim() !== '') {
    return node.referrerPolicy.trim()
  }
  return ''
}

export function collectDomLinks(
  root: DomScanRoot,
  opts: CollectDomLinksOpts = {},
): CollectDomLinksResult {
  const maxUnique = opts.maxUnique ?? EXTRACTOR_MAX_SESSION_ITEMS
  const maxVisit = opts.maxVisit ?? DOM_SCAN_MAX_VISIT
  const maxMs = opts.maxMs ?? DOM_SCAN_MAX_MS
  const now = opts.now ?? (() => performance.now())
  const started = now()
  const list = root.querySelectorAll(DOM_SCAN_SELECTOR)
  const length = list.length
  const items: DomScanHit[] = []
  const seen = new Set<string>()
  let truncated = false
  const docPolicy =
    typeof root.referrerPolicy === 'string' && root.referrerPolicy.trim() !== ''
      ? root.referrerPolicy.trim()
      : ''
  const rootBase = typeof root.baseURI === 'string' && root.baseURI !== '' ? root.baseURI : undefined
  const pageCanon = canonicalizeDirectURL(opts.pageHref || rootBase || '')

  for (let i = 0; i < length; i++) {
    if (i >= maxVisit) {
      truncated = true
      break
    }
    if (i % 32 === 0 && now() - started > maxMs) {
      truncated = true
      break
    }
    const node = list[i]
    if (!node) continue
    const kind = kindOf((node.tagName || '').toUpperCase())
    if (!kind) continue
    const raw = rawUrlOf(node, kind)
    if (!raw) continue
    const base = node.ownerDocument?.baseURI || rootBase
    let absolute: string
    try {
      absolute = base ? new URL(raw, base).href : new URL(raw).href
    } catch {
      continue
    }
    const canonical = canonicalizeDirectURL(absolute)
    if (!canonical) continue
    if (urlPathIsM3uPlaylist(canonical)) continue
    if (pageCanon && canonical === pageCanon) continue
    if (seen.has(canonical)) continue
    if (seen.size >= maxUnique) {
      truncated = true
      break
    }
    seen.add(canonical)
    const ownerPolicy =
      typeof node.ownerDocument?.referrerPolicy === 'string' && node.ownerDocument.referrerPolicy.trim() !== ''
        ? node.ownerDocument.referrerPolicy.trim()
        : docPolicy
    items.push({
      url: canonical,
      kind,
      filename: filenameFromCanonical(canonical),
      documentPolicy: ownerPolicy,
      elementPolicy: elementPolicyOf(node),
      relNoreferrer: kind === 'link' ? relHasNoreferrer(node) : false,
    })
  }
  return {
    items,
    truncated,
    title: typeof root.title === 'string' ? root.title : '',
  }
}
