import { describe, expect, it } from 'vitest'
import { referrerResult } from './domReferrer'

describe('referrerResult', () => {
  it('omits when rel=noreferrer', () => {
    expect(
      referrerResult({
        pageHref: 'https://example.com/page',
        targetHref: 'https://cdn.example.com/a.bin',
        relNoreferrer: true,
      }),
    ).toBeUndefined()
  })

  it('defaults cross-origin to origin-only', () => {
    expect(
      referrerResult({
        pageHref: 'https://example.com/dir/page?q=1#frag',
        targetHref: 'https://cdn.fixture.invalid/a.bin',
      }),
    ).toBe('https://example.com/')
  })

  it('omits on HTTPS to HTTP downgrade', () => {
    expect(
      referrerResult({
        pageHref: 'https://example.com/page',
        targetHref: 'http://example.com/a.bin',
        documentPolicy: 'strict-origin-when-cross-origin',
      }),
    ).toBeUndefined()
    expect(
      referrerResult({
        pageHref: 'https://example.com/page',
        targetHref: 'http://cdn.example.com/a.bin',
        documentPolicy: 'no-referrer-when-downgrade',
      }),
    ).toBeUndefined()
  })

  it('keeps same-origin path and query without fragment or userinfo', () => {
    expect(
      referrerResult({
        pageHref: 'https://example.com/dir/page?q=1#frag',
        targetHref: 'https://example.com/a.bin',
      }),
    ).toBe('https://example.com/dir/page?q=1')
  })

  it('omits same-origin policy on cross-origin targets', () => {
    expect(
      referrerResult({
        pageHref: 'https://example.com/page',
        targetHref: 'https://cdn.fixture.invalid/a.bin',
        documentPolicy: 'same-origin',
      }),
    ).toBeUndefined()
  })

  it('omits results that exceed 2048 bytes after canonicalize', () => {
    const longPath = `https://example.com/${'a'.repeat(2100)}`
    expect(
      referrerResult({
        pageHref: longPath,
        targetHref: longPath,
        documentPolicy: 'unsafe-url',
      }),
    ).toBeUndefined()
  })
})
