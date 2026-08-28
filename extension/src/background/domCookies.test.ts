import { describe, expect, it } from 'vitest'
import { cookieHeaderForItem, collectCookieHeadersForUrls } from './domCookies'

const PAGE = 'https://example.com/page'
const SAME = 'https://cdn.example.com/a.bin'
const CROSS = 'https://cdn.fixture.invalid/a.bin'
const HTTP = 'http://example.com/a.bin'

function cookie(partial: Record<string, unknown>): Record<string, unknown> {
  return {
    name: 'sid',
    value: 'one',
    secure: true,
    sameSite: 'lax',
    ...partial,
  }
}

describe('cookieHeaderForItem', () => {
  it('skips partitioned cookies', () => {
    const line = cookieHeaderForItem(
      [cookie({ partitionKey: { topLevelSite: 'https://example.com' } })],
      PAGE,
      SAME,
    )
    expect(line).toBeUndefined()
  })

  it('omits strict cookies on a cross-site target', () => {
    const line = cookieHeaderForItem([cookie({ sameSite: 'strict' })], PAGE, CROSS)
    expect(line).toBeUndefined()
  })

  it('omits none cookies on http targets', () => {
    const line = cookieHeaderForItem(
      [cookie({ sameSite: 'none', secure: true })],
      'http://example.com/page',
      HTTP,
    )
    expect(line).toBeUndefined()
  })

  it('drops the 65th cookie', () => {
    const cookies = Array.from({ length: 65 }, (_, i) =>
      cookie({ name: `n${String(i).padStart(2, '0')}`, value: 'v' }),
    )
    const line = cookieHeaderForItem(cookies, PAGE, SAME)
    expect(line).toBeDefined()
    expect(line?.includes('n64=')).toBe(false)
    expect(line?.includes('n00=')).toBe(true)
  })

  it('omits Cookie when the serialized line exceeds 4096 bytes', () => {
    const cookies = Array.from({ length: 40 }, (_, i) =>
      cookie({ name: `k${i}`, value: 'x'.repeat(120) }),
    )
    expect(cookieHeaderForItem(cookies, PAGE, SAME)).toBeUndefined()
  })
})

describe('collectCookieHeadersForUrls', () => {
  it('omits Cookie for every item when the store is unproven', async () => {
    const headers = await collectCookieHeadersForUrls({
      urls: [SAME, CROSS],
      sourceHref: PAGE,
      storeId: undefined,
      storeUnproven: true,
      getAll: async () => [cookie({})],
    })
    expect(headers).toEqual([undefined, undefined])
  })

  it('does not reuse another item\'s Cookie line', async () => {
    const getAll = async (details: { url: string; storeId: string }) => {
      expect(details.storeId).toBe('store-a')
      if (details.url === SAME) return [cookie({ name: 'a', value: '1' })]
      return [cookie({ name: 'b', value: '2' })]
    }
    const headers = await collectCookieHeadersForUrls({
      urls: [SAME, 'https://cdn.example.com/b.bin'],
      sourceHref: PAGE,
      storeId: 'store-a',
      storeUnproven: false,
      getAll,
    })
    expect(headers[0]).toBe('Cookie: a=1')
    expect(headers[1]).toBe('Cookie: b=2')
  })

  it('omits Cookie for a URL when getAll throws', async () => {
    const headers = await collectCookieHeadersForUrls({
      urls: [SAME],
      sourceHref: PAGE,
      storeId: 'store-a',
      storeUnproven: false,
      getAll: async () => {
        throw new Error('firstPartyDomain')
      },
    })
    expect(headers).toEqual([undefined])
  })

  it('does not call getAll without a storeId', async () => {
    let called = false
    await collectCookieHeadersForUrls({
      urls: [SAME],
      sourceHref: PAGE,
      storeId: '',
      storeUnproven: false,
      getAll: async () => {
        called = true
        return []
      },
    })
    expect(called).toBe(false)
  })
})
