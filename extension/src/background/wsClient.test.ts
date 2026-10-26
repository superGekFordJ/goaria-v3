import { beforeEach, describe, expect, it, vi } from 'vitest'

const connection = vi.hoisted(() => ({
  capabilities: undefined as string[] | undefined,
  status: 'disconnected',
  wsPort: 0,
  paired: false,
  lastError: '',
  legacyHost: undefined as boolean | undefined,
}))

const captureHostDown = vi.hoisted(() => ({
  notifyCaptureHostDown: vi.fn(),
  dropCaptureOnReconnect: vi.fn(),
}))

const headerCapture = vi.hoisted(() => ({
  clearExtractorHeaderGrants: vi.fn(),
}))

import pkg from '../../package.json' with { type: 'json' }

const domHostDown = vi.hoisted(() => ({
  notifyDomHostDown: vi.fn(),
  dropDomCatalogsOnReconnect: vi.fn(),
}))

vi.mock('../stores/config.svelte', () => ({
  CAP_EXTRACTOR_BATCH: 'extractor.batch',
  CAP_EXTRACTOR_HEADER_CONTEXT: 'extractor.header_context',
  CAP_EXTRACTOR_RESOLVE: 'extractor.resolve',
  CAP_DOWNLOAD_BATCH: 'download.batch',
  CLIENT_VERSION: pkg.version,
  DOWNLOAD_ACK_TIMEOUT_MS: 10_000,
  EXTRACTOR_RESOLVE_ACK_TIMEOUT_MS: 30_000,
  MSG_TYPE_AUTH: 'auth',
  MSG_TYPE_AUTH_ACK: 'auth_ack',
  MSG_TYPE_BATCH_DOWNLOAD: 'batch_download',
  MSG_TYPE_BATCH_DOWNLOAD_ACK: 'batch_download_ack',
  MSG_TYPE_DOWNLOAD: 'download',
  MSG_TYPE_DOWNLOAD_BATCH_ACK: 'download_batch_ack',
  MSG_TYPE_DOWNLOAD_BATCH_STATUS_ACK: 'download_batch_status_ack',
  MSG_TYPE_EXTRACTOR_RESOLVE: 'extractor_resolve',
  MSG_TYPE_EXTRACTOR_RESOLVE_ACK: 'extractor_resolve_ack',
  MSG_TYPE_PROTOCOL_ERROR: 'protocol_error',
  PROTOCOL_VERSION: 2,
  RECONNECT_BASE_DELAY_MS: 5000,
  RECONNECT_MAX_ATTEMPTS: 120,
  REPLAY_TTL_MS: 60_000,
  REQUEST_ACK_TIMEOUT_MS: 10_000,
  STORAGE_KEY_REPLAY_PREFIX: 'replay_',
  STORAGE_KEY_SECRET: 'goaria_secret',
  WS_CONNECT_TIMEOUT_MS: 3000,
  WS_PORT_FALLBACKS: [16801, 16802, 16803],
  WS_SUBPROTOCOL: 'goaria-extension',
}))

vi.mock('webext-bridge/background', () => ({
  sendMessage: async () => undefined,
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    storage: {
      session: {
        get: async () => ({}),
        set: async () => undefined,
        remove: async () => undefined,
      },
    },
  },
}))

vi.mock('../stores/connection.svelte', () => ({
  connectionState: connection,
}))

vi.mock('./extractorVisibility', () => ({
  notifyExtractorHostDown: () => undefined,
  notifyExtractorMatchCleared: () => undefined,
}))

vi.mock('./domHostDown', () => ({
  notifyDomHostDown: (...args: unknown[]) => domHostDown.notifyDomHostDown(...args),
  dropDomCatalogsOnReconnect: (...args: unknown[]) =>
    domHostDown.dropDomCatalogsOnReconnect(...args),
}))

vi.mock('./captureHostDown', () => ({
  notifyCaptureHostDown: (...args: unknown[]) => captureHostDown.notifyCaptureHostDown(...args),
  dropCaptureOnReconnect: (...args: unknown[]) =>
    captureHostDown.dropCaptureOnReconnect(...args),
}))

