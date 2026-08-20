import { describe, expect, it } from 'vitest'
import {
  ackTimeoutMs,
  BATCH_DENYLIST,
  BATCH_ITEM_ERROR_CODES,
  buildExtractorBatchPayload,
  buildExtractorResolvePayload,
  EXTRACTOR_BATCH_TYPE,
  isRpcErrorCode,
  noteRpcTimeout,
  planRpcSend,
  RPC_ERROR_CODES,
  RpcRequestError,
  shouldRemoveReplayOnTimeout,
} from './extractorRpc'
import { createReplayStore, type ReplayStorage } from './replayStore'

describe('ackTimeoutMs', () => {
  it('uses 30s for extractor_resolve and 10s otherwise', () => {
    expect(ackTimeoutMs('extractor_resolve', 30_000, 10_000)).toBe(30_000)
    expect(ackTimeoutMs(EXTRACTOR_BATCH_TYPE, 30_000, 10_000)).toBe(10_000)
  })
})

describe('isRpcErrorCode', () => {
  it('covers the frozen nine-pack host error-code allowlist', () => {
    expect(isRpcErrorCode('unavailable')).toBe(true)
    expect(isRpcErrorCode('busy')).toBe(true)
    expect(isRpcErrorCode('invalid_request')).toBe(true)
    expect(isRpcErrorCode('idempotency_conflict')).toBe(true)
    expect(isRpcErrorCode('unsupported')).toBe(true)
    expect(isRpcErrorCode('auth_expired')).toBe(true)
    expect(isRpcErrorCode('timeout')).toBe(true)
    expect(isRpcErrorCode('pack_error')).toBe(true)
    expect(isRpcErrorCode('session_expired')).toBe(true)
    expect(RPC_ERROR_CODES.size).toBe(9)
    expect(Object.isFrozen(RPC_ERROR_CODES)).toBe(true)
    expect(isRpcErrorCode('matched')).toBe(false)
  })
})

describe('BATCH_ITEM_ERROR_CODES', () => {
  it('allows only the two opaque per-item strings', () => {
    expect([...BATCH_ITEM_ERROR_CODES]).toEqual(['item is not allowed', 'add failed'])
    expect(BATCH_ITEM_ERROR_CODES.size).toBe(2)
  })
})

