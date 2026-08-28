import type { DomPickerPhase } from './domPickerUiState'
import type { PickerPhase } from './pickerUiState'

export type OverlayPaint = 'none' | 'extractor' | 'dom'

export type OverlayPaintInput = {
  extractorPhase: PickerPhase
  domPhase: DomPickerPhase
}

export function paintOverlayKind(input: OverlayPaintInput): OverlayPaint {
  if (input.extractorPhase !== 'closed') return 'extractor'
  if (input.domPhase !== 'closed') return 'dom'
  return 'none'
}

export function overlayHostOpen(kind: OverlayPaint): boolean {
  return kind !== 'none'
}
