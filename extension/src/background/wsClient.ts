import { sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import {
  DOWNLOAD_ACK_TIMEOUT_MS,
  MSG_TYPE_AUTH,
  MSG_TYPE_AUTH_ACK,
  MSG_TYPE_DOWNLOAD,
  MSG_TYPE_DOWNLOAD_ACK,
  RECONNECT_BASE_DELAY_MS,
  RECONNECT_MAX_ATTEMPTS,
  STORAGE_KEY_SECRET,
  WS_CONNECT_TIMEOUT_MS,
  WS_PORT_FALLBACKS,
  WS_SUBPROTOCOL,
} from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'
import type {
  DownloadHandoffMessage,
  DownloadResponse,
  PairStatusMessage,
  WsStatusMessage,
} from '../utils/messaging'

// Pending download request awaiting a download_ack from the Go backend.
type PendingRequest = {
  id: number
  resolve: (resp: DownloadResponse) => void
  reject: (err: Error) => void
  timer: ReturnType<typeof setTimeout>
  sentAt: number
}

// Auth-fail heuristic: if the socket closes very soon after we sent a
// non-empty-secret auth message, the backend rejected the secret and closed
// the connection. Without this we would loop forever retrying the same bad
// secret. Skipped in MVP mode (empty secret) where auth can't fail.
const AUTH_FAIL_WINDOW_MS = 1500

const AUTH_FAIL_ERROR = 'Authentication failed. Please re-pair in GoAria settings.'

/**
 * WebSocket client that probes the GoAria backend ports, authenticates with the
 * stored pairing secret, forwards download requests, and reconnects with
 * linear backoff. Designed for Chrome MV3 service-worker lifecycle:
 * construction is cheap and connect() reads the secret from storage.local so
 * a restarted SW can recover transparently.
 */
export class WsClient {
  private ws: WebSocket | null = null
  private reconnectAttempts = 0
  private currentPort = 0
  private manuallyDisconnected = false
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private authSentAt = 0
  private authFailed = false
  // Whether the last sendAuth used a non-empty secret. The auth-fail
  // heuristic only applies when the secret was non-empty: in MVP mode (empty
  // secret) the Go backend skips validation, so a quick close can't be auth.
  private authSecretNonEmpty = false
  // Monotonic id used to correlate download_ack replies with pending promises.
  private nextRequestId = 1
  private pending = new Map<number, PendingRequest>()
  // Track the in-flight port probe so a late disconnect() can abort it.
  private probing: { cancel: () => void } | null = null

  /** Start port probing + auth. Safe to call multiple times. */
  connect(): void {
    if (this.ws?.readyState === WebSocket.OPEN) return
    // A probe is already in flight; let it complete rather than spawning a
    // second socket that would leak when the first resolves.
    if (this.probing) return
    this.manuallyDisconnected = false
    this.authFailed = false
    // A fresh connect() (SW startup or post-pairing reconnect) is a new
    // starting point — clear the backoff counter so a long outage before
    // pairing doesn't make the first post-pairing reconnect wait minutes.
    // The auto-reconnect path goes through scheduleReconnect() directly,
    // bypassing connect(), so this does not reset the counter mid-backoff.
    this.reconnectAttempts = 0
    void this.probeAndConnect()
  }

  /** Gracefully tear down; suppresses the reconnect loop. */
  disconnect(): void {
    this.manuallyDisconnected = true
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
    if (this.probing) {
      this.probing.cancel()
      this.probing = null
    }
    this.teardownSocket()
    this.failAllPending('WebSocket disconnected')
    this.setStatus('disconnected', '')
  }

  /** Current snapshot for popup queries (ws:getStatus). */
  getStatus(): WsStatusMessage {
    return {
      status: connectionState.status,
      wsPort: this.currentPort,
      paired: connectionState.paired,
      lastError: connectionState.lastError,
    }
  }

  /**
   * Send a download handoff. camelCase TS fields are converted to the
   * snake_case JSON shape expected by the Go DownloadRequest struct.
   * Rejects immediately when the socket is not open (no queueing).
   */
  sendDownloadRequest(req: DownloadHandoffMessage): Promise<DownloadResponse> {
    if (this.ws?.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('WebSocket is not connected'))
    }
    const id = this.nextRequestId++
    // Explicit field mapping: avoid generic key converters so header names
    // inside `headers` are never mangled.
    const payload = {
      type: req.type ?? MSG_TYPE_DOWNLOAD,
      url: req.url,
      final_url: req.finalUrl,
      headers: req.headers,
      file_size: req.fileSize,
      skip_head_probe: req.skipHeadProbe,
      dedup_key: req.dedupKey,
      filename: req.filename,
      download_page: req.downloadPage,
    }
    return new Promise<DownloadResponse>((resolve, reject) => {
      const timer = setTimeout(() => {
        if (this.pending.delete(id)) {
          reject(new Error('Download request timed out'))
        }
      }, DOWNLOAD_ACK_TIMEOUT_MS)
      this.pending.set(id, {
        id,
        resolve,
        reject,
        timer,
        sentAt: Date.now(),
      })
      try {
        this.ws?.send(JSON.stringify(payload))
      } catch (err) {
        this.pending.delete(id)
        clearTimeout(timer)
        reject(err instanceof Error ? err : new Error(String(err)))
      }
    })
  }

  // --- internals ---

  /** Try each fallback port in order; first to open wins. */
  private async probeAndConnect(): Promise<void> {
    this.setStatus('connecting', connectionState.lastError)
    let lastError = ''
    for (const port of WS_PORT_FALLBACKS) {
      if (this.manuallyDisconnected) return
      try {
        await this.tryPort(port)
        // tryPort resolves only after onopen fires + auth is sent.
        return
      } catch (err) {
        lastError = err instanceof Error ? err.message : String(err)
        // Continue to next port.
      }
    }
    // All ports failed: schedule linear backoff.
    this.currentPort = 0
    this.setStatus('connecting', lastError)
    this.scheduleReconnect()
  }

  /** Attempt a single WebSocket connection to one port with a timeout. */
  private tryPort(port: number): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      if (this.manuallyDisconnected) {
        reject(new Error('Disconnected'))
        return
      }
      let settled = false
      const url = `ws://127.0.0.1:${port}`
      let socket: WebSocket
      try {
        socket = new WebSocket(url, [WS_SUBPROTOCOL])
      } catch (err) {
        reject(err instanceof Error ? err : new Error(String(err)))
        return
      }
      const timeout = setTimeout(() => {
        if (settled) return
        settled = true
        this.probing = null
        try {
          socket.close()
        } catch {
          /* ignore */
        }
        reject(new Error(`Connect timeout on port ${port}`))
      }, WS_CONNECT_TIMEOUT_MS)

      this.probing = {
        cancel: () => {
          if (settled) return
          settled = true
          clearTimeout(timeout)
          try {
            socket.close()
          } catch {
            /* ignore */
          }
        },
      }

      socket.onopen = () => {
        if (settled) return
        settled = true
        clearTimeout(timeout)
        this.probing = null
        this.attachSocket(socket, port)
        resolve()
      }
      socket.onerror = () => {
        // onclose follows; only reject if open never fired.
        if (settled) return
      }
      socket.onclose = () => {
        if (settled) {
          // Already resolved (open fired): handled by attached onclose.
          return
        }
        settled = true
        clearTimeout(timeout)
        this.probing = null
        reject(new Error(`Connection refused on port ${port}`))
      }
    })
  }

  /** Bind long-lived handlers to a successfully opened socket. */
  private attachSocket(socket: WebSocket, port: number): void {
    this.ws = socket
    this.currentPort = port
    this.authSentAt = 0
    this.authFailed = false
    this.authSecretNonEmpty = false
    this.setStatus('connecting', '')

    socket.onopen = () => {
      // Re-entry is unexpected for an already-attached socket; no-op.
    }
    socket.onmessage = ev => this.handleMessage(ev)
    socket.onerror = () => {
      // Record error context; onclose will follow and drive reconnect.
      this.setStatus(connectionState.status, 'WebSocket error')
    }
    socket.onclose = () => this.handleClose()

    void this.sendAuth()
  }

  /** Read secret from storage.local and send the auth message. */
  private async sendAuth(): Promise<void> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return
    let secret = ''
    try {
      const result = await browser.storage.local.get(STORAGE_KEY_SECRET)
      secret = (result[STORAGE_KEY_SECRET] as string) ?? ''
    } catch {
      // storage read failure: proceed with empty secret (MVP mode).
    }
    try {
      this.ws.send(JSON.stringify({ type: MSG_TYPE_AUTH, secret }))
      this.authSentAt = Date.now()
      this.authSecretNonEmpty = secret !== ''
    } catch (err) {
      // Socket dropped between open and auth send; let onclose drive reconnect.
      this.setStatus('connecting', err instanceof Error ? err.message : String(err))
    }
  }

  private handleMessage(ev: MessageEvent): void {
    let data: unknown
    try {
      data = JSON.parse(typeof ev.data === 'string' ? ev.data : '')
    } catch {
      return
    }
    if (!data || typeof data !== 'object') return
    const msg = data as Record<string, unknown>
    if (msg.type === MSG_TYPE_DOWNLOAD_ACK) {
      const resp = msg as unknown as DownloadResponse
      // First successful ack implies pairing succeeded (backend only reaches
      // the message loop after auth validation when a secret is set).
      if (resp.success && !connectionState.paired) {
        connectionState.paired = true
        // Push pairing status change to the popup (if open).
        const pairStatus: PairStatusMessage = {
          paired: connectionState.paired,
          status: connectionState.status,
          wsPort: this.currentPort,
        }
        void sendMessage('pair:status', pairStatus, 'popup').catch(() => {})
      }
      // Ack resolves the oldest pending request. This relies on a
      // single-flight assumption: only one download request is in flight per
      // socket at a time. The Go backend (server.go handleConn) does not echo
      // a request id, so FIFO is the only correlation strategy. Concurrent
      // requests would risk mismatched acks; future interceptors must
      // serialize calls or add request-id support (requires Go changes).
      const entry = this.firstPending()
      if (!entry) return
      clearTimeout(entry.timer)
      this.pending.delete(entry.id)
      if (resp.success) {
        entry.resolve(resp)
      } else {
        entry.reject(new Error(resp.error || 'Download rejected by GoAria'))
      }
      // Mark connected on first ack: backend is fully responsive.
      if (connectionState.status !== 'connected') {
        this.setStatus('connected', '')
      }
    } else if (msg.type === MSG_TYPE_AUTH_ACK) {
      // Backend confirms auth succeeded; transition to connected immediately
      // so interception can start without waiting for the first download_ack.
      if (connectionState.status !== 'connected') {
        this.setStatus('connected', '')
      }
    }
  }

  private handleClose(): void {
    const wasAuthFail =
      this.authSentAt > 0 &&
      this.authSecretNonEmpty &&
      Date.now() - this.authSentAt < AUTH_FAIL_WINDOW_MS &&
      !this.manuallyDisconnected
    this.teardownSocket()
    this.failAllPending('WebSocket closed')
    if (this.manuallyDisconnected) {
      this.setStatus('disconnected', '')
      return
    }
    if (wasAuthFail) {
      // Bad secret: stop the infinite-retry loop and surface a clear error.
      this.authFailed = true
      this.setStatus('disconnected', AUTH_FAIL_ERROR)
      return
    }
    this.setStatus('connecting', connectionState.lastError)
    this.scheduleReconnect()
  }

  private scheduleReconnect(): void {
    if (this.manuallyDisconnected || this.authFailed) return
    if (this.reconnectAttempts >= RECONNECT_MAX_ATTEMPTS) {
      this.setStatus('disconnected', 'Max reconnect attempts reached')
      return
    }
    const delay = RECONNECT_BASE_DELAY_MS * (this.reconnectAttempts + 1)
    this.reconnectAttempts++
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      void this.probeAndConnect()
    }, delay)
  }

  private teardownSocket(): void {
    if (this.ws) {
      this.ws.onopen = null
      this.ws.onmessage = null
      this.ws.onerror = null
      this.ws.onclose = null
      try {
        this.ws.close()
      } catch {
        /* ignore */
      }
      this.ws = null
    }
  }

  private failAllPending(reason: string): void {
    for (const entry of this.pending.values()) {
      clearTimeout(entry.timer)
      entry.reject(new Error(reason))
    }
    this.pending.clear()
  }

  private firstPending(): PendingRequest | null {
    for (const entry of this.pending.values()) {
      return entry
    }
    return null
  }

  private setStatus(status: WsStatusMessage['status'], lastError: string): void {
    connectionState.status = status
    connectionState.wsPort = this.currentPort
    connectionState.lastError = lastError
    if (status === 'connected') {
      this.reconnectAttempts = 0
    }
    // Popup may be closed; sendMessage resolves to a no-receiver rejection
    // which we swallow silently.
    void sendMessage('ws:status', this.getStatus(), 'popup').catch(() => {})
  }
}

// Module-level singleton consumed by the background SW and future interceptors.
export const wsClient = new WsClient()
