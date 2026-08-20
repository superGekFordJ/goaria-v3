import { describe, expect, it } from 'vitest'
import { canReuseBatch } from './extractorBatchReuse'
import type { ExtractorSessionRecord } from './extractorSessionStore'

const TOKEN = 'a'.repeat(64)
const mint = () => 'minted-uuid'

function base(extra: Partial<ExtractorSessionRecord> = {}): ExtractorSessionRecord {
  return {
    tabId: 1,
    pageToken: TOKEN,
    generation: 1,
    state: 'error',
    sessionId: 'sess',
    itemIds: ['item-a'],
    ...extra,
  }
}

describe('canReuseBatch', () => {
  it('reuses the same UUID once for unavailable and invalid_request', () => {
    const first = canReuseBatch(
      base({ errorCode: 'unavailable', batchRequestId: 'batch-1', batchRetryUsed: false }),
      mint,
    )
    expect(first).toEqual({
      sessionId: 'sess',
      itemIds: ['item-a'],
      requestId: 'batch-1',
      markRetry: true,
    })
    expect(
      canReuseBatch(
        base({ errorCode: 'unavailable', batchRequestId: 'batch-1', batchRetryUsed: true }),
        mint,
      ),
    ).toBeNull()
    expect(
      canReuseBatch(
        base({ errorCode: 'invalid_request', batchRequestId: 'batch-1', batchRetryUsed: false }),
        mint,
      )?.requestId,
    ).toBe('batch-1')
  })

  it('mints a new request_id for partial single-file when the UUID was cleared', () => {
    const next = canReuseBatch(base({ errorCode: 'generic', batchRequestId: undefined }), mint)
    expect(next).toEqual({
      sessionId: 'sess',
      itemIds: ['item-a'],
      requestId: 'minted-uuid',
      markRetry: false,
    })
  })

  it('reuses the stored UUID on timeout and busy, and mints on idempotency_conflict', () => {
    expect(
      canReuseBatch(base({ errorCode: 'timeout', batchRequestId: 'batch-1' }), mint)?.requestId,
    ).toBe('batch-1')
    expect(
      canReuseBatch(base({ errorCode: 'busy', batchRequestId: 'batch-1' }), mint)?.markRetry,
    ).toBe(false)
    expect(
      canReuseBatch(base({ errorCode: 'idempotency_conflict', batchRequestId: 'old' }), mint)?.requestId,
    ).toBe('minted-uuid')
    expect(canReuseBatch(base({ errorCode: 'auth_expired', batchRequestId: 'batch-1' }), mint)).toBeNull()
  })

  it('reuses a timeout UUID for three remaining ids and rejects auth_expired', () => {
    const three = ['item-a', 'item-b', 'item-c']
    const timeout = canReuseBatch(
      base({ itemIds: three, errorCode: 'timeout', batchRequestId: 'batch-n' }),
      mint,
    )
    expect(timeout).toEqual({
      sessionId: 'sess',
      itemIds: three,
      requestId: 'batch-n',
      markRetry: false,
    })
    expect(
      canReuseBatch(
        base({ itemIds: three, errorCode: 'auth_expired', batchRequestId: 'batch-n' }),
        mint,
      ),
    ).toBeNull()
  })
})
