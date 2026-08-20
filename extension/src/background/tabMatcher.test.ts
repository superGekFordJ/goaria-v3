import { describe, expect, it, vi } from 'vitest'

vi.mock('webextension-polyfill', () => ({
  default: {
    tabs: {
      onUpdated: { addListener() {} },
      query: async () => [],
    },
  },
}))
vi.mock('webext-bridge/background', () => ({
  sendMessage: async () => undefined,
}))
vi.mock('../stores/connection.svelte', () => ({
  connectionState: { capabilities: undefined },
}))
vi.mock('../stores/config.svelte', () => ({
  CAP_EXTRACTOR_RESOLVE: 'extractor.resolve',
}))

import { isHttpUrl, shouldScanChange, unionRescanTabs } from './tabMatcher'

describe('shouldScanChange', () => {
  it('is true for a non-empty url or complete status', () => {
    expect(shouldScanChange({ url: 'https://example.com/' })).toBe(true)
    expect(shouldScanChange({ status: 'complete' })).toBe(true)
    expect(shouldScanChange({ url: '', status: 'loading' })).toBe(false)
    expect(shouldScanChange({})).toBe(false)
  })
})

describe('unionRescanTabs', () => {
  it('keeps http(s) tabs, caps, and unions the active tab by id', () => {
    const tabs = [
      { id: 1, url: 'https://a.example/' },
      { id: 2, url: 'ftp://skip/' },
      { id: 3, url: 'http://b.example/' },
      { id: 4, url: 'https://c.example/' },
    ]
    const capped = unionRescanTabs(tabs, { id: 99, url: 'https://active.example/' }, 2)
    expect(capped.map((t) => t.id)).toEqual([1, 3, 99])
  })

  it('does not duplicate the active tab', () => {
    const tabs = [
      { id: 1, url: 'https://a.example/' },
      { id: 2, url: 'https://b.example/' },
    ]
    const capped = unionRescanTabs(tabs, { id: 1, url: 'https://a.example/' }, 64)
    expect(capped.map((t) => t.id)).toEqual([1, 2])
  })
})

describe('isHttpUrl', () => {
  it('accepts only http and https', () => {
    expect(isHttpUrl('https://example.com/')).toBe(true)
    expect(isHttpUrl('http://example.com/')).toBe(true)
    expect(isHttpUrl('HTTP://example.com/')).toBe(true)
    expect(isHttpUrl('HTTPS://example.com/')).toBe(true)
    expect(isHttpUrl('ftp://example.com/')).toBe(false)
    expect(isHttpUrl(undefined)).toBe(false)
  })
})
