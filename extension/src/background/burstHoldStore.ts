import browser from 'webextension-polyfill'
import type { BurstWindowState } from './burstCoalescer'
import {
  BURST_HOLD_TTL_MS,
  STORAGE_KEY_BURST_HOLD_PREFIX,
  STORAGE_KEY_BURST_WINDOW,
} from '../stores/config.svelte'

const FORBIDDEN = new Set(['cookie', 'cookies', 'header', 'headers'])

export type BurstHold = {
  url: string
  filename: string
  fileSize: number
  startTime: number
  captureId: string
  referrer: string
  incognito: boolean
  mimeType?: string
  finalUrl?: string
}

export type BurstSubmitMapItem = {
  clientItemId: string
  downloadId: number
  index: number
}

export type BurstCatalogEntry = {
  index: number
  downloadId: number
}

export type BurstWindowRecord = BurstWindowState & {
  pickerDeadline?: number
  requestId?: string
  submitItems?: BurstSubmitMapItem[]
  catalog?: BurstCatalogEntry[]
  tabId?: number
  pageHref?: string
  incognito?: boolean
  cookieStoreId?: string
  storeUnproven?: boolean
  documentPolicy?: string
  documentNonce?: string
}

function hasForbidden(rec: Record<string, unknown>): boolean {
  for (const key of Object.keys(rec)) {
    if (FORBIDDEN.has(key.toLowerCase())) return true
  }
  return false
}

export function burstHoldKey(downloadId: number): string {
  return `${STORAGE_KEY_BURST_HOLD_PREFIX}${downloadId}`
}

export function parseBurstHold(
  raw: unknown,
  now = Date.now(),
  opts?: { ignoreTtl?: boolean },
): BurstHold | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const rec = raw as Record<string, unknown>
  if (hasForbidden(rec)) return null
  if (typeof rec.url !== 'string' || rec.url === '') return null
  if (typeof rec.filename !== 'string') return null
  if (typeof rec.fileSize !== 'number') return null
  if (typeof rec.startTime !== 'number' || !Number.isFinite(rec.startTime)) return null
  if (typeof rec.captureId !== 'string' || rec.captureId === '') return null
  if (typeof rec.referrer !== 'string') return null
  if (typeof rec.incognito !== 'boolean') return null
  if (!opts?.ignoreTtl && now - rec.startTime > BURST_HOLD_TTL_MS) return null
  const hold: BurstHold = {
    url: rec.url,
    filename: rec.filename,
    fileSize: rec.fileSize,
    startTime: rec.startTime,
    captureId: rec.captureId,
    referrer: rec.referrer,
    incognito: rec.incognito,
  }
  if (typeof rec.mimeType === 'string') hold.mimeType = rec.mimeType
  if (typeof rec.finalUrl === 'string') hold.finalUrl = rec.finalUrl
  return hold
}

export function parseBurstWindow(
  raw: unknown,
  now = Date.now(),
  opts?: { ignoreTtl?: boolean },
): BurstWindowRecord | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const rec = raw as Record<string, unknown>
  if (hasForbidden(rec)) return null
  if (typeof rec.captureId !== 'string' || rec.captureId === '') return null
  if (!Array.isArray(rec.downloadIds)) return null
  const downloadIds: number[] = []
  for (const id of rec.downloadIds) {
    if (typeof id !== 'number' || !Number.isInteger(id)) return null
    downloadIds.push(id)
  }
  if (typeof rec.firstItemAt !== 'number' || !Number.isFinite(rec.firstItemAt)) return null
  if (typeof rec.lastItemAt !== 'number' || !Number.isFinite(rec.lastItemAt)) return null
  if (rec.phase !== 'coalescing' && rec.phase !== 'picker' && rec.phase !== 'submitting') {
    return null
  }
  const deadline =
    typeof rec.pickerDeadline === 'number' && Number.isFinite(rec.pickerDeadline)
      ? rec.pickerDeadline
      : rec.firstItemAt + BURST_HOLD_TTL_MS
  if (!opts?.ignoreTtl) {
    if (rec.phase !== 'coalescing' && now > deadline) return null
    if (rec.phase === 'coalescing' && now - rec.firstItemAt > BURST_HOLD_TTL_MS) return null
  }
  const window: BurstWindowRecord = {
    captureId: rec.captureId,
    downloadIds,
    firstItemAt: rec.firstItemAt,
    lastItemAt: rec.lastItemAt,
    phase: rec.phase,
  }
  if (typeof rec.pickerDeadline === 'number') window.pickerDeadline = rec.pickerDeadline
  if (typeof rec.requestId === 'string' && rec.requestId !== '') window.requestId = rec.requestId
  if (typeof rec.storeUnproven === 'boolean') window.storeUnproven = rec.storeUnproven
  if (typeof rec.documentPolicy === 'string') window.documentPolicy = rec.documentPolicy
  if (typeof rec.tabId === 'number' && Number.isInteger(rec.tabId) && rec.tabId >= 0) {
    window.tabId = rec.tabId
  }
  if (typeof rec.pageHref === 'string' && rec.pageHref !== '') window.pageHref = rec.pageHref
  if (typeof rec.incognito === 'boolean') window.incognito = rec.incognito
  if (typeof rec.cookieStoreId === 'string' && rec.cookieStoreId !== '') {
    window.cookieStoreId = rec.cookieStoreId
  }
  if (typeof rec.documentNonce === 'string' && rec.documentNonce !== '') {
    window.documentNonce = rec.documentNonce
  }
  if (Array.isArray(rec.catalog)) {
    const catalog: BurstCatalogEntry[] = []
    for (const row of rec.catalog) {
      if (!row || typeof row !== 'object' || Array.isArray(row)) continue
      const item = row as Record<string, unknown>
      if (typeof item.index !== 'number' || !Number.isInteger(item.index)) continue
      if (typeof item.downloadId !== 'number' || !Number.isInteger(item.downloadId)) continue
      catalog.push({ index: item.index, downloadId: item.downloadId })
    }
    if (catalog.length > 0) window.catalog = catalog
  }
  if (Array.isArray(rec.submitItems)) {
    const items: BurstSubmitMapItem[] = []
    for (const row of rec.submitItems) {
      if (!row || typeof row !== 'object' || Array.isArray(row)) continue
      const item = row as Record<string, unknown>
      if (typeof item.clientItemId !== 'string' || typeof item.downloadId !== 'number') continue
      if (typeof item.index !== 'number') continue
      items.push({
        clientItemId: item.clientItemId,
        downloadId: item.downloadId,
        index: item.index,
      })
    }
    if (items.length > 0) window.submitItems = items
  }
  return window
}

