// webext-bridge message type declarations.
// Message IDs follow a `namespace:action` convention:
//   pair:secret          — content script (pair.ts) -> background, forwards pairing secret.
//   ws:status            — background -> popup, pushes WS connection state changes.
//   ws:getStatus         — popup -> background, one-shot query for current WS state.
//   download:intercepted — background -> content script, notifies a download was
//                          intercepted (consumed by the in-page Shadow DOM popup).

import type { ProtocolWithReturn } from 'webext-bridge'

// Type-safe protocol map: webext-bridge infers data/return types from these
// declarations so sendMessage/onMessage callers get compile-time checks.
declare module 'webext-bridge' {
  export interface ProtocolMap {
    ping: ProtocolWithReturn<PingMessage, PongMessage>
    'pair:secret': ProtocolWithReturn<PairSecretMessage, PairResult>
    'pair:status': PairStatusMessage
    'pair:unpair': ProtocolWithReturn<PairUnpairMessage, PairResult>
    'ws:status': WsStatusMessage
    'ws:getStatus': ProtocolWithReturn<GetWsStatusMessage, WsStatusMessage>
    'download:intercepted': ProtocolWithReturn<DownloadInterceptedMessage, InterceptedReply>
    'extractor:detected': ProtocolWithReturn<ExtractorDetectedMessage, InterceptedReply>
    'extractor:hide': ExtractorHideMessage
    'extractor:click': ProtocolWithReturn<ExtractorClickMessage, ExtractorClickReply>
    'extractor:picker-open': ProtocolWithReturn<ExtractorPickerOpenMessage, ExtractorPickerOpenReply>
    'extractor:picker-submit': ProtocolWithReturn<ExtractorPickerSubmitMessage, ExtractorClickReply>
    'extractor:picker-catalog': ExtractorPickerCatalogMessage
    'extractor:ignore': ProtocolWithReturn<ExtractorIgnoreMessage, ExtractorIgnoreReply>
    'extractor:nav': ProtocolWithReturn<ExtractorNavMessage, ExtractorIgnoreReply>
    'extractor:fallback': ProtocolWithReturn<ExtractorFallbackMessage, ExtractorIgnoreReply>
    'extractor:result': ExtractorResultMessage
    'interception:toggle': ProtocolWithReturn<InterceptionToggleMessage, InterceptionToggleResult>
    'dom:ping': ProtocolWithReturn<DomPingMessage, DomPingReply>
    'dom:scan': ProtocolWithReturn<DomScanMessage, DomScanReply>
    'dom:open': ProtocolWithReturn<DomOpenMessage, DomOpenReply>
    'dom:close': DomCloseMessage
    'dom:alive': ProtocolWithReturn<DomAliveMessage, DomAliveReply>
    'dom:submit': ProtocolWithReturn<DomSubmitMessage, DomSubmitReply>
    'dom:cancel': ProtocolWithReturn<DomCancelMessage, DomCancelReply>
  }
}

export type PingMessage = { type: 'ping' }
export type PongMessage = { pong: boolean }

// Internal fields use camelCase (idiomatic TS). The Go DownloadRequest
// (protocol.go) uses snake_case JSON tags (file_size, skip_head_probe,
// dedup_key, download_page). The background must convert when forwarding.
// `type` aligns with Go DownloadRequest.Type (json:"type"): 'download' routes
// to the normal download dispatch; 'hls' requires backend dispatch readiness
// (not sent by this extension yet).
export type DownloadHandoffMessage = {
  type?: 'download' | 'hls'
  url: string
  finalUrl: string
  headers: string[]
  fileSize: number
  skipHeadProbe: boolean
  dedupKey: string
  filename: string
  downloadPage: string
}

export type PairSecretMessage = {
  secret: string
}

// Result returned by the background's pair:secret handler.
export type PairResult = {
  ok: boolean
}

// Mirrors connection.svelte.ts ConnectionStatus.
export type ConnectionStatus = 'disconnected' | 'connecting' | 'connected'

export type WsStatusMessage = {
  status: ConnectionStatus
  wsPort: number
  paired: boolean
  lastError: string
  legacyHost?: boolean
}

// Empty payload: popup asks background for the current WsStatusMessage.
export type GetWsStatusMessage = Record<string, never>

