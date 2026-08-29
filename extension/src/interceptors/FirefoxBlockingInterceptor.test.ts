import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const config = vi.hoisted(() => ({
  autoCapture: true,
  registeredFileTypes: [] as string[],
}))

const connection = vi.hoisted(() => ({
  interceptionEnabled: true,
  status: 'connected',
}))

const capture = vi.hoisted(() => ({
  session: null as null | Record<string, unknown>,
}))

const sessionStore = vi.hoisted(() => {
  const data = new Map<string, unknown>()
  let setOk = true
  let getThrow = false
  return {
    data,
    get setOk() {
      return setOk
    },
    set setOk(v: boolean) {
      setOk = v
    },
    get getThrow() {
      return getThrow
    },
    set getThrow(v: boolean) {
      getThrow = v
    },
    reset() {
      data.clear()
      setOk = true
      getThrow = false
    },
    api: {
      async get(key: string | null) {
        if (getThrow) throw new Error('session get failed')
        if (key === null) {
          const all: Record<string, unknown> = {}
          for (const [k, v] of data) all[k] = v
          return all
        }
        if (typeof key === 'string') return { [key]: data.get(key) }
        return {}
      },
      async set(items: Record<string, unknown>) {
        if (!setOk) throw new Error('persist failed')
        for (const [k, v] of Object.entries(items)) data.set(k, v)
      },
      async remove(key: string) {
        data.delete(key)
      },
    },
  }
})

const webRequest = vi.hoisted(() => {
  type Filter = { urls: string[]; types?: string[] }
  const before: Array<{ filter: Filter }> = []
  const headers: Array<{
    filter: Filter
    extra: string[]
    listener: (details: unknown) => unknown
  }> = []
  const completed: Array<{ filter: Filter }> = []
  const errored: Array<{ filter: Filter }> = []
  return {
    before,
    headers,
    completed,
    errored,
    reset() {
      before.length = 0
      headers.length = 0
      completed.length = 0
      errored.length = 0
    },
    api: {
      onBeforeRequest: {
        addListener(_fn: unknown, filter: Filter) {
          before.push({ filter })
        },
      },
      onHeadersReceived: {
        addListener(fn: (details: unknown) => unknown, filter: Filter, extra: string[]) {
          headers.push({ filter, extra, listener: fn })
        },
      },
      onCompleted: {
        addListener(_fn: unknown, filter: Filter) {
          completed.push({ filter })
        },
      },
      onErrorOccurred: {
        addListener(_fn: unknown, filter: Filter) {
          errored.push({ filter })
        },
      },
    },
  }
})

const tabs = vi.hoisted(() => {
  const removed: number[] = []
  let tab: { url?: string } | undefined = { url: 'about:blank' }
  return {
    removed,
    setTab(next: { url?: string } | undefined) {
      tab = next
    },
    reset() {
      removed.length = 0
      tab = { url: 'about:blank' }
    },
    api: {
      get: async () => tab,
      query: async () => [],
      remove: async (id: number) => {
        removed.push(id)
      },
    },
  }
})

const ws = vi.hoisted(() => ({
  sendDownloadRequest: vi.fn(async () => ({ type: 'download_ack', success: true, gid: 'ar_test' })),
  sendDirectBatch: vi.fn(async () => ({
    succeeded_item_ids: [],
    duplicate_item_ids: [],
    errors_by_item_id: {},
  })),
  sendDirectBatchStatus: vi.fn(async () => ({ status: 'pending' })),
}))

vi.mock('../stores/config.svelte', () => ({
  BURST_HOLD_TTL_MS: 5 * 60 * 1000,
  BURST_MAX_DEADLINE_MS: 15_000,
  BURST_QUIET_WINDOW_MS: 1_000,
  CAP_DOWNLOAD_BATCH: 'download.batch',
  EXTRACTOR_MAX_SESSION_ITEMS: 128,
  STORAGE_KEY_BURST_HOLD_PREFIX: 'bhold_',
  STORAGE_KEY_BURST_WINDOW: 'bwin_window',
  PENDING_DECISION_TTL_MS: 30_000,
  STORAGE_KEY_PENDING_PREFIX: 'pending_',
  configState: config,
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: connection,
}))

vi.mock('../background/captureSession', () => ({
  getCaptureSession: async () => capture.session,
  writeCaptureSession: async () => true,
  disarmCaptureSession: async () => {
    capture.session = null
  },
}))

vi.mock('../background/wsClient', () => ({
  wsClient: ws,
}))

