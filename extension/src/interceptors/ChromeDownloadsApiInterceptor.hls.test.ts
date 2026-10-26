import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DownloadHandoffMessage, DownloadResponse } from '../utils/messaging'

const config = vi.hoisted(() => ({
  autoCapture: true,
  registeredFileTypes: [] as string[],
}))

const connection = vi.hoisted(() => ({
  interceptionEnabled: true,
  status: 'connected',
}))

const pending = vi.hoisted(() => {
  const map = new Map<number, Record<string, unknown>>()
  const expiredIds: number[] = []
  return {
    map,
    expiredIds,
    async savePendingDecision(id: number, decision: Record<string, unknown>) {
      map.set(id, { ...decision })
      return true
    },
    async removePendingDecision(id: number) {
      map.delete(id)
    },
    async getPendingDecision(id: number) {
      return map.get(id) ?? null
    },
    async getAllPendingDecisions() {
      return new Map(map)
    },
    async listExpiredPendingDecisionIds() {
      return [...expiredIds]
    },
    async updatePendingStatus(id: number, status: string) {
      const cur = map.get(id)
      if (cur) map.set(id, { ...cur, status })
    },
    async updatePendingDownloadPage(id: number, downloadPage: string) {
      const cur = map.get(id)
      if (cur) map.set(id, { ...cur, downloadPage })
    },
  }
})

const ws = vi.hoisted(() => ({
  sendDownloadRequest: vi.fn(
    async (_req: DownloadHandoffMessage): Promise<DownloadResponse> => ({
      type: 'download_ack',
      success: true,
      gid: 'ar_test',
    }),
  ),
}))

const cookies = vi.hoisted(() => ({
  getCookiesForUrl: vi.fn(async (_url: string, _storeId?: string) => [] as string[]),
}))

const downloads = vi.hoisted(() => {
  type Listener = (item: unknown) => void
  const created: Listener[] = []
  const changed: Array<(delta: unknown) => void> = []
  const filename: Array<(item: unknown, suggest: (s?: unknown) => void) => boolean | void> = []
  const items = new Map<number, Record<string, unknown>>()
  const calls = {
    pause: [] as number[],
    search: [] as number[],
    cancel: [] as number[],
    erase: [] as number[],
    resume: [] as number[],
  }
  return {
    created,
    changed,
    filename,
    items,
    calls,
    reset() {
      created.length = 0
      changed.length = 0
      filename.length = 0
      items.clear()
      calls.pause = []
      calls.search = []
      calls.cancel = []
      calls.erase = []
      calls.resume = []
    },
    api: {
      onCreated: {
        addListener(fn: Listener) {
          created.push(fn)
        },
      },
      onChanged: {
        addListener(fn: (delta: unknown) => void) {
          changed.push(fn)
        },
      },
      onDeterminingFilename: {
        addListener(fn: (item: unknown, suggest: (s?: unknown) => void) => boolean | void) {
          filename.push(fn)
        },
      },
      pause: async (id: number) => {
        calls.pause.push(id)
        const item = items.get(id)
        if (item) item.paused = true
      },
      search: async (query: { id: number }) => {
        calls.search.push(query.id)
        const item = items.get(query.id)
        return item ? [item] : []
      },
      cancel: async (id: number) => {
        calls.cancel.push(id)
      },
      erase: async (query: { id: number }) => {
        calls.erase.push(query.id)
      },
      resume: async (id: number) => {
        calls.resume.push(id)
        const item = items.get(id)
        if (item) item.paused = false
      },
    },
  }
})

const burst = vi.hoisted(() => ({
  session: null as null | { captureId: string },
  admit: vi.fn(async (_id?: number, _ctx?: unknown, _eventAt?: number) => 'legacy' as const),
  beginClaim: vi.fn(),
  endClaim: vi.fn(),
  recover: vi.fn(async () => {}),
  setBridge: vi.fn(),
  holds: new Map<number, unknown>(),
  expiredHoldIds: [] as number[],
  holdSaveOk: true,
}))

vi.mock('../stores/config.svelte', () => ({
  configState: config,
  SMALL_FILE_THRESHOLD_BYTES: 100 * 1024,
  PENDING_DECISION_TTL_MS: 30_000,
  STORAGE_KEY_PENDING_PREFIX: 'pending_',
  MSG_TYPE_HLS: 'hls',
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: connection,
}))

vi.mock('../background/pendingDecisionStore', () => pending)

vi.mock('../background/burstFlow', () => ({
  admitConfirmedDownload: (id: number, ctx: unknown, eventAt: number) => burst.admit(id, ctx, eventAt),
  beginCaptureClaim: () => burst.beginClaim(),
  endCaptureClaim: () => burst.endClaim(),
  enqueueCaptureWork: async <T>(work: () => Promise<T>) => work(),
  recoverBurstState: () => burst.recover(),
  resolveCoalescerAdmission: async () => burst.session,
  setChromeBurstBridge: (next: unknown) => burst.setBridge(next),
}))

