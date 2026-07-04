// webext-bridge message type declarations (skeleton).
// Future plans add interception, pairing, and download-forwarding messages.

export type PingMessage = { type: 'ping' }
export type PongMessage = { type: 'pong'; value: boolean }

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