vi.mock('../background/cookieCapture', () => ({
  getCookiesForUrl: async () => [],
  resolveCookieStoreIdForTab: async () => undefined,
}))

vi.mock('../background/refererCapture', () => ({
  getDownloadPageUrl: async () => '',
}))

vi.mock('webext-bridge/background', () => ({
  onMessage() {},
  sendMessage: async () => ({
    ok: true,
    document_nonce: 'nonce',
    page_href: 'https://example.test/page',
    extractor_picker_open: false,
    dom_picker_open: false,
    burst_picker_open: false,
  }),
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    webRequest: webRequest.api,
    downloads: { search: vi.fn() },
    storage: { session: sessionStore.api },
    notifications: { create: async () => undefined },
    cookies: { getAll: async () => [] },
    runtime: { getURL: (p: string) => p },
    tabs: tabs.api,
  },
}))

vi.mock('../lib/i18n', () => ({
  t: (key: string) => key,
}))

import { FirefoxBlockingInterceptor } from './FirefoxBlockingInterceptor'
import { resetBootReadyForTests, setBootReady } from '../background/bootState'
import { resetBurstFlowForTests } from '../background/burstFlow'

const SESSION = {
  captureId: 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee',
  tabId: 4,
  documentNonce: 'nonce',
  pageHref: 'https://example.test/page',
  incognito: false,
  storeUnproven: true,
  directConnectGeneration: 1,
  createdAt: Date.now(),
}

function interceptDetails(extra: Record<string, unknown> = {}) {
  return {
    requestId: '1',
    url: 'https://cdn.example.test/file.bin',
    statusCode: 200,
    type: 'main_frame',
    tabId: 3,
    frameId: 0,
    responseHeaders: [
      { name: 'Content-Type', value: 'application/octet-stream' },
      { name: 'Content-Disposition', value: 'attachment; filename="a.bin"' },
    ],
    ...extra,
  }
}

