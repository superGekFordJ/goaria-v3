import { onMessage, sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import { hasCapability } from './capabilities'
import { collectCookieHeadersForUrls } from './domCookies'
import { currentDirectConnectGeneration } from './domConnectGeneration'
import { referrerResult } from './domReferrer'
import { buildDirectBatchPayload } from './directBatchRpc'
import { mintClientItemId, mintDirectBatchRequestId } from './mintRequestId'
import { folderFieldForSubmit } from './pickerFolder'
import { resolveCookieStoreIdForTab } from './cookieCapture'
import { wsClient } from './wsClient'
import { setCaptureHostDownHook } from './captureHostDown'
import {
  admitMember,
  evaluateClose,
  type BurstWindowState,
} from './burstCoalescer'
import {
  disarmCaptureSession,
  getCaptureSession,
  writeCaptureSession,
  type CaptureSession,
} from './captureSession'
import {
  getAllBurstHolds,
  getBurstHold,
  getBurstWindow,
  getBurstWindowIgnoringTtl,
  listExpiredBurstHoldIds,
  removeBurstHold,
  removeBurstWindow,
  saveBurstHold,
  saveBurstWindow,
  type BurstCatalogEntry,
  type BurstHold,
  type BurstWindowRecord,
} from './burstHoldStore'
import {
  removePendingDecision,
  savePendingDecision,
  type PendingDecision,
} from './pendingDecisionStore'
import {
  BURST_HOLD_TTL_MS,
  BURST_MAX_DEADLINE_MS,
  BURST_QUIET_WINDOW_MS,
  CAP_DOWNLOAD_BATCH,
  EXTRACTOR_MAX_SESSION_ITEMS,
} from '../stores/config.svelte'
import { configState } from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'
import { sanitizeDisplayFilename } from './extractorKeys'
import { t } from '../lib/i18n'
import type { I18nKey } from '../lib/i18n-keys'
import type { InterceptionContext } from '../interceptors/LinkGrabberResponse'
import type {
  BurstAliveMessage,
  BurstCancelMessage,
  BurstOpenMessage,
  BurstPickerCatalogItem,
  BurstSubmitMessage,
  BurstSubmitReply,
  DomPingReply,
} from '../utils/messaging'

const PING_TIMEOUT_MS = 1000

export type ChromeBurstBridge = {
  invokeSuggest(id: number): void
  cancelAndErase(id: number): Promise<void>
  resumeDownload(id: number): Promise<void>
  cleanupMemory(id: number): void
  restorePausedMemory(id: number): void
  handlePausedDownload(id: number, ctx: InterceptionContext): Promise<void>
}

let bridge: ChromeBurstBridge | null = null
let quietTimer: ReturnType<typeof setTimeout> | null = null
let maxTimer: ReturnType<typeof setTimeout> | null = null
let pickerTimer: ReturnType<typeof setTimeout> | null = null
let submitInflight = false
let captureChain: Promise<unknown> = Promise.resolve()

type BurstSubmitContext = {
  captureId: string
  tabId: number
  pageHref: string
  storeUnproven: boolean
  cookieStoreId?: string
  documentPolicy?: string
}

function runExclusive<T>(work: () => Promise<T>): Promise<T> {
  const next = captureChain.then(work, work)
  captureChain = next.then(
    () => undefined,
    () => undefined,
  )
  return next
}

export function setChromeBurstBridge(next: ChromeBurstBridge): void {
  bridge = next
}

export function referrerOriginMatches(referrer: string, pageHref: string): boolean {
  const raw = referrer.trim()
  if (!raw) return false
  try {
    return new URL(raw).origin === new URL(pageHref).origin
  } catch {
    return false
  }
}

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

function clearClocks(): void {
  if (quietTimer) {
    clearTimeout(quietTimer)
    quietTimer = null
  }
  if (maxTimer) {
    clearTimeout(maxTimer)
    maxTimer = null
  }
}

function clearPickerClock(): void {
  if (pickerTimer) {
    clearTimeout(pickerTimer)
    pickerTimer = null
  }
}

function contextFromHold(hold: BurstHold): InterceptionContext {
  return {
    url: hold.url,
    finalUrl: hold.finalUrl ?? '',
    tabId: -1,
    mimeType: hold.mimeType ?? '',
    contentDisposition: '',
    fileSize: hold.fileSize,
    filename: hold.filename,
    referrer: hold.referrer,
    incognito: hold.incognito,
  }
}

function pendingFromHold(hold: BurstHold): PendingDecision {
  return {
    url: hold.url,
    filename: hold.filename,
    fileSize: hold.fileSize,
    startTime: Date.now(),
    status: 'pending',
  }
}

function submitContextFromWindow(window: BurstWindowRecord): BurstSubmitContext | null {
  if (typeof window.tabId !== 'number' || typeof window.pageHref !== 'string' || window.pageHref === '') {
    return null
  }
  return {
    captureId: window.captureId,
    tabId: window.tabId,
    pageHref: window.pageHref,
    storeUnproven: window.storeUnproven === true,
    cookieStoreId: window.cookieStoreId,
    documentPolicy: window.documentPolicy,
  }
}

function memberIdsForWindow(window: BurstWindowRecord, holds: Map<number, BurstHold>): number[] {
  const ids = new Set<number>(window.downloadIds)
  for (const [id, hold] of holds) {
    if (hold.captureId === window.captureId) ids.add(id)
  }
  return [...ids]
}

export async function isCoalescerEligible(ctx: InterceptionContext): Promise<boolean> {
  const session = await getCaptureSession()
  if (!session) return false
  if (!referrerOriginMatches(ctx.referrer, session.pageHref)) return false
  if ((ctx.incognito === true) !== session.incognito) return false
  const window = await getBurstWindow()
  if (window && window.phase !== 'coalescing') return false
  return true
}

function scheduleClocks(window: BurstWindowState): void {
  clearClocks()
  const now = Date.now()
  const quietIn = window.lastItemAt + BURST_QUIET_WINDOW_MS - now
  const maxIn = window.firstItemAt + BURST_MAX_DEADLINE_MS - now
  if (quietIn > 0) {
    quietTimer = setTimeout(() => {
      quietTimer = null
      void runExclusive(() => onCoalescerClock())
    }, quietIn)
  }
  if (maxIn > 0) {
    maxTimer = setTimeout(() => {
      maxTimer = null
      void runExclusive(() => onCoalescerClock())
    }, maxIn)
  } else {
    void runExclusive(() => onCoalescerClock())
  }
}

function armPickerClock(deadline: number): void {
  clearPickerClock()
  const ms = deadline - Date.now()
  const fire = (): void => {
    pickerTimer = null
    void runExclusive(() => resumeAllBurstHoldsLocked())
  }
  if (ms <= 0) {
    fire()
    return
  }
  pickerTimer = setTimeout(fire, ms)
}

async function onCoalescerClock(): Promise<void> {
  const window = await getBurstWindow()
  if (!window || window.phase !== 'coalescing') return
  const session = await getCaptureSession()
  const now = Date.now()
  if (!session) {
    await closeCoalescedWindow(window, true)
    return
  }
  const result = evaluateClose(window, now, BURST_QUIET_WINDOW_MS, BURST_MAX_DEADLINE_MS)
  if (result.kind === 'open') {
    scheduleClocks(window)
    return
  }
  await closeCoalescedWindow(window, false)
}

async function migrateToLegacy(downloadId: number, hold: BurstHold): Promise<void> {
  await savePendingDecision(downloadId, pendingFromHold(hold))
  await removeBurstHold(downloadId)
  const b = bridge
  if (b) void b.handlePausedDownload(downloadId, contextFromHold(hold))
}

async function closeCoalescedWindow(window: BurstWindowRecord, forceLegacy: boolean): Promise<void> {
  clearClocks()
  const holds = await getAllBurstHolds()
  const ids = memberIdsForWindow(window, holds)
  if (forceLegacy || ids.filter(id => holds.has(id)).length <= 1) {
    await removeBurstWindow()
    for (const id of ids) {
      const hold = holds.get(id)
      if (hold) await migrateToLegacy(id, hold)
    }
    return
  }
  const session = await getCaptureSession()
  if (!session || session.captureId !== window.captureId) {
    await removeBurstWindow()
    for (const id of ids) {
      const hold = holds.get(id)
      if (hold) await migrateToLegacy(id, hold)
    }
    return
  }
  const ping = await pingContentScript(session.tabId)
  if (
    !ping ||
    ping.extractor_picker_open ||
    ping.dom_picker_open ||
    ping.burst_picker_open
  ) {
    notify('capture_notif_title', 'burst_mutex_body')
    await removeBurstWindow()
    for (const id of ids) {
      const hold = holds.get(id)
      if (hold) await migrateToLegacy(id, hold)
    }
    return
  }
  const catalog: BurstCatalogEntry[] = []
  const items: BurstPickerCatalogItem[] = []
  const snapshotAt = Date.now()
  for (const id of ids) {
    const hold = holds.get(id)
    if (!hold) continue
    const index = catalog.length
    catalog.push({ index, downloadId: id })
    await saveBurstHold(id, { ...hold, startTime: snapshotAt })
    const row: BurstPickerCatalogItem = { index }
    const filename = sanitizeDisplayFilename(hold.filename)
    if (filename) row.filename = filename
    try {
      const u = new URL(hold.url)
      const origin = sanitizeDisplayFilename(u.origin)
      if (origin) row.origin = origin
      const path = sanitizeDisplayFilename(u.pathname)
      if (path) row.path = path
    } catch {
      // omit origin/path
    }
    if (hold.fileSize > 0) row.size_bytes = hold.fileSize
    items.push(row)
  }
  if (items.length < 2) {
    await removeBurstWindow()
    for (const id of ids) {
      const hold = holds.get(id)
      if (hold) await migrateToLegacy(id, hold)
    }
    return
  }
  const pickerDeadline = snapshotAt + BURST_HOLD_TTL_MS
  const next: BurstWindowRecord = {
    ...window,
    downloadIds: catalog.map(entry => entry.downloadId),
    phase: 'picker',
    pickerDeadline,
    catalog,
    tabId: session.tabId,
    pageHref: session.pageHref,
    incognito: session.incognito,
    storeUnproven: session.storeUnproven,
    documentPolicy: session.documentPolicy,
    documentNonce: session.documentNonce,
  }
  if (session.cookieStoreId) next.cookieStoreId = session.cookieStoreId
  await saveBurstWindow(next)
  const payload: BurstOpenMessage = {
    capture_id: session.captureId,
    items,
    store_unproven: session.storeUnproven,
  }
  try {
    const reply = (await sendMessage(
      'burst:open',
      payload,
      `content-script@${session.tabId}`,
    )) as { ok?: boolean } | undefined
    if (reply?.ok !== true) {
      await removeBurstWindow()
      for (const id of ids) {
        const hold = holds.get(id)
        if (hold) await migrateToLegacy(id, hold)
      }
      return
    }
  } catch {
    await removeBurstWindow()
    for (const id of ids) {
      const hold = holds.get(id)
      if (hold) await migrateToLegacy(id, hold)
    }
    return
  }
  armPickerClock(pickerDeadline)
}

export async function admitConfirmedDownload(
  downloadId: number,
  ctx: InterceptionContext,
): Promise<'legacy' | 'coalesced' | 'overflow'> {
  return runExclusive(() => admitConfirmedDownloadLocked(downloadId, ctx))
}

async function admitConfirmedDownloadLocked(
  downloadId: number,
  ctx: InterceptionContext,
): Promise<'legacy' | 'coalesced' | 'overflow'> {
  const session = await getCaptureSession()
  const window = await getBurstWindow()
  const result = admitMember({
    sessionPresent: Boolean(session),
    window,
    captureId: session?.captureId ?? '',
    downloadId,
    now: Date.now(),
    maxItems: EXTRACTOR_MAX_SESSION_ITEMS,
  })
  if (result.kind === 'legacy' || result.kind === 'legacy_overflow') {
    if (result.kind === 'legacy_overflow') {
      notify('capture_notif_title', 'burst_overflow')
    }
    const hold = await getBurstHold(downloadId)
    if (hold) await migrateToLegacy(downloadId, hold)
    else if (bridge) void bridge.handlePausedDownload(downloadId, ctx)
    return result.kind === 'legacy_overflow' ? 'overflow' : 'legacy'
  }
  await saveBurstWindow(result.window)
  scheduleClocks(result.window)
  return 'coalesced'
}

export async function resumeHeldDownload(downloadId: number): Promise<void> {
  const b = bridge
  if (b) {
    b.invokeSuggest(downloadId)
    await b.resumeDownload(downloadId)
    b.cleanupMemory(downloadId)
  }
  await removeBurstHold(downloadId)
  await removePendingDecision(downloadId)
}

export async function resumeAllBurstHolds(): Promise<void> {
  return runExclusive(() => resumeAllBurstHoldsLocked())
}

async function resumeAllBurstHoldsLocked(): Promise<void> {
  clearClocks()
  clearPickerClock()
  await reapExpiredBurstState()
  const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
  const holds = await getAllBurstHolds()
  const ids = new Set<number>([...holds.keys(), ...(window?.downloadIds ?? [])])
  const tabId = window?.tabId ?? (await getCaptureSession())?.tabId
  await removeBurstWindow()
  await disarmCaptureSession()
  for (const id of ids) {
    await resumeHeldDownload(id)
  }
  if (typeof tabId === 'number') {
    void sendMessage('burst:close', {}, `content-script@${tabId}`).catch(() => undefined)
  }
}

async function stillPaused(downloadId: number): Promise<boolean> {
  try {
    const items = await browser.downloads.search({ id: downloadId })
    const item = items[0]
    return Boolean(item && item.state === 'in_progress' && item.paused === true)
  } catch {
    return false
  }
}

async function reapExpiredBurstState(): Promise<void> {
  const expiredIds = await listExpiredBurstHoldIds()
  for (const id of expiredIds) {
    if (await stillPaused(id)) await resumeHeldDownload(id)
    else await removeBurstHold(id)
  }
  const live = await getBurstWindow()
  if (live) return
  const expiredWindow = await getBurstWindowIgnoringTtl()
  if (!expiredWindow) {
    await removeBurstWindow()
    return
  }
  for (const id of expiredWindow.downloadIds) {
    if (await stillPaused(id)) await resumeHeldDownload(id)
    else await removeBurstHold(id)
  }
  await removeBurstWindow()
}

export async function takeoverSuccess(downloadId: number): Promise<void> {
  const b = bridge
  if (b) {
    b.invokeSuggest(downloadId)
    await b.cancelAndErase(downloadId)
    b.cleanupMemory(downloadId)
  }
  await removeBurstHold(downloadId)
  await removePendingDecision(downloadId)
}

async function closeBurstOverlay(tabId: number, captureId?: string): Promise<void> {
  void sendMessage(
    'burst:close',
    captureId ? { capture_id: captureId } : {},
    `content-script@${tabId}`,
  ).catch(() => undefined)
}

export async function handleCaptureArm(): Promise<{ ok: boolean; error?: string }> {
  return runExclusive(() => handleCaptureArmLocked())
}

async function handleCaptureArmLocked(): Promise<{ ok: boolean; error?: string }> {
  if (connectionState.status !== 'connected' || !connectionState.paired) {
    notify('capture_notif_title', 'capture_reject_disconnected')
    return { ok: false, error: 'disconnected' }
  }
  if (!configState.autoCapture) {
    notify('capture_notif_title', 'capture_reject_autocapture')
    return { ok: false, error: 'autocapture' }
  }
  if (!hasCapability(connectionState.capabilities, CAP_DOWNLOAD_BATCH)) {
    notify('capture_notif_title', 'capture_reject_cap')
    return { ok: false, error: 'cap' }
  }
  if (await getCaptureSession()) {
    notify('capture_notif_title', 'capture_reject_session')
    return { ok: false, error: 'session' }
  }
  let tab: browser.Tabs.Tab | undefined
  try {
    const [found] = await browser.tabs.query({ active: true, lastFocusedWindow: true })
    tab = found
  } catch {
    tab = undefined
  }
  if (typeof tab?.id !== 'number') {
    notify('capture_notif_title', 'capture_reject_no_tab')
    return { ok: false, error: 'no_tab' }
  }
  if (tab.discarded === true) {
    notify('capture_notif_title', 'capture_reject_discarded')
    return { ok: false, error: 'discarded' }
  }
  const href = typeof tab.url === 'string' ? tab.url : ''
  if (!href.startsWith('http://') && !href.startsWith('https://')) {
    notify('capture_notif_title', 'capture_reject_scheme')
    return { ok: false, error: 'scheme' }
  }
  const ping = await pingContentScript(tab.id)
  if (!ping || typeof ping.document_nonce !== 'string' || typeof ping.page_href !== 'string') {
    notify('capture_notif_title', 'capture_reject_ping')
    return { ok: false, error: 'ping' }
  }
  if (ping.extractor_picker_open || ping.dom_picker_open || ping.burst_picker_open) {
    notify('capture_notif_title', 'capture_reject_overlay')
    return { ok: false, error: 'overlay' }
  }
  const storeId = await resolveCookieStoreIdForTab(tab)
  const storeUnproven = typeof storeId !== 'string' || storeId.trim() === ''
  const session: CaptureSession = {
    captureId: mintDirectBatchRequestId(),
    tabId: tab.id,
    documentNonce: ping.document_nonce,
    pageHref: ping.page_href,
    incognito: tab.incognito === true,
    storeUnproven,
    directConnectGeneration: currentDirectConnectGeneration(),
    createdAt: Date.now(),
  }
  if (!storeUnproven && storeId) session.cookieStoreId = storeId
  const wrote = await writeCaptureSession(session)
  if (!wrote) {
    notify('capture_notif_title', 'capture_reject_session')
    return { ok: false, error: 'session' }
  }
  notify('capture_notif_title', 'capture_armed_body')
  return { ok: true }
}

function idSetOfAck(value: unknown): Set<string> {
  if (!Array.isArray(value)) return new Set()
  return new Set(value.filter((id): id is string => typeof id === 'string'))
}

async function applyAckPartitions(
  items: Array<{ clientItemId: string; downloadId: number }>,
  ack: Record<string, unknown>,
): Promise<{ succeeded: number; duplicate: number; error: number }> {
  const succeeded = idSetOfAck(ack.succeeded_item_ids)
  const duplicate = idSetOfAck(ack.duplicate_item_ids)
  let ok = 0
  let dup = 0
  let err = 0
  for (const item of items) {
    if (succeeded.has(item.clientItemId)) {
      await takeoverSuccess(item.downloadId)
      ok += 1
      continue
    }
    await resumeHeldDownload(item.downloadId)
    if (duplicate.has(item.clientItemId)) dup += 1
    else err += 1
  }
  return { succeeded: ok, duplicate: dup, error: err }
}

async function queryBurstStatus(requestId: string): Promise<BurstSubmitReply> {
  try {
    const ack = await wsClient.sendDirectBatchStatus(requestId)
    const status = typeof ack.status === 'string' ? ack.status : ''
    if (status === 'pending') return { accepted: false, error_code: 'pending' }
    if (status === 'complete') {
      const window = await getBurstWindow()
      const items = window?.submitItems ?? []
      const parts = await applyAckPartitions(items, ack)
      return { accepted: true, ...parts }
    }
    if (status === 'not_found') return { accepted: false, error_code: 'not_found' }
    return { accepted: false, error_code: 'pending' }
  } catch {
    return { accepted: false, error_code: 'pending' }
  }
}

export async function handleBurstSubmit(
  data: BurstSubmitMessage,
): Promise<BurstSubmitReply> {
  return runExclusive(() => handleBurstSubmitLocked(data))
}

async function handleBurstSubmitLocked(
  data: BurstSubmitMessage,
): Promise<BurstSubmitReply> {
  if (submitInflight) return { accepted: false, error_code: 'busy' }
  const window = await getBurstWindow()
  if (!window || data.capture_id !== window.captureId) {
    await resumeAllBurstHoldsLocked()
    return { accepted: false, error_code: 'invalid_request' }
  }
  const ctx = submitContextFromWindow(window)
  if (!ctx) {
    await resumeAllBurstHoldsLocked()
    return { accepted: false, error_code: 'invalid_request' }
  }
  if (!Array.isArray(data.indices) || data.indices.length === 0) {
    await resumeAllBurstHoldsLocked()
    return { accepted: true }
  }
  if (!hasCapability(connectionState.capabilities, CAP_DOWNLOAD_BATCH)) {
    await resumeAllBurstHoldsLocked()
    return { accepted: false, error_code: 'unsupported' }
  }
  submitInflight = true
  try {
    const holds = await getAllBurstHolds()
    const catalog =
      window.catalog && window.catalog.length > 0
        ? window.catalog
        : window.downloadIds.map((downloadId, index) => ({ index, downloadId }))
    const selected = new Set(data.indices)
    const selectedHolds: Array<{ index: number; downloadId: number; hold: BurstHold }> = []
    for (const entry of catalog) {
      const hold = holds.get(entry.downloadId)
      if (!hold) continue
      if (selected.has(entry.index)) {
        selectedHolds.push({ index: entry.index, downloadId: entry.downloadId, hold })
      } else {
        await resumeHeldDownload(entry.downloadId)
      }
    }
    if (selectedHolds.length === 0) {
      await resumeAllBurstHoldsLocked()
      return { accepted: true }
    }
    const fields = folderFieldForSubmit({
      createGroup: data.create_group === true,
      selectedCount: selectedHolds.length,
      raw: typeof data.folder_name === 'string' ? data.folder_name : '',
    })
    const reuse =
      window.phase === 'submitting' && window.requestId && window.submitItems
        ? { requestId: window.requestId, submitItems: window.submitItems }
        : null
    const built = await buildBurstPayload(
      ctx,
      selectedHolds,
      fields,
      reuse?.submitItems,
    )
    if ('error' in built) {
      await resumeAllBurstHoldsLocked()
      return { accepted: false, error_code: 'invalid_request' }
    }
    const requestId = reuse?.requestId ?? mintDirectBatchRequestId()
    const payload = built.payload
    const submitItems = built.submitItems
    await saveBurstWindow({
      ...window,
      phase: 'submitting',
      requestId,
      submitItems,
    })
    try {
      const ack = await wsClient.sendDirectBatch(payload, requestId)
      const parts = await applyAckPartitions(submitItems ?? [], ack)
      await finishBurstSubmit(ctx.tabId, ctx.captureId)
      return { accepted: true, ...parts }
    } catch (err) {
      const code = errorCodeOf(err)
      if (code === 'busy') {
        return { accepted: false, error_code: 'busy' }
      }
      if (code === 'timeout' || code === 'disconnected') {
        const status = await queryBurstStatus(requestId)
        if (status.accepted || status.error_code === 'not_found') {
          if (status.error_code === 'not_found') {
            for (const item of submitItems ?? []) await resumeHeldDownload(item.downloadId)
            notify('capture_notif_title', 'dom_not_found')
          }
          await finishBurstSubmit(ctx.tabId, ctx.captureId)
        }
        return status
      }
      await resumeAllBurstHoldsLocked()
      return { accepted: false, error_code: code }
    }
  } finally {
    submitInflight = false
  }
}

async function buildBurstPayload(
  ctx: BurstSubmitContext,
  selectedHolds: Array<{ index: number; downloadId: number; hold: BurstHold }>,
  fields: { create_group?: boolean; folder_name?: string },
  reuseIds?: Array<{ clientItemId: string; downloadId: number; index: number }>,
): Promise<
  | { payload: Record<string, unknown>; submitItems: Array<{ clientItemId: string; downloadId: number; index: number }> }
  | { error: string }
> {
  const cookieLines = await collectCookieHeadersForUrls({
    urls: selectedHolds.map(row => row.hold.url),
    sourceHref: ctx.pageHref,
    storeId: ctx.cookieStoreId,
    storeUnproven: ctx.storeUnproven,
    getAll: details => browser.cookies.getAll(details) as Promise<unknown[]>,
  })
  const reused = new Map((reuseIds ?? []).map(row => [row.downloadId, row.clientItemId]))
  const seen = new Set<string>()
  const items: Record<string, unknown>[] = []
  const submitItems: Array<{ clientItemId: string; downloadId: number; index: number }> = []
  for (let i = 0; i < selectedHolds.length; i++) {
    const row = selectedHolds[i]
    let clientId = reused.get(row.downloadId) ?? mintClientItemId()
    while (seen.has(clientId)) clientId = mintClientItemId()
    seen.add(clientId)
    const rec: Record<string, unknown> = {
      client_item_id: clientId,
      url: row.hold.url,
    }
    if (row.hold.finalUrl) rec.final_url = row.hold.finalUrl
    if (row.hold.filename) rec.filename = row.hold.filename
    if (row.hold.fileSize > 0) {
      rec.file_size = row.hold.fileSize
      rec.skip_head_probe = true
    }
    const cookie = cookieLines[i]
    if (cookie) rec.headers = [cookie]
    const page = referrerResult({
      pageHref: ctx.pageHref,
      targetHref: row.hold.url,
      documentPolicy: ctx.documentPolicy,
    })
    if (page) rec.download_page = page
    items.push(rec)
    submitItems.push({ clientItemId: clientId, downloadId: row.downloadId, index: row.index })
  }
  const built = buildDirectBatchPayload({ items, ...fields })
  if ('error' in built) return { error: built.error }
  return { payload: built.payload, submitItems }
}

async function finishBurstSubmit(tabId: number, captureId: string): Promise<void> {
  clearClocks()
  clearPickerClock()
  await removeBurstWindow()
  await disarmCaptureSession()
  await closeBurstOverlay(tabId, captureId)
}

export async function handleBurstCancel(data: BurstCancelMessage): Promise<{ ok: boolean }> {
  return runExclusive(async () => {
    const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
    if (window && data.capture_id && data.capture_id !== window.captureId) return { ok: false }
    await resumeAllBurstHoldsLocked()
    return { ok: true }
  })
}

export async function handleBurstAlive(data: BurstAliveMessage): Promise<{ ok: boolean }> {
  const window = await getBurstWindow()
  if (!window || window.captureId !== data.capture_id) return { ok: false }
  if (window.phase !== 'picker' && window.phase !== 'submitting') return { ok: false }
  return { ok: true }
}

export async function handleBurstStatus(data: BurstAliveMessage): Promise<BurstSubmitReply> {
  return runExclusive(async () => {
    const window = await getBurstWindow()
    if (!window || window.captureId !== data.capture_id || !window.requestId) {
      return { accepted: false, error_code: 'not_found' }
    }
    const status = await queryBurstStatus(window.requestId)
    if (status.accepted || status.error_code === 'not_found') {
      const ctx = submitContextFromWindow(window)
      if (status.error_code === 'not_found') {
        for (const item of window.submitItems ?? []) await resumeHeldDownload(item.downloadId)
        notify('capture_notif_title', 'dom_not_found')
      }
      if (ctx) await finishBurstSubmit(ctx.tabId, ctx.captureId)
    }
    return status
  })
}

export async function recoverBurstState(): Promise<void> {
  return runExclusive(() => recoverBurstStateLocked())
}

async function recoverBurstStateLocked(): Promise<void> {
  await reapExpiredBurstState()
  const holds = await getAllBurstHolds()
  for (const [downloadId] of holds) {
    try {
      const items = await browser.downloads.search({ id: downloadId })
      const item = items[0]
      if (!item || item.state === 'complete' || !(item.state === 'in_progress' && item.paused)) {
        await removeBurstHold(downloadId)
      } else {
        bridge?.restorePausedMemory(downloadId)
      }
    } catch {
      await removeBurstHold(downloadId)
    }
  }
  await resumeAllBurstHoldsLocked()
}

function dropSessionTab(tabId: number): void {
  void runExclusive(async () => {
    const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
    const session = await getCaptureSession()
    if (window?.tabId !== tabId && session?.tabId !== tabId) return
    await resumeAllBurstHoldsLocked()
  })
}

export function initBurstFlow(): void {
  setCaptureHostDownHook(() => resumeAllBurstHolds())
  onMessage('capture:arm', () => handleCaptureArm())
  onMessage('burst:submit', ({ data }: { data: BurstSubmitMessage }) => handleBurstSubmit(data))
  onMessage('burst:cancel', ({ data }: { data: BurstCancelMessage }) => handleBurstCancel(data))
  onMessage('burst:alive', ({ data }: { data: BurstAliveMessage }) => handleBurstAlive(data))
  onMessage('burst:status', ({ data }: { data: BurstAliveMessage }) => handleBurstStatus(data))
  browser.tabs.onRemoved.addListener(tabId => {
    dropSessionTab(tabId)
  })
  const replaced = (
    browser.tabs as {
      onReplaced?: { addListener: (cb: (added: number, removed: number) => void) => void }
    }
  ).onReplaced
  replaced?.addListener((_added, removed) => {
    dropSessionTab(removed)
  })
  browser.tabs.onUpdated.addListener((tabId, changeInfo) => {
    if (changeInfo.discarded === true || typeof changeInfo.url === 'string') {
      dropSessionTab(tabId)
    }
  })
}

export function resetBurstFlowForTests(): void {
  clearClocks()
  clearPickerClock()
  submitInflight = false
  captureChain = Promise.resolve()
  bridge = null
}
