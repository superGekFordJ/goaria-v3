import { describe, expect, it } from 'vitest'
import {
  isValidUrl,
  isPairingUrl,
  isValidPairingUrl,
  isValidReleaseNotesUrl,
  isDuplicateUri,
} from './url'

describe('isValidUrl', () => {
  it('accepts http, https, ftp, sftp, magnet schemes', () => {
    expect(isValidUrl('http://example.com')).toBe(true)
    expect(isValidUrl('https://example.com')).toBe(true)
    expect(isValidUrl('ftp://example.com')).toBe(true)
    expect(isValidUrl('sftp://example.com')).toBe(true)
    expect(isValidUrl('magnet:?xt=urn:btih:abc')).toBe(true)
  })

  it('rejects non-url text', () => {
    expect(isValidUrl('not a url')).toBe(false)
    expect(isValidUrl('file:///etc/passwd')).toBe(false)
  })
})

describe('isPairingUrl', () => {
  it('detects GoAria pairing URLs', () => {
    expect(isPairingUrl('http://127.0.0.1:16810/__goaria_pair__/pair.html?n=abc')).toBe(true)
  })

  it('does not flag regular download URLs', () => {
    expect(isPairingUrl('https://example.com/file.zip')).toBe(false)
    expect(isPairingUrl('http://127.0.0.1:16810/other.html')).toBe(false)
  })
})

describe('isValidPairingUrl', () => {
  it('accepts valid pairing URLs on allowed fallback ports', () => {
    expect(isValidPairingUrl('http://127.0.0.1:16810/__goaria_pair__/pair.html?nonce=abc')).toBe(true)
    expect(isValidPairingUrl('http://127.0.0.1:16814/__goaria_pair__/pair.html?nonce=xyz')).toBe(true)
  })

  it('rejects https scheme', () => {
    expect(isValidPairingUrl('https://127.0.0.1:16810/__goaria_pair__/pair.html?nonce=abc')).toBe(false)
  })

  it('rejects invalid hostnames or domains', () => {
    expect(isValidPairingUrl('http://localhost:16810/__goaria_pair__/pair.html?nonce=abc')).toBe(false)
    expect(isValidPairingUrl('http://evil.com:16810/__goaria_pair__/pair.html?nonce=abc')).toBe(false)
    expect(isValidPairingUrl('http://192.168.1.1:16810/__goaria_pair__/pair.html?nonce=abc')).toBe(false)
  })

  it('rejects ports outside of fallback range', () => {
    expect(isValidPairingUrl('http://127.0.0.1:80/__goaria_pair__/pair.html?nonce=abc')).toBe(false)
    expect(isValidPairingUrl('http://127.0.0.1:16809/__goaria_pair__/pair.html?nonce=abc')).toBe(false)
    expect(isValidPairingUrl('http://127.0.0.1:16815/__goaria_pair__/pair.html?nonce=abc')).toBe(false)
  })

  it('rejects wrong paths', () => {
    expect(isValidPairingUrl('http://127.0.0.1:16810/pair')).toBe(false)
    expect(isValidPairingUrl('http://127.0.0.1:16810/__goaria_pair__/evil.html')).toBe(false)
  })

  it('rejects userinfo injection', () => {
    expect(isValidPairingUrl('http://user:pass@127.0.0.1:16810/__goaria_pair__/pair.html')).toBe(false)
  })

  it('rejects empty and malformed inputs', () => {
    expect(isValidPairingUrl('')).toBe(false)
    expect(isValidPairingUrl(undefined)).toBe(false)
    expect(isValidPairingUrl('not a url')).toBe(false)
  })
})

describe('isValidReleaseNotesUrl', () => {
  it('accepts valid GitHub release URLs for GoAria', () => {
    expect(
      isValidReleaseNotesUrl('https://github.com/superGekFordJ/goaria-v3/releases/tag/v3.2.0'),
    ).toBe(true)
    expect(
      isValidReleaseNotesUrl('https://github.com/superGekFordJ/goaria-v3/releases'),
    ).toBe(true)
  })

  it('rejects non-https schemes', () => {
    expect(
      isValidReleaseNotesUrl('http://github.com/superGekFordJ/goaria-v3/releases/tag/v3.2.0'),
    ).toBe(false)
  })

  it('rejects non-github.com hosts or domain spoofing', () => {
    expect(
      isValidReleaseNotesUrl('https://evil.com/superGekFordJ/goaria-v3/releases'),
    ).toBe(false)
    expect(
      isValidReleaseNotesUrl('https://github.com.evil.com/superGekFordJ/goaria-v3/releases'),
    ).toBe(false)
  })

  it('rejects wrong repository paths', () => {
    expect(
      isValidReleaseNotesUrl('https://github.com/otherUser/otherRepo/releases'),
    ).toBe(false)
  })

  it('rejects userinfo and invalid strings', () => {
    expect(
      isValidReleaseNotesUrl('https://admin@github.com/superGekFordJ/goaria-v3/releases'),
    ).toBe(false)
    expect(isValidReleaseNotesUrl('')).toBe(false)
    expect(isValidReleaseNotesUrl(undefined)).toBe(false)
    expect(isValidReleaseNotesUrl('invalid-url')).toBe(false)
  })
})

describe('isDuplicateUri', () => {
  const fakeStore = { allUris: new Set(['https://example.com/existing']) }

  it('returns true for URIs already in the store', () => {
    expect(isDuplicateUri('https://example.com/existing', fakeStore)).toBe(true)
  })

  it('returns false for new URIs', () => {
    expect(isDuplicateUri('https://example.com/new', fakeStore)).toBe(false)
  })
})
