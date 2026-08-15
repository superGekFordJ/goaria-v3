import type { useTaskStore } from '../stores/task'

type DuplicateUriStore = Pick<ReturnType<typeof useTaskStore>, 'allUris'>

export const isValidUrl = (text: string): boolean => {
  return /^(https?|ftp|sftp|magnet):/i.test(text)
}

export const PAIR_PAGE_PATH = '/__goaria_pair__/pair.html'
export const PAIR_PORT_FALLBACKS = [16810, 16811, 16812, 16813, 16814] as const

export const isPairingUrl = (text: string): boolean => {
  return text.includes(PAIR_PAGE_PATH)
}

/**
 * Validates whether a given URL is a legitimate GoAria extension pairing URL.
 * Requires: http scheme, 127.0.0.1 host, no userinfo, allowed fallback port, and pairing path.
 */
export const isValidPairingUrl = (raw?: string): boolean => {
  if (!raw || typeof raw !== 'string') return false
  try {
    const parsed = new URL(raw.trim())
    if (parsed.protocol !== 'http:') return false
    if (parsed.hostname !== '127.0.0.1') return false
    if (parsed.username || parsed.password) return false
    const port = Number(parsed.port)
    if (!PAIR_PORT_FALLBACKS.includes(port as (typeof PAIR_PORT_FALLBACKS)[number])) return false
    if (parsed.pathname !== PAIR_PAGE_PATH) return false
    return true
  } catch {
    return false
  }
}

/**
 * Validates whether a given URL is a legitimate GitHub release notes URL for GoAria.
 * Requires: https scheme, github.com host, no userinfo, and release notes path.
 */
export const isValidReleaseNotesUrl = (raw?: string): boolean => {
  if (!raw || typeof raw !== 'string') return false
  try {
    const parsed = new URL(raw.trim())
    if (parsed.protocol !== 'https:') return false
    if (parsed.hostname !== 'github.com') return false
    if (parsed.username || parsed.password) return false
    if (!parsed.pathname.startsWith('/superGekFordJ/goaria-v3/releases')) return false
    return true
  } catch {
    return false
  }
}

export const isDuplicateUri = (uri: string, taskStore: DuplicateUriStore): boolean => {
  const needle = uri.trim()
  if (!needle) return false
  return taskStore.allUris.has(needle)
}
