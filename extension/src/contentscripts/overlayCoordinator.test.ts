import { describe, expect, it } from 'vitest'
import { extractorBusyForDomMutex } from './domPickerUiState'
import { overlayHostOpen, paintOverlayKind } from './overlayCoordinator'

describe('paintOverlayKind', () => {
  it('paints extractor when extractor is open or submitting, even if DOM is also open', () => {
    expect(
      paintOverlayKind({ extractorPhase: 'open', domPhase: 'open' }),
    ).toBe('extractor')
    expect(
      paintOverlayKind({ extractorPhase: 'submitting', domPhase: 'open' }),
    ).toBe('extractor')
    expect(overlayHostOpen('extractor')).toBe(true)
  })

  it('paints DOM when extractor is closed and DOM is open', () => {
    expect(
      paintOverlayKind({ extractorPhase: 'closed', domPhase: 'open' }),
    ).toBe('dom')
    expect(
      paintOverlayKind({ extractorPhase: 'closed', domPhase: 'submitting' }),
    ).toBe('dom')
    expect(overlayHostOpen('dom')).toBe(true)
  })

  it('paints none when both phases are closed', () => {
    expect(
      paintOverlayKind({ extractorPhase: 'closed', domPhase: 'closed' }),
    ).toBe('none')
    expect(overlayHostOpen('none')).toBe(false)
  })

  it('does not paint extractor from awaitingCatalog; mutex still blocks DOM', () => {
    expect(
      paintOverlayKind({ extractorPhase: 'closed', domPhase: 'closed' }),
    ).toBe('none')
    expect(overlayHostOpen('none')).toBe(false)
    expect(extractorBusyForDomMutex('closed', true)).toBe(true)
    expect(extractorBusyForDomMutex('closed', false)).toBe(false)
    expect(extractorBusyForDomMutex('open', false)).toBe(true)
  })
})
