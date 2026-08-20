export const MATCH_DIGEST_VERSION = 1
const SALT_HEX = /^[0-9a-f]{32}$/
const PARENT_WALK_CAP = 16

export type MatchDigestSnapshot = {
  digest_version: number
  salt: string
  exact_digests: string[]
  subdomain_digests: string[]
}

export type Sha256Hex = (salt: Uint8Array, host: string) => Promise<string> | string

export function isValidMatchSnapshot(snap: MatchDigestSnapshot | undefined | null): snap is MatchDigestSnapshot {
  if (!snap || typeof snap !== 'object') return false
  if (snap.digest_version !== MATCH_DIGEST_VERSION) return false
  if (typeof snap.salt !== 'string' || !SALT_HEX.test(snap.salt)) return false
  if (!Array.isArray(snap.exact_digests) || !Array.isArray(snap.subdomain_digests)) return false
  if (!snap.exact_digests.every((d) => typeof d === 'string')) return false
  if (!snap.subdomain_digests.every((d) => typeof d === 'string')) return false
  return true
}

export async function hostMatchesDigests(
  host: string,
  snap: MatchDigestSnapshot | undefined | null,
  sha256hex: Sha256Hex,
): Promise<boolean> {
  if (!host || !isValidMatchSnapshot(snap)) {
    return false
  }
  const salt = parseSaltHex(snap.salt)
  if (!salt) {
    return false
  }
  const exact = new Set(snap.exact_digests)
  const subdomain = new Set(snap.subdomain_digests)

  const current = await digestOrFalse(sha256hex, salt, host)
  if (current === undefined) {
    return false
  }
  // IncludeSubdomains apex lives only in subdomain_digests; current host must still hit it.
  if (exact.has(current) || subdomain.has(current)) {
    return true
  }

  for (const parent of parentHosts(host, PARENT_WALK_CAP)) {
    const hashed = await digestOrFalse(sha256hex, salt, parent)
    if (hashed === undefined) {
      return false
    }
    if (subdomain.has(hashed)) {
      return true
    }
  }
  return false
}

async function digestOrFalse(sha256hex: Sha256Hex, salt: Uint8Array, host: string): Promise<string | undefined> {
  try {
    return await sha256hex(salt, host)
  } catch {
    return undefined
  }
}

function parseSaltHex(salt: string): Uint8Array | undefined {
  if (!SALT_HEX.test(salt)) return undefined
  const out = new Uint8Array(16)
  for (let i = 0; i < 16; i++) {
    out[i] = Number.parseInt(salt.slice(i * 2, i * 2 + 2), 16)
  }
  return out
}

function parentHosts(host: string, maxSteps: number): string[] {
  const out: string[] = []
  let current = host
  for (let i = 0; i < maxSteps; i++) {
    const dot = current.indexOf('.')
    if (dot < 0) break
    current = current.slice(dot + 1)
    if (current.split('.').filter(Boolean).length < 2) break
    out.push(current)
  }
  return out
}

export async function sha256HexSubtle(salt: Uint8Array, host: string): Promise<string> {
  const hostBytes = new TextEncoder().encode(host)
  const data = new Uint8Array(salt.length + hostBytes.length)
  data.set(salt, 0)
  data.set(hostBytes, salt.length)
  const digest = await crypto.subtle.digest('SHA-256', data)
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, '0')).join('')
}
