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

vi.mock('../stores/config.svelte', () => ({
  configState: config,
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: connection,
}))

vi.mock('../background/wsClient', () => ({
  wsClient: { sendDownloadRequest: vi.fn() },
}))

vi.mock('../background/cookieCapture', () => ({
  getCookiesForUrl: async () => [],
}))

vi.mock('../background/refererCapture', () => ({
  getDownloadPageUrl: async () => '',
}))

vi.mock('webext-bridge/background', () => ({
  sendMessage: async () => undefined,
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    webRequest: webRequest.api,
    downloads: { search: vi.fn() },
    storage: { session: { get: vi.fn(), set: vi.fn(), remove: vi.fn() } },
    notifications: { create: async () => undefined },
    runtime: { getURL: (p: string) => p },
    tabs: { get: async () => undefined, query: async () => [], remove: async () => undefined },
  },
}))

vi.mock('../lib/i18n', () => ({
  t: (key: string) => key,
}))

import { FirefoxBlockingInterceptor } from './FirefoxBlockingInterceptor'
import { resetBootReadyForTests, setBootReady } from '../background/bootState'

describe('FirefoxBlockingInterceptor', () => {
  let interceptor: FirefoxBlockingInterceptor

  beforeEach(() => {
    resetBootReadyForTests()
    setBootReady(true)
    config.autoCapture = true
    config.registeredFileTypes = []
    connection.interceptionEnabled = true
    webRequest.reset()
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
      responseHeaders: [{ name: 'Content-Disposition', value: 'attachment; filename="a.bin"' }],
    })
    expect(reply).toEqual({})
  })

  it('registers only main_frame and sub_frame', () => {
    for (const entry of [...webRequest.before, ...webRequest.headers, ...webRequest.completed, ...webRequest.errored]) {
      expect(entry.filter.types).toEqual(['main_frame', 'sub_frame'])
    }
  })

  it('recoverPendingDecisions is a no-op', async () => {
    await expect(interceptor.recoverPendingDecisions()).resolves.toBeUndefined()
  })

  it('passes through shouldIntercept before boot', () => {
    resetBootReadyForTests()
    const reply = webRequest.headers[0].listener({
      requestId: '1',
      url: 'https://cdn.example.test/file.bin',
      statusCode: 200,
      type: 'main_frame',
      tabId: 3,
      responseHeaders: [
        { name: 'Content-Type', value: 'application/octet-stream' },
        { name: 'Content-Disposition', value: 'attachment; filename="a.bin"' },
      ],
    })
    expect(reply).toEqual({})
  })
})

describe('background interceptor registration order', () => {
  it('registers before the config await', () => {
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../background/background.ts'), 'utf8')
    const registerIdx = src.indexOf('interceptor.register()')
    const awaitIdx = src.indexOf('await Promise.all([configState.loadEffects()')
    expect(registerIdx).toBeGreaterThan(-1)
    expect(awaitIdx).toBeGreaterThan(-1)
    expect(registerIdx).toBeLessThan(awaitIdx)
  })
})
