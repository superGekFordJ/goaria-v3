import { describe, expect, it } from 'vitest'
import type { ReplayStorage } from './replayStore'
import {
  EXTRACTOR_IGNORE_PREFIX,
  EXTRACTOR_NOTIF_PREFIX,
  EXTRACTOR_SESSION_PREFIX,
  createExtractorSessionStore,
  ignoreStorageKey,
  leaseDeadlineFromAck,
  leaseDeadlineFromSend,
  parseSession,
  sessionStorageKey,
} from './extractorSessionStore'

function memoryStorage(): ReplayStorage & { data: Map<string, unknown> } {
  const data = new Map<string, unknown>()
  return {
    data,
    async get(key) {
      return data.get(key)
    },
    async set(key, value) {
      data.set(key, value)
    },
    async remove(key) {
      data.delete(key)
    },
    async getAll() {
      const all: Record<string, unknown> = {}
      for (const [key, value] of data) all[key] = value
      return all
    },
  }
}

const TOKEN = 'a'.repeat(64)

describe('extractor prefixes', () => {
  it('uses dedicated prefixes instead of replay or pending', () => {
    expect(EXTRACTOR_SESSION_PREFIX).toBe('exs_')
    expect(EXTRACTOR_IGNORE_PREFIX).toBe('exi_')
    expect(EXTRACTOR_NOTIF_PREFIX).toBe('exn_')
    expect(EXTRACTOR_SESSION_PREFIX).not.toBe('replay_')
    expect(EXTRACTOR_IGNORE_PREFIX).not.toBe('pending_')
    expect(EXTRACTOR_NOTIF_PREFIX).not.toBe('replay_')
  })
})

describe('ignore key format', () => {
  it('is tabId plus page token without an href', () => {
    expect(ignoreStorageKey(7, TOKEN)).toBe(`exi_7_${TOKEN}`)
    expect(ignoreStorageKey(7, TOKEN)).not.toContain('https://')
    expect(sessionStorageKey(7)).toBe('exs_7')
  })
})

describe('parseSession', () => {
  it('rejects records that contain forbidden url or cookie fields', () => {
    expect(
      parseSession({
        tabId: 1,
        pageToken: TOKEN,
        generation: 1,
        state: 'idle',
        source_url: 'https://share.alpha.test/s/aaa',
      }),
    ).toBeNull()
    expect(
      parseSession({
        tabId: 1,
        pageToken: TOKEN,
        generation: 1,
        state: 'idle',
        href: 'https://share.alpha.test/s/aaa',
      }),
    ).toBeNull()
    expect(
      parseSession({
        tabId: 1,
        pageToken: TOKEN,
        generation: 1,
        state: 'idle',
        cookies: [{ name: 'sid', value: 'v' }],
      }),
    ).toBeNull()
  })

  it('drops unknown extra fields', () => {
    const parsed = parseSession({
      tabId: 1,
      pageToken: TOKEN,
      generation: 2,
      state: 'ready',
      telemetry: { ping: 1 },
      sessionId: 'sess',
    })
    expect(parsed).toEqual({
      tabId: 1,
      pageToken: TOKEN,
      generation: 2,
      state: 'ready',
      sessionId: 'sess',
    })
  })

  it('keeps last folder text without an href and still rejects source_url', () => {
    const parsed = parseSession({
      tabId: 1,
      pageToken: TOKEN,
      generation: 1,
      state: 'error',
      sessionId: 'sess',
      itemIds: ['itm_a', 'itm_b'],
      lastCreateGroup: true,
      lastFolderName: 'Album',
    })
    expect(parsed).toMatchObject({
      lastCreateGroup: true,
      lastFolderName: 'Album',
    })
    expect(JSON.stringify(parsed)).not.toContain('https://')
    expect(
      parseSession({
        tabId: 1,
        pageToken: TOKEN,
        generation: 1,
        state: 'ready',
        source_url: 'https://share.alpha.test/s/aaa',
        lastFolderName: 'Album',
      }),
    ).toBeNull()
    const droppedUrlFolder = parseSession({
      tabId: 1,
      pageToken: TOKEN,
      generation: 1,
      state: 'ready',
      lastFolderName: 'https://share.fixture.invalid/album',
    })
    expect(droppedUrlFolder?.lastFolderName).toBeUndefined()
  })
})

describe('createExtractorSessionStore', () => {
  it('clears a tab without needing an href', async () => {
    const storage = memoryStorage()
    const store = createExtractorSessionStore(storage, () => 1_000)
    await store.putSession({
      tabId: 9,
      pageToken: TOKEN,
      generation: 1,
      state: 'idle',
    })
    await store.setIgnored(9, TOKEN)
    await store.shouldNotifyFallback(9, TOKEN)
    expect(storage.data.has(`exs_9`)).toBe(true)
    expect(storage.data.has(`exi_9_${TOKEN}`)).toBe(true)
    await store.clearTab(9)
    expect(storage.data.has(`exs_9`)).toBe(false)
    expect(storage.data.has(`exi_9_${TOKEN}`)).toBe(false)
    expect(storage.data.has(`exn_9_${TOKEN}`)).toBe(false)
  })

  it('clearSessions drops session rows and keeps ignore keys', async () => {
    const storage = memoryStorage()
    const store = createExtractorSessionStore(storage, () => 1_000)
    await store.putSession({
      tabId: 4,
      pageToken: TOKEN,
      generation: 1,
      state: 'ready',
    })
    await store.setIgnored(4, TOKEN)
    await store.clearSessions()
    expect(storage.data.has('exs_4')).toBe(false)
    expect(storage.data.has(`exi_4_${TOKEN}`)).toBe(true)
  })

  it('caps fallback notifications once per tab token', async () => {
    const store = createExtractorSessionStore(memoryStorage(), () => 1_000)
    expect(await store.shouldNotifyFallback(3, TOKEN)).toBe(true)
    expect(await store.shouldNotifyFallback(3, TOKEN)).toBe(false)
  })

  it('computes lease deadlines from send and ack clocks', () => {
    expect(leaseDeadlineFromSend(0)).toBe(5 * 60 * 1000)
    expect(leaseDeadlineFromAck(0)).toBe(4.5 * 60 * 1000)
  })
})
