import { afterEach, describe, expect, it } from 'vitest'
import { EXTRACTOR_LEASE_MS } from './extractorKeys'
import { bumpDirectConnectGeneration, resetDirectConnectGenerationForTests } from './domConnectGeneration'
import {
  getDomCatalog,
  invalidateDomCatalogByTab,
  isDomCatalogAlive,
  mapDomCatalogIndices,
  projectDomCatalog,
  putDomCatalog,
  resetDomCatalogsForTests,
  type DomCatalogItem,
} from './domCatalog'

const ITEM: DomCatalogItem = {
  url: 'https://example.com/a.bin',
  filename: 'a.bin',
  kind: 'link',
  referrer: { documentPolicy: '', elementPolicy: '', relNoreferrer: false },
}

function put(overrides: Record<string, unknown> = {}) {
  return putDomCatalog({
    tabId: 1,
    documentNonce: 'nonce-a',
    pageHref: 'https://example.com/page#frag',
    incognito: false,
    cookieStoreId: 'store-a',
    storeUnproven: false,
    truncated: false,
    items: [ITEM],
    ...overrides,
  })
}

describe('domCatalog', () => {
  afterEach(() => {
    resetDomCatalogsForTests()
    resetDirectConnectGenerationForTests(0)
  })

  it('drops the previous catalog id when the same tab is put again', () => {
    const first = put({ catalogId: '11111111-1111-4111-8111-111111111111' })
    const second = put({ catalogId: '22222222-2222-4222-8222-222222222222' })
    expect(getDomCatalog(first.catalogId)).toBeUndefined()
    expect(getDomCatalog(second.catalogId)?.catalogId).toBe(second.catalogId)
    expect(invalidateDomCatalogByTab(1)).toBe(second.catalogId)
  })

  it('refuses stale, duplicate, and out-of-range indices without a partial map', () => {
    const catalog = put({ items: [ITEM, { ...ITEM, url: 'https://example.com/b.bin' }] })
    expect(mapDomCatalogIndices(catalog, [9])).toEqual({ error: 'invalid_request' })
    expect(mapDomCatalogIndices(catalog, [0, 0])).toEqual({ error: 'invalid_request' })
    expect(mapDomCatalogIndices(catalog, [0, 1])).toEqual({
      items: [ITEM, { ...ITEM, url: 'https://example.com/b.bin' }],
    })
  })

  it('treats a generation mismatch as not alive', () => {
    const catalog = put()
    expect(isDomCatalogAlive(catalog.catalogId)).toBe(true)
    bumpDirectConnectGeneration()
    expect(isDomCatalogAlive(catalog.catalogId)).toBe(false)
  })

  it('expires after the TTL', () => {
    const catalog = put({ now: 1_000 })
    expect(getDomCatalog(catalog.catalogId, 1_000 + EXTRACTOR_LEASE_MS + 1)).toBeUndefined()
  })

  it('projects filename, origin, kind, and index without url, query, cookie, or download_page', () => {
    const catalog = put({
      items: [
        {
          url: 'https://example.com/dir/a.bin?sig=secret',
          filename: 'a.bin',
          kind: 'link',
          referrer: { documentPolicy: '', elementPolicy: '', relNoreferrer: false },
        },
      ],
      truncated: true,
      storeUnproven: true,
    })
    const projected = projectDomCatalog(catalog)
    expect(projected.truncated).toBe(true)
    expect(projected.store_unproven).toBe(true)
    expect(JSON.stringify(projected)).not.toContain('https://example.com/dir/a.bin')
    expect(JSON.stringify(projected)).not.toContain('sig=secret')
    expect(JSON.stringify(projected)).not.toContain('Cookie')
    expect(JSON.stringify(projected)).not.toContain('download_page')
    expect(projected.items[0]).toEqual({
      index: 0,
      filename: 'a.bin',
      origin: 'https://example.com',
      kind: 'link',
    })
  })
})