export async function saveBurstHold(downloadId: number, hold: BurstHold): Promise<boolean> {
  try {
    await browser.storage.session.set({ [burstHoldKey(downloadId)]: hold })
    return true
  } catch {
    return false
  }
}

export async function removeBurstHold(downloadId: number): Promise<void> {
  try {
    await browser.storage.session.remove(burstHoldKey(downloadId))
  } catch {
    // best-effort
  }
}

export async function getBurstHold(downloadId: number): Promise<BurstHold | null> {
  try {
    const result = await browser.storage.session.get(burstHoldKey(downloadId))
    const raw = result[burstHoldKey(downloadId)]
    const live = parseBurstHold(raw)
    if (live) return live
    const shaped = parseBurstHold(raw, Date.now(), { ignoreTtl: true })
    if (!shaped) await removeBurstHold(downloadId)
    return null
  } catch {
    return null
  }
}

export async function getAllBurstHolds(): Promise<Map<number, BurstHold>> {
  const map = new Map<number, BurstHold>()
  try {
    const all = await browser.storage.session.get(null)
    for (const [key, raw] of Object.entries(all)) {
      if (!key.startsWith(STORAGE_KEY_BURST_HOLD_PREFIX)) continue
      const id = Number(key.slice(STORAGE_KEY_BURST_HOLD_PREFIX.length))
      if (Number.isNaN(id)) continue
      const live = parseBurstHold(raw)
      if (live) map.set(id, live)
    }
  } catch {
    // ignore
  }
  return map
}

export async function listExpiredBurstHoldIds(now = Date.now()): Promise<number[]> {
  const ids: number[] = []
  try {
    const all = await browser.storage.session.get(null)
    for (const [key, raw] of Object.entries(all)) {
      if (!key.startsWith(STORAGE_KEY_BURST_HOLD_PREFIX)) continue
      const id = Number(key.slice(STORAGE_KEY_BURST_HOLD_PREFIX.length))
      if (Number.isNaN(id)) continue
      const shaped = parseBurstHold(raw, now, { ignoreTtl: true })
      if (!shaped) {
        await removeBurstHold(id)
        continue
      }
      if (!parseBurstHold(raw, now)) ids.push(id)
    }
  } catch {
    // ignore
  }
  return ids
}

export async function saveBurstWindow(window: BurstWindowRecord): Promise<boolean> {
  try {
    await browser.storage.session.set({ [STORAGE_KEY_BURST_WINDOW]: window })
    return true
  } catch {
    return false
  }
}

export async function removeBurstWindow(): Promise<void> {
  try {
    await browser.storage.session.remove(STORAGE_KEY_BURST_WINDOW)
  } catch {
    // ignore
  }
}

export async function getBurstWindow(): Promise<BurstWindowRecord | null> {
  try {
    const result = await browser.storage.session.get(STORAGE_KEY_BURST_WINDOW)
    return parseBurstWindow(result[STORAGE_KEY_BURST_WINDOW])
  } catch {
    return null
  }
}

export async function getBurstWindowIgnoringTtl(): Promise<BurstWindowRecord | null> {
  try {
    const result = await browser.storage.session.get(STORAGE_KEY_BURST_WINDOW)
    return parseBurstWindow(result[STORAGE_KEY_BURST_WINDOW], Date.now(), { ignoreTtl: true })
  } catch {
    return null
  }
}
