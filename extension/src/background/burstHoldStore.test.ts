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
  BURST_HOLD_TTL_MS: 5 * 60 * 1000,
  STORAGE_KEY_BURST_HOLD_PREFIX: 'bhold_',
  STORAGE_KEY_BURST_WINDOW: 'bwin_window',
  PENDING_DECISION_TTL_MS: 30_000,
  STORAGE_KEY_PENDING_PREFIX: 'pending_',
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    storage: { session },
  },
}))

import { PENDING_DECISION_TTL_MS, STORAGE_KEY_PENDING_PREFIX } from '../stores/config.svelte'
import { getAllPendingDecisions } from './pendingDecisionStore'
import {
  getAllBurstHolds,
  getBurstHold,
  parseBurstHold,
  parseBurstWindow,
  saveBurstHold,
  saveBurstWindow,
} from './burstHoldStore'

describe('burstHoldStore', () => {
  beforeEach(() => {
    session.data.clear()
  })

  it('keeps pending_ TTL at 30s and ignores burst prefixes in the pending reader', async () => {
    expect(PENDING_DECISION_TTL_MS).toBe(30_000)
    expect(STORAGE_KEY_PENDING_PREFIX).toBe('pending_')
    await saveBurstHold(9, {
      url: 'https://cdn.example.test/a.bin',
      filename: 'a.bin',
      fileSize: 10,
      startTime: Date.now(),
      captureId: 'cap-1',
      referrer: 'https://example.test/',
      incognito: false,
    })
    session.data.set('pending_3', {
      url: 'https://cdn.example.test/b.bin',
      filename: 'b.bin',
      fileSize: 10,
      startTime: Date.now(),
      status: 'pending',
    })
    const pending = await getAllPendingDecisions()
    expect(pending.has(9)).toBe(false)
    expect(pending.has(3)).toBe(true)
    const holds = await getAllBurstHolds()
    expect(holds.has(9)).toBe(true)
    expect(holds.has(3)).toBe(false)
  })

  it('drops expired holds and rejects Cookie or header fields', async () => {
    expect(
      parseBurstHold({
        url: 'https://cdn.example.test/a.bin',
        filename: 'a.bin',
        fileSize: 1,
        startTime: Date.now(),
        captureId: 'cap-1',
        referrer: '',
        incognito: false,
        cookie: 'a=b',
      }),
    ).toBeNull()
    expect(
      parseBurstWindow({
        captureId: 'cap-1',
        downloadIds: [1],
        firstItemAt: Date.now(),
        lastItemAt: Date.now(),
        phase: 'coalescing',
        headers: ['Cookie: x'],
      }),
    ).toBeNull()
    await saveBurstHold(4, {
      url: 'https://cdn.example.test/a.bin',
      filename: 'a.bin',
      fileSize: 1,
      startTime: Date.now() - 5 * 60 * 1000 - 1,
      captureId: 'cap-1',
      referrer: '',
      incognito: false,
    })
    expect(await getBurstHold(4)).toBeNull()
  })

  it('persists a burst window snapshot without Cookie', async () => {
    await saveBurstWindow({
      captureId: 'cap-1',
      downloadIds: [1, 2],
      firstItemAt: 10,
      lastItemAt: 20,
      phase: 'picker',
      pickerDeadline: Date.now() + 60_000,
    })
    const raw = session.data.get('bwin_window') as Record<string, unknown>
    expect(raw.captureId).toBe('cap-1')
    expect(raw).not.toHaveProperty('cookie')
    expect(raw).not.toHaveProperty('headers')
  })
})
