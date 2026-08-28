import { canonicalizeDirectURL, MAX_DIRECT_DOWNLOAD_PAGE_BYTES } from './domCanonicalUrl'

export type ReferrerPolicyName =
  | 'no-referrer'
  | 'unsafe-url'
  | 'origin'
  | 'origin-when-cross-origin'
  | 'same-origin'
  | 'strict-origin'
  | 'strict-origin-when-cross-origin'
  | 'no-referrer-when-downgrade'

export type ReferrerInput = {
  pageHref: string
  targetHref: string
  documentPolicy?: string
  elementPolicy?: string
  relNoreferrer?: boolean
}

function parseUrl(raw: string): URL | undefined {
  try {
    return new URL(raw)
  } catch {
    return undefined
  }
}

function originSlash(url: URL): string {
  return `${url.origin}/`
}

function fullNoUserinfoFragment(url: URL): string {
  let out = `${url.origin}${url.pathname}`
  if (url.search) out += url.search
  return out
}

function sameOrigin(a: URL, b: URL): boolean {
  return a.origin === b.origin
}

function isHttpsToHttp(source: URL, target: URL): boolean {
  return source.protocol === 'https:' && target.protocol === 'http:'
}

function normalizePolicy(raw: string | undefined): ReferrerPolicyName {
  const value = (raw || '').trim().toLowerCase()
  switch (value) {
    case 'no-referrer':
    case 'unsafe-url':
    case 'origin':
    case 'origin-when-cross-origin':
    case 'same-origin':
    case 'strict-origin':
    case 'strict-origin-when-cross-origin':
    case 'no-referrer-when-downgrade':
      return value
    default:
      return 'strict-origin-when-cross-origin'
  }
}

function computeReferrerUrl(input: ReferrerInput): string | undefined {
  if (input.relNoreferrer) return undefined
  const page = parseUrl(input.pageHref)
  const target = parseUrl(input.targetHref)
  if (!page || !target) return undefined
  const policy = normalizePolicy(
    input.elementPolicy && input.elementPolicy.trim() !== ''
      ? input.elementPolicy
      : input.documentPolicy,
  )
  const cross = !sameOrigin(page, target)
  const downgrade = isHttpsToHttp(page, target)
  switch (policy) {
    case 'no-referrer':
      return undefined
    case 'unsafe-url':
      return fullNoUserinfoFragment(page)
    case 'origin':
      return originSlash(page)
    case 'origin-when-cross-origin':
      return cross ? originSlash(page) : fullNoUserinfoFragment(page)
    case 'same-origin':
      return cross ? undefined : fullNoUserinfoFragment(page)
    case 'strict-origin':
      return downgrade ? undefined : originSlash(page)
    case 'no-referrer-when-downgrade':
      return downgrade ? undefined : fullNoUserinfoFragment(page)
    case 'strict-origin-when-cross-origin':
    default:
      if (!cross) return fullNoUserinfoFragment(page)
      return downgrade ? undefined : originSlash(page)
  }
}

export function referrerResult(input: ReferrerInput): string | undefined {
  const raw = computeReferrerUrl(input)
  if (!raw) return undefined
  const canonical = canonicalizeDirectURL(raw, MAX_DIRECT_DOWNLOAD_PAGE_BYTES)
  return canonical
}
