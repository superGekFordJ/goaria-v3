import type { ReplayStorage } from './replayStore'
import {
  EXTRACTOR_ACK_LEASE_MS,
  EXTRACTOR_IGNORE_PREFIX,
  EXTRACTOR_LEASE_MS,
  EXTRACTOR_NOTIF_PREFIX,
  EXTRACTOR_SESSION_PREFIX,
} from './extractorKeys'

export {
  EXTRACTOR_ACK_LEASE_MS,
  EXTRACTOR_IGNORE_PREFIX,
  EXTRACTOR_LEASE_MS,
  EXTRACTOR_NOTIF_PREFIX,
  EXTRACTOR_SESSION_PREFIX,
}

const FORBIDDEN_FIELDS = new Set(['source_url', 'cookies', 'href', 'url'])

export type ExtractorUiState =
  | 'idle'
  | 'resolving'
  | 'ready'
  | 'committing'
  | 'success'
  | 'error'

export type ExtractorDisplayItem = {
  filename?: string
  size_bytes?: number
  mime_type?: string
}

export type ExtractorSessionRecord = {
  tabId: number
  pageToken: string
  generation: number
  state: ExtractorUiState
  sessionId?: string
  itemIds?: string[]
  batchRequestId?: string
  resolveSentAt?: number
  ackReceivedAt?: number
  leaseDeadline?: number
  displayItems?: ExtractorDisplayItem[]
  errorCode?: string
  batchRetryUsed?: boolean
  lastCreateGroup?: boolean
  lastFolderName?: string
}

export type ExtractorIgnoreRecord = {
  tabId: number
  pageToken: string
  ignoredAt: number
}

export function leaseDeadlineFromSend(now: number): number {
  return now + EXTRACTOR_LEASE_MS
}

export function leaseDeadlineFromAck(now: number): number {
  return now + EXTRACTOR_ACK_LEASE_MS
}

export function sessionStorageKey(tabId: number): string {
  return `${EXTRACTOR_SESSION_PREFIX}${tabId}`
}

export function ignoreStorageKey(tabId: number, pageToken: string): string {
  return `${EXTRACTOR_IGNORE_PREFIX}${tabId}_${pageToken}`
}

export function notifStorageKey(tabId: number, pageToken: string): string {
  return `${EXTRACTOR_NOTIF_PREFIX}${tabId}_${pageToken}`
}

export function parseSession(raw: unknown): ExtractorSessionRecord | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
  const rec = raw as Record<string, unknown>
  for (const key of Object.keys(rec)) {
    if (FORBIDDEN_FIELDS.has(key)) return null
  }
  if (typeof rec.tabId !== 'number' || !Number.isInteger(rec.tabId)) return null
  if (typeof rec.pageToken !== 'string' || rec.pageToken === '') return null
  if (typeof rec.generation !== 'number') return null
  if (!isUiState(rec.state)) return null
  const out: ExtractorSessionRecord = {
    tabId: rec.tabId,
    pageToken: rec.pageToken,
    generation: rec.generation,
    state: rec.state,
  }
  if (typeof rec.sessionId === 'string' && rec.sessionId !== '') out.sessionId = rec.sessionId
  if (Array.isArray(rec.itemIds) && rec.itemIds.every(id => typeof id === 'string')) {
    out.itemIds = rec.itemIds as string[]
  }
  if (typeof rec.batchRequestId === 'string' && rec.batchRequestId !== '') {
    out.batchRequestId = rec.batchRequestId
  }
  if (typeof rec.resolveSentAt === 'number') out.resolveSentAt = rec.resolveSentAt
  if (typeof rec.ackReceivedAt === 'number') out.ackReceivedAt = rec.ackReceivedAt
  if (typeof rec.leaseDeadline === 'number') out.leaseDeadline = rec.leaseDeadline
  if (typeof rec.errorCode === 'string' && rec.errorCode !== '') out.errorCode = rec.errorCode
  if (typeof rec.batchRetryUsed === 'boolean') out.batchRetryUsed = rec.batchRetryUsed
  if (typeof rec.lastCreateGroup === 'boolean') out.lastCreateGroup = rec.lastCreateGroup
  if (isNonUrlFolderName(rec.lastFolderName)) out.lastFolderName = rec.lastFolderName
  if (Array.isArray(rec.displayItems)) {
    out.displayItems = rec.displayItems.map(projectDisplayItem).filter((item): item is ExtractorDisplayItem => item !== null)
  }
  return out
}

function isNonUrlFolderName(value: unknown): value is string {
  if (typeof value !== 'string' || value === '') return false
  if (value.includes('://')) return false
  if (/^https?:/i.test(value)) return false
  return true
}

function isUiState(value: unknown): value is ExtractorUiState {
  return (
    value === 'idle' ||
    value === 'resolving' ||
    value === 'ready' ||
    value === 'committing' ||
    value === 'success' ||
    value === 'error'
  )
}

