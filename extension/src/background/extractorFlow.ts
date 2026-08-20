import { onMessage, sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import { hasCapability } from './capabilities'
import { getStructuredCookiesForUrl, resolveCookieStoreIdForTab } from './cookieCapture'
import {
  failedItemIds,
  isBatchSuccess,
  isResolveHit,
  mapCaughtError,
  resolveItemsAreComplete,
  sanitizeDisplayFilename,
} from './extractorAck'
import { buildExtractorBatchPayload } from './extractorRpc'
import { mintRequestId } from './mintRequestId'
import { canReuseBatch } from './extractorBatchReuse'
import { buildPickerCatalog, mapPickerIndices, sameIdList } from './pickerCatalog'
import { folderFieldForSubmit, type FolderSubmitFields } from './pickerFolder'
import { hasItemOutcomeLists, remainingItemIds, shrinkDisplayItems, zipDisplayItems } from './pickerShrink'
import { pageTokenFromHref } from './pageToken'
import { leaseDeadlineFromSend, type ExtractorDisplayItem, type ExtractorSessionRecord } from './extractorSessionStore'
import {
  broadcastHide,
  getExtractorSessionStore,
  pushExtractorResult,
  pushPickerCatalog,
  showExtractorFallbackNotification,
} from './extractorVisibility'
import {
  cancelAllClicks,
  cancelTabClick,
  clickEpochOf,
  hasInFlight,
  releaseClick,
  tryLockClick,
} from './extractorClickLock'
import { wsClient } from './wsClient'
import {
  CAP_EXTRACTOR_BATCH,
  EXTRACTOR_MAX_RESOLVE_COOKIES,
  MSG_TYPE_BATCH_DOWNLOAD,
  MSG_TYPE_EXTRACTOR_RESOLVE,
} from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'
import type {
  ExtractorClickMessage,
  ExtractorFallbackMessage,
  ExtractorIgnoreMessage,
  ExtractorNavMessage,
  ExtractorPickerOpenMessage,
  ExtractorPickerOpenReply,
  ExtractorPickerSubmitMessage,
  ExtractorResultMessage,
} from '../utils/messaging'

type SenderTab = { tabId?: number }

async function isClickStale(tabId: number, token: string, epoch: number): Promise<boolean> {
  if (clickEpochOf(tabId) !== epoch) return true
  if (await getExtractorSessionStore().isIgnored(tabId, token)) return true
  try {
    const tab = await browser.tabs.get(tabId)
    const live = await pageTokenFromHref(tab.url ?? '')
    if (!live || live !== token) return true
  } catch {
    return true
  }
  return false
}

async function putLiveSession(
  tabId: number,
  token: string,
  epoch: number,
  record: ExtractorSessionRecord,
): Promise<boolean> {
  if (await isClickStale(tabId, token, epoch)) return false
  await getExtractorSessionStore().putSession(record)
  if (await isClickStale(tabId, token, epoch)) {
    await getExtractorSessionStore().deleteSession(tabId)
    return false
  }
  return true
}

async function pushIfLive(
  tabId: number,
  token: string,
  epoch: number,
  payload: ExtractorResultMessage,
): Promise<void> {
  if (await isClickStale(tabId, token, epoch)) return
  await pushExtractorResult(tabId, payload)
}

export function initExtractorFlow(): void {
  onMessage(
    'extractor:click',
    async ({ data, sender }: { data: ExtractorClickMessage; sender: SenderTab }) => {
      return handleClick(data, sender)
    },
  )
  onMessage(
    'extractor:picker-open',
    async ({ data, sender }: { data: ExtractorPickerOpenMessage; sender: SenderTab }) => {
      return handlePickerOpen(data, sender)
    },
  )
  onMessage(
    'extractor:picker-submit',
    async ({ data, sender }: { data: ExtractorPickerSubmitMessage; sender: SenderTab }) => {
      return handlePickerSubmit(data, sender)
    },
  )
  onMessage(
    'extractor:ignore',
    async ({ data, sender }: { data: ExtractorIgnoreMessage; sender: SenderTab }) => {
      return handleIgnore(data, sender)
    },
  )
  onMessage(
    'extractor:nav',
    async ({ data, sender }: { data: ExtractorNavMessage; sender: SenderTab }) => {
      return handleNav(data, sender)
    },
  )
  onMessage(
    'extractor:fallback',
    async ({ data, sender }: { data: ExtractorFallbackMessage; sender: SenderTab }) => {
      return handleFallback(data, sender)
    },
  )
  browser.tabs.onRemoved.addListener(tabId => {
    cancelTabClick(tabId)
    void getExtractorSessionStore().clearTab(tabId)
  })
  void restoreExtractorUi()
}

async function handleIgnore(
  data: ExtractorIgnoreMessage,
  sender: SenderTab,
): Promise<{ ok: boolean }> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.page_token !== 'string' || data.page_token === '') {
    return { ok: false }
  }
  let tab: browser.Tabs.Tab
  try {
    tab = await browser.tabs.get(tabId)
  } catch {
    return { ok: false }
  }
  const token = await pageTokenFromHref(tab.url ?? '')
  if (!token || token !== data.page_token) return { ok: false }
  const sessions = getExtractorSessionStore()
  await sessions.setIgnored(tabId, token)
  cancelTabClick(tabId)
  await sessions.deleteSession(tabId)
  void sendMessage(
    'extractor:hide',
    { reason: 'ignore', page_token: token },
    `content-script@${tabId}`,
  ).catch(() => undefined)
  return { ok: true }
}

