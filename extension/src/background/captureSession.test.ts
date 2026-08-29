import { beforeEach, describe, expect, it, vi } from 'vitest'

const session = vi.hoisted(() => {
  const data = new Map<string, unknown>()
  return {
    data,
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
  }
})

vi.mock('../stores/config.svelte', () => ({
  STORAGE_KEY_CAPTURE_SESSION: 'cap_session',
  STORAGE_KEY_CAPTURE_PREFIX: 'cap_',
  CAPTURE_SESSION_TTL_MS: 10_000,
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    storage: { session },
  },
}))

import {
  disarmCaptureSession,
  getCaptureSession,
  parseCaptureSession,
  writeCaptureSession,
  type CaptureSession,
} from './captureSession'

const SAMPLE: CaptureSession = {
  captureId: 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee',
  tabId: 4,
  documentNonce: 'nonce-1',
  pageHref: 'https://example.test/page#frag',
  incognito: false,
  storeUnproven: true,
  directConnectGeneration: 1,
  createdAt: Date.now(),
}

describe('captureSession', () => {
  beforeEach(() => {
    session.data.clear()
  })

  it('uses the cap_ prefix and stores a single global record', async () => {
    expect(await writeCaptureSession(SAMPLE)).toBe(true)
    expect(session.data.has('cap_session')).toBe(true)
    expect(await getCaptureSession()).toEqual(SAMPLE)
    expect(await writeCaptureSession({ ...SAMPLE, captureId: 'other' })).toBe(false)
    expect((await getCaptureSession())?.captureId).toBe(SAMPLE.captureId)
    await disarmCaptureSession()
    expect(await getCaptureSession()).toBeNull()
  })

  it('rejects cookie or header fields and expired records', () => {
    const now = Date.now()
    const fresh = { ...SAMPLE, createdAt: now }
    expect(parseCaptureSession({ ...fresh, cookie: 'a=b' }, now)).toBeNull()
    expect(parseCaptureSession({ ...fresh, headers: ['Cookie: a'] }, now)).toBeNull()
    expect(parseCaptureSession(fresh, now + 10_001)).toBeNull()
    expect(parseCaptureSession(fresh, now + 9_000)).toEqual(fresh)
  })

  it('accepts a session before the snapshot nonce exists', () => {
    const withoutNonce = { ...SAMPLE }
    delete withoutNonce.documentNonce
    expect(parseCaptureSession(withoutNonce, withoutNonce.createdAt)).toEqual(withoutNonce)
  })
})
