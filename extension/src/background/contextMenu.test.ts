import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DownloadHandoffMessage } from '../utils/messaging'

const state = vi.hoisted(() => ({
  capabilities: ['download.batch'] as string[] | undefined,
  created: [] as Array<{ id: string; title: string; contexts: string[] }>,
  removed: 0,
  clicked: null as null | ((info: unknown, tab: unknown) => Promise<void> | void),
  installed: null as null | (() => void),
  notifications: [] as Array<{ title?: string; message?: string }>,
  collectCalls: 0,
  cookies: ['Cookie: sid=1'] as string[],
  downloadPage: 'https://example.test/page',
}))

const ws = vi.hoisted(() => ({
  sendDownloadRequest: vi.fn(
    async (_req: DownloadHandoffMessage): Promise<{
      type: string
      success: boolean
      gid: string
    }> => ({
      type: 'download_ack',
      success: true,
      gid: 'ar_ctx',
    }),
  ),
}))

vi.mock('../stores/config.svelte', () => ({
  CAP_DOWNLOAD_BATCH: 'download.batch',
  MSG_TYPE_HLS: 'hls',
  configState: {
    autoCapture: true,
    registeredFileTypes: [] as string[],
  },
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: {
    get capabilities() {
      return state.capabilities
    },
  },
}))

vi.mock('./cookieCapture', () => ({
  getCookiesForUrl: vi.fn(async () => [...state.cookies]),
}))

vi.mock('./refererCapture', () => ({
  getDownloadPageUrl: vi.fn(async () => state.downloadPage),
}))

vi.mock('./domFlow', () => ({
  handleCollectPageLinks: vi.fn(async () => {
    state.collectCalls += 1
  }),
}))

vi.mock('./wsClient', () => ({
  wsClient: ws,
}))

vi.mock('webext-bridge/background', () => ({
  sendMessage: async () => 'shown',
  onMessage() {},
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    runtime: {
      getURL: (p: string) => p,
      onInstalled: {
        addListener(fn: () => void) {
          state.installed = fn
        },
      },
    },
    contextMenus: {
      removeAll: async () => {
        state.removed += 1
      },
      create: (opts: { id: string; title: string; contexts: string[] }) => {
        state.created.push(opts)
      },
      onClicked: {
        addListener(fn: (info: unknown, tab: unknown) => Promise<void> | void) {
          state.clicked = fn
        },
      },
    },
    notifications: {
      create: async (opts: { title?: string; message?: string }) => {
        state.notifications.push(opts)
      },
    },
    tabs: { get: async () => undefined, query: async () => [] },
  },
}))

vi.mock('../lib/i18n', () => ({
  t: (key: string) => key,
}))

import { initContextMenu } from './contextMenu'
import { getCookiesForUrl } from './cookieCapture'

const cookiesMock = vi.mocked(getCookiesForUrl)

const tab = { id: 7, title: 'Live Show Page', url: 'https://example.test/page' }

async function click(info: Record<string, unknown>, t: unknown): Promise<void> {
  const listener = state.clicked
  expect(listener).not.toBeNull()
  await listener?.(info, t)
}

function lastRequest(): DownloadHandoffMessage {
  const req = ws.sendDownloadRequest.mock.calls.at(-1)?.[0]
  expect(req).toBeDefined()
  return req as DownloadHandoffMessage
}