export async function handleNav(
  data: ExtractorNavMessage,
  sender: SenderTab,
): Promise<{ ok: boolean }> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.page_token !== 'string' || data.page_token === '') {
    return { ok: false }
  }
  const claimed = data.page_token
  const sessions = getExtractorSessionStore()
  const rec = await sessions.getSession(tabId)
  if (rec && rec.pageToken === claimed) {
    cancelTabClick(tabId)
    await sessions.deleteSession(tabId)
    return { ok: true }
  }
  if (rec && rec.pageToken !== claimed) return { ok: true }
  let live: string | undefined
  try {
    const tab = await browser.tabs.get(tabId)
    live = await pageTokenFromHref(tab.url ?? '')
  } catch {
    cancelTabClick(tabId)
    return { ok: true }
  }
  if (live === claimed) {
    if (hasInFlight(tabId)) {
      cancelTabClick(tabId)
      return { ok: true }
    }
    return { ok: false }
  }
  cancelTabClick(tabId)
  return { ok: true }
}

export async function handleFallback(
  data: ExtractorFallbackMessage,
  sender: SenderTab,
): Promise<{ ok: boolean }> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.page_token !== 'string' || data.page_token === '') {
    return { ok: false }
  }
  let live: string | undefined
  try {
    const tab = await browser.tabs.get(tabId)
    live = await pageTokenFromHref(tab.url ?? '')
  } catch {
    return { ok: false }
  }
  if (!live || live !== data.page_token) return { ok: false }
  await showExtractorFallbackNotification(tabId, data.page_token)
  return { ok: true }
}

export async function handlePickerOpen(
  data: ExtractorPickerOpenMessage,
  sender: SenderTab,
): Promise<ExtractorPickerOpenReply> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.page_token !== 'string' || data.page_token === '') {
    return { ok: false, error_code: 'invalid_request' }
  }
  let tab: browser.Tabs.Tab
  try {
    tab = await browser.tabs.get(tabId)
  } catch {
    return { ok: false, error_code: 'invalid_request' }
  }
  const token = await pageTokenFromHref(tab.url ?? '')
  if (!token || token !== data.page_token) {
    return { ok: false, error_code: 'invalid_request' }
  }
  const sessions = getExtractorSessionStore()
  if (await sessions.isIgnored(tabId, token)) {
    return { ok: false, error_code: 'session_expired' }
  }
  const session = await sessions.getSession(tabId)
  if (!session || session.pageToken !== token) {
    return { ok: false, error_code: 'session_expired' }
  }
  if (session.state !== 'ready' || !session.itemIds || session.itemIds.length < 2) {
    return { ok: false, error_code: 'invalid_request' }
  }
  const catalog = buildPickerCatalog(session.itemIds, session.displayItems, session.leaseDeadline)
  if ('error' in catalog) {
    return { ok: false, error_code: 'invalid_request' }
  }
  return {
    ok: true,
    items: catalog.items,
    count: catalog.count,
    lease_deadline: catalog.lease_deadline,
  }
}

