export function parseHTTPURLHost(rawURL: string): string | undefined {
  let parsed: URL
  try {
    parsed = new URL(rawURL)
  } catch {
    return undefined
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return undefined
  }
  if (parsed.username !== '' || parsed.password !== '') {
    return undefined
  }
  if (parsed.host.includes(':') && parsed.port === '') {
    return undefined
  }

  let host = parsed.hostname
  if (host === '') {
    return undefined
  }
  if (host !== host.trim() || host.endsWith('.')) {
    return undefined
  }
  if (host.includes('%') || hostHasPercentEncoding(rawURL)) {
    return undefined
  }
  if (looksLikeIP(host)) {
    return undefined
  }

  host = host.toLowerCase()
  if (!validDomainHost(host)) {
    return undefined
  }
  return host
}

function hostHasPercentEncoding(rawURL: string): boolean {
  const schemeEnd = rawURL.indexOf('://')
  if (schemeEnd < 0) return false
  const rest = rawURL.slice(schemeEnd + 3)
  const hostEnd = rest.search(/[/?#]/)
  const hostPart = hostEnd < 0 ? rest : rest.slice(0, hostEnd)
  const at = hostPart.lastIndexOf('@')
  const host = at >= 0 ? hostPart.slice(at + 1) : hostPart
  return host.includes('%')
}

function looksLikeIP(host: string): boolean {
  if (host.includes(':')) {
    return true
  }
  const parts = host.split('.')
  if (parts.length !== 4) {
    return false
  }
  return parts.every((part) => {
    if (!/^\d{1,3}$/.test(part)) return false
    const n = Number(part)
    return n >= 0 && n <= 255
  })
}

function validDomainHost(host: string): boolean {
  if (host === '' || host !== host.trim() || host !== host.toLowerCase()) {
    return false
  }
  if (host.includes('://') || /[/\\?#@:]/u.test(host)) {
    return false
  }
  if (host.includes('*')) {
    return false
  }
  if (host.startsWith('.') || host.endsWith('.') || host.includes('..')) {
    return false
  }
  const labels = host.split('.')
  if (labels.length < 2) {
    return false
  }
  return labels.every(validDomainLabel)
}

function validDomainLabel(label: string): boolean {
  if (label === '' || label.length > 63) {
    return false
  }
  if (label.startsWith('-') || label.endsWith('-')) {
    return false
  }
  return /^[a-z0-9-]+$/.test(label)
}
