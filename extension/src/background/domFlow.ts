import { onMessage, sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import { hasCapability } from './capabilities'
import { canonicalizeDirectURL, urlPathIsM3uPlaylist } from './domCanonicalUrl'
import {
  getDomCatalog,
  invalidateAllDomCatalogs,
  invalidateDomCatalogById,
  invalidateDomCatalogByTab,
  isDomCatalogAlive,
  mapDomCatalogIndices,
  projectDomCatalog,
  putDomCatalog,
  rememberDomSubmit,
  type DomCatalog,
  type DomCatalogItem,
} from './domCatalog'
import { collectCookieHeadersForUrls } from './domCookies'
import { currentDirectConnectGeneration } from './domConnectGeneration'
import { broadcastDomClose } from './domHostDown'
import { referrerResult } from './domReferrer'
import { buildDirectBatchPayload } from './directBatchRpc'
import { mintClientItemId, mintDirectBatchRequestId } from './mintRequestId'
import { folderFieldForSubmit, filterFolderName } from './pickerFolder'
import { resolveCookieStoreIdForTab } from './cookieCapture'
import { wsClient } from './wsClient'
import { CAP_DOWNLOAD_BATCH, EXTRACTOR_MAX_SESSION_ITEMS } from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'
import { t } from '../lib/i18n'
import type { I18nKey } from '../lib/i18n-keys'
import type {
  DomAliveMessage,
  DomCancelMessage,
  DomPingReply,
  DomScanReply,
  DomSubmitMessage,
  DomSubmitReply,
} from '../utils/messaging'

export { onDomUnpair, notifyDomHostDown } from './domHostDown'

const PING_TIMEOUT_MS = 1000
const inflightTabs = new Set<number>()
const collectInflight = new Set<number>()

type SenderTab = { tabId?: number }

function notify(titleKey: I18nKey, bodyKey: I18nKey, substitutions?: string[]): void {
  void browser.notifications
    .create({
      type: 'basic',
      iconUrl: browser.runtime.getURL('icons/icon-48.png'),
      title: t(titleKey),
      message: t(bodyKey, substitutions),
    })
    .catch(() => undefined)
}

function errorCodeOf(err: unknown): string {
  const message = err instanceof Error ? err.message : typeof err === 'string' ? err : ''
  if (
    message === 'busy' ||
    message === 'timeout' ||
    message === 'invalid_request' ||
    message === 'unavailable' ||
    message === 'unsupported' ||
    message === 'idempotency_conflict'
  ) {
    return message
  }
  if (message.includes('download.batch')) return 'unsupported'
  if (message.includes('WebSocket')) return 'disconnected'
  return message || 'generic'
}

function indicesKeyOf(indices: number[]): string {
  return [...indices].sort((a, b) => a - b).join(',')
}

function folderKeyOf(createGroup: boolean | undefined, folderName: string | undefined): string {
  return `${createGroup === true ? '1' : '0'}:${folderName ?? ''}`
}

function countList(value: unknown): number {
  return Array.isArray(value) ? value.length : 0
}

function hostnamePrefill(pageHref: string): string | undefined {
  try {
    return filterFolderName(new URL(pageHref).hostname)
  } catch {
    return undefined
  }
}

function clonePayload(payload: Record<string, unknown>): Record<string, unknown> {
  return JSON.parse(JSON.stringify(payload)) as Record<string, unknown>
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number): Promise<T | undefined> {
  let timeoutId: ReturnType<typeof setTimeout> | undefined
  const raced = await Promise.race([
    promise.then(
      value => ({ ok: true as const, value }),
      () => ({ ok: false as const, value: undefined }),
    ),
    new Promise<{ ok: false; value: undefined }>(resolve => {
      timeoutId = setTimeout(() => resolve({ ok: false, value: undefined }), timeoutMs)
    }),
  ])
  if (timeoutId !== undefined) clearTimeout(timeoutId)
  return raced.ok ? raced.value : undefined
}

async function pingContentScript(tabId: number): Promise<DomPingReply | undefined> {
  return withTimeout(
    sendMessage('dom:ping', {}, `content-script@${tabId}`) as Promise<DomPingReply>,
    PING_TIMEOUT_MS,
  )
}

async function scanContentScript(tabId: number): Promise<DomScanReply | undefined> {
  return withTimeout(
    sendMessage('dom:scan', {}, `content-script@${tabId}`) as Promise<DomScanReply>,
    PING_TIMEOUT_MS * 10,
  )
}

async function closeOverlay(tabId: number, catalogId?: string): Promise<void> {
  void sendMessage(
    'dom:close',
    catalogId ? { catalog_id: catalogId } : {},
    `content-script@${tabId}`,
  ).catch(() => undefined)
}

function catalogItemsFromScan(scan: DomScanReply): DomCatalogItem[] {
  const seen = new Set<string>()
  const items: DomCatalogItem[] = []
  const pageCanon = canonicalizeDirectURL(scan.page_href)
  for (const hit of scan.items) {
    if (!hit || typeof hit.url !== 'string') continue
    const canonical = canonicalizeDirectURL(hit.url)
    if (!canonical || urlPathIsM3uPlaylist(canonical)) continue
    if (pageCanon && canonical === pageCanon) continue
    if (seen.has(canonical)) continue
    if (items.length >= EXTRACTOR_MAX_SESSION_ITEMS) break
    seen.add(canonical)
    const item: DomCatalogItem = {
      url: canonical,
      kind:
        hit.kind === 'image' || hit.kind === 'video' || hit.kind === 'audio' || hit.kind === 'source'
          ? hit.kind
          : 'link',
      referrer: {
        documentPolicy: typeof hit.document_policy === 'string' ? hit.document_policy : '',
        elementPolicy: typeof hit.element_policy === 'string' ? hit.element_policy : '',
        relNoreferrer: hit.rel_noreferrer === true,
      },
    }
    if (typeof hit.filename === 'string' && hit.filename !== '') item.filename = hit.filename
    items.push(item)
  }
  return items
}

async function liveTab(tabId: number): Promise<browser.Tabs.Tab | undefined> {
  try {
    const tab = await browser.tabs.get(tabId)
    if (tab.discarded === true) return undefined
    return tab
  } catch {
    return undefined
  }
}

export async function handleCollectPageLinks(
  info: browser.Menus.OnClickData,
  tab: browser.Tabs.Tab | undefined,
): Promise<void> {
  if (!hasCapability(connectionState.capabilities, CAP_DOWNLOAD_BATCH)) {
    notify('dom_missing_cap_title', 'dom_missing_cap_body')
    return
  }
  if (typeof tab?.id !== 'number') {
    notify('dom_no_cs_title', 'dom_no_cs_body')
    return
  }
  if (typeof info.frameId !== 'number' || info.frameId !== 0) {
    notify('dom_iframe_refused_title', 'dom_iframe_refused_body')
    return
  }
  const tabId = tab.id
  if (collectInflight.has(tabId) || inflightTabs.has(tabId)) return
  collectInflight.add(tabId)
  try {
    const ping = await pingContentScript(tabId)
    if (!ping || typeof ping.document_nonce !== 'string' || typeof ping.page_href !== 'string') {
      notify('dom_no_cs_title', 'dom_no_cs_body')
      return
    }
    if (ping.extractor_picker_open) {
      notify('dom_mutex_title', 'dom_mutex_body')
      return
    }
    const storeId = await resolveCookieStoreIdForTab(tab)
    const storeUnproven = typeof storeId !== 'string' || storeId.trim() === ''
    const previousId = invalidateDomCatalogByTab(tabId)
    if (previousId) await closeOverlay(tabId, previousId)
    const scan = await scanContentScript(tabId)
    if (
      !scan ||
      scan.document_nonce !== ping.document_nonce ||
      scan.page_href !== ping.page_href
    ) {
      notify('dom_no_cs_title', 'dom_no_cs_body')
      return
    }
    const items = catalogItemsFromScan(scan)
    if (items.length === 0) {
      notify('dom_empty_scan_title', 'dom_empty_scan_body')
      return
    }
    const folderPrefill = filterFolderName(scan.title) || hostnamePrefill(scan.page_href)
    const catalog = putDomCatalog({
      tabId,
      documentNonce: scan.document_nonce,
      pageHref: scan.page_href,
      incognito: tab.incognito === true,
      cookieStoreId: storeUnproven ? undefined : storeId,
      storeUnproven,
      truncated: scan.truncated === true,
      items,
      folderPrefill,
    })
    const projection = projectDomCatalog(catalog)
    try {
      const reply = (await sendMessage('dom:open', projection, `content-script@${tabId}`)) as
        | { ok?: boolean }
        | undefined
      if (reply?.ok !== true) {
        invalidateDomCatalogById(catalog.catalogId)
        notify('dom_mutex_title', 'dom_mutex_body')
      }
    } catch {
      invalidateDomCatalogById(catalog.catalogId)
      notify('dom_no_cs_title', 'dom_no_cs_body')
    }
  } finally {
    collectInflight.delete(tabId)
  }
}

async function revalidateForSubmit(
  catalog: DomCatalog,
  tabId: number,
): Promise<{ error?: string; ping?: DomPingReply; tab?: browser.Tabs.Tab }> {
  if (!hasCapability(connectionState.capabilities, CAP_DOWNLOAD_BATCH)) {
    return { error: 'unsupported' }
  }
  if (catalog.directConnectGeneration !== currentDirectConnectGeneration()) {
    return { error: 'invalid_request' }
  }
  const tab = await liveTab(tabId)
  if (!tab) return { error: 'invalid_request' }
  if (tab.incognito === true !== catalog.incognito) {
    return { error: 'invalid_request' }
  }
  const ping = await pingContentScript(tabId)
  if (
    !ping ||
    ping.document_nonce !== catalog.documentNonce ||
    ping.page_href !== catalog.pageHref
  ) {
    return { error: 'invalid_request' }
  }
  if (ping.extractor_picker_open) return { error: 'busy' }
  return { ping, tab }
}

function storeStillProven(catalog: DomCatalog, liveStore: string | undefined): boolean {
  return (
    catalog.storeUnproven !== true &&
    typeof catalog.cookieStoreId === 'string' &&
    catalog.cookieStoreId !== '' &&
    typeof liveStore === 'string' &&
    liveStore.trim() === catalog.cookieStoreId
  )
}

function buildItemsPayload(
  selected: DomCatalogItem[],
  pageHref: string,
  cookieLines: Array<string | undefined>,
): Record<string, unknown>[] {
  const seenIds = new Set<string>()
  return selected.map((item, i) => {
    let clientId = mintClientItemId()
    while (seenIds.has(clientId)) clientId = mintClientItemId()
    seenIds.add(clientId)
    const rec: Record<string, unknown> = {
      client_item_id: clientId,
      url: item.url,
    }
    if (item.filename) rec.filename = item.filename
    const cookie = cookieLines[i]
    if (cookie) rec.headers = [cookie]
    const page = referrerResult({
      pageHref,
      targetHref: item.url,
      documentPolicy: item.referrer.documentPolicy,
      elementPolicy: item.referrer.elementPolicy,
      relNoreferrer: item.referrer.relNoreferrer,
    })
    if (page) rec.download_page = page
    return rec
  })
}

async function queryStatus(requestId: string): Promise<DomSubmitReply> {
  try {
    const ack = await wsClient.sendDirectBatchStatus(requestId)
    const status = typeof ack.status === 'string' ? ack.status : ''
    if (status === 'pending') {
      return { accepted: false, error_code: 'pending' }
    }
    if (status === 'complete') {
      return {
        accepted: true,
        succeeded: countList(ack.succeeded_item_ids),
        duplicate: countList(ack.duplicate_item_ids),
        error: ack.errors_by_item_id && typeof ack.errors_by_item_id === 'object'
          ? Object.keys(ack.errors_by_item_id).length
          : 0,
      }
    }
    if (status === 'not_found') {
      return { accepted: false, error_code: 'not_found' }
    }
    return { accepted: false, error_code: 'pending' }
  } catch {
    return { accepted: false, error_code: 'pending' }
  }
}

export async function handleDomSubmit(
  data: DomSubmitMessage,
  sender: SenderTab,
): Promise<DomSubmitReply> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.catalog_id !== 'string' || data.catalog_id === '') {
    return { accepted: false, error_code: 'invalid_request' }
  }
  const catalog = getDomCatalog(data.catalog_id)
  if (!catalog || catalog.tabId !== tabId) {
    return { accepted: false, error_code: 'invalid_request' }
  }
  const mapped = mapDomCatalogIndices(catalog, data.indices)
  if ('error' in mapped) return { accepted: false, error_code: 'invalid_request' }
  const check = await revalidateForSubmit(catalog, tabId)
  if (check.error) {
    if (check.error === 'invalid_request' || check.error === 'unsupported') {
      invalidateDomCatalogById(catalog.catalogId)
      await closeOverlay(tabId, catalog.catalogId)
    }
    return { accepted: false, error_code: check.error }
  }
  if (!check.tab) return { accepted: false, error_code: 'invalid_request' }
  if (inflightTabs.has(tabId)) return { accepted: false, error_code: 'busy' }
  inflightTabs.add(tabId)
  try {
    const fields = folderFieldForSubmit({
      createGroup: data.create_group === true,
      selectedCount: mapped.items.length,
      raw: typeof data.folder_name === 'string' ? data.folder_name : '',
    })
    const idxKey = indicesKeyOf(data.indices)
    const folderKey = folderKeyOf(fields.create_group, fields.folder_name)
    const liveStore = await resolveCookieStoreIdForTab(check.tab)
    const storeLiveOk = storeStillProven(catalog, liveStore)
    const last = catalog.lastSubmit
    let requestId: string
    let payload: Record<string, unknown>
    if (last && last.indicesKey === idxKey && last.folderKey === folderKey && storeLiveOk) {
      requestId = last.requestId
      payload = clonePayload(last.payload)
    } else {
      requestId = mintDirectBatchRequestId()
      const cookieLines = await collectCookieHeadersForUrls({
        urls: mapped.items.map(item => item.url),
        sourceHref: catalog.pageHref,
        storeId: storeLiveOk ? catalog.cookieStoreId : undefined,
        storeUnproven: !storeLiveOk,
        getAll: details => browser.cookies.getAll(details) as Promise<unknown[]>,
      })
      const items = buildItemsPayload(mapped.items, catalog.pageHref, cookieLines)
      const built = buildDirectBatchPayload({
        items,
        ...fields,
      })
      if ('error' in built) {
        return { accepted: false, error_code: 'invalid_request' }
      }
      payload = built.payload
      rememberDomSubmit(catalog, {
        requestId,
        indicesKey: idxKey,
        folderKey,
        payload: clonePayload(payload),
      })
    }
    if (getDomCatalog(catalog.catalogId) !== catalog) {
      return { accepted: false, error_code: 'invalid_request' }
    }
    const again = await revalidateForSubmit(catalog, tabId)
    if (again.error) {
      if (again.error === 'invalid_request' || again.error === 'unsupported') {
        invalidateDomCatalogById(catalog.catalogId)
        await closeOverlay(tabId, catalog.catalogId)
      }
      return { accepted: false, error_code: again.error }
    }
    if (getDomCatalog(catalog.catalogId) !== catalog) {
      return { accepted: false, error_code: 'invalid_request' }
    }
    try {
      const ack = await wsClient.sendDirectBatch(payload, requestId)
      const succeeded = countList(ack.succeeded_item_ids)
      const duplicate = countList(ack.duplicate_item_ids)
      const error =
        ack.errors_by_item_id && typeof ack.errors_by_item_id === 'object'
          ? Object.keys(ack.errors_by_item_id).length
          : 0
      invalidateDomCatalogById(catalog.catalogId)
      await closeOverlay(tabId, catalog.catalogId)
      notify('dom_notif_title', 'dom_success_body', [
        String(succeeded),
        String(duplicate),
        String(error),
      ])
      return { accepted: true, succeeded, duplicate, error }
    } catch (err) {
      const code = errorCodeOf(err)
      if (code === 'busy') {
        return { accepted: false, error_code: 'busy' }
      }
      if (code === 'timeout' || code === 'disconnected') {
        const status = await queryStatus(requestId)
        if (status.accepted) {
          invalidateDomCatalogById(catalog.catalogId)
          await closeOverlay(tabId, catalog.catalogId)
          notify('dom_notif_title', 'dom_success_body', [
            String(status.succeeded ?? 0),
            String(status.duplicate ?? 0),
            String(status.error ?? 0),
          ])
        } else if (status.error_code === 'not_found') {
          invalidateDomCatalogById(catalog.catalogId)
          await closeOverlay(tabId, catalog.catalogId)
          notify('dom_notif_title', 'dom_not_found')
        }
        return status
      }
      invalidateDomCatalogById(catalog.catalogId)
      await closeOverlay(tabId, catalog.catalogId)
      return { accepted: false, error_code: code }
    }
  } finally {
    inflightTabs.delete(tabId)
  }
}

