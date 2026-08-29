import browser from 'webextension-polyfill'

export type TabCandidate = {
  id: number
  url: string
  incognito: boolean
  discarded?: boolean
}

export function originOf(href: string): string | null {
  const value = href.trim()
  if (!value) return null
  try {
    const url = new URL(value)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null
    return url.origin
  } catch {
    return null
  }
}

function withoutFragment(href: string): string {
  const index = href.indexOf('#')
  return index >= 0 ? href.slice(0, index) : href
}

export function pickPresentationTab(
  candidates: TabCandidate[],
  opts: {
    referrer: string
    referrerOrigin: string
    incognito: boolean
    lastFocusedTabId?: number
  },
): TabCandidate | null {
  const matching = candidates.filter(
    tab =>
      Number.isInteger(tab.id) &&
      tab.id >= 0 &&
      tab.discarded !== true &&
      tab.incognito === opts.incognito &&
      originOf(tab.url) === opts.referrerOrigin,
  )
  const exact = matching.filter(
    tab =>
      tab.url === opts.referrer ||
      withoutFragment(tab.url) === withoutFragment(opts.referrer),
  )
  if (exact.length === 1) return exact[0]
  if (matching.length === 1) return matching[0]
  if (typeof opts.lastFocusedTabId === 'number') {
    const focused = matching.filter(tab => tab.id === opts.lastFocusedTabId)
    if (focused.length === 1) return focused[0]
  }
  return null
}

function toCandidate(tab: browser.Tabs.Tab): TabCandidate | null {
  if (typeof tab.id !== 'number' || typeof tab.url !== 'string') return null
  return {
    id: tab.id,
    url: tab.url,
    incognito: tab.incognito === true,
    discarded: tab.discarded === true,
  }
}

export async function resolvePresentationTab(opts: {
  referrer: string
  referrerOrigin: string
  incognito: boolean
}): Promise<TabCandidate | null> {
  try {
    const [allTabs, focusedTabs] = await Promise.all([
      browser.tabs.query({}),
      browser.tabs.query({ active: true, lastFocusedWindow: true }),
    ])
    const candidates = allTabs
      .map(toCandidate)
      .filter((tab): tab is TabCandidate => tab !== null)
    const focused = focusedTabs
      .map(toCandidate)
      .filter((tab): tab is TabCandidate => tab !== null)
    const lastFocusedTabId = focused.length === 1 ? focused[0].id : undefined
    return pickPresentationTab(candidates, { ...opts, lastFocusedTabId })
  } catch {
    return null
  }
}