vi.mock('./domConnectGeneration', () => ({
  bumpDirectConnectGeneration: () => 1,
  currentDirectConnectGeneration: () => 1,
}))

vi.mock('./matchSnapshot', () => ({
  applyParsedMatch: () => undefined,
  clearMatchSnapshot: () => undefined,
}))

vi.mock('./tabMatcher', () => ({
  rescanHttpTabs: async () => undefined,
}))

vi.mock('./browserHeaderCapture', () => ({
  clearExtractorHeaderGrants: (...args: unknown[]) =>
    headerCapture.clearExtractorHeaderGrants(...args),
  clearExtractorHeaderGrantsForTab: () => {},
  clearExtractorHeaderGrantsIfToken: () => {},
  takeHeaderGrantsForResolve: () => [],
  armExtractorHeaderCandidate: async () => {},
  initBrowserHeaderCapture: () => {},
}))

import { WsClient } from './wsClient'
import type { DownloadHandoffMessage } from '../utils/messaging'

describe('WsClient download handoff', () => {
  let sent: string[]

  function attachSocket(client: WsClient): void {
    sent = []
    ;(
      client as unknown as {
        ws: { readyState: number; send(data: string): void }
      }
    ).ws = {
      readyState: 1,
      send(data: string) {
        sent.push(data)
      },
    }
  }

  function feed(client: WsClient, msg: Record<string, unknown>): void {
    ;(
      client as unknown as { handleMessage: (ev: { data: string }) => void }
    ).handleMessage({ data: JSON.stringify(msg) })
  }

  function lastSent(): Record<string, unknown> {
    const raw = sent.at(-1)
    expect(raw).toBeDefined()
    return JSON.parse(raw as string) as Record<string, unknown>
  }

  function handoff(partial: Partial<DownloadHandoffMessage> = {}): DownloadHandoffMessage {
    return {
      url: 'https://cdn.fixture.test/live.m3u8',
      finalUrl: '',
      headers: [],
      fileSize: 0,
      skipHeadProbe: true,
      dedupKey: '',
      filename: 'live.m3u8',
      downloadPage: 'https://example.test/page',
      ...partial,
    }
  }

  beforeEach(() => {
    vi.stubGlobal('WebSocket', { OPEN: 1, CLOSED: 3 })
    connection.capabilities = undefined
    connection.status = 'disconnected'
    connection.paired = false
    sent = []
  })

  it('maps an explicit hls type and page_title onto the wire', async () => {
    const client = new WsClient()
    attachSocket(client)
    const done = client.sendDownloadRequest(
      handoff({ type: 'hls', pageTitle: 'Live Show Page' }),
    )
    await vi.waitFor(() => expect(sent).toHaveLength(1))
    const body = lastSent()
    expect(body.type).toBe('hls')
    expect(body.page_title).toBe('Live Show Page')
    expect(body.url).toBe('https://cdn.fixture.test/live.m3u8')
    expect(body.final_url).toBe('')
    expect(body.skip_head_probe).toBe(true)
    expect(body.download_page).toBe('https://example.test/page')
    expect(body).not.toHaveProperty('pageTitle')
    expect(typeof body.request_id).toBe('string')
    feed(client, {
      type: 'download_ack',
      request_id: body.request_id,
      success: true,
      gid: 'ar_hls',
    })
    await expect(done).resolves.toMatchObject({ type: 'download_ack', success: true, gid: 'ar_hls' })
  })

  it('defaults a missing request type to download and omits an empty page_title', async () => {
    const client = new WsClient()
    attachSocket(client)
    const done = client.sendDownloadRequest(handoff({ url: 'https://cdn.fixture.test/f.bin' }))
    await vi.waitFor(() => expect(sent).toHaveLength(1))
    const body = lastSent()
    expect(body.type).toBe('download')
    expect(body).not.toHaveProperty('page_title')
    feed(client, {
      type: 'download_ack',
      request_id: body.request_id,
      success: true,
      gid: 'ar_dl',
    })
    await expect(done).resolves.toMatchObject({ success: true })
  })

  it('rejects the in-flight hls request when an old host answers protocol_error', async () => {
    const client = new WsClient()
    attachSocket(client)
    const hlsReq = client.sendDownloadRequest(handoff({ type: 'hls', pageTitle: 'Show' }))
    await vi.waitFor(() => expect(sent).toHaveLength(1))
    feed(client, {
      type: 'protocol_error',
      request_id: lastSent().request_id,
      error_code: 'unsupported',
    })
    await expect(hlsReq).rejects.toThrow('unsupported')
    // The pending entry is cleared: a follow-up send still works and resolves.
    const next = client.sendDownloadRequest(handoff({ url: 'https://cdn.fixture.test/f.bin' }))
    await vi.waitFor(() => expect(sent).toHaveLength(2))
    const body = lastSent()
    feed(client, {
      type: 'download_ack',
      request_id: body.request_id,
      success: true,
      gid: 'ar_dl',
    })
    await expect(next).resolves.toMatchObject({ success: true })
  })

  it('surfaces a failed download_ack on an hls request', async () => {
    const client = new WsClient()
    attachSocket(client)
    const hlsReq = client.sendDownloadRequest(handoff({ type: 'hls' }))
    await vi.waitFor(() => expect(sent).toHaveLength(1))
    feed(client, {
      type: 'download_ack',
      request_id: lastSent().request_id,
      success: false,
      error: 'engine busy',
    })
    await expect(hlsReq).rejects.toThrow('engine busy')
  })
})

