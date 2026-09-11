import { beforeEach, describe, expect, it, vi } from 'vitest'

const firefoxMode = vi.hoisted(() => ({ on: false }))

const connection = vi.hoisted(() => ({
  capabilities: undefined as string[] | undefined,
}))

const webRequest = vi.hoisted(() => {
  type Listener = (details: Record<string, unknown>) => unknown
  const registrations: Array<{
    listener: Listener
    filter: { urls?: string[]; types?: string[] }
    options?: string[]
  }> = []
  return {
    registrations,
    fire(details: Record<string, unknown>): unknown {
      return registrations[0]?.listener(details)
    },
    reset() {
      registrations.length = 0
    },
    api: {
      onSendHeaders: {
        addListener(listener: Listener, filter: unknown, options?: string[]) {
          registrations.push({ listener, filter: filter as never, options })
        },
        removeListener() {},
      },
    },
  }
})

const tabsMock = vi.hoisted(() => {
  const updatedListeners: Array<(tabId: number, changeInfo: Record<string, unknown>, tab: unknown) => void> = []
  const removedListeners: Array<(tabId: number) => void> = []
  const getCalls: number[] = []
  let tab: { url?: string; incognito?: boolean; cookieStoreId?: string } | undefined
  return {
    getCalls,
    setTab(next: { url?: string; incognito?: boolean; cookieStoreId?: string } | undefined) {
      tab = next
    },
    fireUrlChange(tabId: number, url: string) {
      for (const fn of updatedListeners) fn(tabId, { url }, { id: tabId })
    },
    fireRemoved(tabId: number) {
      for (const fn of removedListeners) fn(tabId)
    },
    reset() {
      updatedListeners.length = 0
      removedListeners.length = 0
      getCalls.length = 0
      tab = undefined
    },
    api: {
      onUpdated: {
        addListener(fn: (tabId: number, changeInfo: Record<string, unknown>, tab: unknown) => void) {
          updatedListeners.push(fn)
        },
      },
      onRemoved: {
        addListener(fn: (tabId: number) => void) {
          removedListeners.push(fn)
        },
      },
      async get(tabId: number) {
        getCalls.push(tabId)
        if (!tab) throw new Error('no tab')
        return { id: tabId, ...tab }
      },
      async query() {
        return []
      },
    },
  }
})

const sessionStorage = vi.hoisted(() => {
  const data = new Map<string, unknown>()
  return {
    data,
    reset() {
      data.clear()
    },
    api: {
      async get(key: string | null) {
        if (key === null) {
          const all: Record<string, unknown> = {}
          for (const [k, v] of data) all[k] = v
          return all
        }
        if (typeof key === 'string') return { [key]: data.get(key) }
        return {}
      },
      async set(items: Record<string, unknown>) {
        for (const [k, v] of Object.entries(items)) data.set(k, v)
      },
      async remove(key: string) {
        data.delete(key)
      },
    },
  }
})

vi.mock('webextension-polyfill', () => ({
  default: {
    webRequest: webRequest.api,
    tabs: tabsMock.api,
    storage: { session: sessionStorage.api },
    notifications: { create: async () => 'n1' },
    i18n: { getMessage: (key: string) => key },
    runtime: { getURL: (p: string) => p },
  },
}))

vi.mock('webext-bridge/background', () => ({
  sendMessage: vi.fn(async () => 'shown'),
  onMessage: vi.fn(),
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: connection,
}))

vi.mock('../stores/config.svelte', () => ({
  CAP_EXTRACTOR_RESOLVE: 'extractor.resolve',
  CAP_EXTRACTOR_HEADER_CONTEXT: 'extractor.header_context',
}))

vi.mock('../utils/extensionInfo', () => ({
  isFirefox: () => firefoxMode.on,
  isChrome: () => !firefoxMode.on,
  getExtensionBrowserTarget: () => (firefoxMode.on ? 'firefox' : 'chrome'),
}))

import {
  armExtractorHeaderCandidate,
  clearExtractorHeaderGrants,
  initBrowserHeaderCapture,
  takeHeaderGrantsForResolve,
} from './browserHeaderCapture'
import { deliverExtractorDetected } from './extractorVisibility'
import {
  notifyExtractorHostDown,
  notifyExtractorMatchCleared,
} from './extractorVisibility'
import { applyMatchSnapshot, clearMatchSnapshot } from './matchSnapshot'
import { pageTokenFromHref } from './pageToken'

const PAGE_URL = 'https://share.alpha.test/item/aaa'
const PAGE_ORIGIN = 'https://share.alpha.test'
const CAPS = ['request_id', 'extractor.resolve', 'extractor.batch', 'extractor.header_context']

const MATCH = {
  digest_version: 1,
  salt: 'a'.repeat(32),
  exact_digests: ['b'.repeat(64)],
  subdomain_digests: [] as string[],
}

function applyMatch(): number {
  return applyMatchSnapshot(MATCH)
}