export async function handleDomCancel(
  data: DomCancelMessage,
  sender: SenderTab,
): Promise<{ ok: boolean }> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.catalog_id !== 'string') return { ok: false }
  const catalog = getDomCatalog(data.catalog_id)
  if (catalog && catalog.tabId !== tabId) return { ok: false }
  invalidateDomCatalogById(data.catalog_id)
  await closeOverlay(tabId, data.catalog_id)
  return { ok: true }
}

export function handleDomAlive(data: DomAliveMessage, sender: SenderTab): { ok: boolean } {
  if (typeof sender.tabId !== 'number' || typeof data?.catalog_id !== 'string' || data.catalog_id === '') {
    return { ok: false }
  }
  const catalog = getDomCatalog(data.catalog_id)
  if (!catalog || catalog.tabId !== sender.tabId) return { ok: false }
  return { ok: isDomCatalogAlive(data.catalog_id) }
}

export async function handleDomStatus(
  data: DomAliveMessage,
  sender: SenderTab,
): Promise<DomSubmitReply> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.catalog_id !== 'string' || data.catalog_id === '') {
    return { accepted: false, error_code: 'invalid_request' }
  }
  const catalog = getDomCatalog(data.catalog_id)
  if (!catalog || catalog.tabId !== tabId || !catalog.lastSubmit) {
    return { accepted: false, error_code: 'not_found' }
  }
  const status = await queryStatus(catalog.lastSubmit.requestId)
  if (status.accepted) {
    invalidateDomCatalogById(catalog.catalogId)
    await closeOverlay(tabId, catalog.catalogId)
    notify('dom_notif_title', 'dom_success_body', [
      String(status.succeeded ?? 0),
      String(status.duplicate ?? 0),
      String(status.error ?? 0),
    ])
  } else if (status.error_code === 'not_found') {
    invalidateDomCatalogById(catalog.catalogId)
    await closeOverlay(tabId, catalog.catalogId)
    notify('dom_notif_title', 'dom_not_found')
  }
  return status
}

