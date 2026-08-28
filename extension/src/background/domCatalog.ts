import { EXTRACTOR_LEASE_MS, EXTRACTOR_MAX_SESSION_ITEMS, sanitizeDisplayFilename } from './extractorKeys'
import { currentDirectConnectGeneration } from './domConnectGeneration'
import { mintDirectBatchRequestId } from './mintRequestId'

export type DomLinkKind = 'link' | 'image' | 'video' | 'audio' | 'source'

export const DOM_CATALOG_TTL_MS = EXTRACTOR_LEASE_MS

export type DomReferrerInputs = {
  documentPolicy: string
  elementPolicy: string
  relNoreferrer: boolean
}

export type DomCatalogItem = {
  url: string
  filename?: string
  kind: DomLinkKind
  referrer: DomReferrerInputs
}

export type DomLastSubmit = {
  requestId: string
  indicesKey: string
  folderKey: string
  payload: Record<string, unknown>
}

export type DomCatalog = {
  catalogId: string
  directConnectGeneration: number
  tabId: number
  frameId: 0
  documentNonce: string
  pageHref: string
  incognito: boolean
  cookieStoreId?: string
  storeUnproven: boolean
  createdAt: number
  truncated: boolean
  items: DomCatalogItem[]
  folderPrefill?: string
  lastSubmit?: DomLastSubmit
}

export type DomPickerProjectionItem = {
  index: number
  filename?: string
  origin?: string
  path?: string
  kind?: DomLinkKind
  size_bytes?: number
}

export type DomPickerProjection = {
  catalog_id: string
  items: DomPickerProjectionItem[]
  truncated: boolean
  store_unproven: boolean
  folder_prefill?: string
}

const byTab = new Map<number, DomCatalog>()
const byId = new Map<string, DomCatalog>()

function originOf(url: string): string | undefined {
  try {
    return sanitizeDisplayFilename(new URL(url).origin)
  } catch {
    return undefined
  }
}

function isExpired(catalog: DomCatalog, now: number): boolean {
  return now - catalog.createdAt > DOM_CATALOG_TTL_MS
}

function drop(catalog: DomCatalog): void {
  if (byTab.get(catalog.tabId) === catalog) byTab.delete(catalog.tabId)
  byId.delete(catalog.catalogId)
}

export function invalidateDomCatalogByTab(tabId: number): string | undefined {
  const existing = byTab.get(tabId)
  if (!existing) return undefined
  drop(existing)
  return existing.catalogId
}

export function invalidateDomCatalogById(catalogId: string): boolean {
  const existing = byId.get(catalogId)
  if (!existing) return false
  drop(existing)
  return true
}

export function invalidateAllDomCatalogs(): void {
  byTab.clear()
  byId.clear()
}

export type PutDomCatalogInput = {
  tabId: number
  documentNonce: string
  pageHref: string
  incognito: boolean
  cookieStoreId?: string
  storeUnproven: boolean
  truncated: boolean
  items: DomCatalogItem[]
  folderPrefill?: string
  now?: number
  catalogId?: string
  generation?: number
}

export function putDomCatalog(input: PutDomCatalogInput): DomCatalog {
  invalidateDomCatalogByTab(input.tabId)
  const catalog: DomCatalog = {
    catalogId: input.catalogId ?? mintDirectBatchRequestId(),
    directConnectGeneration: input.generation ?? currentDirectConnectGeneration(),
    tabId: input.tabId,
    frameId: 0,
    documentNonce: input.documentNonce,
    pageHref: input.pageHref,
    incognito: input.incognito,
    cookieStoreId: input.cookieStoreId,
    storeUnproven: input.storeUnproven,
    createdAt: input.now ?? Date.now(),
    truncated: input.truncated,
    items: input.items.slice(0, EXTRACTOR_MAX_SESSION_ITEMS),
    folderPrefill: input.folderPrefill,
  }
  byTab.set(catalog.tabId, catalog)
  byId.set(catalog.catalogId, catalog)
  return catalog
}

export function getDomCatalog(catalogId: string, now: number = Date.now()): DomCatalog | undefined {
  const catalog = byId.get(catalogId)
  if (!catalog) return undefined
  if (isExpired(catalog, now)) {
    drop(catalog)
    return undefined
  }
  return catalog
}

export function getDomCatalogForTab(tabId: number, now: number = Date.now()): DomCatalog | undefined {
  const catalog = byTab.get(tabId)
  if (!catalog) return undefined
  if (isExpired(catalog, now)) {
    drop(catalog)
    return undefined
  }
  return catalog
}

export function isDomCatalogAlive(catalogId: string, now: number = Date.now()): boolean {
  const catalog = getDomCatalog(catalogId, now)
  if (!catalog) return false
  return catalog.directConnectGeneration === currentDirectConnectGeneration()
}

export function mapDomCatalogIndices(
  catalog: DomCatalog,
  indices: unknown,
): { items: DomCatalogItem[] } | { error: 'invalid_request' } {
  if (!Array.isArray(indices) || indices.length === 0) return { error: 'invalid_request' }
  if (indices.length > catalog.items.length || indices.length > EXTRACTOR_MAX_SESSION_ITEMS) {
    return { error: 'invalid_request' }
  }
  const seen = new Set<number>()
  const mapped: DomCatalogItem[] = []
  for (const raw of indices) {
    if (typeof raw !== 'number' || !Number.isInteger(raw)) return { error: 'invalid_request' }
    if (raw < 0 || raw >= catalog.items.length || seen.has(raw)) return { error: 'invalid_request' }
    const item = catalog.items[raw]
    if (!item) return { error: 'invalid_request' }
    seen.add(raw)
    mapped.push(item)
  }
  return { items: mapped }
}

function pathOf(url: string): string | undefined {
  try {
    return sanitizeDisplayFilename(new URL(url).pathname)
  } catch {
    return undefined
  }
}

export function projectDomCatalog(catalog: DomCatalog): DomPickerProjection {
  const items: DomPickerProjectionItem[] = catalog.items.map((item, index) => {
    const row: DomPickerProjectionItem = { index, kind: item.kind }
    const filename = sanitizeDisplayFilename(item.filename)
    if (filename) row.filename = filename
    const origin = originOf(item.url)
    if (origin) row.origin = origin
    const path = pathOf(item.url)
    if (path) row.path = path
    return row
  })
  const out: DomPickerProjection = {
    catalog_id: catalog.catalogId,
    items,
    truncated: catalog.truncated,
    store_unproven: catalog.storeUnproven,
  }
  if (catalog.folderPrefill) out.folder_prefill = catalog.folderPrefill
  return out
}

export function rememberDomSubmit(catalog: DomCatalog, submit: DomLastSubmit): void {
  catalog.lastSubmit = submit
}

export function resetDomCatalogsForTests(): void {
  invalidateAllDomCatalogs()
}