describe('WsClient direct batch', () => {
  beforeEach(() => {
    connection.capabilities = undefined
    connection.status = 'disconnected'
    connection.paired = false
    domHostDown.notifyDomHostDown.mockClear()
    domHostDown.dropDomCatalogsOnReconnect.mockClear()
    captureHostDown.notifyCaptureHostDown.mockClear()
    captureHostDown.dropCaptureOnReconnect.mockClear()
  })

  it('does not send download_batch through sendRequest', async () => {
    const client = new WsClient()
    await expect(client.sendRequest('download_batch', { items: [] }, 'id')).rejects.toThrow(
      'sendRequest does not send download_batch',
    )
  })

  it('does not send download_batch_status through sendRequest', async () => {
    const client = new WsClient()
    await expect(client.sendRequest('download_batch_status', {}, 'id')).rejects.toThrow(
      'sendRequest does not send download_batch_status',
    )
  })

  it('fail-closes sendDirectBatch without download.batch', async () => {
    const client = new WsClient()
    connection.capabilities = ['request_id', 'extractor.batch']
    await expect(
      client.sendDirectBatch(
        { items: [{ client_item_id: 'a'.repeat(32), url: 'https://cdn.fixture.invalid/x' }] },
        'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',
      ),
    ).rejects.toThrow('Host does not support download.batch')
  })

  it('fail-closes sendDirectBatchStatus without download.batch', async () => {
    const client = new WsClient()
    await expect(
      client.sendDirectBatchStatus('aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'),
    ).rejects.toThrow('Host does not support download.batch')
  })

  it('rejects sendDirectBatch when the socket is closed even with the cap', async () => {
    const client = new WsClient()
    connection.capabilities = ['request_id', 'download.batch']
    await expect(
      client.sendDirectBatch(
        { items: [{ client_item_id: 'a'.repeat(32), url: 'https://cdn.fixture.invalid/x' }] },
        'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',
      ),
    ).rejects.toThrow('WebSocket is not connected')
  })

  it('drops DOM catalogs on auth_ack', () => {
    const client = new WsClient()
    ;(
      client as unknown as { handleMessage: (ev: { data: string }) => void }
    ).handleMessage({
      data: JSON.stringify({ type: 'auth_ack', protocol_version: 2, capabilities: [] }),
    })
    expect(domHostDown.dropDomCatalogsOnReconnect).toHaveBeenCalledTimes(1)
    expect(domHostDown.notifyDomHostDown).not.toHaveBeenCalled()
    expect(captureHostDown.dropCaptureOnReconnect).toHaveBeenCalledTimes(1)
    expect(captureHostDown.notifyCaptureHostDown).not.toHaveBeenCalled()
  })
})

