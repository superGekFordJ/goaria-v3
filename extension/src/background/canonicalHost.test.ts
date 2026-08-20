import { describe, expect, it } from 'vitest'
import { parseHTTPURLHost } from './canonicalHost'

describe('parseHTTPURLHost', () => {
  it('lowercases mixed-case hosts', () => {
    expect(parseHTTPURLHost('https://ExAmPle.COM/x')).toBe('example.com')
  })

  it('strips a non-default port', () => {
    expect(parseHTTPURLHost('https://example.com:8080/')).toBe('example.com')
  })

  it('rejects userinfo, trailing dot, ftp, localhost, and percent hosts', () => {
    expect(parseHTTPURLHost('https://user:pass@example.com/x')).toBeUndefined()
    expect(parseHTTPURLHost('https://example.com./x')).toBeUndefined()
    expect(parseHTTPURLHost('ftp://example.com/x')).toBeUndefined()
    expect(parseHTTPURLHost('http://localhost/x')).toBeUndefined()
    expect(parseHTTPURLHost('https://ex%61mple.com/x')).toBeUndefined()
  })

  it('rejects ipv4 and ipv6 including the colon heuristic', () => {
    expect(parseHTTPURLHost('http://127.0.0.1/')).toBeUndefined()
    expect(parseHTTPURLHost('http://[::1]/')).toBeUndefined()
    expect(parseHTTPURLHost('http://[2001:db8::1]:443/')).toBeUndefined()
  })
})
