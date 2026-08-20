import browser from 'webextension-polyfill'
import { hasCapability } from './capabilities'
import { parseHTTPURLHost } from './canonicalHost'
import { hostMatchesDigests, sha256HexSubtle } from './digestMatch'
import { getMatchGeneration, getMatchSnapshot, isMatchGenerationCurrent } from './matchSnapshot'
import { connectionState } from '../stores/connection.svelte'
import { CAP_EXTRACTOR_RESOLVE } from '../stores/config.svelte'
import { deliverExtractorDetected } from './extractorVisibility'

const RESCAN_CAP = 64

export type TabScanInfo = {
  url?: string
  status?: string
}

export type RescanTab = {
  id?: number
  url?: string
}

export function shouldScanChange(changeInfo: TabScanInfo): boolean {
  if (typeof changeInfo.url === 'string' && changeInfo.url !== '') {
    return true
  }
  return changeInfo.status === 'complete'
}

export function pickTabUrl(changeInfo: TabScanInfo, tabUrl: string | undefined): string | undefined {
  if (typeof changeInfo.url === 'string' && changeInfo.url !== '') {
    return changeInfo.url
  }
  if (typeof tabUrl === 'string' && tabUrl !== '') {
    return tabUrl
  }
  return undefined
}

export function isHttpUrl(url: string | undefined): boolean {
  if (!url) return false
  const lower = url.toLowerCase()
  return lower.startsWith('https://') || lower.startsWith('http://')
}

export function unionRescanTabs(httpTabs: RescanTab[], activeTab: RescanTab | undefined, cap: number): RescanTab[] {
  const out: RescanTab[] = []
  const seen = new Set<number>()
  for (const tab of httpTabs) {
    if (out.length >= cap) break
    if (typeof tab.id !== 'number' || !isHttpUrl(tab.url) || seen.has(tab.id)) {
      continue
    }
    seen.add(tab.id)
    out.push(tab)
  }
  if (
    activeTab &&
    typeof activeTab.id === 'number' &&
    isHttpUrl(activeTab.url) &&
    !seen.has(activeTab.id)
  ) {
    out.push(activeTab)
  }
  return out
}

export function initTabMatcher(): void {
  browser.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
    void handleTabUpdated(tabId, changeInfo, tab)
  })
}

export async function rescanHttpTabs(): Promise<void> {
  if (!matchingEnabled()) {
    return
  }
  let httpTabs: browser.Tabs.Tab[]
  try {
    httpTabs = await browser.tabs.query({ url: ['http://*/*', 'https://*/*'] })
  } catch {
    httpTabs = []
  }
  let active: browser.Tabs.Tab | undefined
  try {
    const [tab] = await browser.tabs.query({ active: true, lastFocusedWindow: true })
    active = tab
  } catch {
    active = undefined
  }
  for (const tab of unionRescanTabs(httpTabs, active, RESCAN_CAP)) {
    if (typeof tab.id !== 'number' || !tab.url) continue
    await maybeDetect(tab.id, tab.url)
  }
}

function matchingEnabled(): boolean {
  return (
    getMatchSnapshot() !== undefined &&
    hasCapability(connectionState.capabilities, CAP_EXTRACTOR_RESOLVE)
  )
}

async function handleTabUpdated(
  tabId: number,
  changeInfo: browser.Tabs.OnUpdatedChangeInfoType,
  tab: browser.Tabs.Tab,
): Promise<void> {
  if (!shouldScanChange(changeInfo)) return
  if (!matchingEnabled()) return
  await maybeDetect(tabId, pickTabUrl(changeInfo, tab.url))
}

async function maybeDetect(tabId: number, url: string | undefined): Promise<void> {
  const snap = getMatchSnapshot()
  const gen = getMatchGeneration()
  if (!snap || !hasCapability(connectionState.capabilities, CAP_EXTRACTOR_RESOLVE)) {
    return
  }
  const host = parseHTTPURLHost(url ?? '')
  if (!host) return
  const hit = await hostMatchesDigests(host, snap, sha256HexSubtle)
  if (!hit) return
  if (!isMatchGenerationCurrent(gen)) return
  void deliverExtractorDetected(tabId, gen, url).catch(() => undefined)
}
