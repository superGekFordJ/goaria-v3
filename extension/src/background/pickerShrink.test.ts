import { describe, expect, it } from 'vitest'
import { remainingItemIds, shrinkDisplayItems, zipDisplayItems } from './pickerShrink'

describe('remainingItemIds', () => {
  it('drops succeeded and duplicate ids and keeps failed plus unclassified', () => {
    const requested = ['itm_a', 'itm_b', 'itm_c', 'itm_d']
    const remaining = remainingItemIds(requested, {
      success: false,
      succeeded_item_ids: ['itm_a'],
      duplicate_item_ids: ['itm_b'],
      errors_by_item_id: { itm_c: 'add failed' },
      href: 'https://share.alpha.test/s/trap',
    })
    expect(remaining).toEqual(['itm_c', 'itm_d'])
  })

  it('ignores trap url fields and never retries a succeeded handle', () => {
    const remaining = remainingItemIds(['itm_a', 'itm_b'], {
      success: false,
      succeeded_item_ids: ['itm_a', 'https://share.alpha.test/s/aaa'],
      'https://share.fixture.invalid/x': 'nope',
    })
    expect(remaining).toEqual(['itm_b'])
    expect(remaining.join(' ')).not.toContain('https://')
  })
})

describe('shrinkDisplayItems', () => {
  it('drops the succeeded row and reindexes the remaining display items', () => {
    const shrunk = shrinkDisplayItems(
      ['itm_a', 'itm_b', 'itm_c'],
      [
        { filename: 'a.bin', size_bytes: 1 },
        { filename: 'b.bin', size_bytes: 2 },
        { filename: 'c.bin' },
      ],
      ['itm_b', 'itm_c'],
    )
    expect(shrunk).toEqual({
      itemIds: ['itm_b', 'itm_c'],
      displayItems: [{ filename: 'b.bin', size_bytes: 2 }, { filename: 'c.bin' }],
    })
  })

  it('rejects length mismatch instead of guessing rows', () => {
    expect(
      shrinkDisplayItems(['itm_a', 'itm_b'], [{ filename: 'a.bin' }], ['itm_a']),
    ).toEqual({ error: 'invalid_request' })
  })
})

describe('zipDisplayItems', () => {
  it('keeps requested order and drops unselected rows', () => {
    expect(
      zipDisplayItems(
        ['itm_a', 'itm_b', 'itm_c'],
        [{ filename: 'a.bin' }, { filename: 'b.bin' }, { filename: 'c.bin' }],
        ['itm_a', 'itm_c'],
      ),
    ).toEqual({
      itemIds: ['itm_a', 'itm_c'],
      displayItems: [{ filename: 'a.bin' }, { filename: 'c.bin' }],
    })
  })

  it('returns invalid_request instead of positional-matching when shrink cannot map by id', () => {
    expect(
      zipDisplayItems(
        ['itm_a', 'itm_b', 'itm_c'],
        [{ filename: 'a.bin' }, { filename: 'b.bin' }],
        ['itm_a', 'itm_c'],
      ),
    ).toEqual({ error: 'invalid_request' })
  })
})
