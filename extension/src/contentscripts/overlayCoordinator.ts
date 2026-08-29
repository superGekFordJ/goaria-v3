import type { BurstPickerPhase } from './burstPickerUiState'
import type { DomPickerPhase } from './domPickerUiState'
import type { PickerPhase } from './pickerUiState'

export type OverlayPaint = 'none' | 'extractor' | 'dom' | 'burst'

export type OverlayPaintInput = {
  extractorPhase: PickerPhase
  domPhase: DomPickerPhase
  burstPhase: BurstPickerPhase
}

export function paintOverlayKind(input: OverlayPaintInput): OverlayPaint {
  if (input.extractorPhase !== 'closed') return 'extractor'
  if (input.domPhase !== 'closed') return 'dom'
  if (input.burstPhase !== 'closed') return 'burst'
  return 'none'
}

export function overlayHostOpen(kind: OverlayPaint): boolean {
  return kind !== 'none'
}
