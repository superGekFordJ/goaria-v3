import { EXTRACTOR_MAX_SESSION_ITEMS, EXTRACTOR_PICKER_WINDOW } from './extractorKeys'

export type ItemWithIndex = { index: number; size_bytes?: number }

export function selectableCount(
  length: number,
  hardMax: number = EXTRACTOR_MAX_SESSION_ITEMS,
): number {
  if (!Number.isFinite(length) || length <= 0) return 0
  return Math.min(Math.floor(length), hardMax)
}

export function defaultSelectedIndices(
  itemsOrLength: ReadonlyArray<ItemWithIndex> | number,
  windowSize: number = EXTRACTOR_PICKER_WINDOW,
  hardMax: number = EXTRACTOR_MAX_SESSION_ITEMS,
): number[] {
  if (typeof itemsOrLength === 'number') {
    const n = Math.min(selectableCount(itemsOrLength, hardMax), Math.max(0, windowSize))
    const out: number[] = []
    for (let i = 0; i < n; i++) out.push(i)
    return out
  }

  const items = Array.isArray(itemsOrLength) ? itemsOrLength : []
  const n = Math.min(selectableCount(items.length, hardMax), Math.max(0, windowSize))
  const out: number[] = []
  for (let i = 0; i < n; i++) {
    const item = items[i]
    if (item && typeof item.index === 'number') {
      out.push(item.index)
    }
  }
  return out
}

export type PickerSelectPolicy = 'window' | 'empty'

export function initialPickerSelection(
  policy: PickerSelectPolicy,
  itemsOrLength: ReadonlyArray<ItemWithIndex> | number,
): number[] {
  switch (policy) {
    case 'empty':
      return []
    case 'window':
      return defaultSelectedIndices(itemsOrLength)
    default: {
      const _exhaustive: never = policy
      throw new Error(`unknown picker select policy: ${String(_exhaustive)}`)
    }
  }
}

export function toggleIndex(
  selected: ReadonlySet<number>,
  index: number,
  selectable: ReadonlySet<number> | ReadonlyArray<number> | number,
): Set<number> {
  const next = new Set(selected)
  if (!Number.isInteger(index)) return next

  if (typeof selectable === 'number') {
    if (index < 0 || index >= selectable) return next
  } else if (selectable instanceof Set) {
    if (!selectable.has(index)) return next
  } else if (Array.isArray(selectable)) {
    if (!selectable.includes(index)) return next
  } else {
    return next
  }

  if (next.has(index)) next.delete(index)
  else next.add(index)
  return next
}

export function selectAll(
  selectable: ReadonlyArray<number> | ReadonlySet<number> | number,
  existingSelected?: ReadonlySet<number>,
): Set<number> {
  const next = existingSelected ? new Set(existingSelected) : new Set<number>()
  if (typeof selectable === 'number') {
    const n = Math.max(0, selectable)
    for (let i = 0; i < n; i++) next.add(i)
    return next
  }
  for (const idx of selectable) {
    if (Number.isInteger(idx)) next.add(idx)
  }
  return next
}

export function invert(
  selected: ReadonlySet<number>,
  selectable: ReadonlyArray<number> | ReadonlySet<number> | number,
): Set<number> {
  if (typeof selectable === 'number') {
    const next = new Set<number>()
    for (let i = 0; i < selectable; i++) {
      if (!selected.has(i)) next.add(i)
    }
    return next
  }

  const next = new Set(selected)
  for (const idx of selectable) {
    if (!Number.isInteger(idx)) continue
    if (next.has(idx)) next.delete(idx)
    else next.add(idx)
  }
  return next
}

export function selectedBytes(
  displayItems: ReadonlyArray<{ index?: number; size_bytes?: number } | undefined>,
  selected: ReadonlySet<number>,
): number {
  if (!displayItems || displayItems.length === 0 || selected.size === 0) return 0
  let total = 0
  for (let i = 0; i < displayItems.length; i++) {
    const item = displayItems[i]
    if (!item) continue
    const idx = typeof item.index === 'number' ? item.index : i
    if (!selected.has(idx)) continue
    const size = item.size_bytes
    if (typeof size === 'number' && Number.isSafeInteger(size) && size > 0) {
      total += size
    }
  }
  return total
}

export function visibleWindow(
  activeIndex: number,
  length: number,
  windowSize: number,
): { start: number; end: number } {
  if (length <= 0) return { start: 0, end: 0 }
  const size = Math.min(Math.max(1, windowSize), length)
  let start = 0
  const active = Math.min(Math.max(0, activeIndex), length - 1)
  if (active < start) start = active
  if (active >= start + size) start = active - size + 1
  const maxStart = Math.max(0, length - size)
  start = Math.min(Math.max(0, start), maxStart)
  return { start, end: start + size }
}
