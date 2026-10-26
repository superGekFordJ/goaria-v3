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
      'page_title',
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

  it('projects page_title only when supplied as a string', () => {
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url: 'https://cdn.fixture.invalid/index.m3u8', page_title: 'Live Show' }],
      }),
    ).toEqual({
      payload: {
        items: [
          {
            client_item_id: itemId,
            url: 'https://cdn.fixture.invalid/index.m3u8',
            page_title: 'Live Show',
          },
        ],
      },
    })
    // Absent keeps the item payload byte-identical to before.
    expect(
      buildDirectBatchPayload({ items: [{ client_item_id: itemId, url }] }),
    ).toEqual({
      payload: { items: [{ client_item_id: itemId, url }] },
    })
  })

  it('rejects non-string page_title, camelCase, and strategy attempts', () => {
    for (const bad of [
      { page_title: 42 },
      { page_title: null },
      { page_title: ['x'] },
      { page_title: { x: 1 } },
    ]) {
      expect(
        buildDirectBatchPayload({ items: [{ client_item_id: itemId, url, ...bad }] }),
      ).toEqual({ error: 'page_title must be a string' })
    }
    for (const bad of [{ pageTitle: 'x' }, { strategy: 'hls' }, { mime_type: 'application/x-mpegurl' }]) {
      expect(
        buildDirectBatchPayload({ items: [{ client_item_id: itemId, url, ...bad }] }),
      ).toEqual({ error: 'unknown direct batch item field' })
    }
  })

  it('rejects page_title with control characters or beyond the byte cap', () => {
    for (const page_title of ['a\nb', 'x\ry', 'p\0q']) {
      expect(
        buildDirectBatchPayload({ items: [{ client_item_id: itemId, url, page_title }] }),
      ).toEqual({ error: 'page_title is invalid' })
    }
    // 342 * 3-byte chars = 1026 bytes > cap; exactly 1024 bytes stays legal.
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url, page_title: '€'.repeat(342) }],
      }),
    ).toEqual({ error: 'page_title is invalid' })
    expect(
      buildDirectBatchPayload({
        items: [{ client_item_id: itemId, url, page_title: 'x'.repeat(1024) }],
      }),
    ).toEqual({
      payload: { items: [{ client_item_id: itemId, url, page_title: 'x'.repeat(1024) }] },
    })
  })

  it('freezes allowlists', () => {
    expect(Object.isFrozen(DIRECT_BATCH_TOP_ALLOWLIST)).toBe(true)
    expect(Object.isFrozen(DIRECT_BATCH_ITEM_ALLOWLIST)).toBe(true)
  })
})
