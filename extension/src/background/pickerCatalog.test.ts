import { describe, expect, it } from 'vitest'
import { EXTRACTOR_MAX_SESSION_ITEMS } from './extractorKeys'
import { buildPickerCatalog, mapPickerIndices } from './pickerCatalog'

const IDS = ['itm_alpha', 'itm_beta', 'itm_gamma']
const DISPLAY = [
  { filename: 'clip.bin', size_bytes: 12, mime_type: 'application/octet-stream' },
  { filename: 'note.txt' },
  { filename: 'pic.webp', size_bytes: 99, mime_type: 'image/webp' },
]

describe('buildPickerCatalog', () => {
  it('rejects missing arrays, length mismatch, and out-of-range counts', () => {
    expect(buildPickerCatalog(undefined, DISPLAY)).toEqual({ error: 'invalid_request' })
    expect(buildPickerCatalog(IDS, undefined)).toEqual({ error: 'invalid_request' })
    expect(buildPickerCatalog(IDS, DISPLAY.slice(0, 2))).toEqual({ error: 'invalid_request' })
    expect(buildPickerCatalog(['only-one'], [{ filename: 'a.bin' }])).toEqual({
      error: 'invalid_request',
    })
    const tooMany = Array.from({ length: EXTRACTOR_MAX_SESSION_ITEMS + 1 }, (_, i) => `itm_${i}`)
    const tooManyDisplay = tooMany.map(() => ({ filename: 'x.bin' }))
    expect(buildPickerCatalog(tooMany, tooManyDisplay)).toEqual({ error: 'invalid_request' })
  })

  it('preserves ack order, omits missing sizes, and never emits item handles', () => {
    const built = buildPickerCatalog(IDS, DISPLAY, 1_700_000_000_000)
    expect('error' in built).toBe(false)
    if ('error' in built) return
    expect(built.count).toBe(3)
    expect(built.lease_deadline).toBe(1_700_000_000_000)
    expect(built.items.map(row => row.index)).toEqual([0, 1, 2])
    expect(built.items[0]).toEqual({
      index: 0,
      filename: 'clip.bin',
      size_bytes: 12,
      mime_type: 'application/octet-stream',
    })
    expect(built.items[1]).toEqual({ index: 1, filename: 'note.txt' })
    expect(built.items[1]).not.toHaveProperty('size_bytes')
    const encoded = JSON.stringify(built)
    expect(encoded).not.toContain('itm_alpha')
    expect(encoded).not.toContain('item_id')
    expect(encoded).not.toContain('session_id')
  })

  it('caps at 128 parallel rows', () => {
    const ids = Array.from({ length: EXTRACTOR_MAX_SESSION_ITEMS }, (_, i) => `itm_${i}`)
    const display = ids.map((_, i) => ({ filename: `f${i}.bin`, size_bytes: i === 0 ? 8 : undefined }))
    const built = buildPickerCatalog(ids, display)
    expect('items' in built).toBe(true)
    if ('error' in built) return
    expect(built.items.length).toBe(128)
    expect(built.items[127]?.index).toBe(127)
  })

  it('allows a one-row catalog only when minItems is 1', () => {
    const one = buildPickerCatalog(['itm_solo'], [{ filename: 'solo.bin' }], undefined, { minItems: 1 })
    expect('error' in one).toBe(false)
    if ('error' in one) return
    expect(one.count).toBe(1)
    expect(one.items).toEqual([{ index: 0, filename: 'solo.bin' }])
  })
})

describe('mapPickerIndices', () => {
  it('maps unique in-range indices and rejects duplicates or holes', () => {
    expect(mapPickerIndices([2, 0], IDS)).toEqual({ itemIds: ['itm_gamma', 'itm_alpha'] })
    expect(mapPickerIndices([0, 0], IDS)).toEqual({ error: 'invalid_request' })
    expect(mapPickerIndices([3], IDS)).toEqual({ error: 'invalid_request' })
    expect(mapPickerIndices([1.5], IDS)).toEqual({ error: 'invalid_request' })
    expect(mapPickerIndices([], IDS)).toEqual({ error: 'invalid_request' })
  })
})
