import { describe, expect, it } from 'vitest'
import { isSchemefulSameSite } from './domSameSite'

describe('isSchemefulSameSite', () => {
  it('treats www and cdn under example.com as same-site', () => {
    expect(
      isSchemefulSameSite('https://www.example.com/page', 'https://cdn.example.com/a.bin'),
    ).toBe(true)
  })

  it('does not treat a.co.uk and b.co.uk as same-site', () => {
    expect(isSchemefulSameSite('https://a.co.uk/', 'https://b.co.uk/x')).toBe(false)
  })

  it('does not treat distinct github.io users as same-site', () => {
    expect(
      isSchemefulSameSite('https://user1.github.io/', 'https://user2.github.io/x'),
    ).toBe(false)
  })

  it('is schemeful: http vs https is not same-site', () => {
    expect(isSchemefulSameSite('http://example.com/', 'https://example.com/x')).toBe(false)
  })

  it('compares IPv4 hosts exactly', () => {
    expect(isSchemefulSameSite('http://192.0.2.1/', 'http://192.0.2.1/x')).toBe(true)
    expect(isSchemefulSameSite('http://192.0.2.1/', 'http://192.0.2.2/x')).toBe(false)
  })

  it('compares IPv6 hosts with or without brackets', () => {
    expect(isSchemefulSameSite('http://[2001:db8::1]/', 'http://[2001:db8::1]/x')).toBe(true)
    expect(isSchemefulSameSite('http://[2001:db8::1]/', 'http://[2001:db8::2]/x')).toBe(false)
  })
})
