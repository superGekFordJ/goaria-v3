const MULTI_PART_SUFFIXES = [
  'co.uk',
  'org.uk',
  'ac.uk',
  'gov.uk',
  'com.au',
  'net.au',
  'co.jp',
  'co.kr',
  'com.br',
  'co.nz',
  'co.za',
  'com.tw',
  'github.io',
  'pages.dev',
] as const

const IPV4_RE = /^(?:\d{1,3}\.){3}\d{1,3}$/

function parseHref(raw: string): URL | undefined {
  try {
    return new URL(raw)
  } catch {
    return undefined
  }
}

function stripBrackets(host: string): string {
  if (host.startsWith('[') && host.endsWith(']')) return host.slice(1, -1)
  return host
}

function isIpOrLocalhost(hostname: string): boolean {
  const host = stripBrackets(hostname).toLowerCase()
  if (host === 'localhost') return true
  if (IPV4_RE.test(host)) return true
  return host.includes(':')
}

function registrableDomain(hostname: string): string {
  const host = stripBrackets(hostname).toLowerCase()
  if (isIpOrLocalhost(host)) return host
  for (const suffix of MULTI_PART_SUFFIXES) {
    if (host === suffix) return host
    if (host.endsWith(`.${suffix}`)) {
      const rest = host.slice(0, -(suffix.length + 1))
      const label = rest.split('.').pop() || ''
      return label ? `${label}.${suffix}` : suffix
    }
  }
  return host
}

export function isSchemefulSameSite(sourceHref: string, targetHref: string): boolean {
  const source = parseHref(sourceHref)
  const target = parseHref(targetHref)
  if (!source || !target) return false
  if (source.protocol !== target.protocol) return false
  const sourceHost = source.hostname.toLowerCase()
  const targetHost = target.hostname.toLowerCase()
  if (isIpOrLocalhost(sourceHost) || isIpOrLocalhost(targetHost)) {
    return stripBrackets(sourceHost) === stripBrackets(targetHost)
  }
  return registrableDomain(sourceHost) === registrableDomain(targetHost)
}
