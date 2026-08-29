import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { InterceptionContext } from './LinkGrabberResponse'

const config = vi.hoisted(() => ({
  autoCapture: true,
  registeredFileTypes: [] as string[],
}))

const connection = vi.hoisted(() => ({
  interceptionEnabled: true,
  status: 'connected',
}))

vi.mock('../stores/config.svelte', () => ({
  configState: config,
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: connection,
}))

vi.mock('../background/wsClient', () => ({
  wsClient: {
    sendDownloadRequest: vi.fn(),
  },
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
    notifications: { create: async () => undefined },
    runtime: { getURL: (p: string) => p },
    tabs: { get: async () => undefined, query: async () => [] },
  },
}))

vi.mock('../lib/i18n', () => ({
  t: (key: string) => key,
}))

import { DownloadLinkInterceptor } from './DownloadLinkInterceptor'
import { resetBootReadyForTests, setBootReady } from '../background/bootState'

class TestInterceptor extends DownloadLinkInterceptor {
  register(): void {}
  async recoverPendingDecisions(): Promise<void> {}
}

function ctx(partial: Partial<InterceptionContext> = {}): InterceptionContext {
  return {
    url: 'https://cdn.example.test/file.bin',
    tabId: 1,
    mimeType: 'application/octet-stream',
    contentDisposition: '',
    fileSize: 1024,
    filename: 'file.bin',
    referrer: 'https://example.test/page',
    ...partial,
  }
}

describe('shouldIntercept', () => {
  const interceptor = new TestInterceptor()

  beforeEach(() => {
    resetBootReadyForTests()
    setBootReady(true)
    config.autoCapture = true
    config.registeredFileTypes = []
    connection.interceptionEnabled = true
    connection.status = 'connected'
  })

  it('passes before boot even when autoCapture and the websocket are ready', () => {
    resetBootReadyForTests()
    expect(interceptor.shouldIntercept(ctx())).toBe('pass')
  })

  it('passes when autoCapture is off', () => {
    config.autoCapture = false
    expect(interceptor.shouldIntercept(ctx())).toBe('pass')
  })

  it('passes when the websocket is not connected', () => {
    connection.interceptionEnabled = false
    expect(interceptor.shouldIntercept(ctx())).toBe('pass')
  })

  it('passes non-http schemes', () => {
    expect(interceptor.shouldIntercept(ctx({ url: 'blob:https://example.test/1' }))).toBe('pass')
    expect(interceptor.shouldIntercept(ctx({ url: 'data:text/plain,hi' }))).toBe('pass')
    expect(interceptor.shouldIntercept(ctx({ url: 'ftp://files.example.test/a.bin' }))).toBe('pass')
  })

  it('intercepts Content-Disposition attachment without a whitelist', () => {
    expect(
      interceptor.shouldIntercept(
        ctx({
          mimeType: 'text/html',
          contentDisposition: 'attachment; filename="a.html"',
          filename: 'a.html',
        }),
      ),
    ).toBe('intercept')
  })

  it('passes explicit inline disposition', () => {
    expect(
      interceptor.shouldIntercept(
        ctx({
          mimeType: 'application/octet-stream',
          contentDisposition: 'inline; filename="a.bin"',
        }),
      ),
    ).toBe('pass')
  })

  it('passes NON_DOWNLOAD MIME including both HLS playlist types', () => {
    expect(interceptor.shouldIntercept(ctx({ mimeType: 'text/html', filename: 'a.html' }))).toBe(
      'pass',
    )
    expect(
      interceptor.shouldIntercept(ctx({ mimeType: 'application/x-mpegurl', filename: 'a.m3u8' })),
    ).toBe('pass')
    expect(
      interceptor.shouldIntercept(
        ctx({ mimeType: 'application/vnd.apple.mpegurl', filename: 'a.m3u8' }),
      ),
    ).toBe('pass')
  })

  it('intercepts empty MIME without a whitelist', () => {
    expect(interceptor.shouldIntercept(ctx({ mimeType: '', filename: 'file.bin' }))).toBe('intercept')
  })

  it('intercepts download MIME without a whitelist', () => {
    expect(interceptor.shouldIntercept(ctx({ mimeType: 'application/zip' }))).toBe('intercept')
    expect(interceptor.shouldIntercept(ctx({ mimeType: 'application/pdf' }))).toBe('intercept')
  })
})
