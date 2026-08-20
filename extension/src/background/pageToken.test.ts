import { describe, expect, it } from 'vitest'
import { canonicalPageFromHref, pageTokenFromCanonical, pageTokenFromHref } from './pageToken'

const TOKEN_AAA = 'a'.repeat(64)
const TOKEN_BBB = 'b'.repeat(64)
const TOKEN_QUERY = 'c'.repeat(64)

const hasher = async (canonical: string): Promise<string> => {
  if (canonical === 'https://share.alpha.test/s/aaa?x=1') return TOKEN_AAA
  if (canonical === 'https://share.alpha.test/s/bbb?x=1') return TOKEN_BBB
  if (canonical === 'https://share.alpha.test/s/aaa') return TOKEN_QUERY
  return 'd'.repeat(64)
}

describe('canonicalPageFromHref', () => {
  it('keeps origin pathname and search and drops the fragment', () => {
    expect(canonicalPageFromHref('https://share.alpha.test/s/aaa?x=1#frag')).toBe(
      'https://share.alpha.test/s/aaa?x=1',
    )
    expect(canonicalPageFromHref('https://share.alpha.test/s/aaa')).toBe(
      'https://share.alpha.test/s/aaa',
    )
  })

  it('returns undefined for loopback, file, and IP hosts', () => {
    expect(canonicalPageFromHref('http://127.0.0.1/')).toBeUndefined()
    expect(canonicalPageFromHref('file:///tmp/page.html')).toBeUndefined()
    expect(canonicalPageFromHref('http://192.168.0.1/s')).toBeUndefined()
  })
})

describe('pageTokenFromCanonical', () => {
  it('returns the same token for the same canonical page', async () => {
    const a = await pageTokenFromCanonical('https://share.alpha.test/s/aaa?x=1', hasher)
    const b = await pageTokenFromCanonical('https://share.alpha.test/s/aaa?x=1', hasher)
    expect(a).toBe(TOKEN_AAA)
    expect(b).toBe(TOKEN_AAA)
  })

  it('differs across share paths', async () => {
    const a = await pageTokenFromCanonical('https://share.alpha.test/s/aaa?x=1', hasher)
    const b = await pageTokenFromCanonical('https://share.alpha.test/s/bbb?x=1', hasher)
    expect(a).toBe(TOKEN_AAA)
    expect(b).toBe(TOKEN_BBB)
    expect(a).not.toBe(b)
  })
})

describe('pageTokenFromHref', () => {
  it('ignores the fragment and never stores the href', async () => {
    const token = await pageTokenFromHref('https://share.alpha.test/s/aaa?x=1#unused', hasher)
    expect(token).toBe(TOKEN_AAA)
    expect(token).not.toContain('https://')
    expect(token).not.toContain('share.alpha.test')
  })
})
