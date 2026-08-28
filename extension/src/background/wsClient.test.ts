import { beforeEach, describe, expect, it, vi } from 'vitest'

const connection = vi.hoisted(() => ({
  capabilities: undefined as string[] | undefined,
  status: 'disconnected',
  wsPort: 0,
  paired: false,
  lastError: '',
  legacyHost: undefined as boolean | undefined,
}))

const domHostDown = vi.hoisted(() => ({
  notifyDomHostDown: vi.fn(),
  dropDomCatalogsOnReconnect: vi.fn(),
}))

vi.mock('../stores/config.svelte', () => ({
  CAP_EXTRACTOR_BATCH: 'extractor.batch',
  CAP_EXTRACTOR_RESOLVE: 'extractor.resolve',
  CAP_DOWNLOAD_BATCH: 'download.batch',
  CLIENT_VERSION: '0.2.0',
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

import { WsClient } from './wsClient'

describe('WsClient direct batch', () => {
  beforeEach(() => {
    connection.capabilities = undefined
    connection.status = 'disconnected'
    connection.paired = false
    domHostDown.notifyDomHostDown.mockClear()
    domHostDown.dropDomCatalogsOnReconnect.mockClear()
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
  })
})
