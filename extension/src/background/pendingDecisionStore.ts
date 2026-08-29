import browser from 'webextension-polyfill'
import { PENDING_DECISION_TTL_MS, STORAGE_KEY_PENDING_PREFIX } from '../stores/config.svelte'

/**
 * Persisted state for a download whose cancel/resume decision is in flight.
 * Stored in browser.storage.session so a Chrome MV3 service worker restart can
 * resume the decision instead of leaving the browser download stuck paused.
 * storage.session survives SW restarts but is cleared on browser restart.
 */
export type PendingDecision = {
  url: string
  filename: string
  fileSize: number
  startTime: number
  status: 'pending' | 'canceling' | 'resuming'
  /** Resolved download page URL; preserved for SW-restart Referer recovery. */
  downloadPage?: string
}

function keyFor(downloadId: number): string {
  return `${STORAGE_KEY_PENDING_PREFIX}${downloadId}`
}

function isExpired(decision: PendingDecision): boolean {
  return Date.now() - decision.startTime > PENDING_DECISION_TTL_MS
}

function parseDecision(raw: unknown): PendingDecision | null {
  if (!raw || typeof raw !== 'object') return null
  const obj = raw as Record<string, unknown>
  if (
    typeof obj.url !== 'string' ||
    typeof obj.filename !== 'string' ||
    typeof obj.fileSize !== 'number' ||
    typeof obj.startTime !== 'number' ||
    typeof obj.status !== 'string'
  ) {
    return null
  }
  return {
    url: obj.url,
    filename: obj.filename,
    fileSize: obj.fileSize,
    startTime: obj.startTime,
    status: obj.status as PendingDecision['status'],
    downloadPage: typeof obj.downloadPage === 'string' ? obj.downloadPage : undefined,
  }
}

/** Persist a pending decision for a download id. Returns false when storage failed. */
export async function savePendingDecision(
  downloadId: number,
  decision: PendingDecision,
): Promise<boolean> {
  try {
    await browser.storage.session.set({ [keyFor(downloadId)]: decision })
    return true
  } catch {
    return false
  }
}

/** Read a pending decision. Returns null when absent or expired (and cleans up). */
export async function getPendingDecision(downloadId: number): Promise<PendingDecision | null> {
  try {
    const result = await browser.storage.session.get(keyFor(downloadId))
    const decision = parseDecision(result[keyFor(downloadId)])
    if (!decision) return null
    if (isExpired(decision)) return null
    return decision
  } catch {
    return null
  }
}

/** Remove a pending decision after the cancel/resume outcome is applied. */
export async function removePendingDecision(downloadId: number): Promise<void> {
  try {
    await browser.storage.session.remove(keyFor(downloadId))
  } catch {
    // ignore — best-effort cleanup
  }
}

/** Update only the status field of an existing pending decision. */
export async function updatePendingStatus(
  downloadId: number,
  status: PendingDecision['status'],
): Promise<void> {
  const decision = await getPendingDecision(downloadId)
  if (!decision) return
  await savePendingDecision(downloadId, { ...decision, status })
}

/** Persist the resolved download page URL for SW-restart Referer recovery. */
export async function updatePendingDownloadPage(
  downloadId: number,
  downloadPage: string,
): Promise<void> {
  const decision = await getPendingDecision(downloadId)
  if (!decision) return
  await savePendingDecision(downloadId, { ...decision, downloadPage })
}

/** Return all non-expired pending decisions, keyed by download id. */
export async function getAllPendingDecisions(): Promise<Map<number, PendingDecision>> {
  const map = new Map<number, PendingDecision>()
  try {
    const all = await browser.storage.session.get(null)
    for (const [key, raw] of Object.entries(all)) {
      if (!key.startsWith(STORAGE_KEY_PENDING_PREFIX)) continue
      const decision = parseDecision(raw)
      if (!decision) continue
      const id = Number(key.slice(STORAGE_KEY_PENDING_PREFIX.length))
      if (Number.isNaN(id)) continue
      if (isExpired(decision)) continue
      map.set(id, decision)
    }
  } catch {
    // ignore — best-effort
  }
  return map
}

/** Expired pending_ ids still in session storage (not deleted). */
export async function listExpiredPendingDecisionIds(now = Date.now()): Promise<number[]> {
  const ids: number[] = []
  try {
    const all = await browser.storage.session.get(null)
    for (const [key, raw] of Object.entries(all)) {
      if (!key.startsWith(STORAGE_KEY_PENDING_PREFIX)) continue
      const decision = parseDecision(raw)
      if (!decision) continue
      const id = Number(key.slice(STORAGE_KEY_PENDING_PREFIX.length))
      if (Number.isNaN(id)) continue
      if (now - decision.startTime > PENDING_DECISION_TTL_MS) ids.push(id)
    }
  } catch {
    // ignore
  }
  return ids
}
