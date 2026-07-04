// Must stay in sync with internal/extension/protocol.go — change one, update both.
export const WS_PORT_FALLBACKS = [16801, 16802, 16803] as const
export const DEFAULT_WS_PORT = 16801
export const PAIR_PATH_PREFIX = '/__goaria_pair__/'
export const MSG_TYPE_AUTH = 'auth'
export const MSG_TYPE_DOWNLOAD = 'download'
export const MSG_TYPE_DOWNLOAD_ACK = 'download_ack'

class ConfigState {
  autoCapture = $state(true)
  port = $state(DEFAULT_WS_PORT)
  registeredFileTypes = $state<string[]>([])
}

export const configState = new ConfigState()
