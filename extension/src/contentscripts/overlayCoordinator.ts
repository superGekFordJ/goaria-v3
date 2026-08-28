export type OverlayPaint = 'none' | 'extractor' | 'dom'

export type OverlayPaintInput = {
  extractorPhase: string
  domPhase: string
}

export function paintOverlayKind(input: OverlayPaintInput): OverlayPaint {
  if (input.extractorPhase !== 'closed') return 'extractor'
  if (input.domPhase !== 'closed') return 'dom'
  return 'none'
}

export function overlayHostOpen(kind: OverlayPaint): boolean {
  return kind !== 'none'
}