describe('FirefoxBlockingInterceptor', () => {
  let interceptor: FirefoxBlockingInterceptor

  beforeEach(() => {
    resetBootReadyForTests()
    setBootReady(true)
    resetBurstFlowForTests()
    config.autoCapture = true
    config.registeredFileTypes = []
    connection.interceptionEnabled = true
    capture.session = null
    sessionStore.reset()
    webRequest.reset()
    tabs.reset()
    ws.sendDownloadRequest.mockReset()
    ws.sendDownloadRequest.mockResolvedValue({ type: 'download_ack', success: true, gid: 'ar_test' })
    interceptor = new FirefoxBlockingInterceptor()
    interceptor.register()
  })

  it('returns {} on 3xx hops', () => {
    const reply = webRequest.headers[0].listener({
      requestId: '1',
      url: 'https://example.test/file.bin',
      statusCode: 302,
      type: 'main_frame',
      tabId: 3,
      frameId: 0,
      responseHeaders: [{ name: 'Content-Disposition', value: 'attachment; filename="a.bin"' }],
    })
    expect(reply).toEqual({})
  })

  it('registers only main_frame and sub_frame', () => {
    for (const entry of [...webRequest.before, ...webRequest.headers, ...webRequest.completed, ...webRequest.errored]) {
      expect(entry.filter.types).toEqual(['main_frame', 'sub_frame'])
    }
  })

  it('recoverPendingDecisions continues burst recover instead of no-op', async () => {
    sessionStore.data.set('bhold_7', {
      url: 'https://cdn.example.test/7.bin',
      filename: '7.bin',
      fileSize: 10,
      startTime: Date.now(),
      captureId: SESSION.captureId,
      referrer: 'https://example.test/page',
      incognito: false,
      engine: 'firefox',
      tabId: 9,
      mainFrame: true,
    })
    await interceptor.recoverPendingDecisions()
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalled()
      expect(sessionStore.data.has('bhold_7')).toBe(false)
    })
  })

  it('passes through shouldIntercept before boot', () => {
    resetBootReadyForTests()
    const reply = webRequest.headers[0].listener(interceptDetails())
    expect(reply).toEqual({})
  })

  it('copies incognito, store, document, and frame onto the persisted hold', async () => {
    capture.session = { ...SESSION }
    const reply = await Promise.resolve(
      webRequest.headers[0].listener(
        interceptDetails({
          incognito: true,
          cookieStoreId: 'firefox-container-1',
          documentUrl: 'https://example.test/page',
          frameId: 12,
        }),
      ),
    )
    expect(reply).toEqual({ cancel: true })
    const hold = [...sessionStore.data.entries()].find(([k]) => k.startsWith('bhold_'))?.[1] as Record<
      string,
      unknown
    >
    expect(hold).toMatchObject({
      incognito: true,
      cookieStoreId: 'firefox-container-1',
      documentUrl: 'https://example.test/page',
      frameId: 12,
      engine: 'firefox',
    })
  })

  it('removes a blank main_frame tab immediately on unarmed intercept', async () => {
    tabs.setTab({ url: 'about:blank' })
    const reply = await Promise.resolve(webRequest.headers[0].listener(interceptDetails()))
    expect(reply).toEqual({ cancel: true })
    await vi.waitFor(() => {
      expect(tabs.removed).toEqual([3])
    })
    expect([...sessionStore.data.keys()].some(k => k.startsWith('bhold_'))).toBe(false)
  })

  it('does not cancel when armed persist fails', async () => {
    capture.session = { ...SESSION }
    sessionStore.setOk = false
    const reply = await Promise.resolve(webRequest.headers[0].listener(interceptDetails()))
    expect(reply).toEqual({})
    expect(tabs.removed).toEqual([])
  })

  it('cancels only after a successful armed persist', async () => {
    capture.session = { ...SESSION }
    const reply = await Promise.resolve(webRequest.headers[0].listener(interceptDetails()))
    expect(reply).toEqual({ cancel: true })
    expect([...sessionStore.data.keys()].some(k => k.startsWith('bhold_'))).toBe(true)
  })

  it('does not remove a blank tab during the armed cancel turn', async () => {
    capture.session = { ...SESSION }
    type Ack = { type: string; success: boolean; gid: string }
    let release!: (value: Ack | PromiseLike<Ack>) => void
    ws.sendDownloadRequest.mockImplementation(
      () =>
        new Promise<Ack>(resolve => {
          release = resolve
        }),
    )
    const reply = await Promise.resolve(webRequest.headers[0].listener(interceptDetails()))
    expect(reply).toEqual({ cancel: true })
    expect(tabs.removed).toEqual([])
    await vi.waitFor(() => {
      expect(typeof release).toBe('function')
    })
    release({ type: 'download_ack', success: true, gid: 'ar_test' })
    await vi.waitFor(() => {
      expect(tabs.removed).toEqual([3])
    })
  })

  it('never removes a sub_frame parent tab', async () => {
    capture.session = { ...SESSION }
    const reply = await Promise.resolve(
      webRequest.headers[0].listener(interceptDetails({ type: 'sub_frame', tabId: 9 })),
    )
    expect(reply).toEqual({ cancel: true })
    await Promise.resolve()
    await Promise.resolve()
    expect(tabs.removed).toEqual([])
  })

  it('does not cancel when synthetic hold id allocation fails', async () => {
    capture.session = { ...SESSION }
    sessionStore.getThrow = true
    const reply = await Promise.resolve(webRequest.headers[0].listener(interceptDetails()))
    expect(reply).toEqual({})
    expect(tabs.removed).toEqual([])
  })

  it('admits an eligible armed intercept without removing tabs until outcome', async () => {
    capture.session = { ...SESSION }
    const reply = await Promise.resolve(
      webRequest.headers[0].listener(
        interceptDetails({
          initiator: 'https://example.test/page',
          originUrl: 'https://example.test/page',
        }),
      ),
    )
    expect(reply).toEqual({ cancel: true })
    expect(tabs.removed).toEqual([])
    expect([...sessionStore.data.keys()].some(k => k.startsWith('bhold_'))).toBe(true)
    expect(sessionStore.data.has('bwin_window')).toBe(true)
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
  })

  it('does not remove the armed page tab after an armed same-tab cancel', async () => {
    capture.session = { ...SESSION }
    tabs.setTab({ url: 'about:blank' })
    const reply = await Promise.resolve(webRequest.headers[0].listener(interceptDetails({ tabId: 4 })))
    expect(reply).toEqual({ cancel: true })
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalled()
    })
    expect(tabs.removed).toEqual([])
  })
})

describe('background interceptor registration order', () => {
  it('registers before the config await and always starts burst flow', () => {
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../background/background.ts'), 'utf8')
    const registerIdx = src.indexOf('interceptor.register()')
    const awaitIdx = src.indexOf('await Promise.all([configState.loadEffects()')
    expect(registerIdx).toBeGreaterThan(-1)
    expect(awaitIdx).toBeGreaterThan(-1)
    expect(registerIdx).toBeLessThan(awaitIdx)
    expect(src).toContain('initBurstFlow()')
    expect(src).not.toContain('if (!isFirefox()) initBurstFlow()')
  })
})