function projectDisplayItem(value: unknown): ExtractorDisplayItem | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const rec = value as Record<string, unknown>
  const item: ExtractorDisplayItem = {}
  if (typeof rec.filename === 'string') item.filename = rec.filename
  if (typeof rec.size_bytes === 'number') item.size_bytes = rec.size_bytes
  if (typeof rec.mime_type === 'string') item.mime_type = rec.mime_type
  return item
}

export function createExtractorSessionStore(storage: ReplayStorage, now: () => number = () => Date.now()) {
  async function putSession(record: ExtractorSessionRecord): Promise<void> {
    const parsed = parseSession(record)
    if (!parsed) return
    try {
      await storage.set(sessionStorageKey(parsed.tabId), parsed)
    } catch {
      /* ignore */
    }
  }

  async function getSession(tabId: number): Promise<ExtractorSessionRecord | null> {
    let raw: unknown
    try {
      raw = await storage.get(sessionStorageKey(tabId))
    } catch {
      return null
    }
    const parsed = parseSession(raw)
    if (!parsed) return null
    if (typeof parsed.leaseDeadline === 'number' && parsed.leaseDeadline <= now()) {
      await deleteSession(tabId)
      return null
    }
    return parsed
  }

  async function deleteSession(tabId: number): Promise<void> {
    try {
      await storage.remove(sessionStorageKey(tabId))
    } catch {
      /* ignore */
    }
  }

  async function setIgnored(tabId: number, pageToken: string): Promise<void> {
    const record: ExtractorIgnoreRecord = { tabId, pageToken, ignoredAt: now() }
    try {
      await storage.set(ignoreStorageKey(tabId, pageToken), record)
    } catch {
      /* ignore */
    }
  }

  async function isIgnored(tabId: number, pageToken: string): Promise<boolean> {
    let raw: unknown
    try {
      raw = await storage.get(ignoreStorageKey(tabId, pageToken))
    } catch {
      return false
    }
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
    const rec = raw as Record<string, unknown>
    return rec.tabId === tabId && rec.pageToken === pageToken
  }

  async function shouldNotifyFallback(tabId: number, pageToken: string): Promise<boolean> {
    const key = notifStorageKey(tabId, pageToken)
    let raw: unknown
    try {
      raw = await storage.get(key)
    } catch {
      return true
    }
    if (raw) return false
    try {
      await storage.set(key, { tabId, pageToken, notifiedAt: now() })
    } catch {
      /* ignore */
    }
    return true
  }

  async function listAll(): Promise<Record<string, unknown>> {
    if (!storage.getAll) return {}
    try {
      return (await storage.getAll()) ?? {}
    } catch {
      return {}
    }
  }

  async function clearTab(tabId: number): Promise<void> {
    const all = await listAll()
    const sessionKey = sessionStorageKey(tabId)
    const ignorePrefix = `${EXTRACTOR_IGNORE_PREFIX}${tabId}_`
    const notifPrefix = `${EXTRACTOR_NOTIF_PREFIX}${tabId}_`
    const keys = Object.keys(all).filter(
      key => key === sessionKey || key.startsWith(ignorePrefix) || key.startsWith(notifPrefix),
    )
    for (const key of keys) {
      try {
        await storage.remove(key)
      } catch {
        /* ignore */
      }
    }
    if (!storage.getAll) {
      await deleteSession(tabId)
    }
  }

  async function clearSessions(): Promise<void> {
    const all = await listAll()
    for (const key of Object.keys(all)) {
      if (!key.startsWith(EXTRACTOR_SESSION_PREFIX)) continue
      try {
        await storage.remove(key)
      } catch {
        /* ignore */
      }
    }
  }

  async function clearAll(): Promise<void> {
    const all = await listAll()
    for (const key of Object.keys(all)) {
      if (
        key.startsWith(EXTRACTOR_SESSION_PREFIX) ||
        key.startsWith(EXTRACTOR_IGNORE_PREFIX) ||
        key.startsWith(EXTRACTOR_NOTIF_PREFIX)
      ) {
        try {
          await storage.remove(key)
        } catch {
          /* ignore */
        }
      }
    }
  }

  async function listSessions(): Promise<ExtractorSessionRecord[]> {
    const all = await listAll()
    const out: ExtractorSessionRecord[] = []
    for (const [key, raw] of Object.entries(all)) {
      if (!key.startsWith(EXTRACTOR_SESSION_PREFIX)) continue
      const parsed = parseSession(raw)
      if (!parsed) continue
      if (typeof parsed.leaseDeadline === 'number' && parsed.leaseDeadline <= now()) {
        try {
          await storage.remove(key)
        } catch {
          /* ignore */
        }
        continue
      }
      out.push(parsed)
    }
    return out
  }

  return {
    putSession,
    getSession,
    deleteSession,
    setIgnored,
    isIgnored,
    shouldNotifyFallback,
    clearTab,
    clearSessions,
    clearAll,
    listSessions,
  }
}

export type ExtractorSessionStore = ReturnType<typeof createExtractorSessionStore>
