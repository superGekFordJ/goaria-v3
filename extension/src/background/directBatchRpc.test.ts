import { describe, expect, it } from 'vitest'
import {
  buildDirectBatchPayload,
  DIRECT_BATCH_ITEM_ALLOWLIST,
  DIRECT_BATCH_TOP_ALLOWLIST,
  DIRECT_BATCH_STATUS_TYPE,
  DIRECT_BATCH_TYPE,
  planDirectBatchSend,
  planDirectBatchStatusSend,
} from './directBatchRpc'

const itemId = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const url = 'https://cdn.fixture.invalid/file.bin'

describe('direct batch constants', () => {
  it('keeps wire types and allowlists frozen', () => {
    expect(DIRECT_BATCH_TYPE).toBe('download_batch')
    expect(DIRECT_BATCH_STATUS_TYPE).toBe('download_batch_status')
    expect(DIRECT_BATCH_TOP_ALLOWLIST).toEqual(['items', 'create_group', 'folder_name'])
    expect(DIRECT_BATCH_ITEM_ALLOWLIST).toEqual([
      'client_item_id',
      'url',
      'final_url',
      'headers',
      'file_size',
      'skip_head_probe',
      'filename',
      'download_page',
    ])
  })
})

describe('planDirectBatchSend', () => {
  it('requires caller request_id and persists', () => {
    expect(planDirectBatchSend('replay-id')).toEqual({ id: 'replay-id', persist: true })
    expect(planDirectBatchSend(undefined)).toEqual({ error: 'request_id is required' })
    expect(planDirectBatchSend('   ')).toEqual({ error: 'request_id is required' })
  })
})

describe('planDirectBatchStatusSend', () => {
  it('requires the original batch request_id and does not persist', () => {
    expect(planDirectBatchStatusSend('replay-id')).toEqual({ id: 'replay-id', persist: false })
    expect(planDirectBatchStatusSend(undefined)).toEqual({ error: 'request_id is required' })
    expect(planDirectBatchStatusSend('')).toEqual({ error: 'request_id is required' })
  })
})

describe('buildDirectBatchPayload', () => {
  it('projects required fields and omits create_group false', () => {
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url }],
        create_group: false,
        folder_name: '   ',
      }),
    ).toEqual({
      payload: {
        items: [{ client_item_id: itemId, url }],
      },
    })
  })

  it('projects optional item fields and create_group true', () => {
    expect(
      buildDirectBatchPayload({
        items: [
          {
            client_item_id: itemId,
            url,
            final_url: 'https://cdn.fixture.invalid/file.bin?x=1',
            headers: ['Referer: https://page.fixture.invalid/'],
            file_size: 12,
            skip_head_probe: true,
            filename: 'file.bin',
            download_page: 'https://page.fixture.invalid/',
          },
        ],
        create_group: true,
        folder_name: 'Album',
      }),
    ).toEqual({
      payload: {
        items: [
          {
            client_item_id: itemId,
            url,
            final_url: 'https://cdn.fixture.invalid/file.bin?x=1',
            headers: ['Referer: https://page.fixture.invalid/'],
            file_size: 12,
            skip_head_probe: true,
            filename: 'file.bin',
            download_page: 'https://page.fixture.invalid/',
          },
        ],
        create_group: true,
        folder_name: 'Album',
      },
    })
  })

  it('refuses unknown top-level keys', () => {
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url }],
        session_id: 'x',
      }),
    ).toEqual({ error: 'unknown direct batch field' })
  })

  it('refuses unknown item keys', () => {
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url, dedup_key: 'k' }],
      }),
    ).toEqual({ error: 'unknown direct batch item field' })
  })

  it('refuses a missing items array', () => {
    expect(buildDirectBatchPayload({})).toEqual({ error: 'items is required' })
  })

  it('refuses an untrimmed folder_name', () => {
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url }],
        folder_name: ' Album',
      }),
    ).toEqual({ error: 'folder_name must be trimmed' })
  })

  it('refuses folder_name with embedded control characters', () => {
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url }],
        folder_name: 'Alb\num',
      }),
    ).toEqual({ error: 'folder_name is invalid' })
  })

  it('refuses a non-string folder_name', () => {
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url }],
        folder_name: 1,
      }),
    ).toEqual({ error: 'folder_name must be a string' })
  })

  it('freezes allowlists', () => {
    expect(Object.isFrozen(DIRECT_BATCH_TOP_ALLOWLIST)).toBe(true)
    expect(Object.isFrozen(DIRECT_BATCH_ITEM_ALLOWLIST)).toBe(true)
  })
})
