import type { ExtractorDisplayItem } from './extractorSessionStore'

function asIdSet(value: unknown): Set<string> {
  const out = new Set<string>()
  if (!Array.isArray(value)) return out
  for (const id of value) {
    if (typeof id === 'string' && id !== '') out.add(id)
  }
  return out
}

export function remainingItemIds(requested: string[], ack: unknown): string[] {
  if (!Array.isArray(requested)) return []
  const rec = ack && typeof ack === 'object' && !Array.isArray(ack) ? (ack as Record<string, unknown>) : {}
  const succeeded = asIdSet(rec.succeeded_item_ids)
  const duplicates = asIdSet(rec.duplicate_item_ids)
  const remaining: string[] = []
  for (const id of requested) {
    if (typeof id !== 'string' || id === '') continue
    if (succeeded.has(id) || duplicates.has(id)) continue
    remaining.push(id)
  }
  return remaining
}

export function hasItemOutcomeLists(ack: unknown): boolean {
  if (!ack || typeof ack !== 'object' || Array.isArray(ack)) return false
  const rec = ack as Record<string, unknown>
  if (Array.isArray(rec.succeeded_item_ids) && rec.succeeded_item_ids.length > 0) return true
  if (Array.isArray(rec.duplicate_item_ids) && rec.duplicate_item_ids.length > 0) return true
  if (rec.errors_by_item_id && typeof rec.errors_by_item_id === 'object' && !Array.isArray(rec.errors_by_item_id)) {
    return Object.keys(rec.errors_by_item_id).length > 0
  }
  return false
}

export type ShrinkDisplayResult =
  | { itemIds: string[]; displayItems: ExtractorDisplayItem[] }
  | { error: string }

export function shrinkDisplayItems(
  itemIds: string[],
  displayItems: ExtractorDisplayItem[] | undefined,
  remainingIds: string[],
): ShrinkDisplayResult {
  if (!Array.isArray(itemIds) || !Array.isArray(displayItems)) {
    return { error: 'invalid_request' }
  }
  if (itemIds.length !== displayItems.length) {
    return { error: 'invalid_request' }
  }
  const indexById = new Map<string, number>()
  for (let i = 0; i < itemIds.length; i++) {
    const id = itemIds[i]
    if (typeof id !== 'string' || indexById.has(id)) continue
    indexById.set(id, i)
  }
  const nextIds: string[] = []
  const nextDisplay: ExtractorDisplayItem[] = []
  for (const id of remainingIds) {
    const idx = indexById.get(id)
    if (idx === undefined) return { error: 'invalid_request' }
    nextIds.push(id)
    nextDisplay.push(displayItems[idx] ?? {})
  }
  return { itemIds: nextIds, displayItems: nextDisplay }
}

export function zipDisplayItems(
  itemIds: string[] | undefined,
  displayItems: ExtractorDisplayItem[] | undefined,
  requested: string[],
): ShrinkDisplayResult {
  return shrinkDisplayItems(itemIds ?? requested, displayItems ?? [], requested)
}
