import { describe, expect, it } from 'vitest'
import { canonicalizeDirectURL, urlPathIsM3uPlaylist } from './domCanonicalUrl'

describe('canonicalizeDirectURL', () => {
  it('rejects userinfo', () => {
    expect(canonicalizeDirectURL('https://user:pass@example.com/a')).toBeUndefined()
    expect(canonicalizeDirectURL('https://user@example.com/a')).toBeUndefined()
  })

  it('strips fragments so two fragments share an owner', () => {
    const a = canonicalizeDirectURL('https://example.com/file.bin#one')
    const b = canonicalizeDirectURL('https://example.com/file.bin#two')
    expect(a).toBe('https://example.com/file.bin')
    expect(b).toBe(a)
  })

  it('keeps raw query order', () => {
    const first = canonicalizeDirectURL('https://example.com/a?b=1&a=2')
    const second = canonicalizeDirectURL('https://example.com/a?a=2&b=1')
    expect(first).toBe('https://example.com/a?b=1&a=2')
    expect(second).toBe('https://example.com/a?a=2&b=1')
    expect(first).not.toBe(second)
  })

  it('allows IPv4 and IPv6 hosts', () => {
    expect(canonicalizeDirectURL('http://192.0.2.1/x')).toBe('http://192.0.2.1/x')
    expect(canonicalizeDirectURL('https://[2001:db8::1]/x')).toBe('https://[2001:db8::1]/x')
  })

  it('treats http and https as distinct', () => {
    expect(canonicalizeDirectURL('http://example.com/a')).toBe('http://example.com/a')
    expect(canonicalizeDirectURL('https://example.com/a')).toBe('https://example.com/a')
  })

  it('rejects control characters and whitespace', () => {
    expect(canonicalizeDirectURL('https://example.com/a b')).toBeUndefined()
    expect(canonicalizeDirectURL('https://example.com/a\n')).toBeUndefined()
    expect(canonicalizeDirectURL('https://example.com/a\u0000')).toBeUndefined()
  })

  it('rejects javascript and other non-http schemes', () => {
    expect(canonicalizeDirectURL('javascript:alert(1)')).toBeUndefined()
    expect(canonicalizeDirectURL('blob:https://example.com/x')).toBeUndefined()
    expect(canonicalizeDirectURL('data:text/plain,hi')).toBeUndefined()
    expect(canonicalizeDirectURL('ftp://example.com/a')).toBeUndefined()
  })

  it('does not invent a trailing slash for a host-only URL', () => {
    expect(canonicalizeDirectURL('https://example.com')).toBe('https://example.com')
  })

  it('lowercases scheme and host', () => {
    expect(canonicalizeDirectURL('HTTPS://EXAMPLE.COM/A')).toBe('https://example.com/A')
  })
})

describe('urlPathIsM3uPlaylist', () => {
  it('detects m3u and m3u8 path suffixes case-insensitively', () => {
    expect(urlPathIsM3uPlaylist('https://example.com/live.M3U8')).toBe(true)
    expect(urlPathIsM3uPlaylist('https://example.com/live.m3u')).toBe(true)
    expect(urlPathIsM3uPlaylist('https://example.com/live.bin')).toBe(false)
  })
})
