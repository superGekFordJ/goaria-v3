import { describe, expect, it } from 'vitest'
import {
  applyDomPickerEvent,
  extractorBusyForDomMutex,
  isCurrentDomCatalog,
  INITIAL_DOM_PICKER_STATE,
} from './domPickerUiState'
import type { DomPickerCatalogItem } from '../utils/messaging'

const ITEMS: DomPickerCatalogItem[] = [
  { index: 0, filename: 'a.bin', origin: 'https://example.com', kind: 'link' },
  { index: 1, filename: 'b.png', origin: 'https://example.com', kind: 'image' },
]

describe('applyDomPickerEvent', () => {
  it('opens by catalog_id, submits, keeps overlay on busy, and closes', () => {
    const opened = applyDomPickerEvent(INITIAL_DOM_PICKER_STATE, {
      type: 'open',
      catalogId: '11111111-1111-4111-8111-111111111111',
      items: ITEMS,
      truncated: true,
      storeUnproven: true,
      folderPrefill: 'Example',
    })
    expect(opened.phase).toBe('open')
    expect(opened.catalogId).toBe('11111111-1111-4111-8111-111111111111')
    expect(opened.truncated).toBe(true)
    expect(opened.storeUnproven).toBe(true)
    expect(opened.folderPrefill).toBe('Example')
    expect(JSON.stringify(opened)).not.toContain('?')

    const submitting = applyDomPickerEvent(opened, { type: 'submit' })
    expect(submitting.phase).toBe('submitting')

    const busy = applyDomPickerEvent(submitting, { type: 'busy' })
    expect(busy.phase).toBe('open')
    expect(busy.banner).toBe('busy')
    expect(busy.catalogId).toBe(opened.catalogId)
    expect(busy.items).toEqual(ITEMS)

    const pending = applyDomPickerEvent(submitting, { type: 'pending' })
    expect(pending.phase).toBe('submitting')
    expect(pending.banner).toBe('pending')

    const storeLost = applyDomPickerEvent(submitting, { type: 'storeUnproven' })
    expect(storeLost.phase).toBe('open')
    expect(storeLost.storeUnproven).toBe(true)
    expect(storeLost.catalogId).toBe(opened.catalogId)

    expect(applyDomPickerEvent(busy, { type: 'close' })).toEqual(INITIAL_DOM_PICKER_STATE)
  })

  it('does not restore from pageToken or lease fields', () => {
    const opened = applyDomPickerEvent(INITIAL_DOM_PICKER_STATE, {
      type: 'open',
      catalogId: 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee',
      items: ITEMS,
    })
    expect('pageToken' in opened).toBe(false)
    expect('leaseDeadline' in opened).toBe(false)
    expect('readyRestore' in opened).toBe(false)
  })
})

describe('extractorBusyForDomMutex', () => {
  it('treats awaitingCatalog as extractor-busy even when phase is closed', () => {
    expect(extractorBusyForDomMutex('closed')).toBe(false)
    expect(extractorBusyForDomMutex('closed', true)).toBe(true)
    expect(extractorBusyForDomMutex('open')).toBe(true)
    expect(extractorBusyForDomMutex('submitting', false)).toBe(true)
  })
})

describe('isCurrentDomCatalog', () => {
  it('rejects empty or swapped catalog ids', () => {
    expect(isCurrentDomCatalog('aaa', 'aaa')).toBe(true)
    expect(isCurrentDomCatalog('aaa', 'bbb')).toBe(false)
    expect(isCurrentDomCatalog('', 'aaa')).toBe(false)
  })
})