describe('contextMenu', () => {
  beforeEach(() => {
    state.capabilities = ['download.batch']
    state.created = []
    state.removed = 0
    state.clicked = null
    state.installed = null
    state.notifications = []
    state.collectCalls = 0
    state.cookies = ['Cookie: sid=1']
    state.downloadPage = 'https://example.test/page'
    ws.sendDownloadRequest.mockClear()
    ws.sendDownloadRequest.mockResolvedValue({
      type: 'download_ack',
      success: true,
      gid: 'ar_ctx',
    })
    cookiesMock.mockClear()
    initContextMenu()
  })

  it('registers both menus and binds the click listener once', () => {
    expect(state.removed).toBe(1)
    expect(state.created.map(m => m.id)).toEqual([
      'goaria-download-link',
      'goaria-collect-page-links',
    ])
    expect(state.installed).not.toBeNull()
    expect(state.clicked).not.toBeNull()
  })

  it('sends a .m3u8 link as hls with the tab title', async () => {
    await click({ menuItemId: 'goaria-download-link', linkUrl: 'https://cdn.test/live.m3u8' }, tab)
    expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    const req = lastRequest()
    expect(req.type).toBe('hls')
    expect(req.pageTitle).toBe('Live Show Page')
    expect(req.url).toBe('https://cdn.test/live.m3u8')
    expect(req.headers).toEqual(['Cookie: sid=1'])
    expect(req.downloadPage).toBe('https://example.test/page')
  })

  it('detects .M3U8 case-insensitively and tolerates a missing tab', async () => {
    await click(
      { menuItemId: 'goaria-download-link', linkUrl: 'https://cdn.test/LIVE.M3U8?x=1' },
      undefined,
    )
    expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    const req = lastRequest()
    expect(req.type).toBe('hls')
    expect(req.pageTitle).toBeUndefined()
  })

  it('shows the unsupported prompt for .m3u and sends nothing', async () => {
    await click({ menuItemId: 'goaria-download-link', linkUrl: 'https://cdn.test/list.m3u' }, tab)
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(state.notifications.at(-1)).toMatchObject({
      title: 'context_hls_unsupported_title',
      message: 'context_hls_unsupported_body',
    })
  })

  it('sends an ordinary link as download without pageTitle', async () => {
    await click({ menuItemId: 'goaria-download-link', linkUrl: 'https://cdn.test/file.bin' }, tab)
    expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    const req = lastRequest()
    expect(req.type).toBe('download')
    expect(req.pageTitle).toBeUndefined()
  })

  it('ignores clicks without a link/src url or with a non-http scheme', async () => {
    await click({ menuItemId: 'goaria-download-link' }, tab)
    await click({ menuItemId: 'goaria-download-link', srcUrl: 'data:image/png;base64,x' }, tab)
    expect(ws.sendDownloadRequest).not.toHaveBeenCalled()
    expect(state.notifications).toEqual([])
  })

  it('delegates the collect menu to domFlow only with the download.batch cap', async () => {
    await click({ menuItemId: 'goaria-collect-page-links', frameId: 0 }, tab)
    expect(state.collectCalls).toBe(1)
    state.capabilities = []
    await click({ menuItemId: 'goaria-collect-page-links', frameId: 0 }, tab)
    expect(state.collectCalls).toBe(1)
    expect(state.notifications.at(-1)).toMatchObject({
      title: 'dom_missing_cap_title',
      message: 'dom_missing_cap_body',
    })
  })

  it('sanitizes a page-controlled tab title before the hls handoff', async () => {
    await click(
      { menuItemId: 'goaria-download-link', linkUrl: 'https://cdn.test/live.m3u8' },
      { id: 7, title: 'Live\nShow' },
    )
    expect(ws.sendDownloadRequest).toHaveBeenCalledTimes(1)
    expect(lastRequest().pageTitle).toBe('LiveShow')
  })

  it('skips cookies for an incognito tab', async () => {
    await click(
      { menuItemId: 'goaria-download-link', linkUrl: 'https://cdn.test/file.bin' },
      { id: 7, incognito: true },
    )
    expect(cookiesMock).not.toHaveBeenCalled()
    expect(lastRequest().headers).toEqual([])
  })

  it('forwards the tab cookieStoreId to cookie collection', async () => {
    await click(
      { menuItemId: 'goaria-download-link', linkUrl: 'https://cdn.test/file.bin' },
      { id: 7, cookieStoreId: 'firefox-container-1' },
    )
    expect(cookiesMock).toHaveBeenCalledWith('https://cdn.test/file.bin', 'firefox-container-1')
    expect(lastRequest().headers).toEqual(['Cookie: sid=1'])
  })
})
