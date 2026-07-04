// Must stay in sync with internal/extension/protocol.go — change one, update both.
export const WS_PORT_FALLBACKS = [16801, 16802, 16803] as const
export const DEFAULT_WS_PORT = 16801
export const PAIR_PATH_PREFIX = '/__goaria_pair__/'
export const MSG_TYPE_AUTH = 'auth'
export const MSG_TYPE_DOWNLOAD = 'download'
export const MSG_TYPE_DOWNLOAD_ACK = 'download_ack'

// Aligned with server.go upgrader.Subprotocols.
export const WS_SUBPROTOCOL = 'goaria-extension'

// Exponential backoff params (delay = base * attempt, capped attempts).
export const RECONNECT_BASE_DELAY_MS = 5000
export const RECONNECT_MAX_ATTEMPTS = 120

// Extension-only: persisted pairing secret. No Go counterpart.
export const STORAGE_KEY_SECRET = 'goaria_secret'

// Per-port probe timeout before falling back to the next port.
export const WS_CONNECT_TIMEOUT_MS = 3000
// download_ack wait before rejecting a pending download request.
export const DOWNLOAD_ACK_TIMEOUT_MS = 10000

class ConfigState {
  autoCapture = $state(true)
  port = $state(DEFAULT_WS_PORT)
  registeredFileTypes = $state<string[]>([])
}

export const configState = new ConfigState()
