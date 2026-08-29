import { onMessage, sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import { hasCapability } from './capabilities'
import { originOf, resolvePresentationTab } from './captureTabResolver'
import { collectCookieHeadersForUrls } from './domCookies'
import { currentDirectConnectGeneration } from './domConnectGeneration'
import { referrerResult } from './domReferrer'
import { buildDirectBatchPayload } from './directBatchRpc'
import { mintClientItemId, mintDirectBatchRequestId } from './mintRequestId'
import { folderFieldForSubmit } from './pickerFolder'
import { resolveCookieStoreIdForTab } from './cookieCapture'
import { wsClient } from './wsClient'
import { setCaptureHostDownHook, setCaptureReconnectHook } from './captureHostDown'
import {
  admitMember,
  evaluateClose,
  quietWindowFor,
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
  getBurstHoldIgnoringTtl,
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
import { isFirefox } from '../utils/extensionInfo'
import {
  removePendingDecision,
  savePendingDecision,
  type PendingDecision,
} from './pendingDecisionStore'
import {
  BURST_CLAIM_RETRY_MS,
  BURST_HOLD_TTL_MS,
  BURST_MAX_DEADLINE_MS,
  BURST_QUIET_GROUP_MS,
  BURST_QUIET_SOLO_MS,
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

type BlankTabUrls = {
  url?: string
  finalUrl?: string
  mainFrame?: boolean
  skipTabId?: number
}

export type FirefoxBurstBridge = {
  sendLegacy(ctx: InterceptionContext): Promise<void>
  cleanupBlankTab(tabId: number, urls: BlankTabUrls): Promise<void>
}

let bridge: ChromeBurstBridge | null = null
let firefoxBridge: FirefoxBurstBridge | null = null
let quietTimer: ReturnType<typeof setTimeout> | null = null
let maxTimer: ReturnType<typeof setTimeout> | null = null
let pickerTimer: ReturnType<typeof setTimeout> | null = null
let submitInflight = false
let captureChain: Promise<unknown> = Promise.resolve()
let exclusiveDepth = 0
let pendingClaims = 0
const firefoxLegacyClaimed = new Set<number>()

type BurstSubmitContext = {
  captureId: string
  tabId: number
  pageHref: string
  storeUnproven: boolean
  cookieStoreId?: string
  documentPolicy?: string
}

async function enterExclusive<T>(work: () => Promise<T>): Promise<T> {
  exclusiveDepth++
  try {
    return await work()
  } finally {
    exclusiveDepth--
  }
}

function runExclusive<T>(work: () => Promise<T>): Promise<T> {
  const next = captureChain.then(
    () => enterExclusive(work),
    () => enterExclusive(work),
  )
  captureChain = next.then(
    () => undefined,
    () => undefined,
  )
  return next
}

export function enqueueCaptureWork<T>(work: () => Promise<T>): Promise<T> {
  return runExclusive(work)
}

export function beginCaptureClaim(): void {
  pendingClaims++
}

export function endCaptureClaim(): void {
  pendingClaims = Math.max(0, pendingClaims - 1)
}

export function pendingCaptureClaims(): number {
  return pendingClaims
}

export function setChromeBurstBridge(next: ChromeBurstBridge): void {
  bridge = next
}

export function setFirefoxBurstBridge(next: FirefoxBurstBridge): void {
  firefoxBridge = next
}

function usesFirefoxProcess(): boolean {
  return firefoxBridge !== null || isFirefox()
}

function usesFirefoxRelease(hold?: BurstHold | null): boolean {
  if (hold?.engine === 'chrome') return false
  if (hold?.engine === 'firefox') return true
  return usesFirefoxProcess()
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

export function sessionMatchesContext(
  session: CaptureSession,
  ctx: InterceptionContext,
): boolean {
  if (!referrerOriginMatches(ctx.referrer, session.pageHref)) return false
  if ((ctx.incognito === true) !== session.incognito) return false
  if (isFirefox() && session.storeUnproven !== true && session.cookieStoreId) {
    return ctx.cookieStoreId === session.cookieStoreId
  }
  return true
}

export async function mintImplicitCaptureSession(
  ctx: InterceptionContext,
  referrerOrigin: string,
): Promise<CaptureSession | null> {
  const tab = await resolvePresentationTab({
    referrer: ctx.referrer,
    referrerOrigin,
    incognito: ctx.incognito === true,
    cookieStoreId: isFirefox() ? ctx.cookieStoreId : undefined,
  })
  if (!tab) return null
  const rawStoreId = isFirefox()
    ? ctx.cookieStoreId
    : await resolveCookieStoreIdForTab(tab)
  const cookieStoreId =
    typeof rawStoreId === 'string' && rawStoreId.trim() !== '' ? rawStoreId.trim() : undefined
  const session: CaptureSession = {
    captureId: mintDirectBatchRequestId(),
    tabId: tab.id,
    pageHref: tab.url,
    incognito: ctx.incognito === true,
    storeUnproven: cookieStoreId === undefined,
    directConnectGeneration: currentDirectConnectGeneration(),
    createdAt: Date.now(),
  }
  if (cookieStoreId) session.cookieStoreId = cookieStoreId
  return (await writeCaptureSession(session)) ? session : null
}

export async function resolveCoalescerAdmission(
  ctx: InterceptionContext,
): Promise<CaptureSession | null> {
  if (!configState.autoCapture) return null
  if (connectionState.status !== 'connected' || !connectionState.paired) return null
  if (!hasCapability(connectionState.capabilities, CAP_DOWNLOAD_BATCH)) return null
  const referrerOrigin = originOf(ctx.referrer)
  if (!referrerOrigin) return null
  const window = await getBurstWindow()
  if (window && window.phase !== 'coalescing') return null
  const session = await getCaptureSession()
  if (session) return sessionMatchesContext(session, ctx) ? session : null
  return mintImplicitCaptureSession(ctx, referrerOrigin)
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

let submitStatusTimer: ReturnType<typeof setTimeout> | null = null

function clearSubmitStatusClock(): void {
  if (submitStatusTimer) {
    clearTimeout(submitStatusTimer)
    submitStatusTimer = null
  }
}

function armSubmitStatusClock(requestId: string, captureId: string): void {
  clearSubmitStatusClock()
  submitStatusTimer = setTimeout(() => {
    submitStatusTimer = null
    void runExclusive(async () => {
      const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
      if (!window || window.captureId !== captureId || window.requestId !== requestId) return
      if (window.phase !== 'submitting') return
      const status = await queryBurstStatus(requestId)
      if (status.accepted || status.error_code === 'not_found') {
        const ctx = submitContextFromWindow(window)
        if (status.error_code === 'not_found') {
          for (const item of window.submitItems ?? []) {
            await resumeHeldDownload(item.downloadId, window.tabId)
          }
          notify('capture_notif_title', 'dom_not_found')
        }
        if (ctx) await finishBurstSubmit(ctx.tabId, ctx.captureId)
        return
      }
      armSubmitStatusClock(requestId, captureId)
    })
  }, 2000)
}

function contextFromHold(hold: BurstHold): InterceptionContext {
  const ctx: InterceptionContext = {
    url: hold.url,
    finalUrl: hold.finalUrl ?? '',
    tabId: typeof hold.tabId === 'number' ? hold.tabId : -1,
    mimeType: hold.mimeType ?? '',
    contentDisposition: '',
    fileSize: hold.fileSize,
    filename: hold.filename,
    referrer: hold.referrer,
    incognito: hold.incognito,
  }
  if (hold.cookieStoreId) ctx.cookieStoreId = hold.cookieStoreId
  if (hold.documentUrl) ctx.documentUrl = hold.documentUrl
  if (typeof hold.frameId === 'number') ctx.frameId = hold.frameId
  return ctx
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
  if (!sessionMatchesContext(session, ctx)) return false
  const window = await getBurstWindow()
  return window?.phase === 'coalescing'
}

function quietMsForWindow(window: BurstWindowState): number {
  return quietWindowFor(window.downloadIds.length, {
    soloQuietMs: BURST_QUIET_SOLO_MS,
    groupQuietMs: BURST_QUIET_GROUP_MS,
  })
}

function scheduleClocks(window: BurstWindowState): void {
  clearClocks()
  const now = Date.now()
  const quietIn = window.lastItemAt + quietMsForWindow(window) - now
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
  }
  if (quietIn <= 0 || maxIn <= 0) {
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

async function mergeExpiredHolds(
  live: Map<number, BurstHold>,
  extraIds: Iterable<number> = [],
): Promise<Map<number, BurstHold>> {
  const pending = new Set<number>(extraIds)
  for (const id of live.keys()) pending.add(id)
  for (const id of await listExpiredBurstHoldIds()) pending.add(id)
  for (const id of pending) {
    if (live.has(id)) continue
    const hold = await getBurstHoldIgnoringTtl(id)
    if (hold) live.set(id, hold)
  }
  return live
}

function shouldDeferCoalescerClose(window: BurstWindowState, now: number): boolean {
  return pendingCaptureClaims() > 0 && now < window.firstItemAt + BURST_MAX_DEADLINE_MS
}

function deferCoalescerClock(): void {
  clearClocks()
  quietTimer = setTimeout(() => {
    quietTimer = null
    void runExclusive(() => onCoalescerClock())
  }, BURST_CLAIM_RETRY_MS)
}

async function onCoalescerClock(seed?: BurstWindowRecord): Promise<void> {
  const window =
    seed ??
    (await getBurstWindow()) ??
    (usesFirefoxProcess() ? await getBurstWindowIgnoringTtl() : null)
  if (!window || window.phase !== 'coalescing') return
  const now = Date.now()
  if (shouldDeferCoalescerClose(window, now)) {
    deferCoalescerClock()
    return
  }
  const session = await getCaptureSession()
  const afterSession = Date.now()
  if (shouldDeferCoalescerClose(window, afterSession)) {
    deferCoalescerClock()
    return
  }
  if (!session) {
    await closeCoalescedWindow(window, true)
    return
  }
  const result = evaluateClose(window, afterSession, {
    soloQuietMs: BURST_QUIET_SOLO_MS,
    groupQuietMs: BURST_QUIET_GROUP_MS,
    maxMs: BURST_MAX_DEADLINE_MS,
  })
  if (result.kind === 'open') {
    scheduleClocks(window)
    return
  }
  if (shouldDeferCoalescerClose(window, Date.now())) {
    deferCoalescerClock()
    return
  }
  await closeCoalescedWindow(window, false)
}

function blankTabUrls(hold: BurstHold, skipTabId?: number): BlankTabUrls {
  const urls: BlankTabUrls = {
    url: hold.url,
    finalUrl: hold.finalUrl,
    mainFrame: true,
  }
  if (typeof skipTabId === 'number') urls.skipTabId = skipTabId
  return urls
}

async function cleanupFirefoxHold(
  downloadId: number,
  hold?: BurstHold | null,
  skipTabId?: number,
): Promise<void> {
  const b = firefoxBridge
  if (b && hold?.mainFrame === true && typeof hold.tabId === 'number' && hold.tabId >= 0) {
    await b.cleanupBlankTab(hold.tabId, blankTabUrls(hold, skipTabId))
  }
  await removeBurstHold(downloadId)
  await removePendingDecision(downloadId)
}

function queueFirefoxLegacySend(
  downloadId: number,
  hold: BurstHold,
  skipTabId?: number,
  reserved = false,
): void {
  if (!reserved && firefoxLegacyClaimed.has(downloadId)) return
  firefoxLegacyClaimed.add(downloadId)
  const ctx = contextFromHold(hold)
  const tabId = typeof hold.tabId === 'number' ? hold.tabId : ctx.tabId
  const mainFrame = hold.mainFrame === true
  void Promise.resolve().then(async () => {
    const b = firefoxBridge
    try {
      if (b) await b.sendLegacy(ctx)
      else notify('capture_notif_title', 'burst_firefox_cannot_resume')
    } finally {
      if (b && mainFrame && tabId >= 0) {
        await b.cleanupBlankTab(tabId, blankTabUrls(hold, skipTabId))
      }
      await removeBurstHold(downloadId)
      await removePendingDecision(downloadId)
      firefoxLegacyClaimed.delete(downloadId)
    }
  })
}

async function migrateToLegacy(
  downloadId: number,
  hold: BurstHold,
  skipTabId?: number,
): Promise<void> {
  if (usesFirefoxRelease(hold)) {
    queueFirefoxLegacySend(downloadId, hold, skipTabId)
    return
  }
  const saved = await savePendingDecision(downloadId, pendingFromHold(hold))
  if (!saved) {
    await resumeHeldDownload(downloadId)
    return
  }
  await removeBurstHold(downloadId)
  const b = bridge
  if (b) void b.handlePausedDownload(downloadId, contextFromHold(hold))
}

export async function migrateBurstHoldToLegacy(downloadId: number): Promise<void> {
  const hold = (await getBurstHold(downloadId)) ?? (await getBurstHoldIgnoringTtl(downloadId))
  if (!hold) return
  await migrateToLegacy(downloadId, hold)
}

export function claimFirefoxLegacyHandoff(downloadId: number): void {
  firefoxLegacyClaimed.add(downloadId)
}

export function scheduleFirefoxLegacyHandoff(downloadId: number): void {
  void Promise.resolve().then(async () => {
    const hold = (await getBurstHold(downloadId)) ?? (await getBurstHoldIgnoringTtl(downloadId))
    if (!hold) {
      firefoxLegacyClaimed.delete(downloadId)
      return
    }
    queueFirefoxLegacySend(downloadId, hold, undefined, true)
  })
}

async function abandonWindowToLegacy(
  _window: BurstWindowRecord,
  ids: number[],
  holds: Map<number, BurstHold>,
  skipTabId?: number,
): Promise<void> {
  await removeBurstWindow()
  await disarmCaptureSession()
  for (const id of ids) {
    const hold = holds.get(id)
    if (hold) await migrateToLegacy(id, hold, skipTabId)
  }
}

async function closeCoalescedWindow(window: BurstWindowRecord, forceLegacy: boolean): Promise<void> {
  clearClocks()
  const skipTabId = window.tabId ?? (await getCaptureSession())?.tabId
  const holds = usesFirefoxProcess()
    ? await mergeExpiredHolds(await getAllBurstHolds(), window.downloadIds)
    : await getAllBurstHolds()
  const ids = memberIdsForWindow(window, holds)
  if (shouldDeferCoalescerClose(window, Date.now())) {
    deferCoalescerClock()
    return
  }
  if (forceLegacy || ids.filter(id => holds.has(id)).length <= 1) {
    await abandonWindowToLegacy(window, ids, holds, skipTabId)
    return
  }
  const session = await getCaptureSession()
  if (shouldDeferCoalescerClose(window, Date.now())) {
    deferCoalescerClock()
    return
  }
  if (!session || session.captureId !== window.captureId) {
    await abandonWindowToLegacy(window, ids, holds, skipTabId)
    return
  }
  const ping = await pingContentScript(session.tabId)
  if (shouldDeferCoalescerClose(window, Date.now())) {
    deferCoalescerClock()
    return
  }
  if (
    !ping ||
    ping.extractor_picker_open ||
    ping.dom_picker_open ||
    ping.burst_picker_open
  ) {
    notify('capture_notif_title', 'burst_mutex_body')
    if (usesFirefoxProcess()) await closeBurstOverlay(session.tabId, window.captureId)
    await abandonWindowToLegacy(window, ids, holds, skipTabId)
    return
  }
  if (
    typeof ping.page_href !== 'string' ||
    !referrerOriginMatches(ping.page_href, session.pageHref)
  ) {
    await abandonWindowToLegacy(window, ids, holds, skipTabId)
    return
  }
  const catalog: BurstCatalogEntry[] = []
  const items: BurstPickerCatalogItem[] = []
  const snapshotAt = Date.now()
  for (const id of ids) {
    const hold = holds.get(id)
    if (!hold) continue
    const refreshed = await saveBurstHold(id, { ...hold, startTime: snapshotAt })
    if (!refreshed) {
      await abandonWindowToLegacy(window, ids, holds, skipTabId)
      return
    }
    const index = catalog.length
    catalog.push({ index, downloadId: id })
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
    await abandonWindowToLegacy(window, ids, holds, skipTabId)
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
    pageHref: ping.page_href,
    incognito: session.incognito,
    storeUnproven: session.storeUnproven,
    documentPolicy: session.documentPolicy,
  }
  if (session.cookieStoreId) next.cookieStoreId = session.cookieStoreId
  if (typeof ping.document_nonce === 'string' && ping.document_nonce !== '') {
    next.documentNonce = ping.document_nonce
  } else if (session.documentNonce) {
    next.documentNonce = session.documentNonce
  }
  if (!(await saveBurstWindow(next))) {
    await abandonWindowToLegacy(window, ids, holds, skipTabId)
    return
  }
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
      await abandonWindowToLegacy(window, ids, holds, skipTabId)
      return
    }
  } catch {
    await abandonWindowToLegacy(window, ids, holds, skipTabId)
    return
  }
  armPickerClock(pickerDeadline)
}

export async function admitConfirmedDownload(
  downloadId: number,
  ctx: InterceptionContext,
  eventAt: number,
): Promise<'legacy' | 'coalesced' | 'overflow'> {
  if (exclusiveDepth > 0) return admitConfirmedDownloadLocked(downloadId, ctx, eventAt)
  return runExclusive(() => admitConfirmedDownloadLocked(downloadId, ctx, eventAt))
}

async function admitConfirmedDownloadLocked(
  downloadId: number,
  ctx: InterceptionContext,
  eventAt: number,
): Promise<'legacy' | 'coalesced' | 'overflow'> {
  const session = await getCaptureSession()
  const window = await getBurstWindow()
  const result = admitMember({
    sessionPresent: Boolean(session),
    window,
    captureId: session?.captureId ?? '',
    downloadId,
    now: Math.min(eventAt, Date.now()),
    maxItems: EXTRACTOR_MAX_SESSION_ITEMS,
  })
  if (result.kind === 'legacy' || result.kind === 'legacy_overflow') {
    if (result.kind === 'legacy_overflow') {
      notify('capture_notif_title', 'burst_overflow')
    }
    const hold = await getBurstHold(downloadId)
    if (!hold) {
      if (!usesFirefoxRelease(null) && bridge) void bridge.handlePausedDownload(downloadId, ctx)
      return result.kind === 'legacy_overflow' ? 'overflow' : 'legacy'
    }
    if (!usesFirefoxRelease(hold)) await migrateToLegacy(downloadId, hold)
    return result.kind === 'legacy_overflow' ? 'overflow' : 'legacy'
  }
  const savedHold = await getBurstHold(downloadId)
  if (!savedHold) {
    if (!usesFirefoxRelease(null) && bridge) void bridge.handlePausedDownload(downloadId, ctx)
    return 'legacy'
  }
  const record: BurstWindowRecord = { ...result.window }
  if (typeof session?.tabId === 'number') record.tabId = session.tabId
  const savedWindow = await saveBurstWindow(record)
  if (!savedWindow) {
    if (!usesFirefoxRelease(savedHold)) await migrateToLegacy(downloadId, savedHold)
    return 'legacy'
  }
  scheduleClocks(record)
  return 'coalesced'
}

export async function resumeHeldDownload(downloadId: number, skipTabId?: number): Promise<void> {
  const hold = (await getBurstHold(downloadId)) ?? (await getBurstHoldIgnoringTtl(downloadId))
  if (usesFirefoxRelease(hold)) {
    await cleanupFirefoxHold(downloadId, hold, skipTabId)
    return
  }
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
  await releaseAllBurstHoldsLocked('cannot_resume')
}

async function releaseAllBurstHoldsLocked(policy: 'cannot_resume' | 'send_legacy'): Promise<void> {
  clearClocks()
  clearPickerClock()
  clearSubmitStatusClock()
  if (!usesFirefoxProcess()) await reapExpiredBurstState()
  const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
  const holds = usesFirefoxProcess()
    ? await mergeExpiredHolds(await getAllBurstHolds(), window?.downloadIds ?? [])
    : await getAllBurstHolds()
  const ids = new Set<number>([...holds.keys(), ...(window?.downloadIds ?? [])])
  const skipTabId = window?.tabId ?? (await getCaptureSession())?.tabId
  const tabId = skipTabId
  await removeBurstWindow()
  await disarmCaptureSession()
  if (usesFirefoxProcess()) {
    if (policy === 'cannot_resume') {
      if (ids.size > 0) notify('capture_notif_title', 'burst_firefox_cannot_resume')
      for (const id of ids) {
        const hold = holds.get(id) ?? (await getBurstHoldIgnoringTtl(id))
        await cleanupFirefoxHold(id, hold, skipTabId)
      }
    } else {
      for (const id of ids) {
        const hold = holds.get(id) ?? (await getBurstHoldIgnoringTtl(id))
        if (hold) await migrateToLegacy(id, hold, skipTabId)
        else await cleanupFirefoxHold(id, hold, skipTabId)
      }
    }
  } else {
    for (const id of ids) {
      await resumeHeldDownload(id)
    }
  }
  if (typeof tabId === 'number') {
    void sendMessage('burst:close', {}, `content-script@${tabId}`).catch(() => undefined)
  }
}

async function probePaused(downloadId: number): Promise<'paused' | 'gone' | 'unknown'> {
  try {
    const items = await browser.downloads.search({ id: downloadId })
    const item = items[0]
    if (item && item.state === 'in_progress' && item.paused === true) return 'paused'
    return 'gone'
  } catch {
    return 'unknown'
  }
}

async function resumeOrDropHold(downloadId: number): Promise<void> {
  const probe = await probePaused(downloadId)
  if (probe === 'gone') {
    await removeBurstHold(downloadId)
    await removePendingDecision(downloadId)
    return
  }
  await resumeHeldDownload(downloadId)
}

async function reapExpiredBurstState(): Promise<void> {
  const expiredIds = await listExpiredBurstHoldIds()
  for (const id of expiredIds) {
    await resumeOrDropHold(id)
  }
  const live = await getBurstWindow()
  if (live) return
  const expiredWindow = await getBurstWindowIgnoringTtl()
  if (!expiredWindow) {
    await removeBurstWindow()
    return
  }
  for (const id of expiredWindow.downloadIds) {
    await resumeOrDropHold(id)
  }
  await removeBurstWindow()
}

export async function takeoverSuccess(downloadId: number, skipTabId?: number): Promise<void> {
  const hold = (await getBurstHold(downloadId)) ?? (await getBurstHoldIgnoringTtl(downloadId))
  if (usesFirefoxRelease(hold)) {
    await cleanupFirefoxHold(downloadId, hold, skipTabId)
    return
  }
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

function idSetOfAck(value: unknown): Set<string> {
  if (!Array.isArray(value)) return new Set()
  return new Set(value.filter((id): id is string => typeof id === 'string'))
}

async function applyAckPartitions(
  items: Array<{ clientItemId: string; downloadId: number }>,
  ack: Record<string, unknown>,
  skipTabId?: number,
  alreadyNotifiedCannotResume = false,
): Promise<{ succeeded: number; duplicate: number; error: number }> {
  const succeeded = idSetOfAck(ack.succeeded_item_ids)
  const duplicate = idSetOfAck(ack.duplicate_item_ids)
  let ok = 0
  let dup = 0
  let err = 0
  for (const item of items) {
    if (succeeded.has(item.clientItemId)) {
      await takeoverSuccess(item.downloadId, skipTabId)
      ok += 1
      continue
    }
    await resumeHeldDownload(item.downloadId, skipTabId)
    if (duplicate.has(item.clientItemId)) dup += 1
    else err += 1
  }
  if (usesFirefoxProcess() && dup + err > 0 && !alreadyNotifiedCannotResume) {
    notify('capture_notif_title', 'burst_firefox_cannot_resume')
  }
  return { succeeded: ok, duplicate: dup, error: err }
}

async function queryBurstStatus(requestId: string): Promise<BurstSubmitReply> {
  try {
    const ack = await wsClient.sendDirectBatchStatus(requestId)
    const status = typeof ack.status === 'string' ? ack.status : ''
    if (status === 'pending') return { accepted: false, error_code: 'pending' }
    if (status === 'complete') {
      const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
      const items = window?.submitItems ?? []
      const parts = await applyAckPartitions(items, ack, window?.tabId)
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
  if (ctx.storeUnproven !== true) {
    let liveStore: string | undefined
    try {
      const tab = await browser.tabs.get(ctx.tabId)
      liveStore = await resolveCookieStoreIdForTab(tab)
    } catch {
      liveStore = undefined
    }
    const frozen = ctx.cookieStoreId
    const liveOk =
      typeof frozen === 'string' &&
      frozen !== '' &&
      typeof liveStore === 'string' &&
      liveStore.trim() === frozen
    if (!liveOk) {
      return { accepted: false, error_code: 'store_unproven' }
    }
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
    let firefoxUnselected = false
    for (const entry of catalog) {
      const hold = holds.get(entry.downloadId)
      if (!hold) continue
      if (selected.has(entry.index)) {
        selectedHolds.push({ index: entry.index, downloadId: entry.downloadId, hold })
      } else {
        if (usesFirefoxRelease(hold)) firefoxUnselected = true
        await resumeHeldDownload(entry.downloadId, window.tabId)
      }
    }
    if (selectedHolds.length === 0) {
      await resumeAllBurstHoldsLocked()
      return { accepted: true }
    }
    if (firefoxUnselected) notify('capture_notif_title', 'burst_firefox_cannot_resume')
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
      const parts = await applyAckPartitions(submitItems ?? [], ack, ctx.tabId, firefoxUnselected)
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
            for (const item of submitItems ?? []) await resumeHeldDownload(item.downloadId, ctx.tabId)
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
  clearSubmitStatusClock()
  await removeBurstWindow()
  await disarmCaptureSession()
  await closeBurstOverlay(tabId, captureId)
}

export async function handleBurstCancel(data: BurstCancelMessage): Promise<{ ok: boolean }> {
  return runExclusive(async () => {
    const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
    const holds = await getAllBurstHolds()
    const expired = await listExpiredBurstHoldIds()
    if (!window && holds.size === 0 && expired.length === 0) return { ok: true }
    if (window && data.capture_id && data.capture_id !== window.captureId) return { ok: false }
    await resumeAllBurstHoldsLocked()
    return { ok: true }
  })
}

export async function handleBurstAlive(data: BurstAliveMessage): Promise<{ ok: boolean }> {
  const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
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
        for (const item of window.submitItems ?? []) await resumeHeldDownload(item.downloadId, window.tabId)
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
  if (usesFirefoxProcess()) {
    await recoverFirefoxBurstStateLocked()
    return
  }
  await reapExpiredBurstState()
  const holds = await getAllBurstHolds()
  for (const [downloadId] of holds) {
    const probe = await probePaused(downloadId)
    if (probe === 'gone') {
      await removeBurstHold(downloadId)
    } else if (probe === 'paused') {
      bridge?.restorePausedMemory(downloadId)
    } else {
      await resumeHeldDownload(downloadId)
    }
  }
  await resumeAllBurstHoldsLocked()
}

async function recoverFirefoxBurstStateLocked(): Promise<void> {
  const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
  const holds = await mergeExpiredHolds(await getAllBurstHolds(), window?.downloadIds ?? [])
  const skipTabId = window?.tabId ?? (await getCaptureSession())?.tabId
  const expiredIds = await listExpiredBurstHoldIds()
  for (const id of expiredIds) {
    if (window?.downloadIds.includes(id)) continue
    if (firefoxLegacyClaimed.has(id)) continue
    const hold = holds.get(id) ?? (await getBurstHoldIgnoringTtl(id))
    holds.delete(id)
    if (hold) await migrateToLegacy(id, hold, skipTabId)
    else await removeBurstHold(id)
  }
  const session = await getCaptureSession()
  if (window?.requestId) {
    const status = await queryBurstStatus(window.requestId)
    if (status.accepted || status.error_code === 'not_found') {
      if (status.error_code === 'not_found') {
        for (const item of window.submitItems ?? []) {
          const hold = holds.get(item.downloadId) ?? (await getBurstHoldIgnoringTtl(item.downloadId))
          await cleanupFirefoxHold(item.downloadId, hold, skipTabId)
        }
        notify('capture_notif_title', 'dom_not_found')
      }
      const ctx = submitContextFromWindow(window)
      if (ctx) await finishBurstSubmit(ctx.tabId, ctx.captureId)
      return
    }
    await saveBurstWindow({
      ...window,
      pickerDeadline: Date.now() + BURST_HOLD_TTL_MS,
    })
    armSubmitStatusClock(window.requestId, window.captureId)
    return
  }
  if (window?.phase === 'submitting') return
  if (window?.phase === 'picker') {
    await reopenOrLegacyFirefoxPicker(window, holds)
    return
  }
  for (const [id, hold] of holds) {
    if (firefoxLegacyClaimed.has(id)) continue
    if (window?.downloadIds.includes(id)) continue
    if (session && session.captureId === hold.captureId && (!window || window.phase === 'coalescing')) {
      if (!(await isCoalescerEligible(contextFromHold(hold)))) {
        await migrateToLegacy(id, hold, skipTabId)
        continue
      }
      const outcome = await admitConfirmedDownloadLocked(id, contextFromHold(hold), Date.now())
      if (outcome !== 'coalesced') {
        const leftover = (await getBurstHold(id)) ?? (await getBurstHoldIgnoringTtl(id))
        if (leftover) await migrateToLegacy(id, leftover, skipTabId)
      }
    } else {
      await migrateToLegacy(id, hold, skipTabId)
    }
  }
  if (window?.phase === 'coalescing') {
    await onCoalescerClock(window)
  }
}

function pingBlocksBurstReopen(
  ping: DomPingReply | undefined,
  window: BurstWindowRecord,
): boolean {
  if (!ping) return true
  if (typeof ping.document_nonce !== 'string' || typeof ping.page_href !== 'string') return true
  if (ping.extractor_picker_open || ping.dom_picker_open) return true
  if (typeof window.documentNonce === 'string' && window.documentNonce !== '') {
    if (ping.document_nonce !== window.documentNonce) return true
  }
  if (typeof window.pageHref === 'string' && window.pageHref !== '') {
    if (ping.page_href !== window.pageHref) return true
  }
  return false
}

async function sendLegacyAndClosePicker(
  window: BurstWindowRecord,
  holds: Map<number, BurstHold>,
  ids: number[],
  skipTabId?: number,
): Promise<void> {
  await removeBurstWindow()
  await disarmCaptureSession()
  if (typeof window.tabId === 'number') await closeBurstOverlay(window.tabId, window.captureId)
  for (const id of ids) {
    const hold = holds.get(id) ?? (await getBurstHoldIgnoringTtl(id))
    if (hold) await migrateToLegacy(id, hold, skipTabId)
  }
}

async function reopenOrLegacyFirefoxPicker(
  window: BurstWindowRecord,
  holds: Map<number, BurstHold>,
): Promise<void> {
  const ids = memberIdsForWindow(window, holds)
  const tabId = window.tabId
  const skipTabId = tabId
  const ping = typeof tabId === 'number' ? await pingContentScript(tabId) : undefined
  if (typeof tabId !== 'number' || pingBlocksBurstReopen(ping, window)) {
    notify('capture_notif_title', 'capture_reject_ping')
    await sendLegacyAndClosePicker(window, holds, ids, skipTabId)
    return
  }
  const catalog =
    window.catalog && window.catalog.length > 0
      ? window.catalog
      : window.downloadIds.map((downloadId, index) => ({ index, downloadId }))
  const items: BurstPickerCatalogItem[] = []
  for (const entry of catalog) {
    const hold = holds.get(entry.downloadId) ?? (await getBurstHoldIgnoringTtl(entry.downloadId))
    if (!hold) continue
    const row: BurstPickerCatalogItem = { index: entry.index }
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
    await sendLegacyAndClosePicker(window, holds, ids, skipTabId)
    return
  }
  const payload: BurstOpenMessage = {
    capture_id: window.captureId,
    items,
    store_unproven: window.storeUnproven === true,
  }
  try {
    const reply = (await sendMessage(
      'burst:open',
      payload,
      `content-script@${tabId}`,
    )) as { ok?: boolean } | undefined
    if (reply?.ok !== true) {
      notify('capture_notif_title', 'capture_reject_ping')
      await sendLegacyAndClosePicker(window, holds, ids, skipTabId)
      return
    }
  } catch {
    notify('capture_notif_title', 'capture_reject_ping')
    await sendLegacyAndClosePicker(window, holds, ids, skipTabId)
    return
  }
  if (typeof window.pickerDeadline === 'number') armPickerClock(window.pickerDeadline)
}

function dropSessionTab(tabId: number): void {
  void runExclusive(async () => {
    const window = (await getBurstWindow()) ?? (await getBurstWindowIgnoringTtl())
    const session = await getCaptureSession()
    if (window?.tabId !== tabId && session?.tabId !== tabId) return
    if (usesFirefoxProcess()) await releaseAllBurstHoldsLocked('send_legacy')
    else await resumeAllBurstHoldsLocked()
  })
}

export async function handleCaptureReconnect(): Promise<void> {
  if (usesFirefoxProcess()) return
  await resumeAllBurstHolds()
}

export function initBurstFlow(): void {
  setCaptureHostDownHook(() => resumeAllBurstHolds())
  setCaptureReconnectHook(() => handleCaptureReconnect())
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
  clearSubmitStatusClock()
  submitInflight = false
  captureChain = Promise.resolve()
  exclusiveDepth = 0
  pendingClaims = 0
  bridge = null
  firefoxBridge = null
  firefoxLegacyClaimed.clear()
}
