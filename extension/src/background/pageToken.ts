import { parseHTTPURLHost } from './canonicalHost'

export type Sha256Utf8Hex = (canonical: string) => Promise<string> | string

export function canonicalPageFromHref(href: string): string | undefined {
  if (typeof href !== 'string' || href === '') return undefined
  if (parseHTTPURLHost(href) === undefined) return undefined
  let parsed: URL
  try {
    parsed = new URL(href)
  } catch {
    return undefined
  }
  return parsed.origin + parsed.pathname + parsed.search
}

export async function pageTokenFromCanonical(
  canonical: string,
  sha256utf8hex: Sha256Utf8Hex,
): Promise<string> {
  const hex = await sha256utf8hex(canonical)
  return hex.toLowerCase()
}

export async function sha256Utf8HexSubtle(canonical: string): Promise<string> {
  const bytes = new TextEncoder().encode(canonical)
  const digest = await crypto.subtle.digest('SHA-256', bytes)
  return [...new Uint8Array(digest)].map(b => b.toString(16).padStart(2, '0')).join('')
}

export async function pageTokenFromHref(
  href: string,
  sha256utf8hex: Sha256Utf8Hex = sha256Utf8HexSubtle,
): Promise<string | undefined> {
  const canonical = canonicalPageFromHref(href)
  if (!canonical) return undefined
  return pageTokenFromCanonical(canonical, sha256utf8hex)
}
