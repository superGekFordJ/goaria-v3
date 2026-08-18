export type ReplayRecord = {
  requestId: string
  type: string
  expiresAt: number
}

export type ReplayStorage = {
  get: (key: string) => Promise<unknown>
  set: (key: string, value: unknown) => Promise<void>
  remove: (key: string) => Promise<void>
}

export function createReplayStore(
  storage: ReplayStorage,
  prefix: string,
  ttlMs: number,
  now: () => number = () => Date.now(),
) {
  const memory = new Map<string, ReplayRecord>()

  function storageKey(requestId: string): string {
    return `${prefix}${requestId}`
  }

  function parseRecord(raw: unknown): ReplayRecord | null {
    if (!raw || typeof raw !== 'object') return null
    const obj = raw as Record<string, unknown>
    if (
      typeof obj.requestId !== 'string' ||
      typeof obj.type !== 'string' ||
      typeof obj.expiresAt !== 'number'
    ) {
      return null
    }
    return { requestId: obj.requestId, type: obj.type, expiresAt: obj.expiresAt }
  }

  async function persist(type: string, requestId: string): Promise<void> {
    const record: ReplayRecord = { requestId, type, expiresAt: now() + ttlMs }
    memory.set(requestId, record)
    try {
      await storage.set(storageKey(requestId), record)
    } catch {
      // storage unavailable: memory still holds the record for this lifetime.
    }
  }

  async function load(requestId: string): Promise<ReplayRecord | null> {
    const mem = memory.get(requestId)
    if (mem && mem.expiresAt > now()) return mem
    if (mem) memory.delete(requestId)

    let raw: unknown
    try {
      raw = await storage.get(storageKey(requestId))
    } catch {
      return null
    }
    const record = parseRecord(raw)
    if (!record) return null
    if (record.expiresAt <= now()) {
      memory.delete(requestId)
      try {
        await storage.remove(storageKey(requestId))
      } catch {
        /* ignore */
      }
      return null
    }
    memory.set(requestId, record)
    return record
  }

  // Explicit SW-replay: reuse only when the caller already has this requestId.
  async function persistOrReuse(type: string, requestId: string): Promise<string> {
    const existing = await load(requestId)
    if (existing) return existing.requestId
    await persist(type, requestId)
    return requestId
  }

  async function remove(requestId: string): Promise<void> {
    memory.delete(requestId)
    try {
      await storage.remove(storageKey(requestId))
    } catch {
      /* ignore */
    }
  }

  return { persist, load, persistOrReuse, remove }
}

export type ReplayStore = ReturnType<typeof createReplayStore>
