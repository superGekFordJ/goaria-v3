import { describe, expect, it } from 'vitest'
import { applyPickerEvent, INITIAL_PICKER_STATE, pickerEventForCapsuleUi } from './pickerUiState'
import type { PickerCatalogItem } from '../utils/messaging'

const TOKEN = 'a'.repeat(64)
const OTHER = 'b'.repeat(64)

const ITEMS: PickerCatalogItem[] = [
  { index: 0, filename: 'a.bin', size_bytes: 10 },
  { index: 1, filename: 'b.bin' },
]

describe('applyPickerEvent', () => {
  it('opens, submits, replaces the catalog, and closes', () => {
    const opened = applyPickerEvent(INITIAL_PICKER_STATE, {
      type: 'open',
      pageToken: TOKEN,
      items: ITEMS,
      count: 2,
      lease_deadline: 9,
    })
    expect(opened.phase).toBe('open')
    expect(opened.items).toEqual(ITEMS)
    expect(opened.leaseDeadline).toBe(9)

    const submitting = applyPickerEvent(opened, { type: 'submit' })
    expect(submitting.phase).toBe('submitting')

    const replaced = applyPickerEvent(submitting, {
      type: 'catalog',
      pageToken: TOKEN,
      items: [{ index: 0, filename: 'b.bin' }],
      count: 1,
    })
    expect(replaced.phase).toBe('open')
    expect(replaced.items).toEqual([{ index: 0, filename: 'b.bin' }])
    expect(replaced.count).toBe(1)

    const closed = applyPickerEvent(replaced, { type: 'close' })
    expect(closed).toEqual(INITIAL_PICKER_STATE)
  })

  it('forces closed on hide and ignores catalog while closed', () => {
    const opened = applyPickerEvent(INITIAL_PICKER_STATE, {
      type: 'open',
      pageToken: TOKEN,
      items: ITEMS,
    })
    const hidden = applyPickerEvent(opened, { type: 'hide' })
    expect(hidden.phase).toBe('closed')
    expect(
      applyPickerEvent(hidden, { type: 'catalog', pageToken: TOKEN, items: ITEMS }),
    ).toEqual(INITIAL_PICKER_STATE)
  })

  it('closes on ready-restore without a catalog and ignores a mismatched token', () => {
    const opened = applyPickerEvent(INITIAL_PICKER_STATE, {
      type: 'open',
      pageToken: TOKEN,
      items: ITEMS,
    })
    expect(applyPickerEvent(opened, { type: 'readyRestore', pageToken: OTHER })).toEqual(opened)
    const restored = applyPickerEvent(opened, { type: 'readyRestore', pageToken: TOKEN })
    expect(restored.phase).toBe('closed')
    expect(restored.items).toEqual([])

    const submitting = applyPickerEvent(opened, { type: 'submit' })
    expect(submitting.awaitingCatalog).toBe(true)
    const afterSubmitRestore = applyPickerEvent(submitting, { type: 'readyRestore', pageToken: TOKEN })
    expect(afterSubmitRestore.phase).toBe('closed')
    expect(afterSubmitRestore.awaitingCatalog).toBe(true)
    expect(afterSubmitRestore.pageToken).toBe(TOKEN)
    expect(afterSubmitRestore.items).toEqual([])
  })

  it('reopens from a submitting restore when the shrink catalog arrives later', () => {
    const opened = applyPickerEvent(INITIAL_PICKER_STATE, {
      type: 'open',
      pageToken: TOKEN,
      items: ITEMS,
    })
    const stub = applyPickerEvent(applyPickerEvent(opened, { type: 'submit' }), {
      type: 'readyRestore',
      pageToken: TOKEN,
    })
    const cataloged = applyPickerEvent(stub, {
      type: 'catalog',
      pageToken: TOKEN,
      items: [{ index: 0, filename: 'b.bin' }],
      count: 1,
    })
    expect(cataloged.phase).toBe('open')
    expect(cataloged.items).toEqual([{ index: 0, filename: 'b.bin' }])
    expect(cataloged.awaitingCatalog).toBeFalsy()
  })

  it('keeps awaitingCatalog when busy-rollback reopens the overlay', () => {
    const opened = applyPickerEvent(INITIAL_PICKER_STATE, {
      type: 'open',
      pageToken: TOKEN,
      items: ITEMS,
    })
    const submitted = applyPickerEvent(opened, { type: 'submit' })
    const cataloged = applyPickerEvent(submitted, {
      type: 'catalog',
      pageToken: TOKEN,
      items: [{ index: 0, filename: 'b.bin' }],
      count: 1,
    })
    expect(cataloged.awaitingCatalog).toBe(true)
    const rolled = applyPickerEvent(cataloged, {
      type: 'open',
      pageToken: TOKEN,
      items: cataloged.items,
      count: cataloged.count,
      awaitingCatalog: cataloged.awaitingCatalog,
    })
    expect(rolled.phase).toBe('open')
    expect(rolled.awaitingCatalog).toBe(true)
    const restored = applyPickerEvent(rolled, { type: 'readyRestore', pageToken: TOKEN })
    expect(restored.phase).toBe('open')
    expect(restored.items).toEqual([{ index: 0, filename: 'b.bin' }])
  })

  it('keeps the overlay open when catalog wins the race with ready-restore', () => {
    const opened = applyPickerEvent(INITIAL_PICKER_STATE, {
      type: 'open',
      pageToken: TOKEN,
      items: ITEMS,
    })
    const submitting = applyPickerEvent(opened, { type: 'submit' })
    const cataloged = applyPickerEvent(submitting, {
      type: 'catalog',
      pageToken: TOKEN,
      items: [{ index: 0, filename: 'b.bin' }],
      count: 1,
    })
    expect(cataloged.phase).toBe('open')
    expect(cataloged.awaitingCatalog).toBe(true)
    const restored = applyPickerEvent(cataloged, { type: 'readyRestore', pageToken: TOKEN })
    expect(restored.phase).toBe('open')
    expect(restored.items).toEqual([{ index: 0, filename: 'b.bin' }])
    expect(restored.awaitingCatalog).toBe(false)
  })

  it('drops unknown catalog keys instead of storing them', () => {
    const opened = applyPickerEvent(INITIAL_PICKER_STATE, {
      type: 'open',
      pageToken: TOKEN,
      items: [
        {
          index: 0,
          filename: 'a.bin',
          item_id: 'itm_alpha',
          href: 'https://share.alpha.test/s/aaa',
        } as PickerCatalogItem,
      ],
    })
    expect(opened.items).toEqual([{ index: 0, filename: 'a.bin' }])
    expect(JSON.stringify(opened)).not.toContain('itm_alpha')
    expect(JSON.stringify(opened)).not.toContain('https://')
  })
})

describe('pickerEventForCapsuleUi', () => {
  it('closes the picker on capsule success, error, and hide', () => {
    expect(pickerEventForCapsuleUi('hidden')).toEqual({ type: 'hide' })
    expect(pickerEventForCapsuleUi('success')).toEqual({ type: 'close' })
    expect(pickerEventForCapsuleUi('error')).toEqual({ type: 'close' })
    expect(pickerEventForCapsuleUi('ready')).toBeNull()
    expect(pickerEventForCapsuleUi('committing')).toBeNull()
  })
})
