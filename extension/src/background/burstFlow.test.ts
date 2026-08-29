import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RpcRequestError } from './extractorRpc'

const sessionStore = vi.hoisted(() => ({
  session: null as null | Record<string, unknown>,
}))

const holds = vi.hoisted(() => {
  const map = new Map<number, Record<string, unknown>>()
  let window: Record<string, unknown> | null = null
  const ttlMs = 5 * 60 * 1000
  const expired = (hold: Record<string, unknown>, now = Date.now()) =>
    typeof hold.startTime === 'number' && now - hold.startTime > ttlMs
  return {
    map,
    window,
    expired,
    reset() {
      map.clear()
      window = null
    },
    getWindow() {
      return window
    },
    setWindow(next: Record<string, unknown> | null) {
      window = next
    },
  }
})

const bridge = vi.hoisted(() => ({
  paused: new Set<number>(),
  suggest: [] as number[],
  cancel: [] as number[],
  resume: [] as number[],
  legacy: [] as number[],
}))

const ws = vi.hoisted(() => ({
  sendDirectBatch: vi.fn(
    async (_payload?: Record<string, unknown>) => ({
      succeeded_item_ids: [] as string[],
      duplicate_item_ids: [] as string[],
      errors_by_item_id: {} as Record<string, string>,
    }),
  ),
  sendDirectBatchStatus: vi.fn(async () => ({ status: 'pending' })),
  getStatus: () => ({
    status: 'connected' as const,
    wsPort: 0,
    paired: true,
    lastError: '',
  }),
}))

const messages = vi.hoisted(() => ({
  sent: [] as Array<{ type: string; data: unknown }>,
}))

vi.mock('../stores/config.svelte', () => ({
  BURST_HOLD_TTL_MS: 5 * 60 * 1000,
  BURST_MAX_DEADLINE_MS: 15_000,
  BURST_QUIET_WINDOW_MS: 1_000,
  CAP_DOWNLOAD_BATCH: 'download.batch',
  EXTRACTOR_MAX_SESSION_ITEMS: 128,
  configState: { autoCapture: true },
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: {
    status: 'connected',
    paired: true,
    capabilities: ['download.batch'],
  },
}))

vi.mock('../lib/i18n', () => ({
  t: (key: string) => key,
}))

vi.mock('webext-bridge/background', () => ({
  onMessage() {},
  sendMessage: async (type: string, data: unknown) => {
    messages.sent.push({ type, data })
    if (type === 'dom:ping') {
      return {
        document_nonce: 'nonce',
        page_href: 'https://example.test/page',
        extractor_picker_open: false,
        dom_picker_open: false,
        burst_picker_open: false,
      }
    }
    if (type === 'burst:open') return { ok: true }
    return { ok: true }
  },
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    runtime: { getURL: (p: string) => p },
    notifications: { create: async () => undefined },
    cookies: { getAll: async () => [] },
    downloads: {
      search: async ({ id }: { id: number }) => [
        { id, state: 'in_progress', paused: true },
      ],
    },
    tabs: {
      query: async () => [{ id: 4, url: 'https://example.test/page', incognito: false }],
      onRemoved: { addListener() {} },
      onUpdated: { addListener() {} },
    },
  },
}))

vi.mock('./captureSession', () => ({
  getCaptureSession: async () => sessionStore.session,
  writeCaptureSession: async (s: Record<string, unknown>) => {
    if (sessionStore.session) return false
    sessionStore.session = s
    return true
  },
  disarmCaptureSession: async () => {
    sessionStore.session = null
  },
}))

