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
    'interception:toggle': ProtocolWithReturn<InterceptionToggleMessage, InterceptionToggleResult>
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

// Reply from the content script: 'shown' when the in-page toast rendered,
// 'fallback' when it could not (mount failed / not ready). The background
// uses this to decide whether to degrade to a system notification.
export type InterceptedReply = 'shown' | 'fallback'

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
