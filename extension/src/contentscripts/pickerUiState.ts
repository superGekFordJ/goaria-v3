import type { PickerCatalogItem } from '../utils/messaging'
import { sanitizeDisplayFilename } from '../background/extractorKeys'

export type PickerPhase = 'closed' | 'open' | 'submitting'

export type PickerState = {
  phase: PickerPhase
  pageToken: string
  items: PickerCatalogItem[]
  count: number
  leaseDeadline?: number
  awaitingCatalog?: boolean
}

export const INITIAL_PICKER_STATE: PickerState = {
  phase: 'closed',
  pageToken: '',
  items: [],
  count: 0,
}

export type PickerEvent =
  | {
      type: 'open'
      pageToken: string
      items: PickerCatalogItem[]
      count?: number
      lease_deadline?: number
      awaitingCatalog?: boolean
    }
  | {
      type: 'catalog'
      pageToken: string
      items: PickerCatalogItem[]
      count?: number
      lease_deadline?: number
    }
  | { type: 'submit' }
  | { type: 'close' }
  | { type: 'hide' }
  | { type: 'readyRestore'; pageToken?: string }

export function projectCatalogItems(items: unknown): PickerCatalogItem[] {
  if (!Array.isArray(items)) return []
  const out: PickerCatalogItem[] = []
  for (let i = 0; i < items.length; i++) {
    const item: PickerCatalogItem = { index: i }
    const row = items[i]
    if (row && typeof row === 'object' && !Array.isArray(row)) {
      const rec = row as Record<string, unknown>
      const filename = sanitizeDisplayFilename(rec.filename)
      if (filename) item.filename = filename
      if (typeof rec.size_bytes === 'number' && Number.isFinite(rec.size_bytes)) item.size_bytes = rec.size_bytes
      if (typeof rec.mime_type === 'string' && rec.mime_type !== '') item.mime_type = rec.mime_type
    }
    out.push(item)
  }
  return out
}

export function pickerEventForCapsuleUi(ui: string): Extract<PickerEvent, { type: 'hide' | 'close' }> | null {
  if (ui === 'hidden') return { type: 'hide' }
  if (ui === 'success' || ui === 'error') return { type: 'close' }
  return null
}

export function applyPickerEvent(state: PickerState, event: PickerEvent): PickerState {
  switch (event.type) {
    case 'open': {
      if (!event.pageToken || !Array.isArray(event.items) || event.items.length === 0) return state
      const items = projectCatalogItems(event.items)
      if (items.length === 0) return state
      return {
        phase: 'open',
        pageToken: event.pageToken,
        items,
        count: event.count ?? items.length,
        leaseDeadline: event.lease_deadline,
        awaitingCatalog: event.awaitingCatalog === true ? true : undefined,
      }
    }
    case 'catalog': {
      if (state.phase === 'closed' && !state.awaitingCatalog) return state
      if (event.pageToken !== state.pageToken) return state
      if (!Array.isArray(event.items)) return state
      const items = projectCatalogItems(event.items)
      const fromClosed = state.phase === 'closed'
      return {
        phase: 'open',
        pageToken: state.pageToken,
        items,
        count: event.count ?? items.length,
        leaseDeadline: event.lease_deadline ?? state.leaseDeadline,
        awaitingCatalog: fromClosed ? undefined : state.awaitingCatalog,
      }
    }
    case 'submit': {
      if (state.phase !== 'open') return state
      return { ...state, phase: 'submitting', awaitingCatalog: true }
    }
    case 'close':
    case 'hide':
      return { ...INITIAL_PICKER_STATE }
    case 'readyRestore': {
      if (state.phase === 'closed') return state
      if (event.pageToken && event.pageToken !== state.pageToken) return state
      if (state.phase === 'open' && state.awaitingCatalog) {
        return { ...state, awaitingCatalog: false }
      }
      if (state.phase === 'submitting') {
        return {
          ...INITIAL_PICKER_STATE,
          pageToken: state.pageToken,
          awaitingCatalog: true,
        }
      }
      return { ...INITIAL_PICKER_STATE }
    }
  }
}
