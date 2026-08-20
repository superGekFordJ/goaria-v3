import { describe, expect, it } from 'vitest'
import { pickCookieStoreId } from './cookieStoreId'

describe('pickCookieStoreId', () => {
  it('uses a non-empty tab cookieStoreId (Firefox containers)', () => {
    expect(pickCookieStoreId(3, 'firefox-container-1', [{ id: '0', tabIds: [3] }])).toBe(
      'firefox-container-1',
    )
  })

  it('picks the store whose tabIds contain this tab when tab cookieStoreId is missing', () => {
    expect(
      pickCookieStoreId(8, undefined, [
        { id: '0', tabIds: [1, 2] },
        { id: 'private', tabIds: [8] },
      ]),
    ).toBe('private')
  })

  it('may return store 0 only when that store lists the tab', () => {
    expect(pickCookieStoreId(3, undefined, [{ id: '0', tabIds: [3] }])).toBe('0')
  })

  it('returns undefined instead of guessing 0 or 1 when no store lists the tab', () => {
    expect(pickCookieStoreId(9, '', [{ id: '0', tabIds: [1] }])).toBeUndefined()
    expect(pickCookieStoreId(9, '   ', [])).toBeUndefined()
    expect(pickCookieStoreId(9, undefined, [{ id: '1', tabIds: [] }])).toBeUndefined()
  })

  it('returns undefined when more than one store lists the same tab', () => {
    expect(
      pickCookieStoreId(4, undefined, [
        { id: '0', tabIds: [4] },
        { id: 'private', tabIds: [4] },
      ]),
    ).toBeUndefined()
  })
})