vi.mock('../background/burstHoldStore', () => ({
  saveBurstHold: vi.fn(async () => burst.holdSaveOk),
  removeBurstHold: vi.fn(async () => undefined),
  getAllBurstHolds: async () => new Map(burst.holds),
  listExpiredBurstHoldIds: async () => [...burst.expiredHoldIds],
}))

vi.mock('../background/wsClient', () => ({
  wsClient: ws,
}))

vi.mock('../background/cookieCapture', () => ({
  getCookiesForUrl: (url: string, storeId?: string) => cookies.getCookiesForUrl(url, storeId),
}))

vi.mock('../background/refererCapture', () => ({
  getDownloadPageUrl: async () => 'https://example.test/page',
}))

vi.mock('webext-bridge/background', () => ({
  sendMessage: async () => 'shown',
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    downloads: downloads.api,
    notifications: { create: async () => undefined },
    runtime: { getURL: (p: string) => p },
    tabs: { get: async () => undefined, query: async () => [] },
  },
}))

vi.mock('../lib/i18n', () => ({
  t: (key: string) => key,
}))

import { ChromeDownloadsApiInterceptor } from './ChromeDownloadsApiInterceptor'
import { resetBootReadyForTests, setBootReady } from '../background/bootState'

function downloadItem(partial: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 1,
    url: 'https://cdn.example.test/file.bin',
    filename: 'file.bin',
    fileSize: 500_000,
    totalBytes: 500_000,
    referrer: 'https://example.test/page',
    mime: 'application/octet-stream',
    state: 'in_progress',
    paused: false,
    incognito: false,
    ...partial,
  }
}

async function flush(): Promise<void> {
  for (let i = 0; i < 20; i++) await Promise.resolve()
}

function lastRequest(): DownloadHandoffMessage {
  const req = ws.sendDownloadRequest.mock.calls.at(-1)?.[0]
  expect(req).toBeDefined()
  return req as DownloadHandoffMessage
}

