// Keep RPC_ERROR_CODES / BATCH_ITEM_ERROR_CODES string-equal with
// config.svelte.ts ERR_CODE_* and internal/extension/protocol.go.
// This module stays polyfill-free for vitest (no config.svelte import).
import { hasPartitionKey, type WireBrowserCookie } from './browserCookies'

export const EXTRACTOR_RESOLVE_TYPE = 'extractor_resolve'
export const EXTRACTOR_BATCH_TYPE = 'batch_download'

export const BATCH_DENYLIST = [
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
] as const

export function ackTimeoutMs(
  type: string,
  resolveTimeoutMs: number,
  defaultTimeoutMs: number,
): number {
  return type === EXTRACTOR_RESOLVE_TYPE ? resolveTimeoutMs : defaultTimeoutMs
}

export const RPC_ERROR_CODES: ReadonlySet<string> = Object.freeze(
  new Set([
    'unavailable',
    'busy',
    'invalid_request',
    'idempotency_conflict',
    'unsupported',
    'auth_expired',
    'timeout',
    'pack_error',
    'session_expired',
  ]),
)

// Keep string-equal with config.svelte.ts ERR_CODE_* and protocol.go.
export function isRpcErrorCode(code: string): boolean {
  return RPC_ERROR_CODES.has(code)
}

export const BATCH_ITEM_ERROR_CODES: ReadonlySet<string> = Object.freeze(
  new Set(['item is not allowed', 'add failed']),
)

export type BatchDownloadAck = {
  type: string
  request_id?: string
  success: boolean
  group_key?: string
  succeeded_item_ids?: string[]
  duplicate_item_ids?: string[]
  errors_by_item_id?: Record<string, string>
  error_code?: string
  error?: string
}

export class RpcRequestError extends Error {
  readonly requestId: string

  constructor(message: string, requestId: string) {
    super(message)
    this.name = 'RpcRequestError'
    this.requestId = requestId
  }
}

export function shouldRemoveReplayOnTimeout(persist: boolean): boolean {
  return !persist
}

export function noteRpcTimeout(
  persist: boolean,
  requestId: string,
): { error: RpcRequestError; dropReplay: boolean } {
  return {
    error: new RpcRequestError('timeout', requestId),
    dropReplay: shouldRemoveReplayOnTimeout(persist),
  }
}

export type BuildExtractorResolveResult =
  | { payload: Record<string, unknown> }
  | { error: string }

function projectWireCookie(value: unknown): WireBrowserCookie | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined
  }
  const rec = value as Record<string, unknown>
  if (hasPartitionKey(rec)) {
    return undefined
  }
  if (
    typeof rec.name !== 'string' ||
    rec.name === '' ||
    typeof rec.value !== 'string' ||
    typeof rec.domain !== 'string' ||
    typeof rec.path !== 'string' ||
    typeof rec.secure !== 'boolean' ||
    typeof rec.host_only !== 'boolean'
  ) {
    return undefined
  }
  return {
    name: rec.name,
    value: rec.value,
    domain: rec.domain,
    path: rec.path,
    secure: rec.secure,
    host_only: rec.host_only,
  }
}

export function buildExtractorResolvePayload(
  input: Record<string, unknown>,
): BuildExtractorResolveResult {
  if ('headers' in input || 'url' in input || 'final_url' in input) {
    return { error: 'forbidden resolve field' }
  }
  if (typeof input.source_url !== 'string' || input.source_url === '') {
    return { error: 'source_url is required' }
  }
  if (!Array.isArray(input.cookies)) {
    return { error: 'cookies is required' }
  }
  const cookies: WireBrowserCookie[] = []
  for (const item of input.cookies) {
    const projected = projectWireCookie(item)
    if (projected === undefined) {
      return { error: 'cookie projection failed' }
    }
    cookies.push(projected)
  }
  const out: Record<string, unknown> = {
    source_url: input.source_url,
    cookies,
  }
  if (typeof input.user_agent === 'string') {
    out.user_agent = input.user_agent
  }
  if (typeof input.accept_language === 'string') {
    out.accept_language = input.accept_language
  }
  if (typeof input.referer === 'string') {
    out.referer = input.referer
  }
  return { payload: out }
}

export function buildExtractorBatchPayload(
  input: Record<string, unknown>,
): BuildExtractorResolveResult {
  for (const key of BATCH_DENYLIST) {
    if (key in input) {
      return { error: 'forbidden batch field' }
    }
  }
  if (typeof input.session_id !== 'string' || input.session_id.trim() === '') {
    return { error: 'session_id is required' }
  }
  if (input.session_id !== input.session_id.trim()) {
    return { error: 'session_id must be trimmed' }
  }
  if (!Array.isArray(input.item_ids)) {
    return { error: 'item_ids is required' }
  }
  const itemIds: string[] = []
  for (const id of input.item_ids) {
    if (typeof id !== 'string') {
      return { error: 'item_id must be a string' }
    }
    if (id.trim() === '') {
      return { error: 'item_id is required' }
    }
    if (id !== id.trim()) {
      return { error: 'item_id must be trimmed' }
    }
    itemIds.push(id)
  }
  const out: Record<string, unknown> = {
    session_id: input.session_id,
    item_ids: itemIds,
  }
  if ('create_group' in input) {
    if (typeof input.create_group !== 'boolean') {
      return { error: 'create_group must be a boolean' }
    }
    if (input.create_group) {
      out.create_group = true
    }
  }
  if (typeof input.folder_name === 'string' && input.folder_name.trim() !== '') {
    if (input.folder_name !== input.folder_name.trim()) {
      return { error: 'folder_name must be trimmed' }
    }
    out.folder_name = input.folder_name
  }
  return { payload: out }
}

export type PlanRpcSendResult =
  | { id: string; persist: boolean }
  | { error: string }

export function planRpcSend(
  type: string,
  requestId: string | undefined,
  newId: () => string,
): PlanRpcSendResult {
  // extractor_resolve always mints a fresh id and ignores the caller.
  // batch_download must pass the caller requestId on first click, 10s retry,
  // and service-worker wake; omit is a client error, not a mint.
  if (type === EXTRACTOR_RESOLVE_TYPE) {
    return { id: newId(), persist: false }
  }
  const id = typeof requestId === 'string' ? requestId.trim() : ''
  if (!id) {
    return { error: 'request_id is required' }
  }
  return { id, persist: true }
}