function xhrDetails(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    requestId: 'r1',
    url: 'https://api.alpha.test/v1/item?id=fixture',
    method: 'GET',
    frameId: 0,
    parentFrameId: -1,
    tabId: 1,
    type: 'xmlhttprequest',
    timeStamp: 1,
    thirdParty: false,
    incognito: false,
    requestHeaders: [{ name: 'Authorization', value: 'Bearer fixture-token' }],
    ...(firefoxMode.on
      ? { originUrl: PAGE_URL, cookieStoreId: 'firefox-default' }
      : { initiator: PAGE_ORIGIN }),
    ...over,
  }
}

beforeEach(() => {
  firefoxMode.on = false
  connection.capabilities = undefined
  webRequest.reset()
  tabsMock.reset()
  sessionStorage.reset()
  clearMatchSnapshot()
  clearExtractorHeaderGrants()
})

describe('listener registration', () => {
  it('registers a read-only onSendHeaders observer with chrome extraHeaders options', () => {
    initBrowserHeaderCapture()
    expect(webRequest.registrations).toHaveLength(1)
    const reg = webRequest.registrations[0]!
    expect(reg.filter).toEqual({ urls: ['https://*/*'], types: ['xmlhttprequest'] })
    expect(reg.options).toEqual(['requestHeaders', 'extraHeaders'])
    expect(reg.options).not.toContain('blocking')
    expect(reg.listener(xhrDetails())).toBeUndefined()
  })

  it('uses requestHeaders only on firefox', () => {
    firefoxMode.on = true
    initBrowserHeaderCapture()
    expect(webRequest.registrations[0]?.options).toEqual(['requestHeaders'])
  })
})

describe('observer gating', () => {
  it('ignores events when the capability is absent even with an armed tab', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    const token = await pageTokenFromHref(PAGE_URL)
    connection.capabilities = ['request_id', 'extractor.resolve', 'extractor.batch']
    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
    webRequest.fire(xhrDetails())
    expect(
      takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false }),
    ).toEqual([])
  })

  it('ignores malformed capability arrays and near-match strings', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    const token = await pageTokenFromHref(PAGE_URL)
    for (const caps of [
      ['extractor.header_context '],
      ['Extractor.Header_Context'],
      ['extractor.resolve', 'extractor.header_context.extra'],
    ]) {
      connection.capabilities = caps
      await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
      webRequest.fire(xhrDetails())
      expect(
        takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false }),
      ).toEqual([])
    }
  })

  it('captures an eligible observation on an armed candidate tab', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    const token = await pageTokenFromHref(PAGE_URL)
    connection.capabilities = CAPS
    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
    expect(tabsMock.getCalls).toEqual([1])
    webRequest.fire(xhrDetails())
    const grants = takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false })
    expect(grants).toHaveLength(1)
    expect(grants[0]?.source_origin).toBe(PAGE_ORIGIN)
    expect(grants[0]?.headers).toEqual([
      { name: 'authorization', value: 'Bearer fixture-token' },
    ])
    // one-shot: second take is empty even though the page state is unchanged
    expect(takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false })).toEqual([])
  })

  it('drops non-tab and foreign-source events', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    const token = await pageTokenFromHref(PAGE_URL)
    connection.capabilities = CAPS
    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
    webRequest.fire(xhrDetails({ tabId: -1 }))
    webRequest.fire(xhrDetails({ initiator: 'https://unrelated.beta.test' }))
    webRequest.fire(xhrDetails({ url: 'http://api.alpha.test/v1/item' }))
    webRequest.fire(xhrDetails({ method: 'POST' }))
    expect(
      takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false }),
    ).toEqual([])
  })

  it('treats a missing incognito field as non-incognito on chrome only', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    const token = await pageTokenFromHref(PAGE_URL)
    connection.capabilities = CAPS
    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
    // Chrome may omit details.incognito; a split-mode non-incognito context
    // cannot see incognito events, so absence is treated as false here.
    const noField = xhrDetails()
    delete noField.incognito
    webRequest.fire(noField)
    expect(
      takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false }),
    ).toHaveLength(1)
  })

  it('rejects an incognito event under a non-incognito candidate', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    const token = await pageTokenFromHref(PAGE_URL)
    connection.capabilities = CAPS
    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
    webRequest.fire(xhrDetails({ incognito: true }))
    expect(
      takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false }),
    ).toEqual([])
  })

  it('requires an explicit incognito field on firefox', async () => {
    firefoxMode.on = true
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false, cookieStoreId: 'firefox-default' })
    const token = await pageTokenFromHref(PAGE_URL)
    connection.capabilities = CAPS
    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
    const noField = xhrDetails()
    delete noField.incognito
    webRequest.fire(noField)
    expect(
      takeHeaderGrantsForResolve({
        tabId: 1,
        pageToken: token!,
        incognito: false,
        cookieStoreId: 'firefox-default',
      }),
    ).toEqual([])
    webRequest.fire(xhrDetails())
    expect(
      takeHeaderGrantsForResolve({
        tabId: 1,
        pageToken: token!,
        incognito: false,
        cookieStoreId: 'firefox-default',
      }),
    ).toHaveLength(1)
  })

  it('clears the candidate when the tab navigates or is removed', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    const token = await pageTokenFromHref(PAGE_URL)
    connection.capabilities = CAPS
    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
    webRequest.fire(xhrDetails())
    tabsMock.fireUrlChange(1, 'https://share.alpha.test/other')
    expect(
      takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false }),
    ).toEqual([])

    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token!)
    webRequest.fire(xhrDetails())
    tabsMock.fireRemoved(1)
    expect(
      takeHeaderGrantsForResolve({ tabId: 1, pageToken: token!, incognito: false }),
    ).toEqual([])
  })
})

