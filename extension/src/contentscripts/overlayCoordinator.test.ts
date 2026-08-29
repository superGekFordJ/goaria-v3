import { describe, expect, it } from 'vitest'
import { extractorBusyForDomMutex } from './domPickerUiState'
import { burstBusyForOverlay } from './burstPickerUiState'
import {
  overlayHostOpen,
  paintOverlayKind,
  type OverlayPaintInput,
} from './overlayCoordinator'

type PaintInputKeys = keyof OverlayPaintInput
type PaintInputHasOnlyPhases = PaintInputKeys extends 'extractorPhase' | 'domPhase' | 'burstPhase'
  ? 'extractorPhase' | 'domPhase' | 'burstPhase' extends PaintInputKeys
    ? true
    : never
  : never
type PaintInputOmitsAwaitingCatalog = 'awaitingCatalog' extends keyof OverlayPaintInput ? never : true

describe('paintOverlayKind', () => {
  it('paints extractor when extractor is open or submitting, even if DOM is also open', () => {
    expect(
      paintOverlayKind({ extractorPhase: 'open', domPhase: 'open', burstPhase: 'open' }),
    ).toBe('extractor')
    expect(
      paintOverlayKind({ extractorPhase: 'submitting', domPhase: 'open', burstPhase: 'closed' }),
    ).toBe('extractor')
    expect(overlayHostOpen('extractor')).toBe(true)
  })

  it('paints DOM when extractor is closed and DOM is open', () => {
    expect(
      paintOverlayKind({ extractorPhase: 'closed', domPhase: 'open', burstPhase: 'open' }),
    ).toBe('dom')
    expect(
      paintOverlayKind({ extractorPhase: 'closed', domPhase: 'submitting', burstPhase: 'closed' }),
    ).toBe('dom')
    expect(overlayHostOpen('dom')).toBe(true)
  })

  it('paints burst when extractor and DOM are closed', () => {
    expect(
      paintOverlayKind({ extractorPhase: 'closed', domPhase: 'closed', burstPhase: 'open' }),
    ).toBe('burst')
    expect(overlayHostOpen('burst')).toBe(true)
  })

  it('paints none when all phases are closed', () => {
    expect(
      paintOverlayKind({ extractorPhase: 'closed', domPhase: 'closed', burstPhase: 'closed' }),
    ).toBe('none')
    expect(overlayHostOpen('none')).toBe(false)
  })

  it('does not paint extractor from awaitingCatalog; mutex still blocks DOM', () => {
    const _onlyPhases: PaintInputHasOnlyPhases = true
    const _noAwaiting: PaintInputOmitsAwaitingCatalog = true
    expect(_onlyPhases).toBe(true)
    expect(_noAwaiting).toBe(true)

    const input: OverlayPaintInput = {
      extractorPhase: 'closed',
      domPhase: 'closed',
      burstPhase: 'closed',
    }
    expect(paintOverlayKind(input)).toBe('none')
    expect(overlayHostOpen('none')).toBe(false)
    expect(extractorBusyForDomMutex('closed', true)).toBe(true)
    expect(extractorBusyForDomMutex('closed', false)).toBe(false)
    expect(extractorBusyForDomMutex('open', false)).toBe(true)
    expect(burstBusyForOverlay('closed')).toBe(false)
    expect(burstBusyForOverlay('open')).toBe(true)
  })
})
