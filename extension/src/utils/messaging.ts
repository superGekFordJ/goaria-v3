// webext-bridge message type declarations (skeleton).
// Future plans add interception, pairing, and download-forwarding messages.

export type PingMessage = { type: 'ping' }
export type PongMessage = { type: 'pong'; value: boolean }

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
