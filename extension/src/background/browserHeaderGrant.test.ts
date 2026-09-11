import { describe, expect, it } from 'vitest'
import {
  canonicalGrantTargetUrl,
  canonicalSourceOrigin,
  createHeaderGrantStore,
  HEADER_GRANT_MAX_CANDIDATES,
  HEADER_GRANT_MAX_HEADERS,
  HEADER_GRANT_MAX_PER_RESOLVE,
  HEADER_GRANT_OBSERVE_WINDOW_MS,
  HEADER_GRANT_TTL_MS,
  projectBrowserHeaderGrants,
  type HeaderGrantStore,
} from './browserHeaderGrant'

const PAGE_URL = 'https://share.alpha.test/item/aaa'
const PAGE_ORIGIN = 'https://share.alpha.test'
const TARGET = 'https://api.alpha.test/v1/item?id=fixture'
const START = 1_700_000_000_000

function harness(opts: { target?: 'chrome' | 'firefox' } = {}) {
  let now = START
  let generation = 7
  const store = createHeaderGrantStore({
    isFirefoxTarget: () => (opts.target ?? 'chrome') === 'firefox',
    isGenerationCurrent: gen => gen === generation,
    now: () => now,
  })
  return {
    store,
    advance(ms: number) {
      now += ms
    },
    bumpGeneration() {
      generation += 1
    },
  }
}

type ArmInput = Parameters<HeaderGrantStore['arm']>[0]

function armInput(over: Partial<ArmInput> = {}): ArmInput {
  return {
    tabId: 1,
    pageToken: 'token-a',
    sourceUrl: PAGE_URL,
    generation: 7,
    incognito: false,
    ...over,
  }
}

type ObserveInput = Parameters<HeaderGrantStore['observe']>[0]

function xhr(over: Partial<ObserveInput> = {}): ObserveInput {
  return {
    tabId: 1,
    type: 'xmlhttprequest',
    method: 'GET',
    url: TARGET,
    headers: [{ name: 'Authorization', value: 'Bearer fixture-token' }],
    initiator: PAGE_ORIGIN,
    incognito: false,
    ...over,
  }
}

describe('canonicalSourceOrigin', () => {
  it('normalizes scheme, host case, and default port; drops path/query/fragment', () => {
    expect(canonicalSourceOrigin(PAGE_URL)).toBe(PAGE_ORIGIN)
    expect(canonicalSourceOrigin('HTTPS://SHARE.ALPHA.TEST:443/x?q=1#f')).toBe(PAGE_ORIGIN)
    expect(canonicalSourceOrigin('http://share.alpha.test/x')).toBe('http://share.alpha.test')
    expect(canonicalSourceOrigin('https://share.alpha.test:8443/x')).toBe(
      'https://share.alpha.test:8443',
    )
  })

  it('rejects non-http, userinfo, IP hosts, extension schemes, and malformed input', () => {
    expect(canonicalSourceOrigin('')).toBeUndefined()
    expect(canonicalSourceOrigin('not a url')).toBeUndefined()
    expect(canonicalSourceOrigin('ftp://share.alpha.test/')).toBeUndefined()
    expect(canonicalSourceOrigin('chrome-extension://abc/page.html')).toBeUndefined()
    expect(canonicalSourceOrigin('https://user:pw@share.alpha.test/')).toBeUndefined()
    expect(canonicalSourceOrigin('https://127.0.0.1/')).toBeUndefined()
    expect(canonicalSourceOrigin('https://share.alpha.test./')).toBeUndefined()
  })
})

describe('canonicalGrantTargetUrl', () => {
  it('keeps the exact escaped path and query order', () => {
    expect(canonicalGrantTargetUrl(TARGET)).toBe(TARGET)
    expect(canonicalGrantTargetUrl('https://api.alpha.test/v1?b=2&a=1')).toBe(
      'https://api.alpha.test/v1?b=2&a=1',
    )
    expect(canonicalGrantTargetUrl('https://api.alpha.test/v1?a=1&b=2')).not.toBe(
      'https://api.alpha.test/v1?b=2&a=1',
    )
  })

  it('normalizes host case and default port; root path becomes /', () => {
    expect(canonicalGrantTargetUrl('HTTPS://API.ALPHA.TEST:443/v1')).toBe(
      'https://api.alpha.test/v1',
    )
    expect(canonicalGrantTargetUrl('https://api.alpha.test')).toBe('https://api.alpha.test/')
  })

  it('rejects http, userinfo, fragment, and oversized input', () => {
    expect(canonicalGrantTargetUrl('http://api.alpha.test/v1')).toBeUndefined()
    expect(canonicalGrantTargetUrl('https://user@api.alpha.test/v1')).toBeUndefined()
    expect(canonicalGrantTargetUrl('https://api.alpha.test/v1#frag')).toBeUndefined()
    expect(canonicalGrantTargetUrl('')).toBeUndefined()
    expect(canonicalGrantTargetUrl('nope')).toBeUndefined()
    expect(canonicalGrantTargetUrl(`https://api.alpha.test/${'q'.repeat(4096)}`)).toBeUndefined()
  })
})

