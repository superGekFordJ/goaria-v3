// Must stay in sync with internal/extension/protocol.go — change one, update both.
import browser from 'webextension-polyfill'

export const WS_PORT_FALLBACKS = [16801, 16802, 16803] as const
export const DEFAULT_WS_PORT = 16801
export const PAIR_PATH_PREFIX = '/__goaria_pair__/'
export const MSG_TYPE_AUTH = 'auth'
export const MSG_TYPE_AUTH_ACK = 'auth_ack'
export const MSG_TYPE_DOWNLOAD = 'download'
export const MSG_TYPE_DOWNLOAD_ACK = 'download_ack'

// Aligned with server.go upgrader.Subprotocols.
export const WS_SUBPROTOCOL = 'goaria-extension'

// Linear backoff params (delay = base * attempt, capped attempts).
export const RECONNECT_BASE_DELAY_MS = 5000
export const RECONNECT_MAX_ATTEMPTS = 120

// Extension-only: persisted pairing secret. No Go counterpart.
export const STORAGE_KEY_SECRET = 'goaria_secret'

// Extension-only: persisted UI effects mode. No Go counterpart.
export const STORAGE_KEY_EFFECTS = 'goaria_effects'
export const STORAGE_KEY_AUTO_CAPTURE = 'goaria_auto_capture'

// Per-port probe timeout before falling back to the next port.
export const WS_CONNECT_TIMEOUT_MS = 3000
// download_ack wait before rejecting a pending download request.
export const DOWNLOAD_ACK_TIMEOUT_MS = 10000

// Extension-only constants (no Go counterpart).

// Chrome MV3 small-file threshold: downloads.pause is ineffective for <1MB
// files. Files below this size are passed through in onDeterminingFilename.
export const SMALL_FILE_THRESHOLD_BYTES = 100 * 1024 // 100KB

// SW restart pending-decision TTL: stale pending records are cleaned up after this.
export const PENDING_DECISION_TTL_MS = 30_000 // 30s

// storage.session key prefix for pending download decisions (Chrome MV3 path B).
export const STORAGE_KEY_PENDING_PREFIX = 'pending_'

class ConfigState {
  autoCapture = $state(true)
  port = $state(DEFAULT_WS_PORT)
  registeredFileTypes = $state<string[]>([])
  effects = $state<'full' | 'reduced'>('full')

  async loadEffects(): Promise<void> {
    try {
      const result = await browser.storage.local.get(STORAGE_KEY_EFFECTS)
      const val = result[STORAGE_KEY_EFFECTS] as string | undefined
      if (val === 'full' || val === 'reduced') this.effects = val
    } catch {
      // storage read failure: keep default.
    }
  }

  async persistEffects(): Promise<void> {
    try {
      await browser.storage.local.set({ [STORAGE_KEY_EFFECTS]: this.effects })
    } catch {
      // storage write failure: non-fatal.
    }
  }

  async loadAutoCapture(): Promise<void> {
    try {
      const result = await browser.storage.local.get(STORAGE_KEY_AUTO_CAPTURE)
      if (typeof result[STORAGE_KEY_AUTO_CAPTURE] === 'boolean') {
        this.autoCapture = result[STORAGE_KEY_AUTO_CAPTURE] as boolean
      }
    } catch {
      // storage read failure: keep default.
    }
  }

  async persistAutoCapture(): Promise<void> {
    try {
      await browser.storage.local.set({ [STORAGE_KEY_AUTO_CAPTURE]: this.autoCapture })
    } catch {
      // storage write failure: non-fatal.
    }
  }
}

export const configState = new ConfigState()
