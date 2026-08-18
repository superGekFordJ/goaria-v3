import { describe, expect, it } from 'vitest'
import { hasCapability, isLegacyHost, parseAuthAck } from './capabilities'

describe('parseAuthAck / isLegacyHost', () => {
  it('treats missing protocol_version and capabilities as legacy', () => {
    const parsed = parseAuthAck({ type: 'auth_ack' })
    expect(parsed.protocolVersion).toBe(0)
    expect(parsed.capabilities).toBeUndefined()
    expect(isLegacyHost(parsed)).toBe(true)
  })

  it('treats JSON null capabilities as missing/legacy', () => {
    const parsed = parseAuthAck({
      type: 'auth_ack',
      protocol_version: 2,
      capabilities: null,
    })
    expect(parsed.capabilities).toBeUndefined()
    expect(isLegacyHost(parsed)).toBe(true)
  })

  it('treats present empty capabilities as protocol-2, not legacy', () => {
    const parsed = parseAuthAck({
      type: 'auth_ack',
      protocol_version: 2,
      host_version: 'dev',
      capabilities: [],
    })
    expect(parsed.protocolVersion).toBe(2)
    expect(parsed.hostVersion).toBe('dev')
    expect(parsed.capabilities).toEqual([])
    expect(isLegacyHost(parsed)).toBe(false)
  })

  it('parses string capabilities including request_id', () => {
    const parsed = parseAuthAck({
      type: 'auth_ack',
      protocol_version: 2,
      capabilities: ['request_id', 'extractor.resolve', 1, 'extractor.batch'],
    })
    expect(parsed.capabilities).toEqual(['request_id', 'extractor.resolve', 'extractor.batch'])
    expect(isLegacyHost(parsed)).toBe(false)
  })
})

describe('hasCapability', () => {
  it('returns false for missing or empty lists', () => {
    expect(hasCapability(undefined, 'request_id')).toBe(false)
    expect(hasCapability(null, 'request_id')).toBe(false)
    expect(hasCapability([], 'request_id')).toBe(false)
  })

  it('returns true only for an exact listed capability', () => {
    const caps = ['request_id', 'extractor.resolve']
    expect(hasCapability(caps, 'request_id')).toBe(true)
    expect(hasCapability(caps, 'extractor.resolve')).toBe(true)
    expect(hasCapability(caps, 'extractor.batch')).toBe(false)
  })
})