describe('arm', () => {
  it('arms only well-formed current-generation candidates', () => {
    const { store } = harness()
    expect(store.arm(armInput())).toBe(true)
    expect(store.arm(armInput({ tabId: -1 }))).toBe(false)
    expect(store.arm(armInput({ tabId: 1.5 }))).toBe(false)
    expect(store.arm(armInput({ pageToken: '' }))).toBe(false)
    expect(store.arm(armInput({ sourceUrl: 'ftp://share.alpha.test/' }))).toBe(false)
    expect(store.arm(armInput({ incognito: undefined }))).toBe(false)
    expect(store.arm(armInput({ cookieStoreId: '   ' }))).toBe(false)
    expect(store.arm(armInput({ cookieStoreId: ' firefox-default ' }))).toBe(false)
  })

  it('keeps captured grants on an identical re-arm', () => {
    const { store, advance } = harness()
    store.arm(armInput())
    store.observe(xhr())
    advance(30_000)
    // A re-delivery of the same binding refreshes the live candidate
    // instead of wiping the grants captured during page load.
    expect(store.arm(armInput())).toBe(true)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toHaveLength(1)
  })

  it('refreshes the observation window on an identical re-arm', () => {
    const { store, advance } = harness()
    store.arm(armInput())
    advance(HEADER_GRANT_OBSERVE_WINDOW_MS - 1)
    expect(store.arm(armInput())).toBe(true)
    // The original deadline has passed; the refreshed window still accepts.
    advance(1)
    expect(store.observe(xhr())).toBe(true)
  })

  it('wipes captured grants when the re-arm binding differs', () => {
    const { store } = harness()
    store.arm(armInput())
    store.observe(xhr())
    expect(store.arm(armInput({ pageToken: 'token-b' }))).toBe(true)
    expect(store.take({ tabId: 1, pageToken: 'token-b', incognito: false })).toEqual([])
  })

  it('rebuilds instead of refreshing when the old candidate already expired', () => {
    const { store, advance } = harness()
    store.arm(armInput())
    store.observe(xhr())
    advance(HEADER_GRANT_OBSERVE_WINDOW_MS)
    expect(store.arm(armInput())).toBe(true)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it('refuses to arm a stale generation', () => {
    const { store, bumpGeneration } = harness()
    bumpGeneration()
    expect(store.arm(armInput({ generation: 7 }))).toBe(false)
  })

  it('bounds candidates and evicts the oldest tab', () => {
    const { store } = harness()
    for (let tabId = 1; tabId <= HEADER_GRANT_MAX_CANDIDATES; tabId += 1) {
      expect(store.arm(armInput({ tabId, pageToken: `tok-${tabId}` }))).toBe(true)
    }
    expect(store.arm(armInput({ tabId: 99, pageToken: 'tok-99' }))).toBe(true)
    // tab 1 was evicted: its observation is refused even though the input is valid.
    expect(store.observe(xhr({ tabId: 1 }))).toBe(false)
    expect(store.observe(xhr({ tabId: 99, initiator: PAGE_ORIGIN }))).toBe(true)
  })

  it('evicts the soonest-expiring candidate rather than the insertion slot', () => {
    const { store, advance } = harness()
    for (let tabId = 1; tabId < HEADER_GRANT_MAX_CANDIDATES; tabId += 1) {
      expect(store.arm(armInput({ tabId, pageToken: `tok-${tabId}` }))).toBe(true)
    }
    advance(5_000)
    // The identical re-arm refreshes tab 1 past its original deadline while
    // keeping its early insertion slot.
    expect(store.arm(armInput({ tabId: 1, pageToken: 'tok-1' }))).toBe(true)
    expect(
      store.arm(armInput({ tabId: HEADER_GRANT_MAX_CANDIDATES, pageToken: 'tok-max' })),
    ).toBe(true)
    expect(store.arm(armInput({ tabId: 99, pageToken: 'tok-99' }))).toBe(true)
    // tab 2 expired soonest and was evicted; the refreshed tab 1 survives.
    expect(store.observe(xhr({ tabId: 2 }))).toBe(false)
    expect(store.observe(xhr({ tabId: 1 }))).toBe(true)
    expect(store.observe(xhr({ tabId: 99 }))).toBe(true)
  })
})

describe('observe eligibility', () => {
  it('captures authorization and business x-* headers with lowercase sorted names', () => {
    const { store } = harness()
    expect(store.arm(armInput())).toBe(true)
    const seen = store.observe(
      xhr({
        headers: [
          { name: 'X-Request-Proof', value: 'proof-fixture' },
          { name: 'Cookie', value: 'sid=fixture' },
          { name: 'Authorization', value: 'Bearer fixture-token' },
          { name: 'User-Agent', value: 'FixtureBrowser/1.0' },
        ],
      }),
    )
    expect(seen).toBe(true)
    const grants = store.take({ tabId: 1, pageToken: 'token-a', incognito: false })
    expect(grants).toHaveLength(1)
    expect(grants[0]?.headers.map(h => h.name)).toEqual(['authorization', 'x-request-proof'])
    expect(grants[0]?.source_origin).toBe(PAGE_ORIGIN)
    expect(grants[0]?.target_url).toBe(TARGET)
    expect(grants[0]?.method).toBe('GET')
    expect(grants[0]?.expires_at_unix_ms).toBe(START + HEADER_GRANT_TTL_MS)
    expect(grants[0]?.captured_at_unix_ms).toBe(START)
  })

  it('accepts HEAD and rejects other methods and resource types', () => {
    const { store } = harness()
    store.arm(armInput())
    expect(store.observe(xhr({ method: 'HEAD' }))).toBe(true)
    expect(store.observe(xhr({ method: 'POST' }))).toBe(false)
    expect(store.observe(xhr({ method: 'OPTIONS' }))).toBe(false)
    expect(store.observe(xhr({ method: 'get' }))).toBe(false)
    expect(store.observe(xhr({ type: 'main_frame' }))).toBe(false)
    expect(store.observe(xhr({ type: 'image' }))).toBe(false)
    expect(store.observe(xhr({ type: 'beacon' }))).toBe(false)
  })

  it('ignores ordinary browser headers entirely and creates no grant', () => {
    const { store } = harness()
    store.arm(armInput())
    const seen = store.observe(
      xhr({
        headers: [
          { name: 'Cookie', value: 'sid=fixture' },
          { name: 'Referer', value: PAGE_URL },
          { name: 'Origin', value: PAGE_ORIGIN },
          { name: 'Host', value: 'api.alpha.test' },
          { name: 'Accept-Language', value: 'en' },
          { name: 'Sec-Fetch-Mode', value: 'cors' },
          { name: 'Proxy-Authorization', value: 'Basic fixture' },
          { name: 'Content-Length', value: '0' },
          { name: 'Connection', value: 'keep-alive' },
        ],
      }),
    )
    expect(seen).toBe(false)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it.each([
    'x-forwarded-for',
    'x-forwarded-host',
    'x-real-ip',
    'x-client-ip',
    'x-host',
    'x-original-url',
    'x-rewrite-url',
    'x-http-method',
    'x-http-method-override',
    'x-method-override',
    'x-original-host',
    'x-original-path',
    'x-original-method',
    'x-override-method',
    'x-proxy-auth',
    'x-goaria-internal',
  ])('never copies denied routing/proxy name %s into a grant', name => {
    const { store } = harness()
    store.arm(armInput())
    const seen = store.observe(
      xhr({
        headers: [
          { name, value: 'fixture-value' },
          { name: 'Authorization', value: 'Bearer fixture-token' },
        ],
      }),
    )
    expect(seen).toBe(true)
    const grants = store.take({ tabId: 1, pageToken: 'token-a', incognito: false })
    expect(grants[0]?.headers.map(h => h.name)).toEqual(['authorization'])
  })

  it('rejects non-token x-* names while still capturing authorization', () => {
    const { store } = harness()
    store.arm(armInput())
    const seen = store.observe(
      xhr({
        headers: [
          { name: 'x bad name', value: 'fixture' },
          { name: 'Authorization', value: 'Bearer fixture-token' },
        ],
      }),
    )
    expect(seen).toBe(true)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })[0]?.headers).toEqual([
      { name: 'authorization', value: 'Bearer fixture-token' },
    ])
  })

  it.each([
    ['leading whitespace', ' Bearer fixture'],
    ['trailing whitespace', 'Bearer fixture '],
    ['empty after trim', '   '],
    ['CR injection', 'Bearer fixture\rX'],
    ['LF injection', 'Bearer fixture\nX'],
    ['NUL injection', 'Bearer fixture\0X'],
    ['interior tab', 'Bearer fixture\tX'],
    ['interior control byte', 'Bearer fixture' + String.fromCharCode(1) + 'X'],
    ['DEL byte', 'Bearer fixture' + String.fromCharCode(0x7f) + 'X'],
  ])('rejects the whole observation on %s in an eligible value', (_kind, value) => {
    const { store } = harness()
    store.arm(armInput())
    expect(
      store.observe(
        xhr({
          headers: [
            { name: 'Authorization', value },
            { name: 'X-Request-Proof', value: 'ok-fixture' },
          ],
        }),
      ),
    ).toBe(false)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it('rejects a binary-only eligible header value', () => {
    const { store } = harness()
    store.arm(armInput())
    expect(
      store.observe(xhr({ headers: [{ name: 'authorization', binaryValue: [1, 2, 3] }] })),
    ).toBe(false)
  })

  it('rejects case-insensitive duplicate eligible names instead of choosing a winner', () => {
    const { store } = harness()
    store.arm(armInput())
    expect(
      store.observe(
        xhr({
          headers: [
            { name: 'Authorization', value: 'Bearer one' },
            { name: 'AUTHORIZATION', value: 'Bearer two' },
          ],
        }),
      ),
    ).toBe(false)
  })

  it('rejects observations exceeding header count, name, value, or aggregate bounds', () => {
    const { store } = harness()
    store.arm(armInput())
    const many = Array.from({ length: HEADER_GRANT_MAX_HEADERS + 1 }, (_, i) => ({
      name: `x-fixture-${i}`,
      value: 'v',
    }))
    expect(store.observe(xhr({ headers: many }))).toBe(false)

    const longName = `x-${'n'.repeat(200)}`
    expect(
      store.observe(xhr({ headers: [{ name: longName, value: 'v' }] })),
    ).toBe(false)

    expect(
      store.observe(xhr({ headers: [{ name: 'x-fixture', value: 'v'.repeat(5000) }] })),
    ).toBe(false)

    const aggregate = Array.from({ length: HEADER_GRANT_MAX_HEADERS }, (_, i) => ({
      name: `x-fixture-${i}`,
      value: 'v'.repeat(1200),
    }))
    expect(store.observe(xhr({ headers: aggregate }))).toBe(false)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it('keeps only the freshest grant per exact method+target scope', () => {
    const { store, advance } = harness()
    store.arm(armInput())
    expect(store.observe(xhr({ headers: [{ name: 'Authorization', value: 'Bearer first' }] }))).toBe(true)
    advance(1000)
    expect(store.observe(xhr({ headers: [{ name: 'Authorization', value: 'Bearer second' }] }))).toBe(true)
    const grants = store.take({ tabId: 1, pageToken: 'token-a', incognito: false })
    expect(grants).toHaveLength(1)
    expect(grants[0]?.captured_at_unix_ms).toBe(START + 1000)
  })

  it('bounds distinct exact scopes and evicts the oldest', () => {
    const { store, advance } = harness()
    store.arm(armInput())
    for (let i = 0; i < HEADER_GRANT_MAX_PER_RESOLVE + 2; i += 1) {
      advance(10)
      expect(store.observe(xhr({ url: `https://api.alpha.test/v1/item?id=f${i}` }))).toBe(true)
    }
    const grants = store.take({ tabId: 1, pageToken: 'token-a', incognito: false })
    expect(grants).toHaveLength(HEADER_GRANT_MAX_PER_RESOLVE)
    expect(grants.some(g => g.target_url.endsWith('id=f0'))).toBe(false)
    expect(grants.some(g => g.target_url.endsWith('id=f9'))).toBe(true)
  })

  it('returns defensive copies so captured headers cannot be mutated through the take result', () => {
    const { store } = harness()
    store.arm(armInput())
    const headers = [{ name: 'Authorization', value: 'Bearer fixture-token' }]
    expect(store.observe(xhr({ headers }))).toBe(true)
    headers[0]!.value = 'mutated'
    const grants = store.take({ tabId: 1, pageToken: 'token-a', incognito: false })
    expect(grants[0]?.headers[0]?.value).toBe('Bearer fixture-token')
    grants[0]!.headers[0]!.value = 'caller-mutated'
    grants[0]!.headers.push({ name: 'x-injected', value: 'x' })
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })
})

describe('observe association', () => {
  it('refuses events from unknown or non-tab contexts', () => {
    const { store } = harness()
    store.arm(armInput())
    expect(store.observe(xhr({ tabId: 2 }))).toBe(false)
    expect(store.observe(xhr({ tabId: -1 }))).toBe(false)
  })

  it('refuses when the match generation moved on and drops the stale candidate', () => {
    const { store, bumpGeneration } = harness()
    store.arm(armInput())
    bumpGeneration()
    expect(store.observe(xhr())).toBe(false)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it('refuses after the observation window and drops the expired candidate', () => {
    const { store, advance } = harness()
    store.arm(armInput())
    advance(HEADER_GRANT_OBSERVE_WINDOW_MS)
    expect(store.observe(xhr())).toBe(false)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it('refuses http targets, non-canonical targets, and wrong incognito state', () => {
    const { store } = harness()
    store.arm(armInput())
    expect(store.observe(xhr({ url: 'http://api.alpha.test/v1' }))).toBe(false)
    expect(store.observe(xhr({ url: 'https://api.alpha.test/v1#frag' }))).toBe(false)
    expect(store.observe(xhr({ url: '' }))).toBe(false)
    expect(store.observe(xhr({ incognito: true }))).toBe(false)
    // Chrome may omit details.incognito; a split-mode non-incognito context
    // cannot observe incognito events, so absence is treated as false.
    expect(store.observe(xhr({ incognito: undefined }))).toBe(true)
  })

  it('requires the chrome initiator to canonicalize exactly to the source origin', () => {
    const { store } = harness()
    store.arm(armInput())
    expect(store.observe(xhr({ initiator: 'https://other.alpha.test' }))).toBe(false)
    expect(store.observe(xhr({ initiator: undefined }))).toBe(false)
    expect(store.observe(xhr({ initiator: 'chrome-extension://abc' }))).toBe(false)
    expect(store.observe(xhr({ initiator: 'null' }))).toBe(false)
    // Same registrable domain but different origin still fails.
    expect(store.observe(xhr({ initiator: 'https://deep.share.alpha.test' }))).toBe(false)
    expect(store.observe(xhr({ initiator: 'https://share.alpha.test/other/page' }))).toBe(true)
  })
})

describe('firefox source and store association', () => {
  function fxXhr(over: Partial<ObserveInput> = {}): ObserveInput {
    return xhr({
      initiator: undefined,
      originUrl: PAGE_URL,
      cookieStoreId: 'firefox-default',
      ...over,
    })
  }

  function fxArm(over: Partial<ArmInput> = {}): ArmInput {
    return armInput({ cookieStoreId: 'firefox-default', ...over })
  }

  it('matches originUrl or documentUrl canonical origin against the candidate', () => {
    const { store } = harness({ target: 'firefox' })
    store.arm(fxArm())
    expect(store.observe(fxXhr())).toBe(true)
    expect(store.observe(fxXhr({ originUrl: undefined, documentUrl: PAGE_URL }))).toBe(true)
    expect(store.observe(fxXhr({ originUrl: undefined, documentUrl: undefined }))).toBe(false)
  })

  it('rejects disagreement between usable source fields', () => {
    const { store } = harness({ target: 'firefox' })
    store.arm(fxArm())
    expect(store.observe(fxXhr({ documentUrl: 'https://other.alpha.test/page' }))).toBe(false)
    expect(store.observe(fxXhr({ originUrl: 'https://other.alpha.test/x' }))).toBe(false)
  })

  it('fails closed on present-but-non-string source signals', () => {
    const { store } = harness({ target: 'firefox' })
    store.arm(fxArm())
    expect(store.observe(fxXhr({ originUrl: 42 }))).toBe(false)
    expect(store.observe(fxXhr({ documentUrl: '' }))).toBe(false)
  })

  it('requires both candidate and event cookie stores to be non-empty and equal', () => {
    const { store } = harness({ target: 'firefox' })
    store.arm(fxArm())
    expect(store.observe(fxXhr({ cookieStoreId: 'firefox-container-1' }))).toBe(false)
    expect(store.observe(fxXhr({ cookieStoreId: undefined }))).toBe(false)
    expect(store.observe(fxXhr({ cookieStoreId: '' }))).toBe(false)
  })

  it('fails closed when the armed candidate has no store context', () => {
    const { store } = harness({ target: 'firefox' })
    store.arm(fxArm({ cookieStoreId: undefined }))
    expect(store.observe(fxXhr())).toBe(false)
  })

  it('requires an explicit incognito field on firefox', () => {
    const { store } = harness({ target: 'firefox' })
    store.arm(fxArm())
    expect(store.observe(fxXhr({ incognito: undefined }))).toBe(false)
    expect(store.observe(fxXhr({ incognito: true }))).toBe(false)
    expect(store.observe(fxXhr({ incognito: false }))).toBe(true)
  })
})

describe('take one-shot lifecycle', () => {
  it('consumes grants once; a second take returns nothing', () => {
    const { store } = harness()
    store.arm(armInput())
    store.observe(xhr())
    const first = store.take({ tabId: 1, pageToken: 'token-a', incognito: false })
    expect(first).toHaveLength(1)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it('drops expired grants instead of returning them', () => {
    const { store, advance } = harness()
    store.arm(armInput())
    store.observe(xhr())
    advance(HEADER_GRANT_TTL_MS + 1)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it.each([
    ['page token', { pageToken: 'token-b' }],
    ['incognito', { incognito: true }],
    ['missing incognito', { incognito: undefined }],
  ])('discards the candidate on %s mismatch', (_kind, over) => {
    const { store } = harness()
    store.arm(armInput())
    store.observe(xhr())
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false, ...over })).toEqual([])
    // The mismatch cleared the candidate: a later well-formed take still gets nothing.
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })

  it('enforces the firefox store binding on take', () => {
    const { store } = harness({ target: 'firefox' })
    store.arm(armInput({ cookieStoreId: 'firefox-default' }))
    store.observe(
      xhr({ initiator: undefined, originUrl: PAGE_URL, cookieStoreId: 'firefox-default' }),
    )
    expect(
      store.take({ tabId: 1, pageToken: 'token-a', incognito: false, cookieStoreId: 'other' }),
    ).toEqual([])
  })

  it('returns every live grant up to the resolve cap', () => {
    const { store, advance } = harness()
    store.arm(armInput())
    for (let i = 0; i < 3; i += 1) {
      advance(10)
      store.observe(xhr({ url: `https://api.alpha.test/v1/item?id=g${i}` }))
    }
    const grants = store.take({ tabId: 1, pageToken: 'token-a', incognito: false })
    expect(grants).toHaveLength(3)
    expect(JSON.stringify(grants)).toContain('target_url')
  })
})

describe('clearTab / clearAll', () => {
  it('clears a single tab without touching others', () => {
    const { store } = harness()
    store.arm(armInput({ tabId: 1, pageToken: 'token-a' }))
    store.arm(armInput({ tabId: 2, pageToken: 'token-b' }))
    store.observe(xhr({ tabId: 1 }))
    store.observe(xhr({ tabId: 2 }))
    store.clearTab(1)
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
    expect(store.take({ tabId: 2, pageToken: 'token-b', incognito: false })).toHaveLength(1)
  })

  it('clears every candidate on clearAll', () => {
    const { store } = harness()
    store.arm(armInput({ tabId: 1, pageToken: 'token-a' }))
    store.arm(armInput({ tabId: 2, pageToken: 'token-b' }))
    store.clearAll()
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
    expect(store.take({ tabId: 2, pageToken: 'token-b', incognito: false })).toEqual([])
  })

  it('clearTabIfToken drops only the candidate still bound to that token', () => {
    const { store } = harness()
    store.arm(armInput({ tabId: 1, pageToken: 'token-a' }))
    store.observe(xhr({ tabId: 1 }))
    // A nav report for a different (older) page must not touch it.
    store.clearTabIfToken(1, 'token-old')
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toHaveLength(1)
    store.arm(armInput({ tabId: 1, pageToken: 'token-a' }))
    store.clearTabIfToken(1, 'token-a')
    expect(store.take({ tabId: 1, pageToken: 'token-a', incognito: false })).toEqual([])
  })
})

describe('projectBrowserHeaderGrants outbound gate', () => {
  const validGrant = {
    source_origin: PAGE_ORIGIN,
    target_url: TARGET,
    method: 'GET',
    captured_at_unix_ms: START,
    expires_at_unix_ms: START + HEADER_GRANT_TTL_MS,
    headers: [
      { name: 'x-request-proof', value: 'proof-fixture' },
      { name: 'authorization', value: 'Bearer fixture-token' },
    ],
  }

  it('projects a valid grant list and sorts header names deterministically', () => {
    const out = projectBrowserHeaderGrants([validGrant])
    expect(out).toHaveLength(1)
    expect(out?.[0]?.headers.map(h => h.name)).toEqual(['authorization', 'x-request-proof'])
    expect(out?.[0]?.method).toBe('GET')
  })

  it.each([
    ['non-array', 'x'],
    ['empty array', []],
    ['oversized array', Array.from({ length: HEADER_GRANT_MAX_PER_RESOLVE + 1 }, () => validGrant)],
    ['non-object item', [null]],
    ['extra transport key', [{ ...validGrant, request_id: 'x' }]],
    ['missing key', [{ source_origin: PAGE_ORIGIN }]],
    ['bad method', [{ ...validGrant, method: 'POST' }]],
    ['lowercase method', [{ ...validGrant, method: 'get' }]],
    ['non-canonical source origin', [{ ...validGrant, source_origin: `${PAGE_ORIGIN}/page` }]],
    ['http target', [{ ...validGrant, target_url: 'http://api.alpha.test/v1' }]],
    ['fragment target', [{ ...validGrant, target_url: `${TARGET}#f` }]],
    ['zero captured', [{ ...validGrant, captured_at_unix_ms: 0 }]],
    ['expires before captured', [{ ...validGrant, expires_at_unix_ms: START - 1 }]],
    ['ttl exceeded', [{ ...validGrant, expires_at_unix_ms: START + HEADER_GRANT_TTL_MS + 1 }]],
    ['duplicate scope', [validGrant, { ...validGrant }]],
    ['empty headers', [{ ...validGrant, headers: [] }]],
    ['headers not array', [{ ...validGrant, headers: {} }]],
    ['header extra key', [{ ...validGrant, headers: [{ name: 'authorization', value: 'v', secure: true }] }]],
    ['uppercase header name', [{ ...validGrant, headers: [{ name: 'Authorization', value: 'v' }] }]],
    ['ineligible header name', [{ ...validGrant, headers: [{ name: 'cookie', value: 'sid=1' }] }]],
    ['denied x-* name', [{ ...validGrant, headers: [{ name: 'x-forwarded-for', value: '1.2.3.4' }] }]],
    ['crlf value', [{ ...validGrant, headers: [{ name: 'authorization', value: 'a\nb' }] }]],
    ['padded value', [{ ...validGrant, headers: [{ name: 'authorization', value: ' v' }] }]],
    ['denied cousin name', [{ ...validGrant, headers: [{ name: 'x-original-host', value: 'h' }] }]],
    ['interior ctl value', [{ ...validGrant, headers: [{ name: 'authorization', value: 'a' + String.fromCharCode(2) + 'b' }] }]],
  ])('rejects %s', (_kind, value) => {
    expect(projectBrowserHeaderGrants(value)).toBeUndefined()
  })

  it('rejects an already-expired grant only when a clock is supplied', () => {
    const expired = { ...validGrant, expires_at_unix_ms: START + 10 }
    expect(projectBrowserHeaderGrants([expired], START + 20)).toBeUndefined()
    expect(projectBrowserHeaderGrants([validGrant], START + 1)).toHaveLength(1)
    // Without a clock the shape-only contract still validates.
    expect(projectBrowserHeaderGrants([expired])).toHaveLength(1)
  })
})

describe('module surface', () => {
  it('exposes only synchronous in-memory operations', () => {
    const { store } = harness()
    const keys = Object.keys(store).sort()
    expect(keys).toEqual(['arm', 'clearAll', 'clearTab', 'clearTabIfToken', 'observe', 'take'])
  })
})
