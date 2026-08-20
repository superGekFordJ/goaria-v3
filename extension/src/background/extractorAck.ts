import { BATCH_ITEM_ERROR_CODES, isRpcErrorCode, RpcRequestError } from './extractorRpc'
import { sanitizeDisplayFilename } from './extractorKeys'

export { sanitizeDisplayFilename } from './extractorKeys'

function usableResolveItems(items: unknown): Array<{ item_id: string; filename?: string }> {
  if (!Array.isArray(items)) return []
  const out: Array<{ item_id: string; filename?: string }> = []
  for (const item of items) {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return []
    const rec = item as Record<string, unknown>
    if (typeof rec.item_id !== 'string') return []
    const id = rec.item_id.trim()
    if (id === '' || id !== rec.item_id) return []
    out.push({ item_id: id, filename: sanitizeDisplayFilename(rec.filename) })
  }
  return out
}

export function isResolveHit(msg: unknown): boolean {
  if (!msg || typeof msg !== 'object' || Array.isArray(msg)) return false
  const rec = msg as Record<string, unknown>
  if (rec.matched !== true) return false
  if (typeof rec.session_id !== 'string' || rec.session_id === '') return false
  if (!Array.isArray(rec.items) || rec.items.length < 1) return false
  if (typeof rec.error_code === 'string' && rec.error_code !== '') return false
  return resolveItemsAreComplete(rec.items)
}

export function resolveItemsAreComplete(items: unknown): boolean {
  if (!Array.isArray(items) || items.length < 1) return false
  return usableResolveItems(items).length === items.length
}

export function isBatchSuccess(msg: unknown): boolean {
  if (!msg || typeof msg !== 'object' || Array.isArray(msg)) return false
  const rec = msg as Record<string, unknown>
  if (rec.success !== true) return false
  if (typeof rec.error_code === 'string' && rec.error_code !== '') return false
  return true
}

export function failedItemIds(msg: unknown): string[] {
  if (!msg || typeof msg !== 'object' || Array.isArray(msg)) return []
  const rec = msg as Record<string, unknown>
  const errors = rec.errors_by_item_id
  if (!errors || typeof errors !== 'object' || Array.isArray(errors)) return []
  const ids: string[] = []
  for (const [id, value] of Object.entries(errors as Record<string, unknown>)) {
    if (typeof value === 'string' && BATCH_ITEM_ERROR_CODES.has(value)) {
      ids.push(id)
    }
  }
  return ids
}

const MESSAGE_ALIASES: Record<string, string> = {
  'WebSocket is not connected': 'disconnected',
  'WebSocket disconnected': 'disconnected',
  'WebSocket closed': 'disconnected',
  'Host does not support extractor.batch': 'no_batch',
  'Host does not support extractor.resolve': 'unsupported',
  'Request already in flight': 'busy',
}

export function mapCaughtError(err: unknown): string {
  let message = ''
  if (err instanceof RpcRequestError) {
    message = err.message
  } else if (err instanceof Error) {
    message = err.message
  } else if (typeof err === 'string') {
    message = err
  }
  if (isRpcErrorCode(message)) return message
  if (message in MESSAGE_ALIASES) return MESSAGE_ALIASES[message]
  if (message === 'no_store' || message === 'cookie_error' || message === 'no_batch') {
    return message
  }
  return 'generic'
}
