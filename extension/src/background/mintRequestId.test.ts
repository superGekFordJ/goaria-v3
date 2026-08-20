import { afterEach, describe, expect, it, vi } from 'vitest'
import { mintRequestId } from './mintRequestId'

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
