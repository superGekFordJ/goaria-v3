import { describe, expect, it } from 'vitest'
import { RpcRequestError } from './extractorRpc'
import { failedItemIds, isBatchSuccess, isResolveHit, mapCaughtError } from './extractorAck'

const TRAP = 'https://share.alpha.test/secret'

describe('isResolveHit', () => {
  it('rejects matched false, missing session_id, and unknown error_code', () => {
    expect(
      isResolveHit({
        matched: false,
        session_id: 'sess',
        items: [{ item_id: 'a' }],
        trap: TRAP,
      }),
    ).toBe(false)
    expect(isResolveHit({ matched: true, items: [{ item_id: 'a' }], trap: TRAP })).toBe(false)
    expect(
      isResolveHit({
        matched: true,
        session_id: 'sess',
        items: [{ item_id: 'a' }],
        error_code: 'weird',
        trap: TRAP,
      }),
    ).toBe(false)
  })

  it('rejects items that lack a trimmed item_id', () => {
    expect(
      isResolveHit({
        matched: true,
        session_id: 'sess',
        items: [{ filename: 'clip.bin' }],
        trap: TRAP,
      }),
    ).toBe(false)
    expect(
      isResolveHit({
        matched: true,
        session_id: 'sess',
        items: [{ item_id: 'a' }, { filename: 'other.bin' }],
        trap: TRAP,
      }),
    ).toBe(false)
    expect(
      isResolveHit({
        matched: true,
        session_id: 'sess',
        items: [{ item_id: ' a' }],
        trap: TRAP,
      }),
    ).toBe(false)
  })

  it('accepts a matched ack with session_id and usable item_ids', () => {
    expect(
      isResolveHit({
        matched: true,
        session_id: 'sess',
        items: [{ item_id: 'a', filename: 'clip.bin' }],
      }),
    ).toBe(true)
  })
})

describe('isBatchSuccess', () => {
  it('rejects success false and success true with a busy error_code', () => {
    expect(isBatchSuccess({ success: false, trap: TRAP })).toBe(false)
    expect(isBatchSuccess({ success: true, error_code: 'busy', trap: TRAP })).toBe(false)
  })

  it('accepts success true with an empty error_code', () => {
    expect(isBatchSuccess({ success: true, trap: TRAP })).toBe(true)
    expect(isBatchSuccess({ success: true, error_code: '', trap: TRAP })).toBe(true)
  })
})

describe('failedItemIds', () => {
  it('returns only allowlisted per-item errors and ignores trap URLs', () => {
    expect(
      failedItemIds({
        errors_by_item_id: {
          a: 'item is not allowed',
          b: 'add failed',
          c: 'engine exploded at ' + TRAP,
        },
        trap: TRAP,
      }),
    ).toEqual(['a', 'b'])
  })
})

describe('mapCaughtError', () => {
  it('maps rpc codes and local aliases without scanning hrefs', () => {
    expect(mapCaughtError(new RpcRequestError('timeout', 'id-1'))).toBe('timeout')
    expect(mapCaughtError(new Error('WebSocket is not connected'))).toBe('disconnected')
    expect(mapCaughtError(new Error('Host does not support extractor.batch'))).toBe('no_batch')
    expect(mapCaughtError(new Error('Host does not support extractor.resolve'))).toBe('unsupported')
    expect(mapCaughtError(new Error('Request already in flight'))).toBe('busy')
    expect(mapCaughtError(new Error('nope ' + TRAP))).toBe('generic')
  })
})
