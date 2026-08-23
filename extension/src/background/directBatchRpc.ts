export const DIRECT_BATCH_TYPE = 'download_batch'
export const DIRECT_BATCH_STATUS_TYPE = 'download_batch_status'

export const DIRECT_BATCH_TOP_ALLOWLIST = ['items', 'create_group', 'folder_name'] as const

export const DIRECT_BATCH_ITEM_ALLOWLIST = [
  'client_item_id',
  'url',
  'final_url',
  'headers',
  'file_size',
  'skip_head_probe',
  'filename',
  'download_page',
] as const

export type DirectBatchBuildResult = { payload: Record<string, unknown> } | { error: string }

export type PlanDirectSendResult = { id: string; persist: boolean } | { error: string }

const TOP_ALLOWED = new Set<string>(DIRECT_BATCH_TOP_ALLOWLIST)
const ITEM_ALLOWED = new Set<string>(DIRECT_BATCH_ITEM_ALLOWLIST)

export function planDirectBatchSend(requestId: string | undefined): PlanDirectSendResult {
  const id = typeof requestId === 'string' ? requestId.trim() : ''
  if (!id) {
    return { error: 'request_id is required' }
  }
  return { id, persist: true }
}

export function planDirectBatchStatusSend(requestId: string | undefined): PlanDirectSendResult {
  const id = typeof requestId === 'string' ? requestId.trim() : ''
  if (!id) {
    return { error: 'request_id is required' }
  }
  return { id, persist: false }
}

export function buildDirectBatchPayload(input: Record<string, unknown>): DirectBatchBuildResult {
  for (const key of Object.keys(input)) {
    if (!TOP_ALLOWED.has(key)) {
      return { error: 'unknown direct batch field' }
    }
  }
  if (!Array.isArray(input.items)) {
    return { error: 'items is required' }
  }
  const items: Record<string, unknown>[] = []
  for (const raw of input.items) {
    if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
      return { error: 'item must be an object' }
    }
    const rec = raw as Record<string, unknown>
    for (const key of Object.keys(rec)) {
      if (!ITEM_ALLOWED.has(key)) {
        return { error: 'unknown direct batch item field' }
      }
    }
    if (typeof rec.client_item_id !== 'string' || rec.client_item_id === '') {
      return { error: 'client_item_id is required' }
    }
    if (typeof rec.url !== 'string' || rec.url === '') {
      return { error: 'url is required' }
    }
    const item: Record<string, unknown> = {
      client_item_id: rec.client_item_id,
      url: rec.url,
    }
    if ('final_url' in rec) {
      if (typeof rec.final_url !== 'string') {
        return { error: 'final_url must be a string' }
      }
      item.final_url = rec.final_url
    }
    if ('headers' in rec) {
      if (!Array.isArray(rec.headers) || rec.headers.some(h => typeof h !== 'string')) {
        return { error: 'headers must be strings' }
      }
      item.headers = rec.headers
    }
    if ('file_size' in rec) {
      if (
        typeof rec.file_size !== 'number' ||
        !Number.isInteger(rec.file_size) ||
        rec.file_size < 0
      ) {
        return { error: 'file_size must be a non-negative integer' }
      }
      item.file_size = rec.file_size
    }
    if ('skip_head_probe' in rec) {
      if (typeof rec.skip_head_probe !== 'boolean') {
        return { error: 'skip_head_probe must be a boolean' }
      }
      item.skip_head_probe = rec.skip_head_probe
    }
    if ('filename' in rec) {
      if (typeof rec.filename !== 'string') {
        return { error: 'filename must be a string' }
      }
      item.filename = rec.filename
    }
    if ('download_page' in rec) {
      if (typeof rec.download_page !== 'string') {
        return { error: 'download_page must be a string' }
      }
      item.download_page = rec.download_page
    }
    items.push(item)
  }
  const out: Record<string, unknown> = { items }
  if ('create_group' in input) {
    if (typeof input.create_group !== 'boolean') {
      return { error: 'create_group must be a boolean' }
    }
    if (input.create_group) {
      out.create_group = true
    }
  }
  if (typeof input.folder_name === 'string' && input.folder_name.trim() !== '') {
    out.folder_name = input.folder_name
  }
  return { payload: out }
}
