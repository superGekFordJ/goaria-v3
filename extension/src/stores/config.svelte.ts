// Must stay in sync with internal/extension/protocol.go — change one, update both.
import browser from 'webextension-polyfill'

export const WS_PORT_FALLBACKS = [16801, 16802, 16803] as const
export const DEFAULT_WS_PORT = 16801
export const PAIR_PATH_PREFIX = '/__goaria_pair__/'
export const MSG_TYPE_AUTH = 'auth'
export const MSG_TYPE_AUTH_ACK = 'auth_ack'
export const MSG_TYPE_DOWNLOAD = 'download'
export const MSG_TYPE_DOWNLOAD_ACK = 'download_ack'
export const MSG_TYPE_PING = 'ping'
export const MSG_TYPE_EXTRACTOR_RESOLVE = 'extractor_resolve'
export const MSG_TYPE_EXTRACTOR_RESOLVE_ACK = 'extractor_resolve_ack'
export const MSG_TYPE_BATCH_DOWNLOAD = 'batch_download'
export const MSG_TYPE_BATCH_DOWNLOAD_ACK = 'batch_download_ack'
export const MSG_TYPE_DOWNLOAD_BATCH = 'download_batch'
export const MSG_TYPE_DOWNLOAD_BATCH_ACK = 'download_batch_ack'
export const MSG_TYPE_DOWNLOAD_BATCH_STATUS = 'download_batch_status'
export const MSG_TYPE_DOWNLOAD_BATCH_STATUS_ACK = 'download_batch_status_ack'
export const MSG_TYPE_PROTOCOL_ERROR = 'protocol_error'
export const MSG_TYPE_CAPABILITY_UPDATE = 'capability_update'

export const PROTOCOL_VERSION = 2
export const MATCH_DIGEST_VERSION = 1
export const CLIENT_VERSION = '0.4.0'

export const CAP_REQUEST_ID = 'request_id'
export const CAP_EXTRACTOR_RESOLVE = 'extractor.resolve'
export const CAP_EXTRACTOR_BATCH = 'extractor.batch'
export const CAP_DOWNLOAD_BATCH = 'download.batch'

export const ERR_CODE_UNSUPPORTED = 'unsupported'
export const ERR_CODE_UNAVAILABLE = 'unavailable'
export const ERR_CODE_BUSY = 'busy'
export const ERR_CODE_INVALID_REQUEST = 'invalid_request'
export const ERR_CODE_IDEMPOTENCY_CONFLICT = 'idempotency_conflict'
export const ERR_CODE_AUTH_EXPIRED = 'auth_expired'
export const ERR_CODE_TIMEOUT = 'timeout'
export const ERR_CODE_PACK_ERROR = 'pack_error'
export const ERR_CODE_SESSION_EXPIRED = 'session_expired'

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
// batch_download wait; same 10s as download.
export const REQUEST_ACK_TIMEOUT_MS = DOWNLOAD_ACK_TIMEOUT_MS
export const EXTRACTOR_RESOLVE_ACK_TIMEOUT_MS = 30_000

// Extension-only constants (no Go counterpart).

// Chrome MV3 small-file threshold: downloads.pause is ineffective for <1MB
// files. Files below this size are passed through in onDeterminingFilename.
export const SMALL_FILE_THRESHOLD_BYTES = 100 * 1024 // 100KB

// SW restart pending-decision TTL: stale pending records are cleaned up after this.
export const PENDING_DECISION_TTL_MS = 30_000 // 30s

// storage.session key prefix for pending download decisions (Chrome MV3 path B).
export const STORAGE_KEY_PENDING_PREFIX = 'pending_'

export const STORAGE_KEY_CAPTURE_PREFIX = 'cap_'
export const STORAGE_KEY_CAPTURE_SESSION = 'cap_session'
export const CAPTURE_SESSION_TTL_MS = 60_000

export const STORAGE_KEY_BURST_HOLD_PREFIX = 'bhold_'
export const STORAGE_KEY_BURST_WINDOW = 'bwin_window'
export const BURST_QUIET_WINDOW_MS = 1_000
export const BURST_MAX_DEADLINE_MS = 15_000

// storage.session key prefix for replayable extractor request ids (SW restart).
export const STORAGE_KEY_REPLAY_PREFIX = 'replay_'
export const REPLAY_TTL_MS = 60_000

import {
  EXTRACTOR_FOLDER_MAX_RUNES,
  EXTRACTOR_IGNORE_PREFIX,
  EXTRACTOR_LEASE_MS,
  EXTRACTOR_MAX_SESSION_ITEMS,
  EXTRACTOR_NOTIF_PREFIX,
  EXTRACTOR_PICKER_WINDOW,
  EXTRACTOR_SESSION_PREFIX,
} from '../background/extractorKeys'

export {
  EXTRACTOR_SESSION_PREFIX as STORAGE_KEY_EXTRACTOR_SESSION_PREFIX,
  EXTRACTOR_IGNORE_PREFIX as STORAGE_KEY_EXTRACTOR_IGNORE_PREFIX,
  EXTRACTOR_NOTIF_PREFIX as STORAGE_KEY_EXTRACTOR_NOTIF_PREFIX,
  EXTRACTOR_LEASE_MS,
  EXTRACTOR_MAX_SESSION_ITEMS,
  EXTRACTOR_PICKER_WINDOW,
  EXTRACTOR_FOLDER_MAX_RUNES,
}

export const EXTRACTOR_RESOLVE_WATCHDOG_MS = 35_000
export const EXTRACTOR_BATCH_WATCHDOG_MS = 15_000
export const EXTRACTOR_MAX_RESOLVE_COOKIES = 64
export const EXTRACTOR_SUCCESS_HOLD_MS = 1_200
export const EXTRACTOR_SUCCESS_OUT_MS = 160
export const BURST_HOLD_TTL_MS = EXTRACTOR_LEASE_MS

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