export async function handlePickerSubmit(
  data: ExtractorPickerSubmitMessage,
  sender: SenderTab,
): Promise<{ accepted: boolean; error_code?: string }> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.page_token !== 'string' || data.page_token === '') {
    return { accepted: false, error_code: 'invalid_request' }
  }
  const epoch = tryLockClick(tabId)
  if (epoch === undefined) return { accepted: false, error_code: 'busy' }
  let transferred = false
  try {
    let tab: browser.Tabs.Tab
    try {
      tab = await browser.tabs.get(tabId)
    } catch {
      return { accepted: false, error_code: 'invalid_request' }
    }
    const token = await pageTokenFromHref(tab.url ?? '')
    if (!token || token !== data.page_token) {
      return { accepted: false, error_code: 'invalid_request' }
    }
    const sessions = getExtractorSessionStore()
    if (await sessions.isIgnored(tabId, token)) {
      return { accepted: false, error_code: 'session_expired' }
    }
    const session = await sessions.getSession(tabId)
    if (!session || session.pageToken !== token || !session.sessionId || !session.itemIds?.length) {
      return { accepted: false, error_code: 'session_expired' }
    }
    const reuse = canReuseBatch(session)
    if (
      !reuse &&
      (session.errorCode === 'auth_expired' ||
        session.errorCode === 'session_expired' ||
        session.errorCode === 'pack_error' ||
        session.state !== 'ready')
    ) {
      return { accepted: false, error_code: 'session_expired' }
    }
    const mapped = mapPickerIndices(data.indices, session.itemIds)
    if ('error' in mapped) {
      return { accepted: false, error_code: 'invalid_request' }
    }
    const fields = folderFieldForSubmit({
      createGroup: data.create_group === true,
      selectedCount: mapped.itemIds.length,
      raw: typeof data.folder_name === 'string' ? data.folder_name : '',
    })
    if (reuse) {
      if (!sameIdList(mapped.itemIds, reuse.itemIds) || !reuseProjectionMatches(session, fields)) {
        return { accepted: false, error_code: 'invalid_request' }
      }
      void runBatch(
        tabId,
        token,
        reuse.sessionId,
        reuse.itemIds,
        reuse.requestId,
        session,
        reuse.markRetry,
        epoch,
        groupFromSession(session),
      )
        .catch(err => handleFlowError(tabId, token, session, err, epoch))
        .finally(() => {
          releaseClick(tabId, epoch)
        })
      transferred = true
      return { accepted: true }
    }
    const requestId = mintRequestId()
    void runBatch(
      tabId,
      token,
      session.sessionId,
      mapped.itemIds,
      requestId,
      session,
      false,
      epoch,
      fields,
    )
      .catch(err => handleFlowError(tabId, token, session, err, epoch))
      .finally(() => {
        releaseClick(tabId, epoch)
      })
    transferred = true
    return { accepted: true }
  } finally {
    if (!transferred) releaseClick(tabId, epoch)
  }
}

function reuseProjectionMatches(session: ExtractorSessionRecord, fields: FolderSubmitFields): boolean {
  const wantCreate = session.lastCreateGroup === true
  const gotCreate = fields.create_group === true
  if (wantCreate !== gotCreate) return false
  return (session.lastFolderName ?? '') === (fields.folder_name ?? '')
}

function groupFromSession(session: ExtractorSessionRecord | null): FolderSubmitFields | undefined {
  if (!session) return undefined
  const fields: FolderSubmitFields = {}
  if (session.lastCreateGroup === true) fields.create_group = true
  if (session.lastFolderName) fields.folder_name = session.lastFolderName
  return fields.create_group || fields.folder_name ? fields : undefined
}

