import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RpcRequestError } from './extractorRpc'

const sessionStore = vi.hoisted(() => ({
  session: null as null | Record<string, unknown>,
  readGate: null as null | { wait: Promise<void>; started: () => void },
}))

const firefoxMode = vi.hoisted(() => ({ on: false }))

const searchFail = vi.hoisted(() => ({
  ids: new Set<number>(),
}))

const holds = vi.hoisted(() => {
  const map = new Map<number, Record<string, unknown>>()
  let window: Record<string, unknown> | null = null
  let saveOk = true
  const ttlMs = 5 * 60 * 1000
  const expired = (hold: Record<string, unknown>, now = Date.now()) =>
    typeof hold.startTime === 'number' && now - hold.startTime > ttlMs
  return {
    map,
    window,
    expired,
    get saveOk() {
      return saveOk
    },
    set saveOk(v: boolean) {
      saveOk = v
    },
    reset() {
      map.clear()
      window = null
      saveOk = true
      this.windowLive = true
      this.allReadGate = null
    },
    windowLive: true,
    allReadGate: null as null | { wait: Promise<void>; started: () => void },
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

const fx = vi.hoisted(() => ({
  legacy: [] as Array<{ tabId: number; url: string }>,
  cleaned: [] as number[],
  skipTabIds: [] as number[],
}))

const ws = vi.hoisted(() => ({
  sendDirectBatch: vi.fn(
    async (_payload?: Record<string, unknown>) => ({
      succeeded_item_ids: [] as string[],
      duplicate_item_ids: [] as string[],
      errors_by_item_id: {} as Record<string, string>,
    }),
  ),
  sendDirectBatchStatus: vi.fn(
    async () =>
      ({ status: 'pending' }) as {
        status: string
        succeeded_item_ids?: string[]
        duplicate_item_ids?: string[]
        errors_by_item_id?: Record<string, string>
      },
  ),
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

const pingFlags = vi.hoisted(() => ({
  burst: false,
  extractor: false,
  dom: false,
  nonce: 'nonce',
  href: 'https://example.test/page',
}))

const notices = vi.hoisted(() => ({
  messages: [] as string[],
}))

const presentationTab = vi.hoisted(() => ({
  candidate: {
    id: 4,
    url: 'https://example.test/page',
    incognito: false,
  } as null | { id: number; url: string; incognito: boolean; discarded?: boolean },
  calls: [] as unknown[],
}))

vi.mock('../stores/config.svelte', () => ({
  BURST_HOLD_TTL_MS: 5 * 60 * 1000,
  BURST_MAX_DEADLINE_MS: 5_000,
  BURST_QUIET_SOLO_MS: 80,
  BURST_QUIET_GROUP_MS: 500,
  BURST_CLAIM_RETRY_MS: 25,
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
        document_nonce: pingFlags.nonce,
        page_href: pingFlags.href,
        extractor_picker_open: pingFlags.extractor,
        dom_picker_open: pingFlags.dom,
        burst_picker_open: pingFlags.burst,
      }
    }
    if (type === 'burst:open') return { ok: true }
    return { ok: true }
  },
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    runtime: { getURL: (p: string) => p },
    notifications: {
      create: async (opts: { message?: string }) => {
        notices.messages.push(String(opts.message ?? ''))
      },
    },
    cookies: { getAll: async () => [] },
    downloads: {
      search: async ({ id }: { id: number }) => {
        if (searchFail.ids.has(id)) throw new Error('search failed')
        return [{ id, state: 'in_progress', paused: true }]
      },
    },
    tabs: {
      query: async () => [{ id: 4, url: 'https://example.test/page', incognito: false }],
      get: async (id: number) => ({ id, url: 'https://example.test/page', incognito: false }),
      onRemoved: { addListener() {} },
      onUpdated: { addListener() {} },
    },
  },
}))

vi.mock('./captureSession', () => ({
  getCaptureSession: async () => {
    const gate = sessionStore.readGate
    if (gate) {
      sessionStore.readGate = null
      gate.started()
      await gate.wait
    }
    return sessionStore.session
  },
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
  getBurstHoldIgnoringTtl: async (id: number) => holds.map.get(id) ?? null,
  getAllBurstHolds: async () => {
    const gate = holds.allReadGate
    if (gate) {
      holds.allReadGate = null
      gate.started()
      await gate.wait
    }
    const live = new Map<number, Record<string, unknown>>()
    for (const [id, hold] of holds.map) {
      if (!holds.expired(hold)) live.set(id, hold)
    }
    return live
  },
  saveBurstHold: async (id: number, hold: Record<string, unknown>) => {
    if (!holds.saveOk) return false
    holds.map.set(id, hold)
    return true
  },
  removeBurstHold: async (id: number) => {
    holds.map.delete(id)
  },
  getBurstWindow: async () => (holds.windowLive ? holds.getWindow() : null),
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
    return true
  },
  removeBurstWindow: async () => {
    holds.setWindow(null)
  },
}))

const pendingWrite = vi.hoisted(() => ({ ok: true }))

vi.mock('./pendingDecisionStore', () => ({
  savePendingDecision: async () => pendingWrite.ok,
  removePendingDecision: async () => undefined,
}))

vi.mock('./cookieCapture', () => ({
  resolveCookieStoreIdForTab: async () => undefined,
}))

vi.mock('./captureTabResolver', () => ({
  originOf: (href: string) => {
    try {
      const url = new URL(href.trim())
      return url.protocol === 'http:' || url.protocol === 'https:' ? url.origin : null
    } catch {
      return null
    }
  },
  resolvePresentationTab: async (opts: {
    referrerOrigin: string
    incognito: boolean
    cookieStoreId?: string
  }) => {
    presentationTab.calls.push(opts)
    const candidate = presentationTab.candidate
    if (!candidate || candidate.incognito !== opts.incognito) return null
    return new URL(candidate.url).origin === opts.referrerOrigin ? candidate : null
  },
}))

vi.mock('./wsClient', () => ({
  wsClient: ws,
}))

vi.mock('./captureHostDown', () => ({
  setCaptureHostDownHook: () => undefined,
  setCaptureReconnectHook: () => undefined,
}))

vi.mock('../utils/extensionInfo', () => ({
  isFirefox: () => firefoxMode.on,
  isChrome: () => !firefoxMode.on,
  getExtensionBrowserTarget: () => (firefoxMode.on ? 'firefox' : 'chrome'),
}))

vi.mock('./domConnectGeneration', () => ({
  currentDirectConnectGeneration: () => 1,
}))

import {
  admitConfirmedDownload,
  beginCaptureClaim,
  claimFirefoxLegacyHandoff,
  endCaptureClaim,
  enqueueCaptureWork,
  handleBurstAlive,
  handleBurstCancel,
  handleBurstSubmit,
  handleCaptureReconnect,
  isCoalescerEligible,
  pendingCaptureClaims,
  recoverBurstState,
  referrerOriginMatches,
  resolveCoalescerAdmission,
  resetBurstFlowForTests,
  resumeAllBurstHolds,
  scheduleFirefoxLegacyHandoff,
  setChromeBurstBridge,
  setFirefoxBurstBridge,
} from './burstFlow'
import { configState } from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'

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

function firefoxHold(id: number) {
  return {
    ...holdOf(id),
    engine: 'firefox' as const,
    tabId: 8 + id,
    mainFrame: true,
  }
}

function installFirefoxBridge() {
  setFirefoxBurstBridge({
    async sendLegacy(ctx) {
      fx.legacy.push({ tabId: ctx.tabId, url: ctx.url })
    },
    async cleanupBlankTab(tabId, urls) {
      fx.cleaned.push(tabId)
      if (typeof urls?.skipTabId === 'number') fx.skipTabIds.push(urls.skipTabId)
    },
  })
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

function coalescingWindow(extra: Record<string, unknown> = {}) {
  return {
    captureId: SESSION.captureId,
    downloadIds: [1],
    firstItemAt: Date.now(),
    lastItemAt: Date.now(),
    phase: 'coalescing',
    ...extra,
  }
}

async function flushMicrotasks(): Promise<void> {
  for (let i = 0; i < 12; i++) await Promise.resolve()
}

describe('burstFlow', () => {
  beforeEach(() => {
    resetBurstFlowForTests()
    firefoxMode.on = false
    fx.legacy = []
    fx.cleaned = []
    fx.skipTabIds = []
    sessionStore.session = { ...SESSION }
    sessionStore.readGate = null
    holds.reset()
    bridge.paused.clear()
    bridge.suggest = []
    bridge.cancel = []
    bridge.resume = []
    bridge.legacy = []
    messages.sent = []
    notices.messages = []
    pingFlags.burst = false
    pingFlags.extractor = false
    pingFlags.dom = false
    pingFlags.nonce = 'nonce'
    pingFlags.href = 'https://example.test/page'
    searchFail.ids.clear()
    pendingWrite.ok = true
    presentationTab.candidate = {
      id: 4,
      url: 'https://example.test/page',
      incognito: false,
    }
    presentationTab.calls = []
    configState.autoCapture = true
    connectionState.status = 'connected'
    connectionState.paired = true
    connectionState.capabilities = ['download.batch']
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
    holds.setWindow(coalescingWindow())
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
    await expect(
      admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now()),
    ).resolves.toBe(
      'legacy',
    )
    expect(bridge.legacy).toEqual([1])
  })

  it('mints an implicit session and closes a single member after the solo quiet window', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    sessionStore.session = null
    holds.map.set(1, holdOf(1))
    const ctx = {
      url: holdOf(1).url,
      referrer: 'https://example.test/page',
      incognito: false,
    }
    const session = await resolveCoalescerAdmission(ctx as never)
    expect(session).toMatchObject({
      tabId: 4,
      pageHref: 'https://example.test/page',
      storeUnproven: true,
    })
    expect(sessionStore.session).toEqual(session)
    await admitConfirmedDownload(1, ctx as never, Date.now())
    expect(bridge.legacy).toEqual([])
    await vi.advanceTimersByTimeAsync(79)
    expect(bridge.legacy).toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(bridge.legacy).toEqual([1])
    expect(sessionStore.session).toBeNull()
    expect(holds.getWindow()).toBeNull()
  })

  it('passes the Firefox event store into presentation pick and keeps the mint store from the event', async () => {
    firefoxMode.on = true
    sessionStore.session = null
    const ctx = {
      url: holdOf(1).url,
      referrer: 'https://example.test/page',
      incognito: false,
      cookieStoreId: 'firefox-container-work',
    }
    const session = await resolveCoalescerAdmission(ctx as never)
    expect(presentationTab.calls).toEqual([
      expect.objectContaining({
        referrer: 'https://example.test/page',
        referrerOrigin: 'https://example.test',
        incognito: false,
        cookieStoreId: 'firefox-container-work',
      }),
    ])
    expect(session).toMatchObject({
      tabId: 4,
      cookieStoreId: 'firefox-container-work',
      storeUnproven: false,
    })
  })

  it('uses the browser event timestamp for the solo deadline', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(500)
    holds.map.set(1, holdOf(1))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never, 450)
    expect(holds.getWindow()).toMatchObject({ firstItemAt: 450, lastItemAt: 450 })
    await vi.advanceTimersByTimeAsync(29)
    expect(bridge.legacy).toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    expect(bridge.legacy).toEqual([1])
  })

  it('refuses implicit admission when the capability, connection, or intercept setting is unavailable', async () => {
    sessionStore.session = null
    configState.autoCapture = false
    await expect(
      resolveCoalescerAdmission({ referrer: 'https://example.test/page' } as never),
    ).resolves.toBeNull()
    expect(sessionStore.session).toBeNull()
    expect(holds.getWindow()).toBeNull()

    configState.autoCapture = true
    connectionState.status = 'disconnected'
    await expect(
      resolveCoalescerAdmission({ referrer: 'https://example.test/page' } as never),
    ).resolves.toBeNull()

    connectionState.status = 'connected'
    connectionState.paired = false
    await expect(
      resolveCoalescerAdmission({ referrer: 'https://example.test/page' } as never),
    ).resolves.toBeNull()

    connectionState.paired = true
    connectionState.capabilities = []
    await expect(
      resolveCoalescerAdmission({ referrer: 'https://example.test/page' } as never),
    ).resolves.toBeNull()
    expect(sessionStore.session).toBeNull()
    expect(holds.getWindow()).toBeNull()
  })

  it.each([
    ['empty', '', 0],
    ['unparsable', 'not-a-url', 0],
    ['cross-origin', 'https://other.test/page', 1],
  ])(
    'does not mint or schedule a window for a %s referrer',
    async (_kind, referrer, expectedResolverCalls) => {
      vi.useFakeTimers()
      sessionStore.session = null
      await expect(resolveCoalescerAdmission({ referrer } as never)).resolves.toBeNull()
      expect(sessionStore.session).toBeNull()
      expect(holds.getWindow()).toBeNull()
      expect(presentationTab.calls).toHaveLength(expectedResolverCalls)
      expect(vi.getTimerCount()).toBe(0)
    },
  )

  it('refuses a second origin without disturbing the current coalescing window', async () => {
    holds.setWindow(coalescingWindow())
    await expect(
      resolveCoalescerAdmission({ referrer: 'https://other.test/page' } as never),
    ).resolves.toBeNull()
    expect(sessionStore.session).toEqual(SESSION)
    expect(holds.getWindow()).toMatchObject({ captureId: SESSION.captureId, phase: 'coalescing' })
  })

  it('resumes a held download when migrate cannot persist pending_', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    pendingWrite.ok = false
    holds.map.set(1, holdOf(1))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now())
    await vi.advanceTimersByTimeAsync(80)
    expect(bridge.resume).toEqual([1])
    expect(bridge.legacy).toEqual([])
    expect(holds.map.has(1)).toBe(false)
  })

  it('migrates the window to legacy when snapshot TTL refresh fails', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now())
    await admitConfirmedDownload(2, { url: holdOf(2).url } as never, Date.now())
    holds.saveOk = false
    await vi.advanceTimersByTimeAsync(500)
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
    expect(bridge.legacy.slice().sort((a, b) => a - b)).toEqual([1, 2])
  })

  it('opens burst overlay for two members after quiet', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now())
    await admitConfirmedDownload(2, { url: holdOf(2).url } as never, Date.now())
    await vi.advanceTimersByTimeAsync(80)
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
    await vi.advanceTimersByTimeAsync(420)
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(true)
    expect(bridge.legacy).toEqual([])
  })

  it('defers a solo close until queued capture claims drain', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    holds.map.set(1, holdOf(1))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now())
    beginCaptureClaim()
    expect(pendingCaptureClaims()).toBe(1)
    await vi.advanceTimersByTimeAsync(80)
    expect(bridge.legacy).toEqual([])
    endCaptureClaim()
    expect(pendingCaptureClaims()).toBe(0)
    await vi.advanceTimersByTimeAsync(25)
    expect(bridge.legacy).toEqual([1])
  })

  it('defers a force-legacy close when a claim arrives during session lookup', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    holds.map.set(1, holdOf(1))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now())

    let releaseRead!: () => void
    let signalRead!: () => void
    const readStarted = new Promise<void>(resolve => {
      signalRead = resolve
    })
    const readGate = new Promise<void>(resolve => {
      releaseRead = resolve
    })
    sessionStore.readGate = { wait: readGate, started: signalRead }

    vi.advanceTimersByTime(80)
    await readStarted
    beginCaptureClaim()
    const queuedClaim = enqueueCaptureWork(async () => undefined)
    sessionStore.session = null
    releaseRead()
    await flushMicrotasks()
    await queuedClaim

    expect(pendingCaptureClaims()).toBe(1)
    expect(bridge.legacy).toEqual([])
    endCaptureClaim()
    await vi.advanceTimersByTimeAsync(25)
    expect(bridge.legacy).toEqual([1])
  })

  it('defers a legacy close when a claim arrives during hold lookup', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    holds.map.set(1, holdOf(1))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now())

    let releaseRead!: () => void
    let signalRead!: () => void
    const readStarted = new Promise<void>(resolve => {
      signalRead = resolve
    })
    const readGate = new Promise<void>(resolve => {
      releaseRead = resolve
    })
    holds.allReadGate = { wait: readGate, started: signalRead }

    vi.advanceTimersByTime(80)
    await readStarted
    beginCaptureClaim()
    const queuedClaim = enqueueCaptureWork(async () => undefined)
    releaseRead()
    await flushMicrotasks()
    await queuedClaim

    expect(pendingCaptureClaims()).toBe(1)
    expect(bridge.legacy).toEqual([])
    endCaptureClaim()
    await vi.advanceTimersByTimeAsync(25)
    expect(bridge.legacy).toEqual([1])
  })

  it('abandons a burst when the snapshot ping has navigated to another origin', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(0)
    pingFlags.href = 'https://other.test/page'
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    await admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now())
    await admitConfirmedDownload(2, { url: holdOf(2).url } as never, Date.now())
    await vi.advanceTimersByTimeAsync(500)
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
    expect(bridge.legacy.slice().sort((a, b) => a - b)).toEqual([1, 2])
    expect(sessionStore.session).toBeNull()
  })

  it('serializes concurrent admits onto one window', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    await Promise.all([
      admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now()),
      admitConfirmedDownload(2, { url: holdOf(2).url } as never, Date.now()),
    ])
    const ids = (holds.getWindow()?.downloadIds as number[] | undefined)?.slice().sort((a, b) => a - b)
    expect(ids).toEqual([1, 2])
  })

  it('admits from inside enqueueCaptureWork without deadlocking', async () => {
    holds.map.set(1, holdOf(1))
    await expect(
      enqueueCaptureWork(() =>
        admitConfirmedDownload(1, { url: holdOf(1).url } as never, Date.now()),
      ),
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

  it('ignores a mismatched capture_id without resuming the live window', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    holds.setWindow(pickerWindow())
    const reply = await handleBurstSubmit({
      capture_id: 'not-the-capture',
      indices: [0, 1],
    })
    expect(reply.error_code).toBe('invalid_request')
    expect(bridge.resume).toEqual([])
    expect(holds.getWindow()).not.toBeNull()
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

  it('resumes a hold when downloads.search throws', async () => {
    holds.map.set(8, holdOf(8))
    searchFail.ids.add(8)
    await recoverBurstState()
    expect(bridge.resume).toEqual([8])
    expect(holds.map.has(8)).toBe(false)
    expect(bridge.legacy).toEqual([])
  })

  it('refuses send when a proven cookie store cannot be re-proved', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    holds.setWindow(
      pickerWindow({
        storeUnproven: false,
        cookieStoreId: 'store-a',
      }),
    )
    const reply = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [0, 1],
    })
    expect(reply).toEqual({ accepted: false, error_code: 'store_unproven' })
    expect(ws.sendDirectBatch).not.toHaveBeenCalled()
    expect(bridge.resume).toEqual([])
  })

  it('resumes remaining Chrome holds on not_found without minting a new id', async () => {
    holds.map.set(1, holdOf(1))
    holds.map.set(2, holdOf(2))
    holds.setWindow(pickerWindow())
    ws.sendDirectBatch.mockRejectedValue(new RpcRequestError('timeout', 'req-keep'))
    ws.sendDirectBatchStatus.mockResolvedValue({ status: 'not_found' })
    const reply = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [0, 1],
    })
    expect(reply.error_code).toBe('not_found')
    expect(bridge.resume.sort()).toEqual([1, 2])
    expect(bridge.cancel).toEqual([])
    expect(ws.sendDirectBatchStatus).toHaveBeenCalled()
  })

  it('isCoalescerEligible rejects a Firefox store mismatch and accepts a match', async () => {
    firefoxMode.on = true
    sessionStore.session = { ...SESSION, storeUnproven: false, cookieStoreId: 'store-a' }
    holds.setWindow(coalescingWindow())
    const base = {
      url: 'https://cdn.example.test/a.bin',
      referrer: 'https://example.test/page',
      incognito: false,
      frameId: 0,
    }
    expect(await isCoalescerEligible({ ...base, cookieStoreId: 'other' } as never)).toBe(false)
    expect(await isCoalescerEligible({ ...base, cookieStoreId: 'store-a' } as never)).toBe(true)
  })

  it('isCoalescerEligible on Chrome ignores store even when frameId is set', async () => {
    sessionStore.session = { ...SESSION, storeUnproven: false, cookieStoreId: 'store-a' }
    holds.setWindow(coalescingWindow())
    expect(
      await isCoalescerEligible({
        url: 'https://cdn.example.test/a.bin',
        referrer: 'https://example.test/page',
        incognito: false,
        frameId: 0,
        cookieStoreId: 'other',
      } as never),
    ).toBe(true)
  })

  it('opens burst overlay for two Firefox admits after quiet', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    vi.useFakeTimers()
    vi.setSystemTime(0)
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    await admitConfirmedDownload(1, { url: holdOf(1).url, tabId: 9 } as never, Date.now())
    await admitConfirmedDownload(2, { url: holdOf(2).url, tabId: 10 } as never, Date.now())
    expect(holds.getWindow()?.tabId).toBe(4)
    await vi.advanceTimersByTimeAsync(500)
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(true)
    expect(bridge.legacy).toEqual([])
    expect(bridge.resume).toEqual([])
  })

  it('firefox recover continues a coalescing window without silent drop', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow({
      captureId: SESSION.captureId,
      downloadIds: [1, 2],
      firstItemAt: Date.now(),
      lastItemAt: Date.now(),
      phase: 'coalescing',
    })
    await recoverBurstState()
    expect(holds.map.has(1)).toBe(true)
    expect(holds.map.has(2)).toBe(true)
    expect(bridge.resume).toEqual([])
  })

  it('firefox recover legacy-sends an orphan hold when the session is gone', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    sessionStore.session = null
    holds.map.set(5, firefoxHold(5))
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.some(row => row.url.includes('/5.bin'))).toBe(true)
    })
    expect(bridge.resume).toEqual([])
  })

  it('firefox recover legacy-sends a matching orphan when the window is missing', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(5, firefoxHold(5))
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.some(row => row.url.includes('/5.bin'))).toBe(true)
    })
    expect(holds.getWindow()).toBeNull()
    expect(presentationTab.calls).toEqual([])
  })

  it('does not call Chrome resume on Firefox not_found', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(pickerWindow())
    ws.sendDirectBatch.mockRejectedValue(new RpcRequestError('timeout', 'req-keep'))
    ws.sendDirectBatchStatus.mockResolvedValue({ status: 'not_found' })
    const reply = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [0, 1],
    })
    expect(reply.error_code).toBe('not_found')
    expect(bridge.resume).toEqual([])
    expect(bridge.cancel).toEqual([])
  })

  it('resumeAllBurstHolds on Firefox does not chrome-resume cancelled files', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(pickerWindow())
    await resumeAllBurstHolds()
    expect(bridge.resume).toEqual([])
    expect(fx.cleaned.slice().sort((a, b) => a - b)).toEqual([9, 10])
    expect(sessionStore.session).toBeNull()
  })

  it('Firefox cancel does not chrome-resume', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.setWindow(pickerWindow({ downloadIds: [1], catalog: [{ index: 0, downloadId: 1 }] }))
    await handleBurstCancel({ capture_id: SESSION.captureId as string })
    expect(bridge.resume).toEqual([])
    expect(fx.legacy).toEqual([])
    expect(fx.cleaned).toEqual([9])
  })

  it('firefox recover reopens picker when the overlay is already showing', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    pingFlags.burst = true
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(
      pickerWindow({
        documentNonce: 'nonce',
        pageHref: 'https://example.test/page',
        pickerDeadline: Date.now() + 60_000,
      }),
    )
    await recoverBurstState()
    expect(messages.sent.filter(m => m.type === 'burst:open')).toHaveLength(1)
    expect(fx.legacy).toEqual([])
    expect(holds.getWindow()?.phase).toBe('picker')
  })

  it('firefox recover legacy-sends when picker ping nonce does not match', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    pingFlags.nonce = 'other-nonce'
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(pickerWindow({ documentNonce: 'nonce', pageHref: 'https://example.test/page' }))
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.length).toBe(2)
    })
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
    expect(messages.sent.some(m => m.type === 'burst:close')).toBe(true)
  })

  it('firefox recover continues an expired coalescing window without silent drop', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    sessionStore.session = null
    holds.windowLive = false
    const expired = firefoxHold(1)
    expired.startTime = Date.now() - 5 * 60 * 1000 - 1
    const expired2 = firefoxHold(2)
    expired2.startTime = Date.now() - 5 * 60 * 1000 - 1
    holds.map.set(1, expired)
    holds.map.set(2, expired2)
    holds.setWindow({
      captureId: SESSION.captureId,
      downloadIds: [1, 2],
      firstItemAt: 0,
      lastItemAt: 0,
      phase: 'coalescing',
      tabId: 4,
    })
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.map(row => row.url).sort()).toEqual([
        'https://cdn.example.test/1.bin',
        'https://cdn.example.test/2.bin',
      ])
    })
    expect(bridge.resume).toEqual([])
  })

  it('notifies cannot-resume when a Firefox picker row is unselected', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(pickerWindow())
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
      indices: [0],
    })
    expect(reply.accepted).toBe(true)
    expect(notices.messages).toContain('burst_firefox_cannot_resume')
    expect(fx.cleaned).toContain(10)
  })

  it('does not notify cannot-resume when Firefox cancel finds nothing to release', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    sessionStore.session = null
    await handleBurstCancel({ capture_id: SESSION.captureId as string })
    expect(notices.messages).toEqual([])
    expect(fx.legacy).toEqual([])
  })

  it('stamps the presentation tabId on a coalescing window at admit', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, { ...firefoxHold(1), tabId: 4 })
    await admitConfirmedDownload(1, { url: holdOf(1).url, tabId: 4 } as never, Date.now())
    expect(holds.getWindow()?.tabId).toBe(4)
  })

  it('firefox recover session=null without window.tabId still sendLegacy', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    sessionStore.session = null
    holds.map.set(1, { ...firefoxHold(1), tabId: 4, mainFrame: true })
    holds.setWindow({
      captureId: SESSION.captureId,
      downloadIds: [1],
      firstItemAt: Date.now(),
      lastItemAt: Date.now(),
      phase: 'coalescing',
    })
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.some(row => row.url.includes('/1.bin'))).toBe(true)
    })
    expect(bridge.resume).toEqual([])
    expect(fx.skipTabIds).toEqual([])
  })

  it('firefox recover session=null uses window.tabId as skipTabId', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    sessionStore.session = null
    holds.map.set(1, { ...firefoxHold(1), tabId: 4, mainFrame: true })
    holds.setWindow({
      captureId: SESSION.captureId,
      downloadIds: [1],
      firstItemAt: Date.now(),
      lastItemAt: Date.now(),
      phase: 'coalescing',
      tabId: 4,
    })
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.some(row => row.url.includes('/1.bin'))).toBe(true)
    })
    expect(fx.skipTabIds).toContain(4)
  })

  it('claimed interceptor handoff skips recover send then reserved send still runs', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(5, firefoxHold(5))
    claimFirefoxLegacyHandoff(5)
    await recoverBurstState()
    expect(fx.legacy).toEqual([])
    expect(holds.map.has(5)).toBe(true)
    scheduleFirefoxLegacyHandoff(5)
    await vi.waitFor(() => {
      expect(fx.legacy.some(row => row.url.includes('/5.bin'))).toBe(true)
    })
  })

  it('firefox recover legacy-sends when picker ping page_href does not match', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    pingFlags.href = 'https://other.test/page'
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(pickerWindow({ documentNonce: 'nonce', pageHref: 'https://example.test/page' }))
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.length).toBe(2)
    })
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
    expect(messages.sent.some(m => m.type === 'burst:close')).toBe(true)
  })

  it('firefox recover refuses picker reopen when ping omits nonce or href', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    pingFlags.nonce = undefined as never
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(pickerWindow())
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.length).toBe(2)
    })
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
    expect(messages.sent.some(m => m.type === 'burst:close')).toBe(true)
  })

  it('does not stack cannot-resume notifies on unselect plus ack partition failure', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(pickerWindow())
    ws.sendDirectBatch.mockImplementation(async (payload?: Record<string, unknown>) => {
      const items = (payload?.items ?? []) as Array<{ client_item_id: string }>
      return {
        succeeded_item_ids: [],
        duplicate_item_ids: [items[0]?.client_item_id],
        errors_by_item_id: {},
      }
    })
    const reply = await handleBurstSubmit({
      capture_id: SESSION.captureId as string,
      indices: [0],
    })
    expect(reply.accepted).toBe(true)
    expect(notices.messages.filter(m => m === 'burst_firefox_cannot_resume')).toHaveLength(1)
  })

  it('Firefox reconnect auth_ack does not cannot-resume recovered holds', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow({
      captureId: SESSION.captureId,
      downloadIds: [1, 2],
      firstItemAt: Date.now(),
      lastItemAt: Date.now(),
      phase: 'coalescing',
      tabId: 4,
    })
    await recoverBurstState()
    await handleCaptureReconnect()
    expect(holds.map.has(1)).toBe(true)
    expect(holds.map.has(2)).toBe(true)
    expect(notices.messages).not.toContain('burst_firefox_cannot_resume')
    expect(bridge.resume).toEqual([])
  })

  it('Chrome reconnect still fail-closed resumes burst holds', async () => {
    holds.map.set(1, holdOf(1))
    holds.setWindow(pickerWindow({ downloadIds: [1], catalog: [{ index: 0, downloadId: 1 }] }))
    await handleCaptureReconnect()
    expect(bridge.resume).toEqual([1])
  })

  it('a failed burst:alive does not cannot-resume Firefox holds', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(pickerWindow())
    await expect(handleBurstAlive({ capture_id: 'missing' })).resolves.toEqual({ ok: false })
    expect(holds.map.has(1)).toBe(true)
    expect(holds.map.has(2)).toBe(true)
    expect(holds.getWindow()).not.toBeNull()
    expect(notices.messages).not.toContain('burst_firefox_cannot_resume')
  })

  it('firefox recover sendLegacy for an ineligible orphan instead of admitting', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    sessionStore.session = { ...SESSION, storeUnproven: false, cookieStoreId: 'store-a' }
    holds.map.set(3, { ...firefoxHold(3), cookieStoreId: 'store-b' })
    holds.map.set(4, {
      ...firefoxHold(4),
      cookieStoreId: 'store-a',
      referrer: 'https://other.test/',
    })
    await recoverBurstState()
    await vi.waitFor(() => {
      expect(fx.legacy.map(row => row.url).sort()).toEqual([
        'https://cdn.example.test/3.bin',
        'https://cdn.example.test/4.bin',
      ])
    })
    expect(holds.getWindow()).toBeNull()
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
  })

  it('firefox recover of submitting pending keeps the original request id', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(
      pickerWindow({
        phase: 'submitting',
        requestId: 'req-keep',
        submitItems: [
          { clientItemId: 'aa', downloadId: 1, index: 0 },
          { clientItemId: 'bb', downloadId: 2, index: 1 },
        ],
      }),
    )
    ws.sendDirectBatchStatus.mockResolvedValue({ status: 'pending' })
    await recoverBurstState()
    expect(holds.getWindow()?.phase).toBe('submitting')
    expect(holds.getWindow()?.requestId).toBe('req-keep')
    expect(messages.sent.some(m => m.type === 'burst:open')).toBe(false)
    expect(holds.map.has(1)).toBe(true)
  })

  it('submit-status timer still finishes after the live window TTL expires', async () => {
    firefoxMode.on = true
    installFirefoxBridge()
    vi.useFakeTimers()
    holds.windowLive = false
    holds.map.set(1, firefoxHold(1))
    holds.map.set(2, firefoxHold(2))
    holds.setWindow(
      pickerWindow({
        phase: 'submitting',
        requestId: 'req-keep',
        firstItemAt: 0,
        lastItemAt: 0,
        pickerDeadline: 1,
        submitItems: [
          { clientItemId: 'aa', downloadId: 1, index: 0 },
          { clientItemId: 'bb', downloadId: 2, index: 1 },
        ],
      }),
    )
    ws.sendDirectBatchStatus.mockResolvedValue({ status: 'pending' })
    await recoverBurstState()
    expect(holds.getWindow()?.requestId).toBe('req-keep')
    ws.sendDirectBatchStatus.mockResolvedValue({
      status: 'complete',
      succeeded_item_ids: ['aa', 'bb'],
      duplicate_item_ids: [],
      errors_by_item_id: {},
    })
    await vi.advanceTimersByTimeAsync(2000)
    expect(holds.getWindow()).toBeNull()
    expect(holds.map.has(1)).toBe(false)
  })
})
