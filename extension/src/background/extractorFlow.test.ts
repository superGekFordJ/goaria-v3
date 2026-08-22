import { beforeEach, describe, expect, it, vi } from 'vitest'
import { RpcRequestError } from './extractorRpc'
import { cancelAllClicks, clickEpochOf, hasInFlight, tryLockClick } from './extractorClickLock'
import type { ExtractorSessionRecord } from './extractorSessionStore'

const TOKEN = 'a'.repeat(64)
const OTHER = 'b'.repeat(64)

const harness = vi.hoisted(() => {
  const data = new Map<string, unknown>()
  return {
    data,
    hrefAaa: 'https://share.alpha.test/s/aaa',
    hrefBbb: 'https://share.alpha.test/s/bbb',
    tabUrl: 'https://share.alpha.test/s/aaa',
    cookieStoreId: 'fixture-store-a',
    cookies: [] as Array<{
      name: string
      value: string
      domain: string
      path: string
      secure: boolean
      host_only: boolean
    }>,
    userAgent: 'FixtureBrowser/1.0',
    language: 'en-US',
    tabReads: [] as Array<{ tabId: number; url: string; cookieStoreId: string }>,
    storeReads: [] as Array<{ url?: string; cookieStoreId?: string }>,
    cookieReads: [] as Array<{ url: string; storeId: string }>,
    fallbackCalls: [] as Array<{ tabId: number; pageToken: string }>,
    results: [] as Array<Record<string, unknown>>,
    catalogs: [] as Array<Record<string, unknown>>,
    order: [] as Array<{ kind: 'result' | 'catalog'; ui?: string }>,
    rpc: [] as Array<{ type: string; payload: unknown; requestId?: string }>,
    rpcImpl: (async () => ({})) as (
      type: string,
      payload: unknown,
      requestId?: string,
    ) => Promise<Record<string, unknown>>,
    storage: {
      async get(key: string) {
        return data.get(key)
      },
      async set(key: string, value: unknown) {
        data.set(key, value)
      },
      async remove(key: string) {
        data.delete(key)
      },
      async getAll() {
        const all: Record<string, unknown> = {}
        for (const [k, v] of data) all[k] = v
        return all
      },
    },
  }
})

vi.stubGlobal('navigator', {
  get userAgent() {
    return harness.userAgent
  },
  get language() {
    return harness.language
  },
})

vi.mock('webextension-polyfill', () => ({
  default: {
    tabs: {
      get: async (tabId: number) => {
        const tab = { id: tabId, url: harness.tabUrl, cookieStoreId: harness.cookieStoreId }
        harness.tabReads.push({ tabId, url: tab.url, cookieStoreId: tab.cookieStoreId })
        return tab
      },
      onRemoved: { addListener() {} },
    },
  },
}))

vi.mock('webext-bridge/background', () => ({
  onMessage() {},
  sendMessage: async () => undefined,
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: { capabilities: ['extractor.batch'] },
}))

vi.mock('../stores/config.svelte', () => ({
  CAP_EXTRACTOR_BATCH: 'extractor.batch',
  EXTRACTOR_MAX_RESOLVE_COOKIES: 64,
  MSG_TYPE_BATCH_DOWNLOAD: 'batch_download',
  MSG_TYPE_EXTRACTOR_RESOLVE: 'extractor_resolve',
}))

vi.mock('./pageToken', () => ({
  pageTokenFromHref: async (href: string) => {
    if (href.includes('/s/bbb')) return OTHER
    if (href.includes('/s/aaa')) return TOKEN
    return undefined
  },
}))

