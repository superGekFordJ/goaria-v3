import { describe, expect, it } from 'vitest'
import {
  applyBurstPickerEvent,
  burstBusyForOverlay,
  INITIAL_BURST_PICKER_STATE,
} from './burstPickerUiState'
import type { BurstPickerCatalogItem } from '../utils/messaging'

const ITEMS: BurstPickerCatalogItem[] = [
  { index: 0, filename: 'a.bin', origin: 'https://example.com', path: '/a.bin' },
  { index: 1, filename: 'b.bin', origin: 'https://example.com', path: '/b.bin' },
]

describe('applyBurstPickerEvent', () => {
  it('opens by captureId, submits, keeps overlay on busy, and closes', () => {
    const opened = applyBurstPickerEvent(INITIAL_BURST_PICKER_STATE, {
      type: 'open',
      captureId: 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee',
      items: ITEMS,
      storeUnproven: true,
    })
    expect(opened.phase).toBe('open')
    expect(opened.captureId).toBe('aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee')
    expect(opened.storeUnproven).toBe(true)
    expect('pageToken' in opened).toBe(false)
    expect('leaseDeadline' in opened).toBe(false)
    expect('readyRestore' in opened).toBe(false)
    expect('awaitingCatalog' in opened).toBe(false)

    const submitting = applyBurstPickerEvent(opened, { type: 'submit' })
    expect(submitting.phase).toBe('submitting')

    const busy = applyBurstPickerEvent(submitting, { type: 'busy' })
    expect(busy.phase).toBe('open')
    expect(busy.banner).toBe('busy')
    expect(busy.captureId).toBe(opened.captureId)

    expect(applyBurstPickerEvent(busy, { type: 'close' })).toEqual(INITIAL_BURST_PICKER_STATE)
  })

  it('keeps the overlay open when the cookie store is lost', () => {
    const opened = applyBurstPickerEvent(INITIAL_BURST_PICKER_STATE, {
      type: 'open',
      captureId: 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee',
      items: ITEMS,
    })
    const submitting = applyBurstPickerEvent(opened, { type: 'submit' })
    const lost = applyBurstPickerEvent(submitting, { type: 'storeUnproven' })
    expect(lost.phase).toBe('open')
    expect(lost.storeUnproven).toBe(true)
    expect(lost.captureId).toBe(opened.captureId)
  })
})

describe('burstBusyForOverlay', () => {
  it('is independent of awaitingCatalog', () => {
    expect(burstBusyForOverlay('closed')).toBe(false)
    expect(burstBusyForOverlay('open')).toBe(true)
    expect(burstBusyForOverlay('submitting')).toBe(true)
  })
})
