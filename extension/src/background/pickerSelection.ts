import { EXTRACTOR_MAX_SESSION_ITEMS, EXTRACTOR_PICKER_WINDOW } from './extractorKeys'

export function selectableCount(
  length: number,
  hardMax: number = EXTRACTOR_MAX_SESSION_ITEMS,
): number {
  if (!Number.isFinite(length) || length <= 0) return 0
  return Math.min(Math.floor(length), hardMax)
}

export function defaultSelectedIndices(
  length: number,
  windowSize: number = EXTRACTOR_PICKER_WINDOW,
  hardMax: number = EXTRACTOR_MAX_SESSION_ITEMS,
): number[] {
  const n = Math.min(selectableCount(length, hardMax), Math.max(0, windowSize))
  const out: number[] = []
  for (let i = 0; i < n; i++) out.push(i)
  return out
}

export type PickerSelectPolicy = 'window' | 'empty'

export function initialPickerSelection(policy: PickerSelectPolicy, length: number): number[] {
  switch (policy) {
    case 'empty':
      return []
    case 'window':
      return defaultSelectedIndices(length)
    default: {
      const _exhaustive: never = policy
      throw new Error(`unknown picker select policy: ${String(_exhaustive)}`)
    }
  }
}

export function toggleIndex(
  selected: ReadonlySet<number>,
  index: number,
  selectable: number,
): Set<number> {
  const next = new Set(selected)
  if (!Number.isInteger(index) || index < 0 || index >= selectable) return next
  if (next.has(index)) next.delete(index)
  else next.add(index)
  return next
}

export function selectAll(selectable: number): Set<number> {
  const next = new Set<number>()
  const n = Math.max(0, selectable)
  for (let i = 0; i < n; i++) next.add(i)
  return next
}

export function invert(selected: ReadonlySet<number>, selectable: number): Set<number> {
  const next = new Set<number>()
  for (let i = 0; i < selectable; i++) {
    if (!selected.has(i)) next.add(i)
  }
  return next
}

export function selectedBytes(
  displayItems: ReadonlyArray<{ size_bytes?: number } | undefined>,
  selected: ReadonlySet<number>,
): number {
  let total = 0
  for (const index of selected) {
    if (!Number.isInteger(index) || index < 0 || index >= displayItems.length) continue
    const size = displayItems[index]?.size_bytes
    if (typeof size === 'number' && Number.isFinite(size)) total += size
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
