import { afterEach, describe, expect, it, vi } from 'vitest'
import { mintClientItemId, mintDirectBatchRequestId, mintRequestId } from './mintRequestId'

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

describe('mintRequestId', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('returns crypto.randomUUID when it exists', () => {
    vi.stubGlobal('crypto', {
      randomUUID: () => '11111111-2222-4333-8444-555555555555',
    })
    expect(mintRequestId()).toBe('11111111-2222-4333-8444-555555555555')
  })

  it('returns 64 hex chars when only getRandomValues exists', () => {
    vi.stubGlobal('crypto', {
      getRandomValues(bytes: Uint8Array) {
        for (let i = 0; i < bytes.length; i++) bytes[i] = i
        return bytes
      },
    })
    const id = mintRequestId()
    expect(id).toHaveLength(64)
    expect(id).toMatch(/^[0-9a-f]+$/)
  })

  it('returns 64 hex chars when crypto has neither helper', () => {
    vi.stubGlobal('crypto', {})
    const id = mintRequestId()
    expect(id).toHaveLength(64)
    expect(id).toMatch(/^[0-9a-f]+$/)
  })
})

describe('mintDirectBatchRequestId', () => {
  it('returns crypto.randomUUID lowercased when it exists', () => {
    vi.stubGlobal('crypto', {
      randomUUID: () => 'AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE',
    })
    expect(mintDirectBatchRequestId()).toBe('aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee')
  })

  it('formats 16 random bytes as a 36-char UUID when randomUUID is missing', () => {
    vi.stubGlobal('crypto', {
      getRandomValues(bytes: Uint8Array) {
        for (let i = 0; i < bytes.length; i++) bytes[i] = i
        return bytes
      },
    })
    const id = mintDirectBatchRequestId()
    expect(id).toHaveLength(36)
    expect(id).toMatch(UUID_RE)
  })

  it('does not accept the 64-hex mintRequestId fallback as a batch request id', () => {
    vi.stubGlobal('crypto', {
      getRandomValues(bytes: Uint8Array) {
        for (let i = 0; i < bytes.length; i++) bytes[i] = i
        return bytes
      },
    })
    const legacy = mintRequestId()
    const batch = mintDirectBatchRequestId()
    expect(legacy).toHaveLength(64)
    expect(legacy).toMatch(/^[0-9a-f]+$/)
    expect(batch).toHaveLength(36)
    expect(batch).toMatch(UUID_RE)
    expect(batch.includes('-')).toBe(true)
    expect(legacy.includes('-')).toBe(false)
  })
})

describe('mintClientItemId', () => {
  it('returns 32 lowercase hex', () => {
    vi.stubGlobal('crypto', {
      getRandomValues(bytes: Uint8Array) {
        for (let i = 0; i < bytes.length; i++) bytes[i] = 0xab
        return bytes
      },
    })
    const id = mintClientItemId()
    expect(id).toHaveLength(32)
    expect(id).toMatch(/^[0-9a-f]+$/)
  })
})