export async function handleClick(
  data: ExtractorClickMessage,
  sender: SenderTab,
): Promise<{ accepted: boolean; error_code?: string }> {
  const tabId = sender.tabId
  if (typeof tabId !== 'number' || typeof data?.page_token !== 'string' || data.page_token === '') {
    return { accepted: false, error_code: 'invalid_request' }
  }
  const epoch = tryLockClick(tabId)
  if (epoch === undefined) return { accepted: true }
  let transferred = false
  try {
    let tab: browser.Tabs.Tab
    try {
      tab = await browser.tabs.get(tabId)
    } catch {
      return { accepted: false, error_code: 'invalid_request' }
    }
    const token = await pageTokenFromHref(tab.url ?? '')
    if (!token || token !== data.page_token) {
      return { accepted: false, error_code: 'invalid_request' }
    }
    const sessions = getExtractorSessionStore()
    if (await sessions.isIgnored(tabId, token)) {
      return { accepted: false }
    }
    if (!hasCapability(connectionState.capabilities, CAP_EXTRACTOR_BATCH)) {
      pushExtractorResult(tabId, { page_token: token, ui: 'error', error_code: 'no_batch' })
      return { accepted: false, error_code: 'no_batch' }
    }
    const session = await sessions.getSession(tabId)
    if (session && session.pageToken !== token) {
      await sessions.deleteSession(tabId)
    }
    const live = session && session.pageToken === token ? session : null
    if (live?.state === 'ready' && (live.itemIds?.length ?? 0) > 1) {
      pushExtractorResult(tabId, {
        page_token: token,
        ui: 'ready',
        count: live.itemIds?.length,
        filename: live.displayItems?.[0]?.filename,
        lease_deadline: live.leaseDeadline,
      })
      return { accepted: false }
    }
    void runClick(tabId, tab, token, live, epoch).finally(() => {
      releaseClick(tabId, epoch)
    })
    transferred = true
    return { accepted: true }
  } finally {
    if (!transferred) releaseClick(tabId, epoch)
  }
}

async function runClick(
  tabId: number,
  tab: browser.Tabs.Tab,
  token: string,
  session: ExtractorSessionRecord | null,
  epoch: number,
): Promise<void> {
  try {
    if (await isClickStale(tabId, token, epoch)) return
    const reuse = canReuseBatch(session)
    if (reuse) {
      await runBatch(
        tabId,
        token,
        reuse.sessionId,
        reuse.itemIds,
        reuse.requestId,
        session,
        reuse.markRetry,
        epoch,
        groupFromSession(session),
      )
      return
    }
    if (session?.state === 'ready' && session.sessionId && session.itemIds?.length === 1) {
      const requestId = mintRequestId()
      await runBatch(tabId, token, session.sessionId, session.itemIds, requestId, session, false, epoch)
      return
    }
    await runResolveThenMaybeBatch(tabId, tab, token, session, epoch)
  } catch (err) {
    await handleFlowError(tabId, token, session, err, epoch)
  }
}