describe('arm gating', () => {
  async function armWith(over: {
    caps?: string[] | undefined
    tab?: { url?: string; incognito?: boolean; cookieStoreId?: string } | undefined
    generation?: number
    pageToken?: string
    sourceUrl?: string
  }) {
    const generation = over.generation ?? applyMatch()
    const token = over.pageToken ?? (await pageTokenFromHref(PAGE_URL))!
    tabsMock.setTab(
      over.tab === undefined ? { url: PAGE_URL, incognito: false } : over.tab,
    )
    connection.capabilities = over.caps === undefined ? CAPS : over.caps
    await armExtractorHeaderCandidate(1, generation, over.sourceUrl ?? PAGE_URL, token)
    return token
  }

  it('refuses to arm without the exact capability pair', async () => {
    const token = await armWith({ caps: ['request_id', 'extractor.resolve'] })
    expect(tabsMock.getCalls).toEqual([])
    webRequest.fire(xhrDetails())
    expect(takeHeaderGrantsForResolve({ tabId: 1, pageToken: token, incognito: false })).toEqual([])
  })

  it('refuses to arm under a stale match generation', async () => {
    const generation = applyMatch()
    clearMatchSnapshot()
    await armWith({ generation })
    expect(tabsMock.getCalls).toEqual([])
  })

  it('refuses to arm for a non-http source page', async () => {
    await armWith({ sourceUrl: 'chrome-extension://abc/page.html' })
    expect(tabsMock.getCalls).toEqual([])
  })

  it('refuses when the live tab no longer matches the detected page token', async () => {
    const token = await armWith({ tab: { url: 'https://share.alpha.test/navigated', incognito: false } })
    expect(takeHeaderGrantsForResolve({ tabId: 1, pageToken: token, incognito: false })).toEqual([])
    webRequest.fire(xhrDetails())
    expect(takeHeaderGrantsForResolve({ tabId: 1, pageToken: token, incognito: false })).toEqual([])
  })

  it('refuses when the capability disappears during the async live-tab check', async () => {
    const generation = applyMatch()
    const token = (await pageTokenFromHref(PAGE_URL))!
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    connection.capabilities = CAPS
    const pending = armExtractorHeaderCandidate(1, generation, PAGE_URL, token)
    connection.capabilities = ['extractor.resolve']
    await pending
    webRequest.fire(xhrDetails())
    expect(takeHeaderGrantsForResolve({ tabId: 1, pageToken: token, incognito: false })).toEqual([])
  })
})

describe('detection integration', () => {
  it('arms the candidate after token and ignore checks inside deliverExtractorDetected', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    connection.capabilities = CAPS
    await deliverExtractorDetected(1, generation, PAGE_URL)
    expect(tabsMock.getCalls).toEqual([1])
    webRequest.fire(xhrDetails())
    const token = (await pageTokenFromHref(PAGE_URL))!
    const grants = takeHeaderGrantsForResolve({ tabId: 1, pageToken: token, incognito: false })
    expect(grants).toHaveLength(1)
  })

  it('does not arm when the page is ignored', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    connection.capabilities = CAPS
    const token = (await pageTokenFromHref(PAGE_URL))!
    const { getExtractorSessionStore } = await import('./extractorVisibility')
    await getExtractorSessionStore().setIgnored(1, token)
    await deliverExtractorDetected(1, generation, PAGE_URL)
    expect(tabsMock.getCalls).toEqual([])
    webRequest.fire(xhrDetails())
    expect(takeHeaderGrantsForResolve({ tabId: 1, pageToken: token, incognito: false })).toEqual([])
  })

  it('does not arm for an undetectable page url', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    connection.capabilities = CAPS
    await deliverExtractorDetected(1, generation, undefined)
    expect(tabsMock.getCalls).toEqual([])
  })

  it('clears all candidates when the host disconnects or the match is replaced', async () => {
    initBrowserHeaderCapture()
    const generation = applyMatch()
    tabsMock.setTab({ url: PAGE_URL, incognito: false })
    connection.capabilities = CAPS
    const token = (await pageTokenFromHref(PAGE_URL))!
    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token)
    webRequest.fire(xhrDetails())

    notifyExtractorHostDown('disconnect')
    expect(takeHeaderGrantsForResolve({ tabId: 1, pageToken: token, incognito: false })).toEqual([])

    await armExtractorHeaderCandidate(1, generation, PAGE_URL, token)
    webRequest.fire(xhrDetails())
    notifyExtractorMatchCleared()
    expect(takeHeaderGrantsForResolve({ tabId: 1, pageToken: token, incognito: false })).toEqual([])
  })
})