vi.mock('./extractorVisibility', async () => {
  const { createExtractorSessionStore } = await vi.importActual<typeof import('./extractorSessionStore')>(
    './extractorSessionStore',
  )
  const store = createExtractorSessionStore(harness.storage)
  return {
    getExtractorSessionStore: () => store,
    pushExtractorResult(_tabId: number, payload: Record<string, unknown>) {
      harness.results.push(payload)
      harness.order.push({
        kind: 'result',
        ui: typeof payload.ui === 'string' ? payload.ui : undefined,
      })
    },
    pushPickerCatalog(_tabId: number, payload: Record<string, unknown>) {
      harness.catalogs.push(payload)
      harness.order.push({ kind: 'catalog' })
    },
    showExtractorFallbackNotification: async (tabId: number, pageToken: string) => {
      harness.fallbackCalls.push({ tabId, pageToken })
    },
    broadcastHide: async () => {},
  }
})

vi.mock('./wsClient', () => ({
  wsClient: {
    sendRequest: async (type: string, payload: unknown, requestId?: string) => {
      harness.rpc.push({ type, payload, requestId })
      return harness.rpcImpl(type, payload, requestId)
    },
  },
}))

vi.mock('./mintRequestId', () => ({
  mintRequestId: () => 'minted-batch-id',
}))

vi.mock('./cookieCapture', () => ({
  getStructuredCookiesForUrl: async (url: string, storeId: string) => {
    harness.cookieReads.push({ url, storeId })
    return { cookies: harness.cookies.map(cookie => ({ ...cookie })) }
  },
  resolveCookieStoreIdForTab: async (tab: { url?: string; cookieStoreId?: string }) => {
    harness.storeReads.push({ url: tab.url, cookieStoreId: tab.cookieStoreId })
    return tab.cookieStoreId
  },
}))

vi.mock('./capabilities', () => ({
  hasCapability: () => true,
}))

import {
  handleClick,
  handleFallback,
  handleFlowError,
  handleNav,
  handlePickerOpen,
  handlePickerSubmit,
} from './extractorFlow'
import { getExtractorSessionStore } from './extractorVisibility'
import { canReuseBatch } from './extractorBatchReuse'

function committingRow(tabId: number): ExtractorSessionRecord {
  return {
    tabId,
    pageToken: TOKEN,
    generation: 1,
    state: 'committing',
    sessionId: 'sess-1',
    itemIds: ['item-a'],
    batchRequestId: 'batch-1',
  }
}

describe('handleFlowError envelope RPC', () => {
  beforeEach(() => {
    harness.data.clear()
    harness.fallbackCalls = []
    harness.results = []
    harness.catalogs = []
    harness.order = []
    harness.rpc = []
    harness.rpcImpl = async () => ({})
    harness.tabUrl = harness.hrefAaa
    cancelAllClicks()
  })

  it.each(['timeout', 'unavailable'] as const)(
    'keeps the stored batch UUID when sendRequest rejects %s and the pre-click session is null',
    async code => {
      const tabId = 21
      const epoch = tryLockClick(tabId)
      expect(epoch).toBe(clickEpochOf(tabId))
      await getExtractorSessionStore().putSession(committingRow(tabId))

      await handleFlowError(tabId, TOKEN, null, new RpcRequestError(code, 'req-1'), epoch ?? 0)

      const stored = await getExtractorSessionStore().getSession(tabId)
      expect(stored?.batchRequestId).toBe('batch-1')
      expect(stored?.sessionId).toBe('sess-1')
      expect(stored?.itemIds).toEqual(['item-a'])
      expect(stored?.state).toBe('error')
      expect(stored?.errorCode).toBe(code)
      const reuse = canReuseBatch(stored ?? null)
      expect(reuse?.requestId).toBe('batch-1')
      expect(reuse?.markRetry).toBe(code === 'unavailable')
    },
  )
})

describe('handleNav', () => {
  beforeEach(() => {
    harness.data.clear()
    harness.tabUrl = harness.hrefAaa
    cancelAllClicks()
  })

  it('treats CS nav as authoritative when the claimed token still matches the session', async () => {
    const tabId = 22
    await getExtractorSessionStore().putSession(committingRow(tabId))
    tryLockClick(tabId)
    harness.tabUrl = harness.hrefAaa

    const reply = await handleNav({ page_token: TOKEN }, { tabId })

    expect(reply).toEqual({ ok: true })
    expect(await getExtractorSessionStore().getSession(tabId)).toBeNull()
    expect(hasInFlight(tabId)).toBe(false)
  })
})

