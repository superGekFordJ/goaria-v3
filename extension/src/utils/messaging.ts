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
    'download:intercepted': DownloadInterceptedMessage
    'interception:toggle': ProtocolWithReturn<InterceptionToggleMessage, InterceptionToggleResult>
  }
}

export type PingMessage = { type: 'ping' }
export type PongMessage = { pong: boolean }

// Internal fields use camelCase (idiomatic TS). The Go DownloadRequest
// (protocol.go) uses snake_case JSON tags (file_size, skip_head_probe,
// dedup_key, download_page). The background must convert when forwarding.
export type DownloadHandoffMessage = {
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
}

// One-way notification: background -> content script. Fired after the
// background resolves a download interception so the in-page popup can show a
// confirmation/failure toast. Content scripts may not be injected on every
// page, so sendMessage failures are expected and silently ignored.
export type DownloadInterceptedMessage = {
  url: string
  filename: string
  success: boolean
  error?: string
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
