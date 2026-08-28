import { sanitizeDisplayFilename } from '../background/extractorKeys'
import type { DomPickerCatalogItem } from '../utils/messaging'

export type DomPickerPhase = 'closed' | 'open' | 'submitting'

export type DomPickerBanner = 'busy' | 'pending' | 'not_found' | ''

export type DomPickerState = {
  phase: DomPickerPhase
  catalogId: string
  items: DomPickerCatalogItem[]
  truncated: boolean
  storeUnproven: boolean
  folderPrefill: string
  banner: DomPickerBanner
}

export const INITIAL_DOM_PICKER_STATE: DomPickerState = {
  phase: 'closed',
  catalogId: '',
  items: [],
  truncated: false,
  storeUnproven: false,
  folderPrefill: '',
  banner: '',
}

export type DomPickerEvent =
  | {
      type: 'open'
      catalogId: string
      items: DomPickerCatalogItem[]
      truncated?: boolean
      storeUnproven?: boolean
      folderPrefill?: string
    }
  | { type: 'submit' }
  | { type: 'busy' }
  | { type: 'pending' }
  | { type: 'not_found' }
  | { type: 'close' }

function projectItems(items: unknown): DomPickerCatalogItem[] {
  if (!Array.isArray(items)) return []
  const out: DomPickerCatalogItem[] = []
  for (let i = 0; i < items.length; i++) {
    const item: DomPickerCatalogItem = { index: i }
    const row = items[i]
    if (row && typeof row === 'object' && !Array.isArray(row)) {
      const rec = row as Record<string, unknown>
      const filename = sanitizeDisplayFilename(rec.filename)
      if (filename) item.filename = filename
      const origin = sanitizeDisplayFilename(rec.origin)
      if (origin) item.origin = origin
      if (
        rec.kind === 'link' ||
        rec.kind === 'image' ||
        rec.kind === 'video' ||
        rec.kind === 'audio' ||
        rec.kind === 'source'
      ) {
        item.kind = rec.kind
      }
      if (typeof rec.size_bytes === 'number' && Number.isFinite(rec.size_bytes)) {
        item.size_bytes = rec.size_bytes
      }
      if (typeof rec.index === 'number' && Number.isInteger(rec.index)) item.index = rec.index
    }
    out.push(item)
  }
  return out
}

export function applyDomPickerEvent(state: DomPickerState, event: DomPickerEvent): DomPickerState {
  switch (event.type) {
    case 'open': {
      if (!event.catalogId || !Array.isArray(event.items) || event.items.length === 0) return state
      const items = projectItems(event.items)
      if (items.length === 0) return state
      return {
        phase: 'open',
        catalogId: event.catalogId,
        items,
        truncated: event.truncated === true,
        storeUnproven: event.storeUnproven === true,
        folderPrefill: typeof event.folderPrefill === 'string' ? event.folderPrefill : '',
        banner: '',
      }
    }
    case 'submit': {
      if (state.phase !== 'open') return state
      return { ...state, phase: 'submitting', banner: '' }
    }
    case 'busy': {
      if (state.phase === 'closed') return state
      return { ...state, phase: 'open', banner: 'busy' }
    }
    case 'pending': {
      if (state.phase === 'closed') return state
      return { ...state, phase: 'submitting', banner: 'pending' }
    }
    case 'not_found':
    case 'close':
      return { ...INITIAL_DOM_PICKER_STATE }
  }
}