describe('ChromeDownloadsApiInterceptor HLS', () => {
  let interceptor: ChromeDownloadsApiInterceptor

  beforeEach(() => {
    resetBootReadyForTests()
    setBootReady(true)
    config.autoCapture = true
    config.registeredFileTypes = []
    connection.interceptionEnabled = true
    pending.map.clear()
    pending.expiredIds.length = 0
    burst.session = null
    burst.holdSaveOk = true
    burst.holds.clear()
    burst.expiredHoldIds.length = 0
    burst.admit.mockClear()
    burst.beginClaim.mockClear()
    burst.endClaim.mockClear()
    burst.recover.mockClear()
    burst.setBridge.mockClear()
    cookies.getCookiesForUrl.mockClear()
    cookies.getCookiesForUrl.mockResolvedValue([])
    ws.sendDownloadRequest.mockClear()
    ws.sendDownloadRequest.mockResolvedValue({
      type: 'download_ack',
      success: true,
      gid: 'ar_test',
    })
    downloads.reset()
    interceptor = new ChromeDownloadsApiInterceptor()
    interceptor.register()
  })

  it('claims a small .m3u8 manifest that the size gate would skip', async () => {
    const item = downloadItem({
      url: 'https://cdn.example.test/live.m3u8',
      filename: 'live.m3u8',
      fileSize: 2_048,
      totalBytes: 2_048,
      mime: 'application/octet-stream',
    })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(downloads.calls.pause).toEqual([1])
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(lastRequest().type).toBe('hls')
    expect(burst.admit).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(downloads.calls.cancel).toEqual([1])
    })
  })

  it('keeps HLS on the single-item path when a burst session is armed', async () => {
    burst.session = { captureId: 'cap-1' }
    const item = downloadItem({
      url: 'https://cdn.example.test/live.m3u8',
      filename: 'live.m3u8',
      fileSize: 500_000,
      totalBytes: 500_000,
    })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(downloads.calls.pause).toEqual([1])
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(lastRequest().type).toBe('hls')
    expect(burst.admit).not.toHaveBeenCalled()
    expect(burst.beginClaim).toHaveBeenCalledTimes(1)
    await vi.waitFor(() => {
      expect(burst.endClaim).toHaveBeenCalledTimes(1)
    })
  })

  it('resumes the browser download when the HLS ack fails', async () => {
    ws.sendDownloadRequest.mockResolvedValue({
      type: 'download_ack',
      success: false,
      gid: '',
      error: 'busy',
    })
    const item = downloadItem({
      url: 'https://cdn.example.test/live.m3u8',
      filename: 'live.m3u8',
      fileSize: 2_048,
      totalBytes: 2_048,
    })
    downloads.items.set(1, item)
    const suggest = vi.fn()
    downloads.created[0](item)
    downloads.filename[0](
      {
        id: 1,
        url: item.url,
        filename: item.filename,
        fileSize: item.fileSize,
        totalBytes: item.totalBytes,
        referrer: item.referrer,
        mime: item.mime,
      },
      suggest,
    )
    await vi.waitFor(() => {
      expect(suggest).toHaveBeenCalled()
      expect(downloads.calls.resume).toEqual([1])
    })
    expect(downloads.calls.cancel).toEqual([])
    expect(lastRequest().type).toBe('hls')
  })

  it('holds the filename suggestion for a claimed small HLS manifest', async () => {
    const item = downloadItem({
      url: 'https://cdn.example.test/live.m3u8',
      filename: 'live.m3u8',
      fileSize: 2_048,
      totalBytes: 2_048,
      mime: 'application/octet-stream',
    })
    downloads.items.set(1, item)
    downloads.created[0](item)
    // pausedIds is claimed synchronously before any await, so the filename
    // listener still sees the pending hold and keeps suggest alive.
    const suggest = vi.fn()
    const held = downloads.filename[0](
      {
        id: 1,
        url: item.url,
        filename: 'live.m3u8',
        fileSize: 2_048,
        totalBytes: 2_048,
        referrer: '',
        mime: 'application/octet-stream',
      },
      suggest,
    )
    expect(held).toBe(true)
    expect(suggest).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
  })

  it('claims an extensionless manifest on a playlist MIME alone', async () => {
    const item = downloadItem({
      url: 'https://cdn.example.test/live/edge',
      filename: 'edge',
      fileSize: 1_024,
      totalBytes: 1_024,
      mime: 'application/vnd.apple.mpegurl',
    })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(downloads.calls.pause).toEqual([1])
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(lastRequest().type).toBe('hls')
  })

  it('does not promote a .m3u playlist MIME to an HLS handoff', async () => {
    const item = downloadItem({
      url: 'https://cdn.example.test/list.m3u',
      filename: 'list.m3u',
      fileSize: 500_000,
      totalBytes: 500_000,
      mime: 'application/x-mpegurl',
    })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await flush()
    expect(downloads.calls.pause).toEqual([])
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
  })

  it('recovers an extensionless pending_ manifest as hls from the live item MIME', async () => {
    pending.map.set(3, {
      url: 'https://cdn.example.test/live/edge',
      filename: 'edge',
      fileSize: 1_024,
      startTime: Date.now(),
      status: 'pending',
    })
    downloads.items.set(
      3,
      downloadItem({
        id: 3,
        url: 'https://cdn.example.test/live/edge',
        filename: 'edge',
        mime: 'application/vnd.apple.mpegurl',
        paused: true,
        state: 'in_progress',
      }),
    )
    await interceptor.recoverPendingDecisions()
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    const req = lastRequest()
    expect(req.type).toBe('hls')
    expect(req.url).toBe('https://cdn.example.test/live/edge')
    expect(burst.recover).toHaveBeenCalledTimes(1)
  })

  it('holds the filename suggestion when only finalUrl reveals the manifest', async () => {
    const item = downloadItem({
      url: 'https://cdn.example.test/get',
      finalUrl: 'https://cdn.example.test/live.m3u8',
      filename: 'get',
      fileSize: 2_048,
      totalBytes: 2_048,
      mime: 'application/octet-stream',
    })
    downloads.items.set(1, item)
    downloads.created[0](item)
    // onCreated claims HLS via finalUrl; the filename listener must evaluate
    // the same finalUrl or it releases the manifest before pause lands.
    const suggest = vi.fn()
    const held = downloads.filename[0](
      {
        id: 1,
        url: item.url,
        finalUrl: item.finalUrl,
        filename: item.filename,
        fileSize: item.fileSize,
        totalBytes: item.totalBytes,
        referrer: '',
        mime: item.mime,
      },
      suggest,
    )
    expect(held).toBe(true)
    expect(suggest).not.toHaveBeenCalled()
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(lastRequest().type).toBe('hls')
  })

  it('still releases a small non-HLS item at the filename listener', async () => {
    const item = downloadItem({ fileSize: 2_048, totalBytes: 2_048 })
    downloads.items.set(1, item)
    downloads.created[0](item)
    const suggest = vi.fn()
    const held = downloads.filename[0](
      {
        id: 1,
        url: item.url,
        filename: item.filename,
        fileSize: item.fileSize,
        totalBytes: item.totalBytes,
        referrer: item.referrer,
        mime: item.mime,
      },
      suggest,
    )
    expect(held).toBeUndefined()
    expect(suggest).toHaveBeenCalledTimes(1)
  })

  it('keeps cookies off an incognito pending_ recovery', async () => {
    pending.map.set(3, {
      url: 'https://cdn.example.test/live.m3u8',
      filename: 'live.m3u8',
      fileSize: 2_048,
      startTime: Date.now(),
      status: 'pending',
    })
    downloads.items.set(
      3,
      downloadItem({
        id: 3,
        url: 'https://cdn.example.test/live.m3u8',
        filename: 'live.m3u8',
        incognito: true,
        paused: true,
        state: 'in_progress',
      }),
    )
    await interceptor.recoverPendingDecisions()
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    const req = lastRequest()
    expect(req.type).toBe('hls')
    expect(req.headers).toEqual([])
    expect(cookies.getCookiesForUrl).not.toHaveBeenCalled()
  })
})
