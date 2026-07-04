// webext-bridge message type declarations.
// Message IDs follow a `namespace:action` convention:
//   pair:secret   — content script (pair.ts) -> background, forwards pairing secret.
//   ws:status     — background -> popup, pushes WS connection state changes.
//   ws:getStatus  — popup -> background, one-shot query for current WS state.
//   download:*    — reserved for future interception SPECs.

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