async function runResolveThenMaybeBatch(
  tabId: number,
  tab: browser.Tabs.Tab,
  token: string,
  previous: ExtractorSessionRecord | null,
  epoch: number,
): Promise<void> {
  if (await isClickStale(tabId, token, epoch)) return
  const storeId = await resolveCookieStoreIdForTab(tab)
  if (await isClickStale(tabId, token, epoch)) return
  if (!storeId) {
    await putErrorSession(tabId, token, previous, 'no_store', epoch)
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'no_store' })
    return
  }
  const collected = await getStructuredCookiesForUrl(tab.url ?? '', storeId)
  if (await isClickStale(tabId, token, epoch)) return
  if ('error' in collected) {
    await putErrorSession(tabId, token, previous, 'cookie_error', epoch)
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'cookie_error' })
    return
  }
  if (collected.cookies.length > EXTRACTOR_MAX_RESOLVE_COOKIES) {
    await putErrorSession(tabId, token, previous, 'invalid_request', epoch)
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'invalid_request' })
    return
  }
  const resolveSentAt = Date.now()
  const payload: Record<string, unknown> = {
    source_url: tab.url,
    cookies: collected.cookies,
  }
  if (typeof navigator !== 'undefined' && typeof navigator.userAgent === 'string') {
    payload.user_agent = navigator.userAgent
  }
  if (typeof navigator !== 'undefined' && typeof navigator.language === 'string') {
    payload.accept_language = navigator.language
  }
  if (await isClickStale(tabId, token, epoch)) return
  if (!(await putLiveSession(tabId, token, epoch, {
    tabId,
    pageToken: token,
    generation: previous?.generation ?? 0,
    state: 'resolving',
    resolveSentAt,
    leaseDeadline: leaseDeadlineFromSend(resolveSentAt),
  }))) {
    return
  }
  if (await isClickStale(tabId, token, epoch)) return
  const ack = await wsClient.sendRequest(MSG_TYPE_EXTRACTOR_RESOLVE, payload)
  await onResolveAck(tabId, token, previous, ack, resolveSentAt, epoch)
}

async function onResolveAck(
  tabId: number,
  token: string,
  previous: ExtractorSessionRecord | null,
  ack: Record<string, unknown>,
  resolveSentAt: number,
  epoch: number,
): Promise<void> {
  if (await isClickStale(tabId, token, epoch)) return
  if (!isResolveHit(ack)) {
    const code = typeof ack.error_code === 'string' && ack.error_code !== '' ? ack.error_code : ''
    if (
      ack.matched === false ||
      !Array.isArray(ack.items) ||
      ack.items.length === 0 ||
      !resolveItemsAreComplete(ack.items)
    ) {
      await getExtractorSessionStore().deleteSession(tabId)
      void sendHide(tabId, 'matched_false', token)
      return
    }
    const mapped = code || 'generic'
    if (mapped === 'unsupported') {
      await getExtractorSessionStore().deleteSession(tabId)
      void sendHide(tabId, 'matched_false', token)
      return
    }
    await putErrorSession(tabId, token, previous, mapped, epoch)
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: mapped })
    return
  }
  const sessionId = ack.session_id as string
  const parsedItems = readResolveItems(ack.items)
  const itemIds = parsedItems.map(item => item.item_id)
  const displayItems = parsedItems.map(toDisplayItem)
  const ackReceivedAt = Date.now()
  const leaseDeadline = leaseDeadlineFromSend(resolveSentAt)
  if (itemIds.length > 1) {
    if (!(await putLiveSession(tabId, token, epoch, {
      tabId,
      pageToken: token,
      generation: previous?.generation ?? 0,
      state: 'ready',
      sessionId,
      itemIds,
      displayItems,
      resolveSentAt,
      ackReceivedAt,
      leaseDeadline,
    }))) {
      return
    }
    await pushIfLive(tabId, token, epoch, {
      page_token: token,
      ui: 'ready',
      count: itemIds.length,
      filename: displayItems[0]?.filename,
      lease_deadline: leaseDeadline,
    })
    return
  }
  const batchRequestId = mintRequestId()
  if (!(await putLiveSession(tabId, token, epoch, {
    tabId,
    pageToken: token,
    generation: previous?.generation ?? 0,
    state: 'committing',
    sessionId,
    itemIds,
    batchRequestId,
    displayItems,
    resolveSentAt,
    ackReceivedAt,
    leaseDeadline,
  }))) {
    return
  }
  await runBatch(tabId, token, sessionId, itemIds, batchRequestId, previous, false, epoch)
}

