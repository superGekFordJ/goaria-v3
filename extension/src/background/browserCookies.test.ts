import { describe, expect, it } from 'vitest'
import { collectStructuredCookies, mapBrowserCookie } from './browserCookies'

describe('mapBrowserCookie', () => {
  it('maps Chrome/Firefox fields onto the wire DTO including host_only', () => {
    const mapped = mapBrowserCookie({
      name: 'sid',
      value: 'browser-sid',
      domain: '.alpha.test',
      path: '/',
      secure: true,
      hostOnly: false,
      storeId: 'firefox-container-1',
    })
    expect(mapped).toEqual({
      cookie: {
        name: 'sid',
        value: 'browser-sid',
        domain: '.alpha.test',
        path: '/',
        secure: true,
        host_only: false,
      },
    })
  })

  it('returns an error when hostOnly is missing', () => {
    const mapped = mapBrowserCookie({
      name: 'sid',
      value: 'browser-sid',
      domain: 'alpha.test',
      path: '/',
      secure: true,
    })
    expect('error' in mapped).toBe(true)
  })

  it('returns an error when hostOnly is null', () => {
    const mapped = mapBrowserCookie({
      name: 'sid',
      value: 'browser-sid',
      domain: 'alpha.test',
      path: '/',
      secure: true,
      hostOnly: null,
    })
    expect('error' in mapped).toBe(true)
  })

  it('returns an error when secure is omitted', () => {
    const mapped = mapBrowserCookie({
      name: 'sid',
      value: 'browser-sid',
      domain: 'alpha.test',
      path: '/',
      hostOnly: false,
    })
    expect('error' in mapped).toBe(true)
  })

  it('returns an error when name is empty', () => {
    const mapped = mapBrowserCookie({
      name: '',
      value: 'v',
      domain: 'alpha.test',
      path: '/',
      secure: true,
      hostOnly: false,
    })
    expect('error' in mapped).toBe(true)
  })

  it('skips partitioned cookies so callers cannot bypass the collector', () => {
    const mapped = mapBrowserCookie({
      name: 'sid',
      value: 'chips',
      domain: '.alpha.test',
      path: '/',
      secure: true,
      hostOnly: false,
      partitionKey: { topLevelSite: 'https://share.alpha.test' },
    })
    expect(mapped).toEqual({ skip: true })
  })

  it('drops storeId and other off-wire fields', () => {
    const mapped = mapBrowserCookie({
      name: 'session',
      value: 'v',
      domain: 'api.alpha.test',
      path: '/s',
      secure: false,
      hostOnly: true,
      storeId: 'private',
      httpOnly: true,
      sameSite: 'lax',
      expirationDate: 1,
      session: false,
    })
    expect(mapped).toEqual({
      cookie: {
        name: 'session',
        value: 'v',
        domain: 'api.alpha.test',
        path: '/s',
        secure: false,
        host_only: true,
      },
    })
    if ('cookie' in mapped) {
      expect(mapped.cookie).not.toHaveProperty('storeId')
      expect(mapped.cookie).not.toHaveProperty('httpOnly')
      expect(mapped.cookie).not.toHaveProperty('sameSite')
      expect(mapped.cookie).not.toHaveProperty('expirationDate')
      expect(mapped.cookie).not.toHaveProperty('session')
      expect(mapped.cookie).not.toHaveProperty('partitionKey')
      expect(JSON.stringify(mapped.cookie)).not.toContain('Cookie:')
    }
  })
})

describe('collectStructuredCookies', () => {
  it('passes storeId through to getAll and maps host_only', async () => {
    const seen: Array<{ url: string; storeId: string }> = []
    const result = await collectStructuredCookies(
      'https://share.alpha.test/s',
      'firefox-container-1',
      async details => {
        seen.push(details)
        return [
          {
            name: 'sid',
            value: 'browser-sid',
            domain: '.alpha.test',
            path: '/',
            secure: true,
            hostOnly: false,
            storeId: 'firefox-container-1',
          },
        ]
      },
    )
    expect(seen).toEqual([{ url: 'https://share.alpha.test/s', storeId: 'firefox-container-1' }])
    expect(result).toEqual({
      cookies: [
        {
          name: 'sid',
          value: 'browser-sid',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          host_only: false,
        },
      ],
    })
  })

  it('passes a trimmed storeId to getAll', async () => {
    const seen: Array<{ url: string; storeId: string }> = []
    const result = await collectStructuredCookies(
      'https://share.alpha.test/s',
      '  firefox-container-1  ',
      async details => {
        seen.push(details)
        return []
      },
    )
    expect(seen).toEqual([{ url: 'https://share.alpha.test/s', storeId: 'firefox-container-1' }])
    expect(result).toEqual({ cookies: [] })
  })

  it('returns an error when storeId is empty', async () => {
    let called = false
    const result = await collectStructuredCookies('https://share.alpha.test/s', '', async () => {
      called = true
      return []
    })
    expect(called).toBe(false)
    expect(result).toEqual({ error: 'storeId is required' })
  })

  it('returns an error when storeId is whitespace', async () => {
    let called = false
    const result = await collectStructuredCookies('https://share.alpha.test/s', '   ', async () => {
      called = true
      return []
    })
    expect(called).toBe(false)
    expect(result).toEqual({ error: 'storeId is required' })
  })

  it('returns an error when url is not a valid http(s) host', async () => {
    let called = false
    const result = await collectStructuredCookies('ftp://share.alpha.test/s', 'store-1', async () => {
      called = true
      return []
    })
    expect(called).toBe(false)
    expect('error' in result).toBe(true)
  })

  it('returns a successful empty jar when getAll maps every cookie', async () => {
    const result = await collectStructuredCookies(
      'https://share.alpha.test/s',
      'store-1',
      async () => [],
    )
    expect(result).toEqual({ cookies: [] })
  })

  it('returns an error when any cookie is missing hostOnly', async () => {
    const result = await collectStructuredCookies(
      'https://share.alpha.test/s',
      'store-1',
      async () => [
        {
          name: 'sid',
          value: 'ok',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          hostOnly: false,
        },
        {
          name: 'session',
          value: 'partial',
          domain: '.alpha.test',
          path: '/',
          secure: true,
        },
      ],
    )
    expect('error' in result).toBe(true)
  })

  it('returns an error when getAll throws', async () => {
    const result = await collectStructuredCookies(
      'https://share.alpha.test/s',
      'store-1',
      async () => {
        throw new Error('cookies permission missing')
      },
    )
    expect(result).toEqual({ error: 'cookies.getAll failed' })
  })

  it('drops partitioned cookies and keeps the rest', async () => {
    const result = await collectStructuredCookies(
      'https://share.alpha.test/s',
      'store-1',
      async () => [
        {
          name: 'sid',
          value: 'ok',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          hostOnly: false,
        },
        {
          name: 'session',
          value: 'chips',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          hostOnly: false,
          partitionKey: { topLevelSite: 'https://share.alpha.test' },
        },
      ],
    )
    expect(result).toEqual({
      cookies: [
        {
          name: 'sid',
          value: 'ok',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          host_only: false,
        },
      ],
    })
  })

  it('keeps cookies whose partitionKey is an empty object', async () => {
    const result = await collectStructuredCookies(
      'https://share.alpha.test/s',
      'store-1',
      async () => [
        {
          name: 'sid',
          value: 'ok',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          hostOnly: false,
          partitionKey: {},
        },
      ],
    )
    expect(result).toEqual({
      cookies: [
        {
          name: 'sid',
          value: 'ok',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          host_only: false,
        },
      ],
    })
  })
})
