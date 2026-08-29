import { sanitizeDisplayFilename } from '../background/extractorKeys'
import type { BurstPickerCatalogItem } from '../utils/messaging'

export type BurstPickerPhase = 'closed' | 'open' | 'submitting'

export type BurstPickerBanner = 'busy' | 'pending' | ''

export type BurstPickerState = {
  phase: BurstPickerPhase
  captureId: string
  items: BurstPickerCatalogItem[]
  storeUnproven: boolean
  banner: BurstPickerBanner
}

export const INITIAL_BURST_PICKER_STATE: BurstPickerState = {
  phase: 'closed',
  captureId: '',
  items: [],
  storeUnproven: false,
  banner: '',
}

export type BurstPickerEvent =
  | {
      type: 'open'
      captureId: string
      items: BurstPickerCatalogItem[]
      storeUnproven?: boolean
    }
  | { type: 'submit' }
  | { type: 'busy' }
  | { type: 'pending' }
  | { type: 'close' }

function projectItems(items: unknown): BurstPickerCatalogItem[] {
  if (!Array.isArray(items)) return []
  const out: BurstPickerCatalogItem[] = []
  for (let i = 0; i < items.length; i++) {
    const item: BurstPickerCatalogItem = { index: i }
    const row = items[i]
    if (row && typeof row === 'object' && !Array.isArray(row)) {
      const rec = row as Record<string, unknown>
      const filename = sanitizeDisplayFilename(rec.filename)
      if (filename) item.filename = filename
      const origin = sanitizeDisplayFilename(rec.origin)
      if (origin) item.origin = origin
      const path = sanitizeDisplayFilename(rec.path)
      if (path) item.path = path
      if (typeof rec.size_bytes === 'number' && Number.isFinite(rec.size_bytes)) {
        item.size_bytes = rec.size_bytes
      }
      if (typeof rec.index === 'number' && Number.isInteger(rec.index)) item.index = rec.index
    }
    out.push(item)
  }
  return out
}

export function burstBusyForOverlay(phase: BurstPickerPhase): boolean {
  return phase !== 'closed'
}

export function isCurrentBurstCapture(requestedId: string, currentId: string): boolean {
  return requestedId !== '' && requestedId === currentId
}

export function applyBurstPickerEvent(state: BurstPickerState, event: BurstPickerEvent): BurstPickerState {
  switch (event.type) {
    case 'open': {
      if (!event.captureId || !Array.isArray(event.items) || event.items.length === 0) return state
      const items = projectItems(event.items)
      if (items.length === 0) return state
      return {
        phase: 'open',
        captureId: event.captureId,
        items,
        storeUnproven: event.storeUnproven === true,
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
    case 'close':
      return { ...INITIAL_BURST_PICKER_STATE }
  }
}