vi.mock('./burstHoldStore', () => ({
  getBurstHold: async (id: number) => {
    const hold = holds.map.get(id)
    if (!hold || holds.expired(hold)) return null
    return hold
  },
  getAllBurstHolds: async () => {
    const live = new Map<number, Record<string, unknown>>()
    for (const [id, hold] of holds.map) {
      if (!holds.expired(hold)) live.set(id, hold)
    }
    return live
  },
  saveBurstHold: async (id: number, hold: Record<string, unknown>) => {
    holds.map.set(id, hold)
  },
  removeBurstHold: async (id: number) => {
    holds.map.delete(id)
  },
  getBurstWindow: async () => holds.getWindow(),
  getBurstWindowIgnoringTtl: async () => holds.getWindow(),
  listExpiredBurstHoldIds: async () => {
    const ids: number[] = []
    for (const [id, hold] of holds.map) {
      if (holds.expired(hold)) ids.push(id)
    }
    return ids
  },
  saveBurstWindow: async (win: Record<string, unknown>) => {
    holds.setWindow(win)
  },
  removeBurstWindow: async () => {
    holds.setWindow(null)
  },
}))

vi.mock('./pendingDecisionStore', () => ({
  savePendingDecision: async () => undefined,
  removePendingDecision: async () => undefined,
}))

vi.mock('./cookieCapture', () => ({
  resolveCookieStoreIdForTab: async () => undefined,
}))

vi.mock('./wsClient', () => ({
  wsClient: ws,
}))

vi.mock('./captureHostDown', () => ({
  setCaptureHostDownHook: () => undefined,
}))

vi.mock('./domConnectGeneration', () => ({
  currentDirectConnectGeneration: () => 1,
}))

import {
  admitConfirmedDownload,
  enqueueCaptureWork,
  handleBurstSubmit,
  isCoalescerEligible,
  recoverBurstState,
  referrerOriginMatches,
  resetBurstFlowForTests,
  resumeAllBurstHolds,
  setChromeBurstBridge,
} from './burstFlow'

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

function holdOf(id: number, url = `https://cdn.example.test/${id}.bin`) {
  return {
    url,
    filename: `${id}.bin`,
    fileSize: 10,
    startTime: Date.now(),
    captureId: SESSION.captureId,
    referrer: 'https://example.test/page',
    incognito: false,
  }
}

function pickerWindow(extra: Record<string, unknown> = {}) {
  return {
    captureId: SESSION.captureId,
    downloadIds: [1, 2],
    firstItemAt: 0,
    lastItemAt: 0,
    phase: 'picker',
    tabId: 4,
    pageHref: 'https://example.test/page',
    incognito: false,
    storeUnproven: true,
    catalog: [
      { index: 0, downloadId: 1 },
      { index: 1, downloadId: 2 },
    ],
    ...extra,
  }
}