describe('handleFallback', () => {
  beforeEach(() => {
    harness.data.clear()
    harness.fallbackCalls = []
    harness.tabUrl = harness.hrefAaa
  })

  it('notifies only when the live tab token still matches', async () => {
    expect(await handleFallback({ page_token: TOKEN }, { tabId: 23 })).toEqual({ ok: true })
    expect(harness.fallbackCalls).toEqual([{ tabId: 23, pageToken: TOKEN }])

    harness.tabUrl = harness.hrefBbb
    harness.fallbackCalls = []
    expect(await handleFallback({ page_token: TOKEN }, { tabId: 23 })).toEqual({ ok: false })
    expect(harness.fallbackCalls).toEqual([])
  })
})

function readyMulti(tabId: number, extra: Partial<ExtractorSessionRecord> = {}): ExtractorSessionRecord {
  return {
    tabId,
    pageToken: TOKEN,
    generation: 1,
    state: 'ready',
    sessionId: 'sess-n',
    itemIds: ['itm_alpha', 'itm_beta', 'itm_gamma'],
    displayItems: [
      { filename: 'a.bin', size_bytes: 10 },
      { filename: 'b.bin' },
      { filename: 'c.bin', size_bytes: 30 },
    ],
    leaseDeadline: Date.now() + 60_000,
    ...extra,
  }
}

async function waitUntil(pred: () => boolean): Promise<void> {
  await vi.waitFor(
    () => {
      if (!pred()) throw new Error('not ready')
    },
    { timeout: 1000, interval: 1 },
  )
}