describe('buildExtractorResolvePayload', () => {
  const validCookie = {
    name: 'sid',
    value: 'v',
    domain: '.alpha.test',
    path: '/',
    secure: true,
    host_only: false,
  }

  it('allowlists source_url cookies and optional headers and strips storeId', () => {
    const result = buildExtractorResolvePayload({
      source_url: 'https://share.alpha.test/s',
      cookies: [
        {
          ...validCookie,
          storeId: 'firefox-container-1',
          hostOnly: false,
          httpOnly: true,
        },
      ],
      user_agent: 'GoAria',
      accept_language: 'en',
      referer: 'https://share.alpha.test/',
      storeId: 'firefox-container-1',
      partitionKey: { topLevelSite: 'https://share.alpha.test' },
    })
    expect(result).toEqual({
      payload: {
        source_url: 'https://share.alpha.test/s',
        cookies: [validCookie],
        user_agent: 'GoAria',
        accept_language: 'en',
        referer: 'https://share.alpha.test/',
      },
    })
    if ('payload' in result) {
      expect(result.payload).not.toHaveProperty('storeId')
      expect(result.payload).not.toHaveProperty('partitionKey')
    }
  })

  it('refuses headers url and final_url instead of stripping them', () => {
    expect(
      buildExtractorResolvePayload({
        source_url: 'https://share.alpha.test/s',
        cookies: [validCookie],
        url: 'https://share.alpha.test/s',
      }),
    ).toEqual({ error: 'forbidden resolve field' })
    expect(
      buildExtractorResolvePayload({
        source_url: 'https://share.alpha.test/s',
        cookies: [validCookie],
        headers: ['Cookie: sid=v'],
      }),
    ).toEqual({ error: 'forbidden resolve field' })
    expect(
      buildExtractorResolvePayload({
        source_url: 'https://share.alpha.test/s',
        cookies: [validCookie],
        final_url: 'https://share.alpha.test/s',
      }),
    ).toEqual({ error: 'forbidden resolve field' })
  })

  it('refuses a missing source_url', () => {
    expect(buildExtractorResolvePayload({ cookies: [] })).toEqual({ error: 'source_url is required' })
  })

  it('refuses missing or non-array cookies', () => {
    expect(
      buildExtractorResolvePayload({ source_url: 'https://share.alpha.test/s' }),
    ).toEqual({ error: 'cookies is required' })
    expect(
      buildExtractorResolvePayload({
        source_url: 'https://share.alpha.test/s',
        cookies: { name: 'sid' },
      }),
    ).toEqual({ error: 'cookies is required' })
  })

  it('keeps a valid empty cookies array', () => {
    expect(
      buildExtractorResolvePayload({
        source_url: 'https://share.alpha.test/s',
        cookies: [],
      }),
    ).toEqual({
      payload: {
        source_url: 'https://share.alpha.test/s',
        cookies: [],
      },
    })
  })

  it('refuses a mixed array when any cookie fails projection', () => {
    const result = buildExtractorResolvePayload({
      source_url: 'https://share.alpha.test/s',
      cookies: [
        {
          name: 'sid',
          value: 'v',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          host_only: false,
        },
        {
          name: 'session',
          value: 'v',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          hostOnly: false,
        },
      ],
    })
    expect(result).toEqual({ error: 'cookie projection failed' })
  })

  it('refuses an all-invalid cookies array instead of sending an empty jar', () => {
    const result = buildExtractorResolvePayload({
      source_url: 'https://share.alpha.test/s',
      cookies: [
        {
          name: 'sid',
          value: 'v',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          hostOnly: false,
        },
      ],
    })
    expect(result).toEqual({ error: 'cookie projection failed' })
  })

  it('refuses an empty cookie name', () => {
    const result = buildExtractorResolvePayload({
      source_url: 'https://share.alpha.test/s',
      cookies: [
        {
          name: '',
          value: 'v',
          domain: '.alpha.test',
          path: '/',
          secure: true,
          host_only: false,
        },
      ],
    })
    expect(result).toEqual({ error: 'cookie projection failed' })
  })

  it('refuses a cookie with a non-empty partitionKey', () => {
    const result = buildExtractorResolvePayload({
      source_url: 'https://share.alpha.test/s',
      cookies: [
        {
          ...validCookie,
          partitionKey: { topLevelSite: 'https://share.alpha.test' },
        },
      ],
    })
    expect(result).toEqual({ error: 'cookie projection failed' })
  })

  it('keeps a cookie whose partitionKey is an empty object', () => {
    expect(
      buildExtractorResolvePayload({
        source_url: 'https://share.alpha.test/s',
        cookies: [{ ...validCookie, partitionKey: {} }],
      }),
    ).toEqual({
      payload: {
        source_url: 'https://share.alpha.test/s',
        cookies: [validCookie],
      },
    })
  })
})