describe('WsClient browser header grants', () => {
  const grant = {
    source_origin: 'https://share.alpha.test',
    target_url: 'https://api.alpha.test/v1/item?id=fixture',
    method: 'GET',
    captured_at_unix_ms: Date.now() - 1_000,
    expires_at_unix_ms: Date.now() + 59_000,
    headers: [{ name: 'authorization', value: 'Bearer fixture-token' }],
  }
  let sent: string[]

  function attachSocket(client: WsClient): void {
    sent = []
    ;(
      client as unknown as {
        ws: { readyState: number; send(data: string): void }
      }
    ).ws = {
      readyState: 1,
      send(data: string) {
        sent.push(data)
      },
    }
  }

  beforeEach(() => {
    vi.stubGlobal('WebSocket', { OPEN: 1, CLOSED: 3 })
    connection.capabilities = undefined
    connection.status = 'disconnected'
    connection.paired = false
    headerCapture.clearExtractorHeaderGrants.mockClear()
    sent = []
  })

  it('clears armed grants on every auth_ack', () => {
    const client = new WsClient()
    ;(
      client as unknown as { handleMessage: (ev: { data: string }) => void }
    ).handleMessage({
      data: JSON.stringify({ type: 'auth_ack', protocol_version: 2, capabilities: [] }),
    })
    expect(headerCapture.clearExtractorHeaderGrants).toHaveBeenCalledTimes(1)
  })

  it('clears armed grants on disconnect', () => {
    const client = new WsClient()
    client.disconnect()
    expect(headerCapture.clearExtractorHeaderGrants).toHaveBeenCalled()
  })

  it('rejects browser_header_grants on resolve without the exact capability', async () => {
    const client = new WsClient()
    attachSocket(client)
    connection.capabilities = ['request_id', 'extractor.resolve', 'extractor.batch']
    await expect(
      client.sendRequest('extractor_resolve', {
        source_url: 'https://share.alpha.test/s',
        cookies: [],
        browser_header_grants: [grant],
      }),
    ).rejects.toThrow('forbidden resolve field')
    expect(sent).toEqual([])
  })

  it('rejects browser_header_grants on resolve under a near-match capability', async () => {
    const client = new WsClient()
    attachSocket(client)
    connection.capabilities = [
      'request_id',
      'extractor.resolve',
      'extractor.header_context.extra',
    ]
    await expect(
      client.sendRequest('extractor_resolve', {
        source_url: 'https://share.alpha.test/s',
        cookies: [],
        browser_header_grants: [grant],
      }),
    ).rejects.toThrow('forbidden resolve field')
    expect(sent).toEqual([])
  })

  it('sends projected grants with a fresh request_id when the capability is granted', async () => {
    const client = new WsClient()
    attachSocket(client)
    connection.capabilities = [
      'request_id',
      'extractor.resolve',
      'extractor.batch',
      'extractor.header_context',
    ]
    const pending = client.sendRequest('extractor_resolve', {
      source_url: 'https://share.alpha.test/s',
      cookies: [],
      request_id: 'caller-supplied-id',
      browser_header_grants: [grant],
    })
    await vi.waitFor(() => expect(sent).toHaveLength(1))
    const body = JSON.parse(sent[0]!) as Record<string, unknown>
    expect(body.type).toBe('extractor_resolve')
    expect(body.request_id).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
    )
    expect(body.request_id).not.toBe('caller-supplied-id')
    expect(body.browser_header_grants).toEqual([grant])
    expect(body).not.toHaveProperty('headers')
    expect(body).not.toHaveProperty('url')
    ;(
      client as unknown as { handleMessage: (ev: { data: string }) => void }
    ).handleMessage({
      data: JSON.stringify({
        type: 'extractor_resolve_ack',
        request_id: body.request_id,
        matched: false,
        items: [],
      }),
    })
    await expect(pending).resolves.toMatchObject({ matched: false })
  })
})
