import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { bumpDirectConnectGeneration, resetDirectConnectGenerationForTests } from './domConnectGeneration'
import { getDomCatalog, invalidateDomCatalogById, resetDomCatalogsForTests } from './domCatalog'
import { RpcRequestError } from './extractorRpc'

const harness = vi.hoisted(() => {
  return {
    capabilities: ['download.batch'] as string[] | undefined,
    ping: {
      document_nonce: 'nonce-a',
      page_href: 'https://example.com/page#frag',
      extractor_picker_open: false,
      dom_picker_open: false,
    },
    pingHold: null as Promise<void> | null,
    onPing: null as null | (() => void),
    scan: {
      items: [
        {
          url: 'https://example.com/a.bin',
          kind: 'link' as const,
          filename: 'a.bin',
          document_policy: '',
          element_policy: '',
          rel_noreferrer: false,
        },
        {
          url: 'https://cdn.example.com/b.bin',
          kind: 'image' as const,
          filename: 'b.bin',
          document_policy: '',
          element_policy: '',
          rel_noreferrer: false,
        },
      ],
      truncated: false,
      title: 'Example',
      document_nonce: 'nonce-a',
      page_href: 'https://example.com/page#frag',
    },
    storeId: 'store-a' as string | undefined,
    tab: {
      id: 7,
      incognito: false,
      discarded: false,
      url: 'https://example.com/page',
    },
    cookies: [] as unknown[],
    cookieCalls: [] as Array<{ url: string; storeId: string }>,
    opens: [] as Array<Record<string, unknown>>,
    openReply: { ok: true } as { ok: boolean },
    closes: [] as string[],
    notifications: [] as Array<{ title: string; message: string }>,
    batch: [] as Array<{ payload: Record<string, unknown>; requestId: string }>,
    status: [] as string[],
    sendDirectBatch: async (payload: Record<string, unknown>, requestId: string) => {
      harness.batch.push({ payload, requestId })
      return {
        success: true,
        succeeded_item_ids: ['a'],
        duplicate_item_ids: [],
        errors_by_item_id: {},
      }
    },
    sendDirectBatchStatus: async (requestId: string) => {
      harness.status.push(requestId)
      return { status: 'pending' as string }
    },
  }
})

vi.mock('../stores/connection.svelte', () => ({
  connectionState: {
    get capabilities() {
      return harness.capabilities
    },
  },
}))

vi.mock('../stores/config.svelte', () => ({
  CAP_DOWNLOAD_BATCH: 'download.batch',
  EXTRACTOR_MAX_SESSION_ITEMS: 128,
}))

vi.mock('../lib/i18n', () => ({
  t: (key: string) => key,
}))

vi.mock('webext-bridge/background', () => ({
  onMessage() {},
  sendMessage: async (type: string, data: Record<string, unknown>) => {
    if (type === 'dom:ping') {
      if (harness.pingHold) await harness.pingHold
      harness.onPing?.()
      return { ...harness.ping }
    }
    if (type === 'dom:scan') return { ...harness.scan }
    if (type === 'dom:open') {
      harness.opens.push(data)
      return { ...harness.openReply }
    }
    if (type === 'dom:close') {
      harness.closes.push(String(data.catalog_id ?? ''))
      return undefined
    }
    return undefined
  },
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    runtime: { getURL: (path: string) => path },
    notifications: {
      create: async (opts: { title: string; message: string }) => {
        harness.notifications.push({ title: opts.title, message: opts.message })
      },
    },
    cookies: {
      getAll: async (details: { url: string; storeId: string }) => {
        harness.cookieCalls.push(details)
        return harness.cookies
      },
    },
    tabs: {
      get: async () => ({ ...harness.tab }),
      query: async () => [{ id: harness.tab.id }],
      onRemoved: { addListener() {} },
      onUpdated: { addListener() {} },
    },
  },
}))

vi.mock('./cookieCapture', () => ({
  resolveCookieStoreIdForTab: async () => harness.storeId,
}))

vi.mock('./wsClient', () => ({
  wsClient: {
    sendDirectBatch: (payload: Record<string, unknown>, requestId: string) =>
      harness.sendDirectBatch(payload, requestId),
    sendDirectBatchStatus: (requestId: string) => harness.sendDirectBatchStatus(requestId),
  },
}))

import {
  handleCollectPageLinks,
  handleDomAlive,
  handleDomCancel,
  handleDomStatus,
  handleDomSubmit,
  resetDomFlowForTests,
} from './domFlow'

