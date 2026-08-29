import browser from 'webextension-polyfill'
import {
  CAPTURE_SESSION_TTL_MS,
  STORAGE_KEY_CAPTURE_SESSION,
} from '../stores/config.svelte'

const FORBIDDEN = new Set(['cookie', 'cookies', 'header', 'headers'])

export type CaptureSession = {
  captureId: string
  tabId: number
  documentNonce?: string
  pageHref: string
  incognito: boolean
  cookieStoreId?: string
  storeUnproven: boolean
  directConnectGeneration: number
  createdAt: number
  documentPolicy?: string
}

function hasForbidden(rec: Record<string, unknown>): boolean {
  for (const key of Object.keys(rec)) {
    if (FORBIDDEN.has(key.toLowerCase())) return true
  }
  return false
}

export function parseCaptureSession(raw: unknown, now = Date.now()): CaptureSession | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const rec = raw as Record<string, unknown>
  if (hasForbidden(rec)) return null
  if (typeof rec.captureId !== 'string' || rec.captureId === '') return null
  if (typeof rec.tabId !== 'number' || !Number.isInteger(rec.tabId) || rec.tabId < 0) return null
  if (typeof rec.pageHref !== 'string' || rec.pageHref === '') return null
  if (typeof rec.incognito !== 'boolean') return null
  if (typeof rec.storeUnproven !== 'boolean') return null
  if (typeof rec.directConnectGeneration !== 'number') return null
  if (typeof rec.createdAt !== 'number' || !Number.isFinite(rec.createdAt)) return null
  if (now - rec.createdAt > CAPTURE_SESSION_TTL_MS) return null
  const session: CaptureSession = {
    captureId: rec.captureId,
    tabId: rec.tabId,
    pageHref: rec.pageHref,
    incognito: rec.incognito,
    storeUnproven: rec.storeUnproven,
    directConnectGeneration: rec.directConnectGeneration,
    createdAt: rec.createdAt,
  }
  if (typeof rec.documentNonce === 'string' && rec.documentNonce !== '') {
    session.documentNonce = rec.documentNonce
  }
  if (typeof rec.cookieStoreId === 'string' && rec.cookieStoreId !== '') {
    session.cookieStoreId = rec.cookieStoreId
  }
  if (typeof rec.documentPolicy === 'string' && rec.documentPolicy !== '') {
    session.documentPolicy = rec.documentPolicy
  }
  return session
}

export async function getCaptureSession(): Promise<CaptureSession | null> {
  try {
    const all = await browser.storage.session.get(STORAGE_KEY_CAPTURE_SESSION)
    const parsed = parseCaptureSession(all[STORAGE_KEY_CAPTURE_SESSION])
    if (!parsed) {
      await disarmCaptureSession()
      return null
    }
    return parsed
  } catch {
    return null
  }
}

export async function writeCaptureSession(session: CaptureSession): Promise<boolean> {
  const existing = await getCaptureSession()
  if (existing) return false
  try {
    await browser.storage.session.set({ [STORAGE_KEY_CAPTURE_SESSION]: session })
    return true
  } catch {
    return false
  }
}

export async function disarmCaptureSession(): Promise<void> {
  try {
    await browser.storage.session.remove(STORAGE_KEY_CAPTURE_SESSION)
  } catch {
    // best-effort
  }
}
