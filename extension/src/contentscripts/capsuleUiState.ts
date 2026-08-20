import { sanitizeDisplayFilename } from '../background/extractorKeys'

export type CapsuleUi =
  | 'hidden'
  | 'idle'
  | 'resolving'
  | 'ready'
  | 'committing'
  | 'success'
  | 'error'

export type CapsuleState = {
  ui: CapsuleUi
  generation: number
  pageToken: string
  count: number
  filename: string
  errorCode: string
}

export const INITIAL_CAPSULE_STATE: CapsuleState = {
  ui: 'hidden',
  generation: 0,
  pageToken: '',
  count: 0,
  filename: '',
  errorCode: '',
}

export type CapsuleEvent =
  | { type: 'detect'; generation: number; pageToken: string; ignored?: boolean }
  | { type: 'hide'; reason?: string; pageToken?: string }
  | { type: 'clickAccepted' }
  | {
      type: 'result'
      pageToken: string
      ui: CapsuleUi
      count?: number
      filename?: string
      error_code?: string
    }
  | { type: 'watchdog' }

function isPainted(ui: CapsuleUi): boolean {
  return ui !== 'hidden'
}

export function applyCapsuleEvent(state: CapsuleState, event: CapsuleEvent): CapsuleState {
  switch (event.type) {
    case 'detect': {
      if (event.ignored) return state
      if (!event.pageToken) return state
      if (isPainted(state.ui) && state.pageToken === event.pageToken) {
        return { ...state, generation: event.generation }
      }
      return {
        ...INITIAL_CAPSULE_STATE,
        ui: 'idle',
        generation: event.generation,
        pageToken: event.pageToken,
      }
    }
    case 'hide': {
      if (event.pageToken && event.pageToken !== state.pageToken) return state
      return { ...INITIAL_CAPSULE_STATE }
    }
    case 'clickAccepted': {
      if (state.ui === 'hidden' || state.ui === 'success') return state
      if (state.ui === 'ready') return state
      if (state.ui === 'resolving' || state.ui === 'committing') return state
      return { ...state, ui: 'resolving', errorCode: '' }
    }
    case 'result': {
      if (event.pageToken !== state.pageToken) {
        if (state.ui !== 'hidden' || state.pageToken !== '') return state
      }
      if (state.ui === 'success' && event.ui !== 'hidden' && event.ui !== 'idle') {
        if (event.pageToken === state.pageToken && event.ui !== 'success') return state
      }
      return {
        ...state,
        ui: event.ui,
        pageToken: event.pageToken || state.pageToken,
        count: event.count ?? state.count,
        filename:
          event.filename === undefined
            ? state.filename
            : (sanitizeDisplayFilename(event.filename) ?? ''),
        errorCode: event.error_code ?? '',
      }
    }
    case 'watchdog': {
      if (state.ui === 'resolving' || state.ui === 'committing') {
        return { ...state, ui: 'error', errorCode: 'timeout' }
      }
      return state
    }
  }
}