async function runBatch(
  tabId: number,
  token: string,
  sessionId: string,
  itemIds: string[],
  requestId: string,
  previous: ExtractorSessionRecord | null,
  markRetry: boolean,
  epoch: number,
  group?: FolderSubmitFields,
): Promise<void> {
  if (await isClickStale(tabId, token, epoch)) return
  const input: Record<string, unknown> = { session_id: sessionId, item_ids: itemIds }
  if (group?.create_group === true) input.create_group = true
  if (typeof group?.folder_name === 'string' && group.folder_name !== '') {
    input.folder_name = group.folder_name
  }
  const built = buildExtractorBatchPayload(input)
  if ('error' in built) {
    await putErrorSession(tabId, token, previous, 'invalid_request', epoch, {
      sessionId,
      itemIds,
      batchRequestId: requestId,
      batchRetryUsed: markRetry,
      lastCreateGroup: group?.create_group === true ? true : undefined,
      lastFolderName: group?.folder_name,
    })
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'invalid_request' })
    return
  }
  const current = await getExtractorSessionStore().getSession(tabId)
  if (await isClickStale(tabId, token, epoch)) return
  const sourceIds = current?.itemIds ?? previous?.itemIds ?? itemIds
  const sourceDisplay = current?.displayItems ?? previous?.displayItems ?? []
  const zipped = zipDisplayItems(sourceIds, sourceDisplay, itemIds)
  if ('error' in zipped) {
    await putErrorSession(tabId, token, previous, 'invalid_request', epoch, {
      sessionId,
      itemIds,
      batchRequestId: requestId,
      batchRetryUsed: markRetry,
      lastCreateGroup: group?.create_group === true ? true : undefined,
      lastFolderName: group?.folder_name,
    })
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'invalid_request' })
    return
  }
  const displayItems = zipped.displayItems
  if (!(await putLiveSession(tabId, token, epoch, {
    tabId,
    pageToken: token,
    generation: current?.generation ?? previous?.generation ?? 0,
    state: 'committing',
    sessionId,
    itemIds,
    batchRequestId: requestId,
    displayItems,
    resolveSentAt: current?.resolveSentAt ?? previous?.resolveSentAt,
    ackReceivedAt: current?.ackReceivedAt ?? previous?.ackReceivedAt,
    leaseDeadline: current?.leaseDeadline ?? previous?.leaseDeadline,
    batchRetryUsed: markRetry || current?.batchRetryUsed,
    lastCreateGroup: group?.create_group === true ? true : undefined,
    lastFolderName: group?.folder_name,
  }))) {
    return
  }
  await pushIfLive(tabId, token, epoch, {
    page_token: token,
    ui: 'committing',
    count: itemIds.length,
    filename: displayItems[0]?.filename,
  })
  if (await isClickStale(tabId, token, epoch)) return
  const ack = await wsClient.sendRequest(MSG_TYPE_BATCH_DOWNLOAD, built.payload, requestId)
  await onBatchAck(tabId, token, sessionId, itemIds, requestId, previous, ack, markRetry, epoch)
}