describe('handlePickerOpen / handlePickerSubmit', () => {
  beforeEach(() => {
    harness.data.clear()
    harness.fallbackCalls = []
    harness.results = []
    harness.catalogs = []
    harness.order = []
    harness.rpc = []
    harness.rpcImpl = async () => ({ success: true })
    harness.tabUrl = harness.hrefAaa
    harness.cookieStoreId = 'fixture-store-a'
    harness.cookies = []
    harness.userAgent = 'FixtureBrowser/1.0'
    harness.language = 'en-US'
    harness.tabReads = []
    harness.storeReads = []
    harness.cookieReads = []
    cancelAllClicks()
  })

  it('returns session_expired when no row exists', async () => {
    const reply = await handlePickerOpen({ page_token: TOKEN }, { tabId: 40 })
    expect(reply).toEqual({ ok: false, error_code: 'session_expired' })
  })

  it('rejects length mismatch instead of guessing a catalog', async () => {
    await getExtractorSessionStore().putSession(
      readyMulti(41, { displayItems: [{ filename: 'a.bin' }] }),
    )
    const reply = await handlePickerOpen({ page_token: TOKEN }, { tabId: 41 })
    expect(reply).toEqual({ ok: false, error_code: 'invalid_request' })
  })

  it('maps submit indices to handles, mints request_id, and never resolves', async () => {
    await getExtractorSessionStore().putSession(readyMulti(42))
    const reply = await handlePickerSubmit(
      { page_token: TOKEN, indices: [1, 2], create_group: true, folder_name: 'Album' },
      { tabId: 42 },
    )
    expect(reply).toEqual({ accepted: true })
    await waitUntil(() => harness.rpc.length === 1)
    expect(harness.rpc[0]?.type).toBe('batch_download')
    expect(harness.rpc[0]?.requestId).toBe('minted-batch-id')
    expect(harness.rpc[0]?.payload).toMatchObject({
      session_id: 'sess-n',
      item_ids: ['itm_beta', 'itm_gamma'],
      create_group: true,
      folder_name: 'Album',
    })
    expect(harness.rpc.some(call => call.type === 'extractor_resolve')).toBe(false)
    await waitUntil(() => harness.results.some(row => row.ui === 'success'))
  })

  it('returns accepted false with busy on a picker-submit lock miss', async () => {
    await getExtractorSessionStore().putSession(readyMulti(43))
    tryLockClick(43)
    const reply = await handlePickerSubmit({ page_token: TOKEN, indices: [0, 1] }, { tabId: 43 })
    expect(reply).toEqual({ accepted: false, error_code: 'busy' })
    expect(harness.rpc).toEqual([])
  })

  it('shrinks remaining ids after a partial ack and pushes a new catalog', async () => {
    await getExtractorSessionStore().putSession(readyMulti(44))
    harness.rpcImpl = async () => ({
      success: false,
      succeeded_item_ids: ['itm_alpha'],
      errors_by_item_id: { itm_beta: 'add failed' },
    })
    const reply = await handlePickerSubmit({ page_token: TOKEN, indices: [0, 1, 2] }, { tabId: 44 })
    expect(reply.accepted).toBe(true)
    await waitUntil(() => harness.catalogs.length === 1)
    const stored = await getExtractorSessionStore().getSession(44)
    expect(stored?.state).toBe('ready')
    expect(stored?.itemIds).toEqual(['itm_beta', 'itm_gamma'])
    expect(stored?.batchRequestId).toBeUndefined()
    expect(JSON.stringify(harness.catalogs[0])).not.toContain('itm_beta')
    expect(harness.catalogs[0]?.count).toBe(2)
    expect(harness.results.some(row => row.ui === 'ready' && row.count === 2)).toBe(true)
  })

  it('reuses the stored N-id timeout UUID on picker-submit', async () => {
    const ids = ['itm_alpha', 'itm_beta', 'itm_gamma']
    await getExtractorSessionStore().putSession(
      readyMulti(45, {
        state: 'error',
        errorCode: 'timeout',
        itemIds: ids,
        batchRequestId: 'batch-n',
        lastCreateGroup: true,
        lastFolderName: 'Album',
      }),
    )
    const reuse = canReuseBatch(await getExtractorSessionStore().getSession(45))
    expect(reuse?.requestId).toBe('batch-n')
    const reply = await handlePickerSubmit(
      { page_token: TOKEN, indices: [0, 1, 2], create_group: true, folder_name: 'Album' },
      { tabId: 45 },
    )
    expect(reply).toEqual({ accepted: true })
    await waitUntil(() => harness.rpc.length === 1)
    expect(harness.rpc[0]?.requestId).toBe('batch-n')
    expect(harness.rpc[0]?.payload).toMatchObject({
      item_ids: ids,
      create_group: true,
      folder_name: 'Album',
    })
    expect(harness.rpc[0]?.type).toBe('batch_download')
  })

  it('zips displayItems to the submitted subset so a partial ack can shrink', async () => {
    await getExtractorSessionStore().putSession(readyMulti(46))
    harness.rpcImpl = async () => {
      const mid = await getExtractorSessionStore().getSession(46)
      expect(mid?.itemIds).toEqual(['itm_alpha', 'itm_gamma'])
      expect(mid?.displayItems).toEqual([
        { filename: 'a.bin', size_bytes: 10 },
        { filename: 'c.bin', size_bytes: 30 },
      ])
      return { success: false, succeeded_item_ids: ['itm_alpha'] }
    }
    const reply = await handlePickerSubmit({ page_token: TOKEN, indices: [0, 2] }, { tabId: 46 })
    expect(reply.accepted).toBe(true)
    await waitUntil(() => harness.catalogs.length === 1)
    const stored = await getExtractorSessionStore().getSession(46)
    expect(stored?.state).toBe('ready')
    expect(stored?.itemIds).toEqual(['itm_gamma'])
    expect(stored?.displayItems).toEqual([{ filename: 'c.bin', size_bytes: 30 }])
    expect(stored?.batchRequestId).toBeUndefined()
    expect(harness.catalogs[0]?.count).toBe(1)
    expect(harness.catalogs[0]?.items).toEqual([{ index: 0, filename: 'c.bin', size_bytes: 30 }])
  })

  it('pushes committing, then ready, then picker-catalog on a partial ack', async () => {
    await getExtractorSessionStore().putSession(readyMulti(47))
    harness.rpcImpl = async () => ({
      success: false,
      succeeded_item_ids: ['itm_alpha'],
    })
    await handlePickerSubmit({ page_token: TOKEN, indices: [0, 2] }, { tabId: 47 })
    await waitUntil(() => harness.order.some(row => row.kind === 'catalog'))
    const labels = harness.order.map(row => (row.kind === 'catalog' ? 'catalog' : row.ui))
    const committingAt = labels.indexOf('committing')
    const readyAt = labels.lastIndexOf('ready')
    const catalogAt = labels.indexOf('catalog')
    expect(committingAt).toBeGreaterThanOrEqual(0)
    expect(readyAt).toBeGreaterThan(committingAt)
    expect(catalogAt).toBeGreaterThan(readyAt)
  })

  it('does not reuse a UUID on the first ready picker submit', async () => {
    await getExtractorSessionStore().putSession(readyMulti(48))
    expect(canReuseBatch(await getExtractorSessionStore().getSession(48))).toBeNull()
    const reply = await handlePickerSubmit({ page_token: TOKEN, indices: [0, 1] }, { tabId: 48 })
    expect(reply).toEqual({ accepted: true })
    await waitUntil(() => harness.rpc.length === 1)
    expect(harness.rpc[0]?.requestId).toBe('minted-batch-id')
  })

  it('rejects picker-submit after auth_expired instead of minting a new batch', async () => {
    await getExtractorSessionStore().putSession(
      readyMulti(51, { state: 'error', errorCode: 'auth_expired' }),
    )
    const reply = await handlePickerSubmit({ page_token: TOKEN, indices: [0, 1] }, { tabId: 51 })
    expect(reply).toEqual({ accepted: false, error_code: 'session_expired' })
    expect(harness.rpc).toEqual([])
  })

  it('rejects picker-submit when displayItems cannot zip to the requested ids', async () => {
    await getExtractorSessionStore().putSession(
      readyMulti(52, { displayItems: [{ filename: 'a.bin' }, { filename: 'b.bin' }] }),
    )
    const reply = await handlePickerSubmit({ page_token: TOKEN, indices: [0, 1] }, { tabId: 52 })
    expect(reply).toEqual({ accepted: true })
    await waitUntil(() => harness.results.some(row => row.ui === 'error' && row.error_code === 'invalid_request'))
    expect(harness.rpc).toEqual([])
  })
})

