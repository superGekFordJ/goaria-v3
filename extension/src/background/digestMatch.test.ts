import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { hostMatchesDigests, MATCH_DIGEST_VERSION, sha256HexSubtle, type MatchDigestSnapshot } from './digestMatch'

const GOLDEN =
  '43555cdbabd343dc782e44cdabbb56241898532bb1d8319d21e22a6997e6840b'

function sha256hex(salt: Uint8Array, host: string): string {
  const h = createHash('sha256')
  h.update(salt)
  h.update(host)
  return h.digest('hex')
}

function snap(partial: Partial<MatchDigestSnapshot> & Pick<MatchDigestSnapshot, 'salt'>): MatchDigestSnapshot {
  return {
    digest_version: 1,
    exact_digests: [],
    subdomain_digests: [],
    ...partial,
  }
}

describe('hostMatchesDigests', () => {
  it('matches the golden raw-salt vector', async () => {
    const salt = new Uint8Array(16)
    salt[15] = 0x01
    expect(sha256hex(salt, 'example.com')).toBe(GOLDEN)
    const ok = await hostMatchesDigests(
      'example.com',
      snap({ salt: '00000000000000000000000000000001', exact_digests: [GOLDEN] }),
      sha256hex,
    )
    expect(ok).toBe(true)
  })

  it('matches the same golden vector via sha256HexSubtle', async () => {
    const salt = new Uint8Array(16)
    salt[15] = 0x01
    expect(await sha256HexSubtle(salt, 'example.com')).toBe(GOLDEN)
  })

  it('does not put fixture hosts in JSON of a hashed snapshot', async () => {
    const salt = new Uint8Array(16)
    salt[15] = 0x01
    const digest = await sha256HexSubtle(salt, 'example.com')
    const payload = snap({
      salt: '00000000000000000000000000000001',
      exact_digests: [digest],
    })
    expect(JSON.stringify(payload)).not.toContain('example.com')
  })

  it('keeps MATCH_DIGEST_VERSION aligned with the protocol constant', () => {
    const configSrc = readFileSync(new URL('../stores/config.svelte.ts', import.meta.url), 'utf8')
    expect(MATCH_DIGEST_VERSION).toBe(1)
    expect(configSrc).toMatch(/export const MATCH_DIGEST_VERSION = 1\b/)
  })

  it('matches the apex of an IncludeSubdomains rule via the subdomain set', async () => {
    const saltHex = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    const salt = Uint8Array.from(saltHex.match(/../g)!.map((b) => Number.parseInt(b, 16)))
    const apex = sha256hex(salt, 'example.test')
    const ok = await hostMatchesDigests(
      'example.test',
      snap({ salt: saltHex, subdomain_digests: [apex] }),
      sha256hex,
    )
    expect(ok).toBe(true)
  })

  it('does not parent-walk an exact-only cdn host', async () => {
    const saltHex = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
    const salt = Uint8Array.from(saltHex.match(/../g)!.map((b) => Number.parseInt(b, 16)))
    const cdn = sha256hex(salt, 'cdn.example.test')
    const ok = await hostMatchesDigests(
      'foo.cdn.example.test',
      snap({ salt: saltHex, exact_digests: [cdn] }),
      sha256hex,
    )
    expect(ok).toBe(false)
  })

  it('parent-walks against subdomain_digests only', async () => {
    const saltHex = 'cccccccccccccccccccccccccccccccc'
    const salt = Uint8Array.from(saltHex.match(/../g)!.map((b) => Number.parseInt(b, 16)))
    const apex = sha256hex(salt, 'example.test')
    const ok = await hostMatchesDigests(
      'a.b.example.test',
      snap({ salt: saltHex, subdomain_digests: [apex] }),
      sha256hex,
    )
    expect(ok).toBe(true)
  })

  it('returns false when the digest function throws', async () => {
    const ok = await hostMatchesDigests(
      'example.com',
      snap({
        salt: '00000000000000000000000000000001',
        exact_digests: [GOLDEN],
      }),
      () => {
        throw new Error('subtle missing')
      },
    )
    expect(ok).toBe(false)
  })

  it('returns false for invalid snapshots', async () => {
    expect(await hostMatchesDigests('example.com', undefined, sha256hex)).toBe(false)
    expect(
      await hostMatchesDigests(
        'example.com',
        snap({ digest_version: 2, salt: '00000000000000000000000000000001' }),
        sha256hex,
      ),
    ).toBe(false)
    expect(
      await hostMatchesDigests(
        'example.com',
        snap({ salt: 'ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ' }),
        sha256hex,
      ),
    ).toBe(false)
  })
})