describe('burstFlow', () => {
  beforeEach(() => {
    resetBurstFlowForTests()
    sessionStore.session = { ...SESSION }
    holds.reset()
    bridge.paused.clear()
    bridge.suggest = []
    bridge.cancel = []
    bridge.resume = []
    bridge.legacy = []
    messages.sent = []
    ws.sendDirectBatch.mockReset()
    ws.sendDirectBatchStatus.mockReset()
    ws.sendDirectBatch.mockResolvedValue({
      succeeded_item_ids: [],
      duplicate_item_ids: [],
      errors_by_item_id: {},
    })
    setChromeBurstBridge({
      invokeSuggest(id) {
        bridge.suggest.push(id)
      },
      async cancelAndErase(id) {
        bridge.cancel.push(id)
      },
      async resumeDownload(id) {
        bridge.resume.push(id)
      },
      cleanupMemory(id) {
        bridge.paused.delete(id)
      },
      restorePausedMemory(id) {
        bridge.paused.add(id)
      },
      async handlePausedDownload(id) {
        bridge.legacy.push(id)
      },
    })
  })

  afterEach(() => {
    resetBurstFlowForTests()
    vi.useRealTimers()
  })

  it('matches referrer by origin only', () => {
    expect(
      referrerOriginMatches('https://example.test/a', 'https://example.test/page#frag'),
    ).toBe(true)
    expect(referrerOriginMatches('', 'https://example.test/page')).toBe(false)
    expect(referrerOriginMatches('   ', 'https://example.test/page')).toBe(false)
    expect(
      referrerOriginMatches('https://other.test/x', 'https://example.test/page'),
    ).toBe(false)
  })

  it('isCoalescerEligible rejects empty, unparsable, and mismatched referrers', async () => {
    const base = { url: 'https://cdn.example.test/a.bin', incognito: false }
    expect(await isCoalescerEligible({ ...base, referrer: '' } as never)).toBe(false)
    expect(await isCoalescerEligible({ ...base, referrer: '   ' } as never)).toBe(false)
    expect(await isCoalescerEligible({ ...base, referrer: 'not-a-url' } as never)).toBe(false)
    expect(
      await isCoalescerEligible({ ...base, referrer: 'https://other.test/' } as never),
    ).toBe(false)
    expect(
      await isCoalescerEligible({ ...base, referrer: 'https://example.test/x' } as never),
    ).toBe(true)
  })

  it('isCoalescerEligible rejects incognito mismatch and picker-phase windows', async () => {
    expect(
      await isCoalescerEligible({
        url: 'https://cdn.example.test/a.bin',
        referrer: 'https://example.test/page',
        incognito: true,
      } as never),
    ).toBe(false)
    holds.setWindow(pickerWindow())
    expect(
      await isCoalescerEligible({
        url: 'https://cdn.example.test/a.bin',
        referrer: 'https://example.test/page',
        incognito: false,
      } as never),
    ).toBe(false)
  })

  it('does not merge without a session', async () => {
    sessionStore.session = null
    holds.map.set(1, holdOf(1))
    await expect(admitConfirmedDownload(1, { url: holdOf(1).url } as never)).resolves.toBe(
      'legacy',
    )
    expect(bridge.legacy).toEqual([1])
  })

  it('closes a single member to legacy after the quiet window', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    holds.map.set(1, holdOf(1))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never)
    expect(bridge.legacy).toEqual([])
    await vi.advanceTimersByTimeAsync(1000)
    expect(bridge.legacy).toEqual([1])
  })

  it('opens burst overlay for two members after quiet', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never)
    await admitConfirmedDownload(2, { url: holdOf(2).url } as never)
    await vi.advanceTimersByTimeAsync(1000)
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(true)
    expect(bridge.legacy).toEqual([])
  })

  it('serializes concurrent admits onto one window', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    await Promise.all([
      admitConfirmedDownload(1, { url: holdOf(1).url } as never),
      admitConfirmedDownload(2, { url: holdOf(2).url } as never),
    ])
    const ids = (holds.getWindow()?.downloadIds as number[] | undefined)?.slice().sort((a, b) => a - b)
    expect(ids).toEqual([1, 2])
  })

  it('admits from inside enqueueCaptureWork without deadlocking', async () => {
    holds.map.set(1, holdOf(1))
    await expect(
      enqueueCaptureWork(() => admitConfirmedDownload(1, { url: holdOf(1).url } as never)),
    ).resolves.toBe('coalesced')
  })

  it('resumes duplicates and errors, and erases only succeeded ids', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    holds.map.set(3, holdOf(3))
    holds.setWindow(
      pickerWindow({
        downloadIds: [1, 2, 3],
        catalog: [
          { index: 0, downloadId: 1 },
          { index: 1, downloadId: 2 },
          { index: 2, downloadId: 3 },
        ],
      }),
    )
    ws.sendDirectBatch.mockImplementation(async (payload?: Record<string, unknown>) => {
      const items = (payload?.items ?? []) as Array<{ client_item_id: string }>
      return {
        succeeded_item_ids: [items[0]?.client_item_id],
        duplicate_item_ids: [items[1]?.client_item_id],
        errors_by_item_id: { [items[2]?.client_item_id ?? '']: 'unavailable' },
      }
    })
    const reply = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [0, 1, 2],
    })
    expect(reply.accepted).toBe(true)
    expect(bridge.cancel).toEqual([1])
    expect(bridge.resume).toEqual([2, 3])
  })

  it('keeps holds and the original request id on busy', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    holds.setWindow(pickerWindow())
    ws.sendDirectBatch.mockRejectedValue(new RpcRequestError('busy', 'req'))
    const first = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [0, 1],
    })
    expect(first.error_code).toBe('busy')
    expect(holds.map.has(1)).toBe(true)
    expect(holds.getWindow()?.phase).toBe('submitting')
    const id = holds.getWindow()?.requestId
    expect(typeof id).toBe('string')
    const second = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [0, 1],
    })
    expect(second.error_code).toBe('busy')
    expect(holds.getWindow()?.requestId).toBe(id)
  })

  it('submits from frozen window credentials after the arm session is gone', async () => {
    sessionStore.session = null
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    holds.setWindow(pickerWindow())
    ws.sendDirectBatch.mockResolvedValue({
      succeeded_item_ids: ['aa', 'bb'],
      duplicate_item_ids: [],
      errors_by_item_id: {},
    })
    const reply = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [0, 1],
    })
    expect(reply.accepted).toBe(true)
    expect(ws.sendDirectBatch).toHaveBeenCalled()
  })

  it('maps compacted catalog indices instead of downloadIds slots', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(3, holdOf(3))
    holds.setWindow(
      pickerWindow({
        downloadIds: [1, 2, 3],
        catalog: [
          { index: 0, downloadId: 1 },
          { index: 1, downloadId: 3 },
        ],
      }),
    )
    ws.sendDirectBatch.mockImplementation(async (payload?: Record<string, unknown>) => {
      const items = (payload?.items ?? []) as Array<{ client_item_id: string }>
      return {
        succeeded_item_ids: items.map(row => row.client_item_id),
        duplicate_item_ids: [],
        errors_by_item_id: {},
      }
    })
    const reply = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [1],
    })
    expect(reply.accepted).toBe(true)
    expect(bridge.cancel).toEqual([3])
    expect(bridge.resume).toEqual([1])
  })

  it('resumes remaining holds when submit is invalid_request', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    holds.setWindow(pickerWindow())
    const reply = await handleBurstSubmit({
      capture_id: 'not-the-capture',
      indices: [0, 1],
    })
    expect(reply.error_code).toBe('invalid_request')
    expect(bridge.resume.sort()).toEqual([1, 2])
  })

  it('fail-closed resumes burst holds on recover without legacy handoff', async () => {
    holds.map.set(9, holdOf(9))
    holds.setWindow(
      pickerWindow({
        downloadIds: [9],
        catalog: [{ index: 0, downloadId: 9 }],
        pickerDeadline: Date.now() + 60_000,
      }),
    )
    await recoverBurstState()
    expect(bridge.resume).toEqual([9])
    expect(bridge.legacy).toEqual([])
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
  })

  it('resumeAllBurstHolds resumes remaining Chrome downloads', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    holds.setWindow({
      captureId: SESSION.captureId,
      downloadIds: [1, 2],
      firstItemAt: 0,
      lastItemAt: 0,
      phase: 'picker',
    })
    await resumeAllBurstHolds()
    expect(bridge.resume.sort()).toEqual([1, 2])
    expect(sessionStore.session).toBeNull()
  })

  it('reaps an expired still-paused hold by resuming then dropping the key', async () => {
    holds.map.set(4, holdOf(4))
    const expired = holds.map.get(4)
    if (expired) expired.startTime = Date.now() - 5 * 60 * 1000 - 1
    expect(holds.map.has(4)).toBe(true)
    await recoverBurstState()
    expect(bridge.resume).toEqual([4])
    expect(holds.map.has(4)).toBe(false)
    expect(bridge.legacy).toEqual([])
  })
})