async function onBatchAck(
  tabId: number,
  token: string,
  sessionId: string,
  itemIds: string[],
  requestId: string,
  previous: ExtractorSessionRecord | null,
  ack: Record<string, unknown>,
  markRetry: boolean,
  epoch: number,
): Promise<void> {
  if (await isClickStale(tabId, token, epoch)) return
  const current = await getExtractorSessionStore().getSession(tabId)
  const filename = current?.displayItems?.[0]?.filename ?? previous?.displayItems?.[0]?.filename
  if (isBatchSuccess(ack)) {
    await getExtractorSessionStore().deleteSession(tabId)
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'success', filename })
    return
  }
  const failed = failedItemIds(ack)
  if (failed.length === 1 && itemIds.length === 1 && failed[0] === itemIds[0]) {
    await putErrorSession(tabId, token, current ?? previous, 'generic', epoch, {
      sessionId,
      itemIds: failed,
      batchRequestId: undefined,
    })
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'generic', filename })
    return
  }
  const code =
    typeof ack.error_code === 'string' && ack.error_code !== '' ? ack.error_code : 'generic'
  if (code === 'auth_expired' || code === 'session_expired' || code === 'pack_error') {
    await putErrorSession(tabId, token, current ?? previous, code, epoch, {
      sessionId,
      itemIds,
      batchRequestId: requestId,
      batchRetryUsed: markRetry,
    })
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: code, filename })
    return
  }
  if (!hasItemOutcomeLists(ack) || itemIds.length === 1) {
    await putErrorSession(tabId, token, current ?? previous, code, epoch, {
      sessionId,
      itemIds,
      batchRequestId: requestId,
      batchRetryUsed: markRetry,
    })
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: code, filename })
    return
  }
  const remaining = remainingItemIds(itemIds, ack)
  if (remaining.length === 0) {
    await putErrorSession(tabId, token, current ?? previous, code, epoch, {
      sessionId,
      itemIds: undefined,
      batchRequestId: undefined,
      lastCreateGroup: undefined,
      lastFolderName: undefined,
    })
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: code, filename })
    return
  }
  const alignedIds = current?.itemIds ?? itemIds
  let alignedDisplay = current?.displayItems
  if (!alignedDisplay) {
    const zipped = zipDisplayItems(previous?.itemIds ?? itemIds, previous?.displayItems ?? [], itemIds)
    if ('error' in zipped) {
      await putErrorSession(tabId, token, current ?? previous, 'invalid_request', epoch, {
        sessionId,
        itemIds: undefined,
        batchRequestId: undefined,
      })
      await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'invalid_request', filename })
      return
    }
    alignedDisplay = zipped.displayItems
  }
  const shrunk = shrinkDisplayItems(alignedIds, alignedDisplay, remaining)
  if ('error' in shrunk) {
    await putErrorSession(tabId, token, current ?? previous, 'invalid_request', epoch, {
      sessionId,
      itemIds: undefined,
      batchRequestId: undefined,
    })
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'invalid_request', filename })
    return
  }
  const leaseDeadline = current?.leaseDeadline ?? previous?.leaseDeadline
  if (!(await putLiveSession(tabId, token, epoch, {
    tabId,
    pageToken: token,
    generation: current?.generation ?? previous?.generation ?? 0,
    state: 'ready',
    sessionId,
    itemIds: shrunk.itemIds,
    displayItems: shrunk.displayItems,
    resolveSentAt: current?.resolveSentAt ?? previous?.resolveSentAt,
    ackReceivedAt: current?.ackReceivedAt ?? previous?.ackReceivedAt,
    leaseDeadline,
  }))) {
    return
  }
  await pushIfLive(tabId, token, epoch, {
    page_token: token,
    ui: 'ready',
    count: shrunk.itemIds.length,
    filename: shrunk.displayItems[0]?.filename ?? filename,
    lease_deadline: leaseDeadline,
  })
  if (await isClickStale(tabId, token, epoch)) return
  const catalog = buildPickerCatalog(shrunk.itemIds, shrunk.displayItems, leaseDeadline, { minItems: 1 })
  if (!('error' in catalog)) {
    await pushPickerCatalog(tabId, {
      page_token: token,
      items: catalog.items,
      count: catalog.count,
      lease_deadline: catalog.lease_deadline,
    })
  }
}

export async function handleFlowError(
  tabId: number,
  token: string,
  session: ExtractorSessionRecord | null,
  err: unknown,
  epoch: number,
): Promise<void> {
  if (await isClickStale(tabId, token, epoch)) return
  const code = mapCaughtError(err)
  if (code === 'unsupported') {
    await getExtractorSessionStore().deleteSession(tabId)
    void sendHide(tabId, 'matched_false', token)
    return
  }
  if (code === 'disconnected') {
    await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: 'disconnected' })
    return
  }
  await putErrorSession(tabId, token, session, code, epoch)
  await pushIfLive(tabId, token, epoch, { page_token: token, ui: 'error', error_code: code })
}

