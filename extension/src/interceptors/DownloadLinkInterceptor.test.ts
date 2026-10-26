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
  MSG_TYPE_HLS: 'hls',
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: connection,
}))

vi.mock('../background/wsClient', () => ({
  wsClient: {
    sendDownloadRequest: vi.fn(),
  },
}))

const cookies = vi.hoisted(() => ({
  getCookiesForUrl: vi.fn(async (_url?: string, _storeId?: string) => ['Cookie: a=1']),
}))

const tabsApi = vi.hoisted(() => ({
  get: vi.fn<(id: number) => Promise<{ title?: string } | undefined>>(async () => undefined),
}))

vi.mock('../background/cookieCapture', () => ({
  getCookiesForUrl: (url: string, storeId?: string) => cookies.getCookiesForUrl(url, storeId),
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
    tabs: { get: (id: number) => tabsApi.get(id), query: async () => [] },
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

  it('passes NON_DOWNLOAD MIME but both HLS playlist types now intercept', () => {
    expect(interceptor.shouldIntercept(ctx({ mimeType: 'text/html', filename: 'a.html' }))).toBe(
      'pass',
    )
    // MIME-only HLS: no .m3u8 anywhere, but the playlist MIME is decisive.
    expect(
      interceptor.shouldIntercept(ctx({ mimeType: 'application/x-mpegurl', filename: 'a.m3u8' })),
    ).toBe('intercept')
    expect(
      interceptor.shouldIntercept(
        ctx({ mimeType: 'application/vnd.apple.mpegurl', filename: 'a.m3u8' }),
      ),
    ).toBe('intercept')
  })

  it('intercepts .m3u8 urls regardless of MIME, case, query, or fragment', () => {
    for (const mimeType of ['', 'text/plain', 'application/octet-stream']) {
      expect(
        interceptor.shouldIntercept(
          ctx({ url: 'https://cdn.example.test/live.m3u8', mimeType, filename: '' }),
        ),
      ).toBe('intercept')
    }
    expect(
      interceptor.shouldIntercept(ctx({ url: 'https://cdn.example.test/LIVE.M3U8?tok=1#frag' })),
    ).toBe('intercept')
    // finalUrl .m3u8 covers the redirect-to-manifest shape.
    expect(
      interceptor.shouldIntercept(
        ctx({
          url: 'https://cdn.example.test/watch',
          finalUrl: 'https://cdn.example.test/play.m3u8',
        }),
      ),
    ).toBe('intercept')
  })

  it('intercepts HLS even under a strict whitelist that lacks m3u8', () => {
    config.registeredFileTypes = ['zip']
    expect(
      interceptor.shouldIntercept(ctx({ url: 'https://cdn.example.test/live.m3u8', filename: '' })),
    ).toBe('intercept')
    expect(
      interceptor.shouldIntercept(
        ctx({
          url: 'https://cdn.example.test/edge',
          mimeType: 'application/x-mpegurl',
          filename: '',
        }),
      ),
    ).toBe('intercept')
  })

  it('keeps ordinary whitelist behavior for non-HLS files', () => {
    config.registeredFileTypes = ['zip']
    expect(
      interceptor.shouldIntercept(
        ctx({
          url: 'https://cdn.example.test/a.bin',
          filename: 'a.bin',
          mimeType: 'application/octet-stream',
        }),
      ),
    ).toBe('pass')
    expect(
      interceptor.shouldIntercept(
        ctx({
          url: 'https://cdn.example.test/a.zip',
          filename: 'a.zip',
          mimeType: 'application/octet-stream',
        }),
      ),
    ).toBe('intercept')
  })

  it('keeps disposition semantics around the HLS branch', async () => {
    // Explicit inline never intercepts, even with HLS evidence.
    expect(
      interceptor.shouldIntercept(
        ctx({ url: 'https://cdn.example.test/live.m3u8', contentDisposition: 'inline' }),
      ),
    ).toBe('pass')
    // Attachment + no whitelist intercepts and still hands off as hls.
    const attached = ctx({
      url: 'https://cdn.example.test/live.m3u8',
      contentDisposition: 'attachment; filename="live.m3u8"',
      filename: 'live.m3u8',
    })
    expect(interceptor.shouldIntercept(attached)).toBe('intercept')
    const req = await interceptor.constructDownloadRequest(attached)
    expect(req.type).toBe('hls')
    // Attachment + strict whitelist miss still passes even with HLS evidence.
    config.registeredFileTypes = ['zip']
    expect(interceptor.shouldIntercept(attached)).toBe('pass')
    // Whitelist hit intercepts and stays HLS.
    config.registeredFileTypes = ['m3u8']
    expect(interceptor.shouldIntercept(attached)).toBe('intercept')
  })

  it('never promotes .m3u through MIME, even when m3u is registered', () => {
    for (const mimeType of ['application/x-mpegurl', 'application/vnd.apple.mpegurl']) {
      expect(
        interceptor.shouldIntercept(
          ctx({ url: 'https://cdn.example.test/audio.m3u', mimeType, filename: 'audio.m3u' }),
        ),
      ).toBe('pass')
    }
    config.registeredFileTypes = ['m3u']
    expect(
      interceptor.shouldIntercept(
        ctx({
          url: 'https://cdn.example.test/audio.m3u',
          mimeType: 'application/x-mpegurl',
          filename: 'audio.m3u',
        }),
      ),
    ).toBe('pass')
  })

  it('keeps audio/mpegurl on the ordinary download rules', async () => {
    config.registeredFileTypes = ['m3u']
    const ordinary = ctx({
      url: 'https://cdn.example.test/audio.m3u',
      mimeType: 'audio/mpegurl',
      filename: 'audio.m3u',
    })
    expect(interceptor.shouldIntercept(ordinary)).toBe('intercept')
    const req = await interceptor.constructDownloadRequest(ordinary)
    expect(req.type).toBeUndefined()
    expect(req.pageTitle).toBeUndefined()
    // A .m3u -> .m3u8 redirect has a decisive signal and is HLS.
    const redirected = ctx({
      url: 'https://cdn.example.test/audio.m3u',
      finalUrl: 'https://cdn.example.test/live.m3u8',
      mimeType: 'audio/mpegurl',
    })
    expect(interceptor.shouldIntercept(redirected)).toBe('intercept')
    const req2 = await interceptor.constructDownloadRequest(redirected)
    expect(req2.type).toBe('hls')
  })

  it('does not let a .m3u8 filename or query text trigger HLS', async () => {
    const byName = ctx({
      url: 'https://cdn.example.test/dl?id=1',
      filename: 'live.m3u8',
      mimeType: 'application/octet-stream',
    })
    expect(interceptor.shouldIntercept(byName)).toBe('intercept')
    const namedReq = await interceptor.constructDownloadRequest(byName)
    expect(namedReq.type).toBeUndefined()

    const byQuery = ctx({
      url: 'https://cdn.example.test/file.bin?next=live.m3u8',
      mimeType: 'application/octet-stream',
    })
    const queryReq = await interceptor.constructDownloadRequest(byQuery)
    expect(queryReq.type).toBeUndefined()
  })

  it('intercepts empty MIME without a whitelist', () => {
    expect(interceptor.shouldIntercept(ctx({ mimeType: '', filename: 'file.bin' }))).toBe('intercept')
  })

  it('intercepts download MIME without a whitelist', () => {
    expect(interceptor.shouldIntercept(ctx({ mimeType: 'application/zip' }))).toBe('intercept')
    expect(interceptor.shouldIntercept(ctx({ mimeType: 'application/pdf' }))).toBe('intercept')
  })
})

describe('constructDownloadRequest', () => {
  const interceptor = new TestInterceptor()

  beforeEach(() => {
    cookies.getCookiesForUrl.mockClear()
    cookies.getCookiesForUrl.mockResolvedValue(['Cookie: a=1'])
    tabsApi.get.mockClear()
    tabsApi.get.mockResolvedValue(undefined)
  })

  it('builds an hls request carrying the tab title when resolvable', async () => {
    tabsApi.get.mockResolvedValue({ title: 'Live Show' })
    const req = await interceptor.constructDownloadRequest(
      ctx({ url: 'https://cdn.example.test/live.m3u8' }),
    )
    expect(req.type).toBe('hls')
    expect(req.pageTitle).toBe('Live Show')
    expect(tabsApi.get).toHaveBeenCalledTimes(1)
    expect(tabsApi.get).toHaveBeenCalledWith(1)
  })

  it('builds hls without a title when the tab is gone or the lookup fails', async () => {
    tabsApi.get.mockResolvedValue(undefined)
    const req = await interceptor.constructDownloadRequest(
      ctx({ url: 'https://cdn.example.test/a.m3u8' }),
    )
    expect(req.type).toBe('hls')
    expect(req.pageTitle).toBeUndefined()

    tabsApi.get.mockRejectedValue(new Error('denied'))
    const req2 = await interceptor.constructDownloadRequest(
      ctx({ url: 'https://cdn.example.test/b.m3u8' }),
    )
    expect(req2.type).toBe('hls')
    expect(req2.pageTitle).toBeUndefined()
  })

  it('never queries tabs for tabId<0 or non-HLS requests', async () => {
    const req = await interceptor.constructDownloadRequest(
      ctx({ url: 'https://cdn.example.test/live.m3u8', tabId: -1 }),
    )
    expect(req.type).toBe('hls')
    expect(req.pageTitle).toBeUndefined()
    expect(tabsApi.get).not.toHaveBeenCalled()

    const ordinary = await interceptor.constructDownloadRequest(ctx())
    expect(ordinary.type).toBeUndefined()
    expect(ordinary.pageTitle).toBeUndefined()
    expect(tabsApi.get).not.toHaveBeenCalled()
  })

  it('still skips cookies for incognito HLS', async () => {
    const req = await interceptor.constructDownloadRequest(
      ctx({ url: 'https://cdn.example.test/live.m3u8', incognito: true }),
    )
    expect(req.type).toBe('hls')
    expect(cookies.getCookiesForUrl).not.toHaveBeenCalled()
    expect(req.headers).toEqual([])
    expect(req.skipHeadProbe).toBe(true)
  })

  it('omits Cookie for incognito downloads', async () => {
    const req = await interceptor.constructDownloadRequest(ctx({ incognito: true }))
    expect(cookies.getCookiesForUrl).not.toHaveBeenCalled()
    expect(req.headers).toEqual([])
  })

  it('collects Cookie for a normal-profile download', async () => {
    const req = await interceptor.constructDownloadRequest(ctx({ incognito: false }))
    expect(cookies.getCookiesForUrl).toHaveBeenCalledTimes(1)
    expect(cookies.getCookiesForUrl).toHaveBeenCalledWith('https://cdn.example.test/file.bin', undefined)
    expect(req.headers).toEqual(['Cookie: a=1'])
  })

  it('passes cookieStoreId into legacy cookie collection', async () => {
    await interceptor.constructDownloadRequest(
      ctx({ incognito: false, cookieStoreId: 'firefox-container-1' }),
    )
    expect(cookies.getCookiesForUrl).toHaveBeenCalledWith(
      'https://cdn.example.test/file.bin',
      'firefox-container-1',
    )
  })
})
