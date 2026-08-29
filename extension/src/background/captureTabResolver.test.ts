import { beforeEach, describe, expect, it, vi } from 'vitest'

const tabs = vi.hoisted(() => ({
  all: [] as unknown[],
  focused: [] as unknown[],
  fail: false,
  queries: [] as Array<Record<string, unknown>>,
}))

vi.mock('webextension-polyfill', () => ({
  default: {
    tabs: {
      query: async (query: Record<string, unknown>) => {
        tabs.queries.push(query)
        if (tabs.fail) throw new Error('tabs unavailable')
        return query.active === true ? tabs.focused : tabs.all
      },
    },
  },
}))

import {
  originOf,
  pickPresentationTab,
  resolvePresentationTab,
  type TabCandidate,
} from './captureTabResolver'

const ORIGIN = 'https://example.test'

function tab(id: number, url: string, extra: Partial<TabCandidate> = {}): TabCandidate {
  return { id, url, incognito: false, ...extra }
}

describe('captureTabResolver', () => {
  beforeEach(() => {
    tabs.all = []
    tabs.focused = []
    tabs.fail = false
    tabs.queries = []
  })

  it('accepts trimmed http(s) origins and rejects unsupported values', () => {
    expect(originOf(' https://example.test/path ')).toBe(ORIGIN)
    expect(originOf('')).toBeNull()
    expect(originOf('not a url')).toBeNull()
    expect(originOf('file:///tmp/file')).toBeNull()
  })

  it('prefers an exact referrer match over same-origin siblings', () => {
    const result = pickPresentationTab(
      [tab(1, `${ORIGIN}/other`), tab(2, `${ORIGIN}/page#old`)],
      {
        referrer: `${ORIGIN}/page#new`,
        referrerOrigin: ORIGIN,
        incognito: false,
      },
    )
    expect(result?.id).toBe(2)
  })

  it('chooses the only matching origin candidate', () => {
    const result = pickPresentationTab(
      [tab(1, 'https://other.test/page'), tab(2, `${ORIGIN}/page`)],
      {
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
      },
    )
    expect(result?.id).toBe(2)
  })

  it('refuses ambiguous same-origin candidates without a focused match', () => {
    const result = pickPresentationTab(
      [tab(1, `${ORIGIN}/one`), tab(2, `${ORIGIN}/two`)],
      {
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
      },
    )
    expect(result).toBeNull()
  })

  it('uses the last-focused tab only among origin-proven candidates', () => {
    const result = pickPresentationTab(
      [tab(1, `${ORIGIN}/one`), tab(2, `${ORIGIN}/two`)],
      {
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
        lastFocusedTabId: 2,
      },
    )
    expect(result?.id).toBe(2)
  })

  it('picks the candidate whose cookieStoreId matches when two same-origin tabs differ', () => {
    const result = pickPresentationTab(
      [
        tab(1, `${ORIGIN}/page`, { cookieStoreId: 'firefox-default' }),
        tab(2, `${ORIGIN}/page`, { cookieStoreId: 'firefox-container-work' }),
      ],
      {
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
        lastFocusedTabId: 1,
        cookieStoreId: 'firefox-container-work',
      },
    )
    expect(result?.id).toBe(2)
  })

  it('returns null when no candidate matches the requested cookieStoreId', () => {
    const result = pickPresentationTab(
      [
        tab(1, `${ORIGIN}/page`, { cookieStoreId: 'firefox-default' }),
        tab(2, `${ORIGIN}/other`, { cookieStoreId: 'firefox-container-personal' }),
      ],
      {
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
        lastFocusedTabId: 1,
        cookieStoreId: 'firefox-container-work',
      },
    )
    expect(result).toBeNull()
  })

  it('filters incognito mismatches, discarded tabs, non-http(s), invalid URLs, and other origins', () => {
    const result = pickPresentationTab(
      [
        tab(1, `${ORIGIN}/private`, { incognito: true }),
        tab(2, `${ORIGIN}/discarded`, { discarded: true }),
        tab(3, 'file:///tmp/file'),
        tab(4, 'not a url'),
        tab(5, 'https://other.test/page'),
      ],
      {
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
      },
    )
    expect(result).toBeNull()
  })

  it('queries all tabs and resolves an eligible focused candidate', async () => {
    tabs.all = [tab(1, `${ORIGIN}/one`), tab(2, `${ORIGIN}/two`)]
    tabs.focused = [tab(2, `${ORIGIN}/two`)]

    await expect(
      resolvePresentationTab({
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
      }),
    ).resolves.toMatchObject({ id: 2 })
    expect(tabs.queries).toEqual([{}, { active: true, lastFocusedWindow: true }])
  })

  it('copies cookieStoreId from queried tabs and filters by store', async () => {
    tabs.all = [
      tab(1, `${ORIGIN}/page`, { cookieStoreId: 'firefox-default' }),
      tab(2, `${ORIGIN}/page`, { cookieStoreId: 'firefox-container-work' }),
    ]
    tabs.focused = [tab(1, `${ORIGIN}/page`, { cookieStoreId: 'firefox-default' })]

    await expect(
      resolvePresentationTab({
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
        cookieStoreId: 'firefox-container-work',
      }),
    ).resolves.toMatchObject({ id: 2, cookieStoreId: 'firefox-container-work' })
  })

  it('fails closed when a tab query throws', async () => {
    tabs.fail = true
    await expect(
      resolvePresentationTab({
        referrer: `${ORIGIN}/download`,
        referrerOrigin: ORIGIN,
        incognito: false,
      }),
    ).resolves.toBeNull()
  })
})