async function putErrorSession(
  tabId: number,
  token: string,
  previous: ExtractorSessionRecord | null,
  errorCode: string,
  epoch: number,
  extra?: Partial<ExtractorSessionRecord>,
): Promise<void> {
  if (await isClickStale(tabId, token, epoch)) return
  const current = await getExtractorSessionStore().getSession(tabId)
  const base = current ?? previous
  await putLiveSession(tabId, token, epoch, {
    tabId,
    pageToken: token,
    generation: base?.generation ?? 0,
    state: 'error',
    errorCode,
    sessionId: extra && 'sessionId' in extra ? extra.sessionId : base?.sessionId,
    itemIds: extra && 'itemIds' in extra ? extra.itemIds : base?.itemIds,
    batchRequestId: extra && 'batchRequestId' in extra ? extra.batchRequestId : base?.batchRequestId,
    displayItems: extra && 'displayItems' in extra ? extra.displayItems : base?.displayItems,
    resolveSentAt: extra && 'resolveSentAt' in extra ? extra.resolveSentAt : base?.resolveSentAt,
    ackReceivedAt: extra && 'ackReceivedAt' in extra ? extra.ackReceivedAt : base?.ackReceivedAt,
    leaseDeadline: extra && 'leaseDeadline' in extra ? extra.leaseDeadline : base?.leaseDeadline,
    batchRetryUsed: extra && 'batchRetryUsed' in extra ? extra.batchRetryUsed : base?.batchRetryUsed,
    lastCreateGroup: extra && 'lastCreateGroup' in extra ? extra.lastCreateGroup : base?.lastCreateGroup,
    lastFolderName: extra && 'lastFolderName' in extra ? extra.lastFolderName : base?.lastFolderName,
  })
}

function readResolveItems(items: unknown): Array<{ item_id: string } & ExtractorDisplayItem> {
  if (!Array.isArray(items)) return []
  const out: Array<{ item_id: string } & ExtractorDisplayItem> = []
  for (const item of items) {
    if (!item || typeof item !== 'object' || Array.isArray(item)) continue
    const rec = item as Record<string, unknown>
    if (typeof rec.item_id !== 'string' || rec.item_id.trim() === '') continue
    out.push({
      item_id: rec.item_id.trim(),
      filename: sanitizeDisplayFilename(rec.filename),
      size_bytes: typeof rec.size_bytes === 'number' ? rec.size_bytes : undefined,
      mime_type: typeof rec.mime_type === 'string' ? rec.mime_type : undefined,
    })
  }
  return out
}

function toDisplayItem(item: { item_id: string } & ExtractorDisplayItem): ExtractorDisplayItem {
  return {
    filename: item.filename,
    size_bytes: item.size_bytes,
    mime_type: item.mime_type,
  }
}

function sendHide(tabId: number, reason: 'nav' | 'matched_false' | 'ignore', pageToken: string): void {
  void sendMessage(
    'extractor:hide',
    { reason, page_token: pageToken },
    `content-script@${tabId}`,
  ).catch(() => undefined)
}

async function restoreExtractorUi(): Promise<void> {
  const sessions = await getExtractorSessionStore().listSessions()
  for (const rec of sessions) {
    try {
      const tab = await browser.tabs.get(rec.tabId)
      const token = await pageTokenFromHref(tab.url ?? '')
      if (!token || token !== rec.pageToken) {
        await getExtractorSessionStore().deleteSession(rec.tabId)
        continue
      }
      if (await getExtractorSessionStore().isIgnored(rec.tabId, token)) {
        await getExtractorSessionStore().deleteSession(rec.tabId)
        continue
      }
      let ui = rec.state
      let errorCode = rec.errorCode
      if (ui === 'resolving' || ui === 'committing') {
        await putErrorSession(rec.tabId, token, rec, 'timeout', clickEpochOf(rec.tabId))
        ui = 'error'
        errorCode = 'timeout'
      }
      if (ui === 'success') {
        await getExtractorSessionStore().deleteSession(rec.tabId)
        continue
      }
      pushExtractorResult(rec.tabId, {
        page_token: token,
        ui,
        count: rec.itemIds?.length,
        filename: rec.displayItems?.[0]?.filename,
        error_code: errorCode,
        lease_deadline: ui === 'ready' ? rec.leaseDeadline : undefined,
      })
    } catch {
      await getExtractorSessionStore().clearTab(rec.tabId)
    }
  }
}

export async function onExtractorUnpair(): Promise<void> {
  cancelAllClicks()
  await getExtractorSessionStore().clearAll()
  await broadcastHide('unpair')
}