describe('handleClick', () => {
  beforeEach(() => {
    harness.data.clear()
    harness.fallbackCalls = []
    harness.results = []
    harness.catalogs = []
    harness.order = []
    harness.rpc = []
    harness.rpcImpl = async () => ({ success: true })
    harness.tabUrl = harness.hrefAaa
    harness.cookieStoreId = 'fixture-store-a'
    harness.cookies = []
    harness.userAgent = 'FixtureBrowser/1.0'
    harness.language = 'en-US'
    harness.tabReads = []
    harness.storeReads = []
    harness.cookieReads = []
    cancelAllClicks()
  })

  it('rejects a multi-file capsule click without RPC', async () => {
    await getExtractorSessionStore().putSession(readyMulti(49))
    const reply = await handleClick({ page_token: TOKEN }, { tabId: 49 })
    expect(reply).toEqual({ accepted: false })
    expect(harness.rpc).toEqual([])
    expect(harness.results.some(row => row.ui === 'ready' && row.count === 3)).toBe(true)
  })

  it('does not copy lastCreateGroup onto a later 1-id commit', async () => {
    await getExtractorSessionStore().putSession({
      tabId: 50,
      pageToken: TOKEN,
      generation: 1,
      state: 'ready',
      sessionId: 'sess-1',
      itemIds: ['itm_solo'],
      displayItems: [{ filename: 'solo.bin' }],
      lastCreateGroup: true,
      lastFolderName: 'Album',
    })
    const reply = await handleClick({ page_token: TOKEN }, { tabId: 50 })
    expect(reply).toEqual({ accepted: true })
    await waitUntil(() => harness.rpc.length === 1)
    expect(harness.rpc[0]?.payload).not.toHaveProperty('create_group')
    expect(harness.rpc[0]?.payload).not.toHaveProperty('folder_name')
  })

  it('starts a fresh resolve on the next click after the local lease deadline', async () => {
    const tabId = 52
    await getExtractorSessionStore().putSession(
      readyMulti(tabId, { leaseDeadline: Date.now() - 1 }),
    )
    harness.rpcImpl = async () => ({ matched: false, items: [] })

    const reply = await handleClick({ page_token: TOKEN }, { tabId })
    expect(reply).toEqual({ accepted: true })
    await waitUntil(() => harness.rpc.length === 1)
    expect(harness.rpc[0]?.type).toBe('extractor_resolve')
    expect(harness.rpc[0]?.payload).not.toHaveProperty('session_id')
    expect(harness.rpc[0]?.payload).not.toHaveProperty('item_ids')
    expect(harness.rpc.some(call => call.type === 'batch_download')).toBe(false)
  })

  it('recaptures the current browser context on the next click after auth_expired', async () => {
    const tabId = 53
    const oldSessionId = 'session-old-fixture'
    const oldItemId = 'item-old-fixture'
    const oldRequestId = 'request-old-fixture'
    await getExtractorSessionStore().putSession({
      tabId,
      pageToken: TOKEN,
      generation: 1,
      state: 'error',
      errorCode: 'auth_expired',
      sessionId: oldSessionId,
      itemIds: [oldItemId],
      batchRequestId: oldRequestId,
      displayItems: [{ filename: 'old.bin' }],
    })

    const currentUrl = 'https://share.alpha.test/s/aaa?context=current'
    harness.tabUrl = currentUrl
    harness.cookieStoreId = 'fixture-store-current'
    harness.cookies = [
      {
        name: 'sid',
        value: 'current-fixture-value',
        domain: 'share.alpha.test',
        path: '/',
        secure: true,
        host_only: true,
      },
    ]
    harness.userAgent = 'FixtureBrowser/2.0'
    harness.language = 'fr-FR'
    harness.rpcImpl = async () => ({ matched: false, items: [] })

    const reply = await handleClick({ page_token: TOKEN }, { tabId })
    expect(reply).toEqual({ accepted: true })
    await waitUntil(() => harness.rpc.length === 1)

    expect(harness.rpc[0]).toMatchObject({
      type: 'extractor_resolve',
      payload: {
        source_url: currentUrl,
        cookies: harness.cookies,
        user_agent: 'FixtureBrowser/2.0',
        accept_language: 'fr-FR',
      },
    })
    expect(harness.rpc[0]?.requestId).toBeUndefined()
    expect(harness.storeReads).toContainEqual({
      url: currentUrl,
      cookieStoreId: 'fixture-store-current',
    })
    expect(harness.cookieReads).toEqual([
      { url: currentUrl, storeId: 'fixture-store-current' },
    ])
    expect(harness.tabReads.length).toBeGreaterThan(0)
    expect(harness.tabReads.every(read => read.url === currentUrl)).toBe(true)
    expect(harness.rpc.some(call => call.type === 'batch_download')).toBe(false)

    const payload = harness.rpc[0]?.payload as Record<string, unknown>
    expect(payload).not.toHaveProperty('session_id')
    expect(payload).not.toHaveProperty('item_ids')
    expect(payload).not.toHaveProperty('request_id')
    const serialized = JSON.stringify(payload)
    expect(serialized).not.toContain(oldSessionId)
    expect(serialized).not.toContain(oldItemId)
    expect(serialized).not.toContain(oldRequestId)
  })
})
