import { sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import { bumpDirectConnectGeneration } from './domConnectGeneration'
import { invalidateAllDomCatalogs } from './domCatalog'

const CLOSE_TAB_CAP = 64

export async function broadcastDomClose(catalogId?: string): Promise<void> {
  let httpTabs: browser.Tabs.Tab[]
  try {
    httpTabs = await browser.tabs.query({ url: ['http://*/*', 'https://*/*'] })
  } catch {
    httpTabs = []
  }
  const payload = catalogId ? { catalog_id: catalogId } : {}
  let sent = 0
  for (const tab of httpTabs) {
    if (sent >= CLOSE_TAB_CAP) break
    if (typeof tab.id !== 'number') continue
    void sendMessage('dom:close', payload, `content-script@${tab.id}`).catch(() => undefined)
    sent += 1
  }
}

export function dropDomCatalogsOnReconnect(): void {
  invalidateAllDomCatalogs()
  void broadcastDomClose()
}

export function notifyDomHostDown(): void {
  bumpDirectConnectGeneration()
  dropDomCatalogsOnReconnect()
}

export async function onDomUnpair(): Promise<void> {
  invalidateAllDomCatalogs()
  await broadcastDomClose()
}
