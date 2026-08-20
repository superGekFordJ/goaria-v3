import {
  EXTRACTOR_MAX_SESSION_ITEMS,
  sanitizeDisplayFilename,
} from './extractorKeys'
import type { ExtractorDisplayItem } from './extractorSessionStore'
import type { PickerCatalogItem } from '../utils/messaging'

export type { PickerCatalogItem }

export type BuildPickerCatalogResult =
  | { items: PickerCatalogItem[]; count: number; lease_deadline?: number }
  | { error: string }

export function buildPickerCatalog(
  itemIds: unknown,
  displayItems: unknown,
  leaseDeadline?: unknown,
  opts?: { minItems?: number },
): BuildPickerCatalogResult {
  if (!Array.isArray(itemIds) || !Array.isArray(displayItems)) {
    return { error: 'invalid_request' }
  }
  if (itemIds.length !== displayItems.length) {
    return { error: 'invalid_request' }
  }
  const minItems = opts?.minItems ?? 2
  if (itemIds.length < minItems || itemIds.length > EXTRACTOR_MAX_SESSION_ITEMS) {
    return { error: 'invalid_request' }
  }
  if (!itemIds.every(id => typeof id === 'string' && id !== '')) {
    return { error: 'invalid_request' }
  }
  const items: PickerCatalogItem[] = []
  for (let index = 0; index < itemIds.length; index++) {
    const row = projectRow(displayItems[index], index)
    items.push(row)
  }
  const out: { items: PickerCatalogItem[]; count: number; lease_deadline?: number } = {
    items,
    count: items.length,
  }
  if (typeof leaseDeadline === 'number' && Number.isFinite(leaseDeadline)) {
    out.lease_deadline = leaseDeadline
  }
  return out
}

function projectRow(value: unknown, index: number): PickerCatalogItem {
  const item: PickerCatalogItem = { index }
  if (!value || typeof value !== 'object' || Array.isArray(value)) return item
  const rec = value as ExtractorDisplayItem
  const filename = sanitizeDisplayFilename(rec.filename)
  if (filename) item.filename = filename
  if (typeof rec.size_bytes === 'number') item.size_bytes = rec.size_bytes
  if (typeof rec.mime_type === 'string' && rec.mime_type !== '') item.mime_type = rec.mime_type
  return item
}

export function mapPickerIndices(
  indices: unknown,
  itemIds: string[],
): { itemIds: string[] } | { error: string } {
  if (!Array.isArray(indices) || indices.length === 0) {
    return { error: 'invalid_request' }
  }
  if (indices.length > itemIds.length || indices.length > EXTRACTOR_MAX_SESSION_ITEMS) {
    return { error: 'invalid_request' }
  }
  const seen = new Set<number>()
  const mapped: string[] = []
  for (const raw of indices) {
    if (typeof raw !== 'number' || !Number.isInteger(raw)) {
      return { error: 'invalid_request' }
    }
    if (raw < 0 || raw >= itemIds.length || seen.has(raw)) {
      return { error: 'invalid_request' }
    }
    seen.add(raw)
    mapped.push(itemIds[raw] as string)
  }
  return { itemIds: mapped }
}

export function sameIdList(left: string[], right: string[]): boolean {
  if (left.length !== right.length) return false
  return left.every((id, i) => id === right[i])
}