describe('buildExtractorBatchPayload', () => {
  const sessionId = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  const itemId = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'

  it('projects session_id and item_ids and omits missing create_group', () => {
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [itemId],
        ignored: true,
      }),
    ).toEqual({
      payload: {
        session_id: sessionId,
        item_ids: [itemId],
      },
    })
  })

  it('projects create_group true and optional folder_name', () => {
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [itemId],
        create_group: true,
        folder_name: 'Album',
      }),
    ).toEqual({
      payload: {
        session_id: sessionId,
        item_ids: [itemId],
        create_group: true,
        folder_name: 'Album',
      },
    })
  })

  it('keeps the frozen deputy denylist', () => {
    expect(BATCH_DENYLIST).toEqual([
      'url',
      'final_url',
      'headers',
      'items',
      'cookies',
      'source_url',
      'auth_profile_ref',
      'header_profile_ref',
      'gid',
      'gids',
    ])
  })

  it.each(BATCH_DENYLIST)('refuses deputy key %s instead of stripping it', key => {
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [itemId],
        [key]: 'https://download.fixture.invalid/x',
      }),
    ).toEqual({ error: 'forbidden batch field' })
  })

  it('refuses a missing or blank session_id', () => {
    expect(buildExtractorBatchPayload({ item_ids: [itemId] })).toEqual({
      error: 'session_id is required',
    })
    expect(
      buildExtractorBatchPayload({ session_id: '', item_ids: [itemId] }),
    ).toEqual({
      error: 'session_id is required',
    })
    expect(
      buildExtractorBatchPayload({ session_id: '   ', item_ids: [itemId] }),
    ).toEqual({
      error: 'session_id is required',
    })
    expect(
      buildExtractorBatchPayload({
        session_id: `${sessionId} `,
        item_ids: [itemId],
      }),
    ).toEqual({ error: 'session_id must be trimmed' })
  })

  it('refuses a missing or non-array item_ids', () => {
    expect(buildExtractorBatchPayload({ session_id: sessionId })).toEqual({
      error: 'item_ids is required',
    })
    expect(
      buildExtractorBatchPayload({ session_id: sessionId, item_ids: 'x' }),
    ).toEqual({
      error: 'item_ids is required',
    })
  })

  it('projects an empty item_ids array for the host to reject', () => {
    expect(
      buildExtractorBatchPayload({ session_id: sessionId, item_ids: [] }),
    ).toEqual({
      payload: {
        session_id: sessionId,
        item_ids: [],
      },
    })
  })

  it('refuses a non-string, empty, or whitespace item_id', () => {
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [itemId, 1],
      }),
    ).toEqual({ error: 'item_id must be a string' })
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [''],
      }),
    ).toEqual({ error: 'item_id is required' })
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: ['   '],
      }),
    ).toEqual({ error: 'item_id is required' })
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [`${itemId} `],
      }),
    ).toEqual({ error: 'item_id must be trimmed' })
  })

  it('omits create_group false and whitespace-only folder_name', () => {
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [itemId],
        create_group: false,
        folder_name: '   ',
      }),
    ).toEqual({
      payload: {
        session_id: sessionId,
        item_ids: [itemId],
      },
    })
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [itemId],
        folder_name: '',
      }),
    ).toEqual({
      payload: {
        session_id: sessionId,
        item_ids: [itemId],
      },
    })
  })

  it('refuses an untrimmed folder_name', () => {
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [itemId],
        folder_name: '\nAlbum',
      }),
    ).toEqual({ error: 'folder_name must be trimmed' })
  })

  it('refuses a non-boolean create_group', () => {
    expect(
      buildExtractorBatchPayload({
        session_id: sessionId,
        item_ids: [itemId],
        create_group: 'true',
      }),
    ).toEqual({ error: 'create_group must be a boolean' })
  })
})

describe('planRpcSend', () => {
  it('ignores caller requestId and skips persist for extractor_resolve', () => {
    let n = 0
    const planned = planRpcSend('extractor_resolve', 'replay-id', () => {
      n += 1
      return `fresh-${n}`
    })
    expect(planned).toEqual({ id: 'fresh-1', persist: false })
  })

  it('reuses caller requestId and persists for other RPC types', () => {
    expect(planRpcSend(EXTRACTOR_BATCH_TYPE, 'replay-id', () => 'fresh')).toEqual({
      id: 'replay-id',
      persist: true,
    })
  })

  it('rejects omitted requestId for batch_download instead of minting', () => {
    expect(planRpcSend(EXTRACTOR_BATCH_TYPE, undefined, () => 'fresh')).toEqual({
      error: 'request_id is required',
    })
    expect(planRpcSend(EXTRACTOR_BATCH_TYPE, '   ', () => 'fresh')).toEqual({
      error: 'request_id is required',
    })
  })
})

function memoryReplayStorage(): ReplayStorage & { data: Map<string, unknown> } {
  const data = new Map<string, unknown>()
  return {
    data,
    async get(key) {
      return data.get(key)
    },
    async set(key, value) {
      data.set(key, value)
    },
    async remove(key) {
      data.delete(key)
    },
  }
}

describe('rpc timeout identity', () => {
  it('rejects with timeout and requestId so isRpcErrorCode works', () => {
    const err = new RpcRequestError('timeout', 'batch-uuid')
    expect(err.message).toBe('timeout')
    expect(isRpcErrorCode(err.message)).toBe(true)
    expect(err.requestId).toBe('batch-uuid')
    expect(shouldRemoveReplayOnTimeout(true)).toBe(false)
    expect(shouldRemoveReplayOnTimeout(false)).toBe(true)
  })

  it('does not delete replay for persist batch timeout', async () => {
    const storage = memoryReplayStorage()
    const store = createReplayStore(storage, 'replay_', 60_000, () => 1_000)
    await store.persist(EXTRACTOR_BATCH_TYPE, 'batch-uuid')
    const timed = noteRpcTimeout(true, 'batch-uuid')
    expect(timed.error.message).toBe('timeout')
    expect(timed.error.requestId).toBe('batch-uuid')
    expect(timed.dropReplay).toBe(false)
    if (timed.dropReplay) {
      await store.remove('batch-uuid')
    }
    expect(await store.load('batch-uuid')).not.toBeNull()
  })
})
