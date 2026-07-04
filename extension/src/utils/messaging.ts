// webext-bridge message type declarations.
// Message IDs follow a `namespace:action` convention:
//   pair:secret   — content script (pair.ts) -> background, forwards pairing secret.
//   ws:status     — background -> popup, pushes WS connection state changes.
//   ws:getStatus  — popup -> background, one-shot query for current WS state.
//   download:*    — reserved for future interception features.

import type { ProtocolWithReturn } from 'webext-bridge'

// Type-safe protocol map: webext-bridge infers data/return types from these
// declarations so sendMessage/onMessage callers get compile-time checks.
declare module 'webext-bridge' {
  export interface ProtocolMap {
    ping: ProtocolWithReturn<PingMessage, PongMessage>
    'pair:secret': ProtocolWithReturn<PairSecretMessage, PairResult>
    'ws:status': WsStatusMessage
    'ws:getStatus': ProtocolWithReturn<GetWsStatusMessage, WsStatusMessage>
  }
}

export type PingMessage = { type: 'ping' }
export type PongMessage = { pong: boolean }

// Internal fields use camelCase (idiomatic TS). The Go DownloadRequest
// (protocol.go) uses snake_case JSON tags (file_size, skip_head_probe,
// dedup_key, download_page). The background must convert when forwarding.
export type DownloadHandoffMessage = {
  url: string
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