// Ack shape returned by the Go backend (protocol.go DownloadResponse).
export type DownloadResponse = {
  type: 'download_ack'
  success: boolean
  gid: string
  error?: string
  request_id?: string
}

// background -> content script: fired after a download interception resolves
// so the in-page Shadow DOM popup can show a confirmation/failure toast. The
// content script replies 'shown' or 'fallback'; on 'fallback' (or delivery
// failure) the background degrades to browser.notifications.create.
export type DownloadInterceptedMessage = {
  url: string
  filename: string
  success: boolean
  error?: string
}

// Reply from the content script: 'shown' when the in-page toast/capsule rendered,
// 'pending' only while a capsule mount is actually scheduled (do not notify yet),
// 'fallback' when it could not (no host / mount failed / iframe / not ready).
export type InterceptedReply = 'shown' | 'fallback' | 'pending'

export type ExtractorDetectedMessage = {
  generation: number
  page_token: string
}

export type ExtractorHideReason =
  | 'nav'
  | 'disconnect'
  | 'unpair'
  | 'generation'
  | 'matched_false'
  | 'ignore'

export type ExtractorHideMessage = {
  reason: ExtractorHideReason
  page_token?: string
}

export type ExtractorClickMessage = {
  page_token: string
}

export type ExtractorClickReply = {
  accepted: boolean
  error_code?: string
}

export type ExtractorIgnoreMessage = {
  page_token: string
}

export type ExtractorIgnoreReply = {
  ok: boolean
}

export type ExtractorNavMessage = {
  page_token: string
}

export type ExtractorFallbackMessage = {
  page_token: string
}

export type ExtractorResultUi = 'idle' | 'resolving' | 'ready' | 'committing' | 'success' | 'error'

export type ExtractorResultMessage = {
  page_token: string
  ui: ExtractorResultUi
  count?: number
  filename?: string
  error_code?: string
  lease_deadline?: number
}

export type PickerCatalogItem = {
  index: number
  filename?: string
  size_bytes?: number
  mime_type?: string
}

export type ExtractorPickerOpenMessage = {
  page_token: string
}

export type ExtractorPickerOpenReply = {
  ok: boolean
  error_code?: string
  items?: PickerCatalogItem[]
  lease_deadline?: number
  count?: number
}

export type ExtractorPickerSubmitMessage = {
  page_token: string
  indices: number[]
  create_group?: boolean
  folder_name?: string
}

export type ExtractorPickerCatalogMessage = {
  page_token: string
  items: PickerCatalogItem[]
  lease_deadline?: number
  count?: number
}

export type DomLinkKind = 'link' | 'image' | 'video' | 'audio' | 'source'

export type DomPickerCatalogItem = {
  index: number
  filename?: string
  origin?: string
  kind?: DomLinkKind
  size_bytes?: number
}

export type DomPingMessage = Record<string, never>
export type DomPingReply = {
  document_nonce: string
  page_href: string
  extractor_picker_open: boolean
  dom_picker_open: boolean
}

export type DomScanMessage = Record<string, never>
export type DomScanHitMessage = {
  url: string
  kind: DomLinkKind
  filename?: string
  document_policy: string
  element_policy: string
  rel_noreferrer: boolean
}
export type DomScanReply = {
  items: DomScanHitMessage[]
  truncated: boolean
  title: string
  document_nonce: string
  page_href: string
}

export type DomOpenMessage = {
  catalog_id: string
  items: DomPickerCatalogItem[]
  truncated: boolean
  store_unproven: boolean
  folder_prefill?: string
}
export type DomOpenReply = {
  ok: boolean
}

export type DomCloseMessage = {
  catalog_id?: string
}

export type DomAliveMessage = {
  catalog_id: string
}
export type DomAliveReply = {
  ok: boolean
}

export type DomSubmitMessage = {
  catalog_id: string
  indices: number[]
  create_group?: boolean
  folder_name?: string
}
export type DomSubmitReply = {
  accepted: boolean
  error_code?: string
  succeeded?: number
  duplicate?: number
  error?: number
}

export type DomCancelMessage = {
  catalog_id: string
}
export type DomCancelReply = {
  ok: boolean
}

export type InterceptionToggleMessage = {
  enabled: boolean
}

export type InterceptionToggleResult = {
  ok: boolean
}

export type PairStatusMessage = {
  paired: boolean
  status: ConnectionStatus
  wsPort: number
}

export type PairUnpairMessage = Record<string, never>