const clickInfo = { frameId: 0, menuItemId: 'goaria-collect-page-links' }

async function openCatalog() {
  await handleCollectPageLinks(clickInfo as never, harness.tab as never)
  const catalogId = String(harness.opens.at(-1)?.catalog_id ?? '')
  expect(catalogId).not.toBe('')
  return catalogId
}

describe('domFlow', () => {
  beforeEach(() => {
    harness.capabilities = ['download.batch']
    harness.ping.extractor_picker_open = false
    harness.pingHold = null
    harness.onPing = null
    harness.ping.document_nonce = 'nonce-a'
    harness.ping.page_href = 'https://example.com/page#frag'
    harness.scan.document_nonce = 'nonce-a'
    harness.scan.page_href = 'https://example.com/page#frag'
    harness.scan.truncated = false
    harness.scan.items = [
      {
        url: 'https://example.com/a.bin',
        kind: 'link',
        filename: 'a.bin',
        document_policy: '',
        element_policy: '',
        rel_noreferrer: false,
      },
      {
        url: 'https://cdn.example.com/b.bin',
        kind: 'image',
        filename: 'b.bin',
        document_policy: '',
        element_policy: '',
        rel_noreferrer: false,
      },
    ]
    harness.storeId = 'store-a'
    harness.tab.incognito = false
    harness.tab.discarded = false
    harness.cookies = [{ name: 'sid', value: '1', secure: true, sameSite: 'lax' }]
    harness.cookieCalls = []
    harness.opens = []
    harness.openReply = { ok: true }
    harness.closes = []
    harness.notifications = []
    harness.batch = []
    harness.status = []
    harness.sendDirectBatch = async (payload, requestId) => {
      harness.batch.push({ payload, requestId })
      return {
        success: true,
        succeeded_item_ids: ['a'],
        duplicate_item_ids: [],
        errors_by_item_id: {},
      }
    }
    harness.sendDirectBatchStatus = async requestId => {
      harness.status.push(requestId)
      return { status: 'pending' }
    }
    resetDomFlowForTests()
    resetDomCatalogsForTests()
    resetDirectConnectGenerationForTests(0)
  })

  afterEach(() => {
    resetDomFlowForTests()
    resetDomCatalogsForTests()
    resetDirectConnectGenerationForTests(0)
  })

  it('refuses when the extractor picker is already open', async () => {
    harness.ping.extractor_picker_open = true
    await handleCollectPageLinks(clickInfo as never, harness.tab as never)
    expect(harness.opens).toEqual([])
    expect(harness.notifications.some(n => n.message === 'dom_mutex_body')).toBe(true)
  })

  it('notifies and skips scan when download.batch is missing', async () => {
    harness.capabilities = []
    await handleCollectPageLinks(clickInfo as never, harness.tab as never)
    expect(harness.opens).toEqual([])
    expect(harness.notifications.some(n => n.message === 'dom_missing_cap_body')).toBe(true)
  })

  it('marks store unproven and still opens a URL-free picker', async () => {
    harness.storeId = undefined
    const catalogId = await openCatalog()
    const open = harness.opens[0]
    expect(open?.store_unproven).toBe(true)
    expect(JSON.stringify(open)).not.toContain('https://example.com/a.bin')
    expect(getDomCatalog(catalogId)?.storeUnproven).toBe(true)
  })

  it('refuses a stale index without sending', async () => {
    const catalogId = await openCatalog()
    const reply = await handleDomSubmit(
      { catalog_id: catalogId, indices: [9] },
      { tabId: 7 },
    )
    expect(reply).toEqual({ accepted: false, error_code: 'invalid_request' })
    expect(harness.batch).toEqual([])
  })

  it('refuses submit after a generation bump', async () => {
    const catalogId = await openCatalog()
    bumpDirectConnectGeneration()
    const reply = await handleDomSubmit(
      { catalog_id: catalogId, indices: [0] },
      { tabId: 7 },
    )
    expect(reply.error_code).toBe('invalid_request')
    expect(harness.batch).toEqual([])
    expect(getDomCatalog(catalogId)).toBeUndefined()
  })

  it('keeps the catalog on busy and reuses the same request id', async () => {
    harness.sendDirectBatch = async (payload, requestId) => {
      harness.batch.push({ payload, requestId })
      throw new RpcRequestError('busy', requestId)
    }
    const catalogId = await openCatalog()
    const first = await handleDomSubmit(
      { catalog_id: catalogId, indices: [0] },
      { tabId: 7 },
    )
    expect(first).toEqual({ accepted: false, error_code: 'busy' })
    expect(getDomCatalog(catalogId)).toBeDefined()
    const second = await handleDomSubmit(
      { catalog_id: catalogId, indices: [0] },
      { tabId: 7 },
    )
    expect(second.error_code).toBe('busy')
    expect(harness.batch).toHaveLength(2)
    expect(harness.batch[0]?.requestId).toBe(harness.batch[1]?.requestId)
    expect(harness.batch[0]?.payload.items).toEqual(harness.batch[1]?.payload.items)
    expect(harness.cookieCalls).toHaveLength(1)
    expect(harness.batch[0]?.requestId).toHaveLength(36)
  })

  it('uses the original UUID on ack-loss status and never mints a second id', async () => {
    harness.sendDirectBatch = async (payload, requestId) => {
      harness.batch.push({ payload, requestId })
      throw new RpcRequestError('timeout', requestId)
    }
    const catalogId = await openCatalog()
    const reply = await handleDomSubmit(
      { catalog_id: catalogId, indices: [0] },
      { tabId: 7 },
    )
    expect(reply).toEqual({ accepted: false, error_code: 'pending' })
    expect(harness.status).toEqual([harness.batch[0]?.requestId])
    expect(harness.batch).toHaveLength(1)
    expect(getDomCatalog(catalogId)).toBeDefined()
  })

  it('omits Cookie for an item when the serialized line exceeds 4096 bytes', async () => {
    harness.cookies = Array.from({ length: 40 }, (_, i) => ({
      name: `k${i}`,
      value: 'x'.repeat(120),
      secure: true,
      sameSite: 'lax',
    }))
    const catalogId = await openCatalog()
    await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    const item = (harness.batch[0]?.payload.items as Record<string, unknown>[])[0]
    expect(item?.headers).toBeUndefined()
    expect(item?.url).toBe('https://example.com/a.bin')
  })

  it('drops SameSite strict cookies for a cross-site item', async () => {
    harness.cookies = [{ name: 'sid', value: '1', secure: true, sameSite: 'strict' }]
    harness.scan.items = [
      {
        url: 'https://example.com/a.bin',
        kind: 'link',
        filename: 'a.bin',
        document_policy: '',
        element_policy: '',
        rel_noreferrer: false,
      },
      {
        url: 'https://cdn.fixture.invalid/b.bin',
        kind: 'link',
        filename: 'b.bin',
        document_policy: '',
        element_policy: '',
        rel_noreferrer: false,
      },
    ]
    const catalogId = await openCatalog()
    await handleDomSubmit({ catalog_id: catalogId, indices: [0, 1] }, { tabId: 7 })
    const items = harness.batch[0]?.payload.items as Record<string, unknown>[]
    expect(items[0]?.headers).toEqual(['Cookie: sid=1'])
    expect(items[1]?.headers).toBeUndefined()
  })

  it('drops the catalog on explicit cancel', async () => {
    const catalogId = await openCatalog()
    const reply = await handleDomCancel({ catalog_id: catalogId }, { tabId: 7 })
    expect(reply).toEqual({ ok: true })
    expect(getDomCatalog(catalogId)).toBeUndefined()
    expect(handleDomAlive({ catalog_id: catalogId }, { tabId: 7 })).toEqual({ ok: false })
  })

  it('does not send a Referer header alongside Cookie', async () => {
    const catalogId = await openCatalog()
    await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    const item = (harness.batch[0]?.payload.items as Record<string, unknown>[])[0]
    const headers = item?.headers as string[] | undefined
    expect(headers?.some(h => h.toLowerCase().startsWith('referer:'))).not.toBe(true)
    expect(typeof item?.download_page === 'string' || item?.download_page === undefined).toBe(true)
  })

  it('refuses iframe menu clicks', async () => {
    await handleCollectPageLinks(
      { frameId: 1, menuItemId: 'goaria-collect-page-links' } as never,
      harness.tab as never,
    )
    expect(harness.opens).toEqual([])
    expect(harness.notifications.some(n => n.message === 'dom_iframe_refused_body')).toBe(true)
  })

  it('refuses submit when the page fragment no longer matches', async () => {
    const catalogId = await openCatalog()
    harness.ping.page_href = 'https://example.com/page#other'
    const reply = await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    expect(reply).toEqual({ accepted: false, error_code: 'invalid_request' })
    expect(harness.batch).toEqual([])
    expect(getDomCatalog(catalogId)).toBeUndefined()
  })

  it('refuses submit when the tab is discarded', async () => {
    const catalogId = await openCatalog()
    harness.tab.discarded = true
    const reply = await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    expect(reply).toEqual({ accepted: false, error_code: 'invalid_request' })
    expect(harness.batch).toEqual([])
    expect(getDomCatalog(catalogId)).toBeUndefined()
  })

  it('refuses submit when incognito no longer matches the catalog', async () => {
    const catalogId = await openCatalog()
    harness.tab.incognito = true
    const reply = await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    expect(reply).toEqual({ accepted: false, error_code: 'invalid_request' })
    expect(harness.batch).toEqual([])
    expect(getDomCatalog(catalogId)).toBeUndefined()
  })

  it('keeps the catalog when status query throws after ack-loss', async () => {
    harness.sendDirectBatch = async (payload, requestId) => {
      harness.batch.push({ payload, requestId })
      throw new RpcRequestError('timeout', requestId)
    }
    harness.sendDirectBatchStatus = async requestId => {
      harness.status.push(requestId)
      throw new Error('WebSocket is not connected')
    }
    const catalogId = await openCatalog()
    const reply = await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    expect(reply).toEqual({ accepted: false, error_code: 'pending' })
    expect(harness.status).toEqual([harness.batch[0]?.requestId])
    expect(getDomCatalog(catalogId)).toBeDefined()
  })

  it('serializes overlapping collects for one tab', async () => {
    let release!: () => void
    harness.pingHold = new Promise(resolve => {
      release = resolve
    })
    const first = handleCollectPageLinks(clickInfo as never, harness.tab as never)
    await Promise.resolve()
    await handleCollectPageLinks(clickInfo as never, harness.tab as never)
    expect(harness.opens).toEqual([])
    release()
    await first
    expect(harness.opens).toHaveLength(1)
  })

  it('drops the catalog when the content script refuses dom:open', async () => {
    harness.openReply = { ok: false }
    await handleCollectPageLinks(clickInfo as never, harness.tab as never)
    const catalogId = String(harness.opens.at(-1)?.catalog_id ?? '')
    expect(catalogId).not.toBe('')
    expect(getDomCatalog(catalogId)).toBeUndefined()
    expect(harness.notifications.some(n => n.message === 'dom_mutex_body')).toBe(true)
  })

  it('omits cookies when the live store no longer matches the catalog', async () => {
    const catalogId = await openCatalog()
    harness.storeId = 'store-other'
    await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    const item = (harness.batch[0]?.payload.items as Record<string, unknown>[])[0]
    expect(item?.headers).toBeUndefined()
    expect(harness.cookieCalls).toEqual([])
  })

  it('aborts send when the catalog is dropped after cookie collection', async () => {
    const catalogId = await openCatalog()
    let pings = 0
    harness.onPing = () => {
      pings += 1
      if (pings >= 2) invalidateDomCatalogById(catalogId)
    }
    const reply = await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    expect(reply).toEqual({ accepted: false, error_code: 'invalid_request' })
    expect(harness.batch).toEqual([])
  })

  it('binds alive probes to the catalog tab', async () => {
    const catalogId = await openCatalog()
    expect(handleDomAlive({ catalog_id: catalogId }, { tabId: 99 })).toEqual({ ok: false })
    expect(handleDomAlive({ catalog_id: catalogId }, { tabId: 7 })).toEqual({ ok: true })
  })

  it('re-polls status without minting a new id', async () => {
    harness.sendDirectBatch = async (payload, requestId) => {
      harness.batch.push({ payload, requestId })
      throw new RpcRequestError('timeout', requestId)
    }
    const catalogId = await openCatalog()
    const first = await handleDomSubmit({ catalog_id: catalogId, indices: [0] }, { tabId: 7 })
    expect(first.error_code).toBe('pending')
    harness.sendDirectBatchStatus = async requestId => {
      harness.status.push(requestId)
      return {
        status: 'complete',
        succeeded_item_ids: ['a'],
        duplicate_item_ids: [],
        errors_by_item_id: {},
      }
    }
    const second = await handleDomStatus({ catalog_id: catalogId }, { tabId: 7 })
    expect(second.accepted).toBe(true)
    expect(harness.status[1]).toBe(harness.batch[0]?.requestId)
    expect(harness.batch).toHaveLength(1)
    expect(getDomCatalog(catalogId)).toBeUndefined()
  })
})
