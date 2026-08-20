import { beforeEach, describe, expect, it } from 'vitest'
import {
  applyMatchSnapshot,
  clearMatchSnapshot,
  getMatchGeneration,
  getMatchSnapshot,
  isMatchGenerationCurrent,
} from './matchSnapshot'
import type { MatchDigestSnapshot } from './digestMatch'
import { MATCH_DIGEST_VERSION, sha256HexSubtle } from './digestMatch'

const sample: MatchDigestSnapshot = {
  digest_version: 1,
  salt: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  exact_digests: ['aa'],
  subdomain_digests: ['bb'],
}

describe('matchSnapshot', () => {
  beforeEach(() => {
    clearMatchSnapshot()
  })

  it('replaces wholesale and increments generation on apply and clear', () => {
    const before = getMatchGeneration()
    const g1 = applyMatchSnapshot(sample)
    expect(g1).toBe(before + 1)
    expect(getMatchSnapshot()?.exact_digests).toEqual(['aa'])

    const g2 = applyMatchSnapshot({
      ...sample,
      exact_digests: ['cc'],
      subdomain_digests: ['dd'],
    })
    expect(g2).toBe(g1 + 1)
    expect(getMatchSnapshot()?.exact_digests).toEqual(['cc'])
    expect(getMatchSnapshot()?.subdomain_digests).toEqual(['dd'])

    const g3 = clearMatchSnapshot()
    expect(g3).toBe(g2 + 1)
    expect(getMatchSnapshot()).toBeUndefined()
  })

  it('returns a shallow copy so callers cannot mutate the store', () => {
    applyMatchSnapshot(sample)
    const view = getMatchSnapshot()
    expect(view).toBeDefined()
    view!.exact_digests.push('mutated')
    view!.subdomain_digests.push('mutated')
    expect(getMatchSnapshot()?.exact_digests).toEqual(['aa'])
    expect(getMatchSnapshot()?.subdomain_digests).toEqual(['bb'])
  })

  it('store snapshot JSON does not contain fixture hosts', async () => {
    const salt = new Uint8Array(16)
    salt[15] = 0x01
    const digest = await sha256HexSubtle(salt, 'example.com')
    applyMatchSnapshot({
      digest_version: MATCH_DIGEST_VERSION,
      salt: '00000000000000000000000000000001',
      exact_digests: [digest],
      subdomain_digests: [],
    })
    expect(JSON.stringify(getMatchSnapshot())).not.toContain('example.com')
  })

  it('treats a generation captured before clear as stale', () => {
    const gen = applyMatchSnapshot(sample)
    expect(isMatchGenerationCurrent(gen)).toBe(true)
    clearMatchSnapshot()
    expect(isMatchGenerationCurrent(gen)).toBe(false)
  })

  it('does not expose a storage write API', async () => {
    const mod = await import('./matchSnapshot')
    const names = Object.keys(mod)
    expect(names.some((name) => /storage/i.test(name))).toBe(false)
    expect(names).toEqual(
      expect.arrayContaining([
        'applyMatchSnapshot',
        'clearMatchSnapshot',
        'getMatchSnapshot',
        'getMatchGeneration',
        'isMatchGenerationCurrent',
      ]),
    )
  })
})
