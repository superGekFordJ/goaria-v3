import { describe, expect, it } from 'vitest'
import { isValidUrl, isPairingUrl, isDuplicateUri } from './url'

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

describe('isDuplicateUri', () => {
  const fakeStore = { allUris: new Set(['https://example.com/existing']) }

  it('returns true for URIs already in the store', () => {
    expect(isDuplicateUri('https://example.com/existing', fakeStore)).toBe(true)
  })

  it('returns false for new URIs', () => {
    expect(isDuplicateUri('https://example.com/new', fakeStore)).toBe(false)
  })
})