function dropTab(tabId: number): void {
  const id = invalidateDomCatalogByTab(tabId)
  if (id) void closeOverlay(tabId, id)
}

export function initDomFlow(): void {
  onMessage('dom:submit', ({ data, sender }: { data: DomSubmitMessage; sender: SenderTab }) =>
    handleDomSubmit(data, sender),
  )
  onMessage('dom:cancel', ({ data, sender }: { data: DomCancelMessage; sender: SenderTab }) =>
    handleDomCancel(data, sender),
  )
  onMessage('dom:alive', ({ data, sender }: { data: DomAliveMessage; sender: SenderTab }) =>
    handleDomAlive(data, sender),
  )
  onMessage('dom:status', ({ data, sender }: { data: DomAliveMessage; sender: SenderTab }) =>
    handleDomStatus(data, sender),
  )
  browser.tabs.onRemoved.addListener(tabId => {
    dropTab(tabId)
  })
  const replaced = (
    browser.tabs as { onReplaced?: { addListener: (cb: (added: number, removed: number) => void) => void } }
  ).onReplaced
  replaced?.addListener((_added, removed) => {
    dropTab(removed)
  })
  browser.tabs.onUpdated.addListener((tabId, changeInfo) => {
    if (changeInfo.discarded === true || typeof changeInfo.url === 'string') {
      dropTab(tabId)
    }
  })
}

export function resetDomFlowForTests(): void {
  inflightTabs.clear()
  collectInflight.clear()
  invalidateAllDomCatalogs()
}

export { broadcastDomClose }
