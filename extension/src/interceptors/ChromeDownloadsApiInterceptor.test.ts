import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DownloadResponse } from '../utils/messaging'

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
  let hangSave: Promise<void> | null = null
  return {
    map,
    expiredIds,
    hangSave(p: Promise<void> | null) {
      hangSave = p
    },
    async savePendingDecision(id: number, decision: Record<string, unknown>) {
      if (hangSave) await hangSave
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
  sendDownloadRequest: vi.fn(async (): Promise<DownloadResponse> => ({
    type: 'download_ack',
    success: true,
    gid: 'ar_test',
  })),
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
    suggest: [] as number[],
  }
  let confirmPaused = true
  let searchThrows = false
  return {
    created,
    changed,
    filename,
    items,
    calls,
    setConfirmPaused(v: boolean) {
      confirmPaused = v
    },
    setSearchThrows(v: boolean) {
      searchThrows = v
    },
    reset() {
      created.length = 0
      changed.length = 0
      filename.length = 0
      items.clear()
      confirmPaused = true
      searchThrows = false
      calls.pause = []
      calls.search = []
      calls.cancel = []
      calls.erase = []
      calls.resume = []
      calls.suggest = []
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
        if (item && confirmPaused) item.paused = true
      },
      search: async (query: { id: number }) => {
        calls.search.push(query.id)
        if (searchThrows) throw new Error('search failed')
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

vi.mock('../stores/config.svelte', () => ({
  configState: config,
  SMALL_FILE_THRESHOLD_BYTES: 100 * 1024,
  PENDING_DECISION_TTL_MS: 30_000,
  STORAGE_KEY_PENDING_PREFIX: 'pending_',
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: connection,
}))

vi.mock('../background/pendingDecisionStore', () => pending)

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
  getCookiesForUrl: async () => [],
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

function downloadItem(
  partial: Record<string, unknown> = {},
): Record<string, unknown> {
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

describe('ChromeDownloadsApiInterceptor live path B', () => {
  let interceptor: ChromeDownloadsApiInterceptor

  beforeEach(() => {
    resetBootReadyForTests()
    setBootReady(true)
    config.autoCapture = true
    config.registeredFileTypes = []
    connection.interceptionEnabled = true
    pending.map.clear()
    pending.expiredIds.length = 0
    pending.hangSave(null)
    burst.session = null
    burst.holdSaveOk = true
    burst.holds.clear()
    burst.expiredHoldIds.length = 0
    burst.admit.mockClear()
    burst.beginClaim.mockClear()
    burst.endClaim.mockClear()
    burst.recover.mockClear()
    burst.setBridge.mockClear()
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

  it('skips downloads started by another extension', async () => {
    const item = downloadItem({ byExtensionId: 'other@id' })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await flush()
    expect(downloads.calls.pause).toEqual([])
    expect(pending.map.size).toBe(0)
  })

  it('skips known small files', async () => {
    const item = downloadItem({ fileSize: 1024, totalBytes: 1024 })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await flush()
    expect(downloads.calls.pause).toEqual([])
    expect(pending.map.size).toBe(0)
  })

  it('still claims unknown size (0 bytes)', async () => {
    const item = downloadItem({ fileSize: 0, totalBytes: 0 })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(downloads.calls.pause).toEqual([1])
      expect(ws.sendDownloadRequest).toHaveBeenCalled()
    })
  })

  it('claims pausedIds before awaiting savePendingDecision', async () => {
    let release!: () => void
    pending.hangSave(
      new Promise<void>(resolve => {
        release = resolve
      }),
    )
    const item = downloadItem()
    downloads.items.set(1, item)
    downloads.created[0](item)
    await flush()
    expect(pending.map.has(1)).toBe(false)
    expect(downloads.calls.pause).toEqual([])
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
    expect(held).toBe(true)
    expect(suggest).not.toHaveBeenCalled()
    release()
    await vi.waitFor(() => {
      expect(downloads.calls.pause).toEqual([1])
    })
  })

  it('pauses then search-confirms before a legacy handoff', async () => {
    const item = downloadItem()
    downloads.items.set(1, item)
    downloads.created[0](item)
    await flush()
    expect(downloads.calls.pause).toEqual([1])
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(downloads.calls.search).toEqual([1])
    expect(burst.admit).not.toHaveBeenCalled()
  })

  it('invokes suggest then cancel+erase on a successful takeover', async () => {
    const item = downloadItem()
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
      expect(downloads.calls.cancel).toEqual([1])
      expect(downloads.calls.erase).toEqual([1])
    })
    expect(downloads.calls.resume).toEqual([])
  })

  it('invokes suggest then resume when the host refuses the handoff', async () => {
    ws.sendDownloadRequest.mockResolvedValue({
      type: 'download_ack',
      success: false,
      gid: '',
      error: 'busy',
    })
    const item = downloadItem()
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
  })

  it('releases claim when pause is not confirmed', async () => {
    downloads.setConfirmPaused(false)
    const item = downloadItem({ paused: false })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(downloads.calls.pause).toEqual([1])
      expect(downloads.calls.search.length).toBeGreaterThan(0)
    })
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(burst.admit).not.toHaveBeenCalled()
    expect(burst.beginClaim).toHaveBeenCalledTimes(1)
    await vi.waitFor(() => {
      expect(burst.endClaim).toHaveBeenCalledTimes(1)
    })
  })

  it('takes immediate legacy when the implicit session is unavailable', async () => {
    const item = downloadItem({ referrer: '  ' })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(burst.admit).not.toHaveBeenCalled()
    expect(burst.beginClaim).toHaveBeenCalledTimes(1)
    expect(burst.endClaim).toHaveBeenCalledTimes(1)
  })

  it('takes immediate legacy on incognito mismatch', async () => {
    // Predicate coverage lives in burstFlow; this case stubs refused admission → path B.
    const item = downloadItem({ incognito: true })
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(burst.admit).not.toHaveBeenCalled()
    expect(burst.beginClaim).toHaveBeenCalledTimes(1)
    expect(burst.endClaim).toHaveBeenCalledTimes(1)
  })

  it('admits implicit-session items instead of sending legacy download', async () => {
    burst.session = { captureId: 'cap-1' }
    const item = downloadItem()
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(burst.admit).toHaveBeenCalledTimes(1)
    })
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(burst.beginClaim).toHaveBeenCalledTimes(1)
    expect(burst.endClaim).toHaveBeenCalledTimes(1)
    expect(burst.admit.mock.calls[0]?.[2]).toEqual(expect.any(Number))
  })

  it('resumes a paused item when search cannot confirm the pause', async () => {
    downloads.setSearchThrows(true)
    const item = downloadItem()
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(downloads.calls.pause).toEqual([1])
      expect(downloads.calls.resume).toEqual([1])
    })
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(burst.admit).not.toHaveBeenCalled()
    expect(burst.beginClaim).toHaveBeenCalledTimes(1)
    expect(burst.endClaim).toHaveBeenCalledTimes(1)
  })

  it('recovers pending_ via legacy send then calls recoverBurstState for holds', async () => {
    pending.map.set(3, {
      url: 'https://cdn.example.test/file.bin',
      filename: 'file.bin',
      fileSize: 500_000,
      startTime: Date.now(),
      status: 'pending',
    })
    downloads.items.set(3, downloadItem({ id: 3, paused: true, state: 'in_progress' }))
    await interceptor.recoverPendingDecisions()
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(burst.recover).toHaveBeenCalledTimes(1)
  })

  it('skips pending_ ids that still have a burst hold', async () => {
    burst.holds.set(3, { url: 'https://cdn.example.test/file.bin' })
    pending.map.set(3, {
      url: 'https://cdn.example.test/file.bin',
      filename: 'file.bin',
      fileSize: 500_000,
      startTime: Date.now(),
      status: 'pending',
    })
    downloads.items.set(3, downloadItem({ id: 3, paused: true, state: 'in_progress' }))
    await interceptor.recoverPendingDecisions()
    await flush()
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(burst.recover).toHaveBeenCalledTimes(1)
  })

  it('releases the claim when burst hold persist fails', async () => {
    burst.session = { captureId: 'cap-1' }
    burst.holdSaveOk = false
    const item = downloadItem()
    downloads.items.set(1, item)
    downloads.created[0](item)
    await flush()
    expect(downloads.calls.pause).toEqual([])
    expect(burst.admit).not.toHaveBeenCalled()
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(burst.beginClaim).toHaveBeenCalledTimes(1)
    expect(burst.endClaim).toHaveBeenCalledTimes(1)
  })

  it('falls back to legacy when the implicit session cannot be minted', async () => {
    const item = downloadItem()
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(downloads.calls.pause).toEqual([1])
    expect(burst.admit).not.toHaveBeenCalled()
    expect(burst.beginClaim).toHaveBeenCalledTimes(1)
    expect(burst.endClaim).toHaveBeenCalledTimes(1)
  })

  it('drops expired pending_ without resuming when a live hold owns the id', async () => {
    burst.holds.set(3, { url: 'https://cdn.example.test/file.bin' })
    pending.expiredIds.push(3)
    pending.map.set(3, {
      url: 'https://cdn.example.test/file.bin',
      filename: 'file.bin',
      fileSize: 500_000,
      startTime: Date.now(),
      status: 'pending',
    })
    downloads.items.set(3, downloadItem({ id: 3, paused: true, state: 'in_progress' }))
    await interceptor.recoverPendingDecisions()
    await flush()
    expect(downloads.calls.resume).toEqual([])
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(pending.map.has(3)).toBe(false)
    expect(burst.recover).toHaveBeenCalledTimes(1)
  })

  it('drops expired pending_ without resuming when an expired hold still owns the id', async () => {
    burst.expiredHoldIds.push(3)
    pending.expiredIds.push(3)
    pending.map.set(3, {
      url: 'https://cdn.example.test/file.bin',
      filename: 'file.bin',
      fileSize: 500_000,
      startTime: Date.now(),
      status: 'pending',
    })
    downloads.items.set(3, downloadItem({ id: 3, paused: true, state: 'in_progress' }))
    await interceptor.recoverPendingDecisions()
    await flush()
    expect(downloads.calls.resume).toEqual([])
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(pending.map.has(3)).toBe(false)
  })

  it('resumes expired pending_ that has no burst hold', async () => {
    pending.expiredIds.push(3)
    pending.map.set(3, {
      url: 'https://cdn.example.test/file.bin',
      filename: 'file.bin',
      fileSize: 500_000,
      startTime: Date.now(),
      status: 'pending',
    })
    downloads.items.set(3, downloadItem({ id: 3, paused: true, state: 'in_progress' }))
    await interceptor.recoverPendingDecisions()
    await flush()
    expect(downloads.calls.resume).toEqual([3])
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(pending.map.has(3)).toBe(false)
  })

  it('does not drop stores on complete while path-B send is in flight', async () => {
    let finish!: (value: DownloadResponse) => void
    ws.sendDownloadRequest.mockImplementation(
      () =>
        new Promise<DownloadResponse>(resolve => {
          finish = resolve
        }),
    )
    const item = downloadItem()
    downloads.items.set(1, item)
    downloads.created[0](item)
    await vi.waitFor(() => {
      expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    })
    expect(pending.map.has(1)).toBe(true)
    downloads.changed[0]({ id: 1, state: { current: 'complete' } })
    await flush()
    expect(pending.map.has(1)).toBe(true)
    expect(downloads.calls.resume).toEqual([])
    finish({ type: 'download_ack', success: true, gid: 'ar_test' })
    await vi.waitFor(() => {
      expect(downloads.calls.cancel).toEqual([1])
    })
  })
})
