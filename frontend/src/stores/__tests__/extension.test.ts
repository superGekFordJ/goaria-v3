import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useExtensionStore } from '../extension'

const bindingMocks = vi.hoisted(() => ({
  GetExtensionStatus: vi.fn(),
  PairExtension: vi.fn(),
  UnpairExtension: vi.fn(),
  RegeneratePairing: vi.fn(),
  OpenPairingURLInBrowser: vi.fn(),
}))

vi.mock('../../../bindings/goaria-v3/internal/wailsapp/app.js', () => bindingMocks)

const eventsMock = vi.hoisted(() => {
  const handlers: Record<string, ((ev: unknown) => void) | undefined> = {}
  return {
    On: vi.fn((event: string, handler: (ev: unknown) => void) => {
      handlers[event] = handler
      return () => {
        delete handlers[event]
      }
    }),
    emit: (event: string, data?: unknown) => {
      handlers[event]?.(data)
    },
  }
})

vi.mock('@wailsio/runtime', () => ({
  Events: eventsMock,
}))

vi.mock('../../utils/clipboard', () => ({
  copyToClipboard: vi.fn().mockResolvedValue(true),
  clearClipboardIfMatches: vi.fn().mockResolvedValue(undefined),
}))

describe('extension store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('pair() sets pairingPanelOpen and pairUrl after successful PairExtension', async () => {
    bindingMocks.PairExtension.mockResolvedValue('http://127.0.0.1:16810/pair')
    bindingMocks.GetExtensionStatus.mockResolvedValue({
      status: 'listening',
      ws_port: 16801,
      connected_clients: 0,
      paired: false,
    })

    const store = useExtensionStore()
    await store.pair()

    expect(store.pairUrl).toBe('http://127.0.0.1:16810/pair')
    expect(store.pairingPanelOpen).toBe(true)
    expect(store.pairing).toBe(false)
  })

  it('pair() does not open panel when PairExtension returns empty', async () => {
    bindingMocks.PairExtension.mockResolvedValue('')
    bindingMocks.GetExtensionStatus.mockResolvedValue({
      status: 'listening',
      ws_port: 16801,
      connected_clients: 0,
      paired: false,
    })

    const store = useExtensionStore()
    await store.pair()

    expect(store.pairUrl).toBe('')
    expect(store.pairingPanelOpen).toBe(false)
  })

  it('extension:paired listener closes panel, clears pairUrl, and clears clipboard', async () => {
    bindingMocks.GetExtensionStatus.mockResolvedValue({
      status: 'paired',
      ws_port: 16801,
      connected_clients: 1,
      paired: true,
    })

    const { clearClipboardIfMatches } = await import('../../utils/clipboard')

    const store = useExtensionStore()
    store.pairingPanelOpen = true
    store.pairUrl = 'http://127.0.0.1:16810/pair'
    store.subscribeToEvents()

    eventsMock.emit('extension:paired')

    expect(store.pairingPanelOpen).toBe(false)
    expect(store.pairUrl).toBe('')
    expect(store.paired).toBe(true)
    expect(clearClipboardIfMatches).toHaveBeenCalledWith('http://127.0.0.1:16810/pair')
  })

  it('extension:auth_failed listener sets paired=false and surfaces notice', async () => {
    const store = useExtensionStore()
    store.paired = true
    store.status = 'paired'
    store.pairUrl = 'http://127.0.0.1:16810/pair'
    store.subscribeToEvents()

    eventsMock.emit('extension:auth_failed')

    expect(store.paired).toBe(false)
    expect(store.status).toBe('listening')
    expect(store.pairUrl).toBe('')
    expect(store.authFailedNotice).toBe(true)
  })

  it('regenerate() calls RegeneratePairing and updates pairUrl', async () => {
    bindingMocks.RegeneratePairing.mockResolvedValue('http://127.0.0.1:16811/pair')

    const store = useExtensionStore()
    store.pairUrl = 'http://127.0.0.1:16810/pair'
    await store.regenerate()

    expect(bindingMocks.RegeneratePairing).toHaveBeenCalledOnce()
    expect(store.pairUrl).toBe('http://127.0.0.1:16811/pair')
    expect(store.regenerating).toBe(false)
  })

  it('regenerate() guards against concurrent calls', async () => {
    bindingMocks.RegeneratePairing.mockResolvedValue('http://127.0.0.1:16811/pair')

    const store = useExtensionStore()
    const p1 = store.regenerate()
    const p2 = store.regenerate()
    await Promise.all([p1, p2])

    expect(bindingMocks.RegeneratePairing).toHaveBeenCalledOnce()
  })

  it('unpair() sets unpairRotatedNotice and reflects unpaired state', async () => {
    bindingMocks.UnpairExtension.mockResolvedValue(undefined)
    bindingMocks.GetExtensionStatus.mockResolvedValue({
      status: 'listening',
      ws_port: 16801,
      connected_clients: 0,
      paired: false,
    })

    const store = useExtensionStore()
    store.paired = true
    store.showUnpairConfirm = true
    await store.unpair()

    expect(store.paired).toBe(false)
    expect(store.status).toBe('listening')
    expect(store.pairUrl).toBe('')
    expect(store.unpairRotatedNotice).toBe(true)
    expect(store.showUnpairConfirm).toBe(false)
  })

  it('requestUnpair() sets showUnpairConfirm to true', () => {
    const store = useExtensionStore()
    expect(store.showUnpairConfirm).toBe(false)
    store.requestUnpair()
    expect(store.showUnpairConfirm).toBe(true)
  })

  it('cancelUnpair() resets showUnpairConfirm to false', () => {
    const store = useExtensionStore()
    store.showUnpairConfirm = true
    store.cancelUnpair()
    expect(store.showUnpairConfirm).toBe(false)
  })

  it('openInBrowser() calls OpenPairingURLInBrowser binding', async () => {
    bindingMocks.OpenPairingURLInBrowser.mockResolvedValue(undefined)

    const store = useExtensionStore()
    await store.openInBrowser('http://127.0.0.1:16810/pair')

    expect(bindingMocks.OpenPairingURLInBrowser).toHaveBeenCalledWith('http://127.0.0.1:16810/pair')
  })
})
