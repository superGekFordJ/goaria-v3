import { sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import { hasCapability, parseAuthAck, shouldShowLegacyHostHint } from './capabilities'
import { createPendingMap } from './requestAssociation'
import { createReplayStore, type ReplayStorage } from './replayStore'
import { mintRequestId } from './mintRequestId'
import { notifyExtractorHostDown, notifyExtractorMatchCleared } from './extractorVisibility'
import { applyParsedMatch, clearMatchSnapshot } from './matchSnapshot'
import { rescanHttpTabs } from './tabMatcher'
import {
  ackTimeoutMs,
  buildExtractorBatchPayload,
  buildExtractorResolvePayload,
  isRpcErrorCode,
  noteRpcTimeout,
  planRpcSend,
} from './extractorRpc'
import {
  CAP_EXTRACTOR_BATCH,
  CAP_EXTRACTOR_RESOLVE,
  CLIENT_VERSION,
  DOWNLOAD_ACK_TIMEOUT_MS,
  EXTRACTOR_RESOLVE_ACK_TIMEOUT_MS,
  MSG_TYPE_AUTH,
  MSG_TYPE_AUTH_ACK,
  MSG_TYPE_BATCH_DOWNLOAD,
  MSG_TYPE_BATCH_DOWNLOAD_ACK,
  MSG_TYPE_DOWNLOAD,
  MSG_TYPE_EXTRACTOR_RESOLVE,
  MSG_TYPE_EXTRACTOR_RESOLVE_ACK,
  MSG_TYPE_PROTOCOL_ERROR,
  PROTOCOL_VERSION,
  RECONNECT_BASE_DELAY_MS,
  RECONNECT_MAX_ATTEMPTS,
  REPLAY_TTL_MS,
  REQUEST_ACK_TIMEOUT_MS,
  STORAGE_KEY_REPLAY_PREFIX,
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

function sessionReplayStorage(): ReplayStorage {
  return {
    async get(key) {
      try {
        const result = await browser.storage.session.get(key)
        return result[key]
      } catch {
        throw new Error('storage.session unavailable')
      }
    },
    async set(key, value) {
      try {
        await browser.storage.session.set({ [key]: value })
      } catch {
        throw new Error('storage.session unavailable')
      }
    },
    async remove(key) {
      try {
        await browser.storage.session.remove(key)
      } catch {
        throw new Error('storage.session unavailable')
      }
    },
    async getAll() {
      try {
        return (await browser.storage.session.get(null)) as Record<string, unknown>
      } catch {
        return undefined
      }
    },
  }
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
  private pending = createPendingMap<unknown>()
  private pendingTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private replay = createReplayStore(
    sessionReplayStorage(),
    STORAGE_KEY_REPLAY_PREFIX,
    REPLAY_TTL_MS,
  )
  // Track the in-flight port probe so a late disconnect() can abort it.
  private probing: { cancel: () => void } | null = null
  // Serializes download sends. Correlation prefers request_id; FIFO is
  // download-kind fallback only when the ack omits id.
  private sendChain: Promise<unknown> = Promise.resolve()

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
    this.clearProtocolState()
    this.setStatus('disconnected', '')
  }

  /** Current snapshot for popup queries (ws:getStatus). */
  getStatus(): WsStatusMessage {
    return {
      status: connectionState.status,
      wsPort: this.currentPort,
      paired: connectionState.paired,
      lastError: connectionState.lastError,
      legacyHost: connectionState.status === 'connected' ? connectionState.legacyHost : undefined,
    }
  }

  /**
   * Send a download handoff. camelCase TS fields are converted to the
   * snake_case JSON shape expected by the Go DownloadRequest struct.
   * Rejects immediately when the socket is not open (no queueing).
   * Serialized: correlation prefers request_id; FIFO is download-kind
   * fallback only when the ack omits id. Concurrent sends stay queued.
   */
  sendDownloadRequest(req: DownloadHandoffMessage): Promise<DownloadResponse> {
    const next = this.sendChain.then(() => this.doSendDownloadRequest(req))
    // Keep the chain alive on rejection so a failure doesn't block later sends.
    this.sendChain = next.catch(() => undefined)
    return next
  }

  /**
   * Send extractor_resolve / batch_download correlated by request_id.
   * Not serialized on the download sendChain. Local-rejects when the
   * required extractor capability is missing.
   * extractor_resolve always ignores caller requestId and mints a fresh id.
   * Every batch_download (first click, 10s retry, and service-worker wake)
   * must pass the caller requestId; omitting it is a client error, not a mint.
   * persist:true is identity-stable only while that id is supplied;
   * ReplayStore is not a payload store.
   */
  sendRequest(
    type: string,
    payload: Record<string, unknown> = {},
    requestId?: string,
  ): Promise<Record<string, unknown>> {
    if (type !== MSG_TYPE_EXTRACTOR_RESOLVE && type !== MSG_TYPE_BATCH_DOWNLOAD) {
      return Promise.reject(new Error(`sendRequest does not send ${type}`))
    }
    if (
      type === MSG_TYPE_EXTRACTOR_RESOLVE &&
      !hasCapability(connectionState.capabilities, CAP_EXTRACTOR_RESOLVE)
    ) {
      return Promise.reject(new Error('Host does not support extractor.resolve'))
    }
    if (
      type === MSG_TYPE_BATCH_DOWNLOAD &&
      !hasCapability(connectionState.capabilities, CAP_EXTRACTOR_BATCH)
    ) {
      return Promise.reject(new Error('Host does not support extractor.batch'))
    }
    return this.doSendRequest(type, payload, requestId)
  }

  private async doSendRequest(
    type: string,
    payload: Record<string, unknown>,
    requestId?: string,
  ): Promise<Record<string, unknown>> {
    const socket = this.ws
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('WebSocket is not connected'))
    }
    const planned = planRpcSend(type, requestId, mintRequestId)
    if ('error' in planned) {
      return Promise.reject(new Error(planned.error))
    }
    const { id, persist } = planned
    if (persist) {
      await this.replay.persistOrReuse(type, id)
    }
    if (this.ws !== socket || socket.readyState !== WebSocket.OPEN) {
      if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
        void this.replay.remove(id)
        return Promise.reject(new Error('WebSocket is not connected'))
      }
      return Promise.reject(new Error('WebSocket was replaced before send'))
    }
    let outbound: Record<string, unknown> = payload
    if (type === MSG_TYPE_EXTRACTOR_RESOLVE) {
      const built = buildExtractorResolvePayload(payload)
      if ('error' in built) {
        void this.replay.remove(id)
        return Promise.reject(new Error(built.error))
      }
      outbound = built.payload
    } else if (type === MSG_TYPE_BATCH_DOWNLOAD) {
      const built = buildExtractorBatchPayload(payload)
      if ('error' in built) {
        void this.replay.remove(id)
        return Promise.reject(new Error(built.error))
      }
      outbound = built.payload
    }
    const body = { ...outbound, type, request_id: id }
    const timeoutMs = ackTimeoutMs(type, EXTRACTOR_RESOLVE_ACK_TIMEOUT_MS, REQUEST_ACK_TIMEOUT_MS)
    return new Promise<Record<string, unknown>>((resolve, reject) => {
      const tracked = this.trackPending(
        id,
        'rpc',
        resolve as (value: unknown) => void,
        reject,
        timeoutMs,
        persist,
      )
      if (!tracked) {
        reject(new Error('Request already in flight'))
        return
      }
      try {
        socket.send(JSON.stringify(body))
      } catch (err) {
        this.clearPending(id)
        void this.replay.remove(id)
        reject(err instanceof Error ? err : new Error(String(err)))
      }
    })
  }

  private doSendDownloadRequest(req: DownloadHandoffMessage): Promise<DownloadResponse> {
    const socket = this.ws
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('WebSocket is not connected'))
    }
    const id = mintRequestId()
    // Explicit field mapping: avoid generic key converters so header names
    // inside `headers` are never mangled.
    const payload = {
      type: req.type ?? MSG_TYPE_DOWNLOAD,
      request_id: id,
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
      const tracked = this.trackPending(
        id,
        'download',
        value => resolve(value as DownloadResponse),
        reject,
        DOWNLOAD_ACK_TIMEOUT_MS,
        false,
        'Download request timed out',
      )
      if (!tracked) {
        reject(new Error('Request already in flight'))
        return
      }
      try {
        socket.send(JSON.stringify(payload))
      } catch (err) {
        this.clearPending(id)
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
      this.ws.send(
        JSON.stringify({
          type: MSG_TYPE_AUTH,
          secret,
          protocol_version: PROTOCOL_VERSION,
          client_version: CLIENT_VERSION,
        }),
      )
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
    if (msg.type === MSG_TYPE_AUTH_ACK) {
      const parsed = parseAuthAck(msg)
      connectionState.capabilities = parsed.capabilities
      connectionState.protocolVersion = parsed.protocolVersion
      connectionState.hostVersion = parsed.hostVersion
      connectionState.legacyHost = shouldShowLegacyHostHint(parsed)
      if (parsed.match && hasCapability(parsed.capabilities, CAP_EXTRACTOR_RESOLVE)) {
        applyParsedMatch(parsed.match)
        void rescanHttpTabs().catch(() => undefined)
      } else {
        clearMatchSnapshot()
        notifyExtractorMatchCleared()
      }
      if (!connectionState.paired) {
        connectionState.paired = true
        const pairStatus: PairStatusMessage = {
          paired: connectionState.paired,
          status: connectionState.status,
          wsPort: this.currentPort,
        }
        void sendMessage('pair:status', pairStatus, 'popup').catch(() => {})
      }
      if (connectionState.status !== 'connected') {
        this.setStatus('connected', '')
      }
      return
    }

    const routed = this.pending.routeMessage(msg)
    if (routed.kind === 'ignored') return
    this.clearTimer(routed.entry.id)

    if (routed.kind === 'download_ack') {
      const resp = msg as unknown as DownloadResponse
      if (resp.success && !connectionState.paired) {
        connectionState.paired = true
        const pairStatus: PairStatusMessage = {
          paired: connectionState.paired,
          status: connectionState.status,
          wsPort: this.currentPort,
        }
        void sendMessage('pair:status', pairStatus, 'popup').catch(() => {})
      }
      if (resp.success) {
        routed.entry.resolve(resp)
      } else {
        routed.entry.reject(new Error(resp.error || 'Download rejected by GoAria'))
      }
      if (connectionState.status !== 'connected') {
        this.setStatus('connected', '')
      }
      return
    }

    if (routed.kind === 'protocol_error') {
      void this.replay.remove(routed.entry.id)
      const code = typeof msg.error_code === 'string' ? msg.error_code : MSG_TYPE_PROTOCOL_ERROR
      routed.entry.reject(new Error(code))
      return
    }

    const errorCode = typeof msg.error_code === 'string' ? msg.error_code : ''
    if (isRpcErrorCode(errorCode)) {
      routed.entry.reject(new Error(errorCode))
    } else {
      routed.entry.resolve(msg)
    }
    const msgType = typeof msg.type === 'string' ? msg.type : ''
    if (msgType === MSG_TYPE_EXTRACTOR_RESOLVE_ACK) {
      void this.replay.remove(routed.entry.id)
    } else if (msgType === MSG_TYPE_BATCH_DOWNLOAD_ACK) {
      void this.replay.remove(routed.entry.id)
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
    this.clearProtocolState()
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
    for (const timer of this.pendingTimers.values()) {
      clearTimeout(timer)
    }
    this.pendingTimers.clear()
    this.pending.failAll(reason)
  }

  private trackPending(
    id: string,
    kind: 'download' | 'rpc',
    resolve: (value: unknown) => void,
    reject: (err: Error) => void,
    timeoutMs: number,
    persist = false,
    timeoutMessage?: string,
  ): boolean {
    if (!this.pending.add({ id, kind, resolve, reject })) {
      return false
    }
    const timer = setTimeout(() => {
      const entry = this.pending.completeById(id)
      this.pendingTimers.delete(id)
      if (timeoutMessage) {
        void this.replay.remove(id)
        entry?.reject(new Error(timeoutMessage))
        return
      }
      const timed = noteRpcTimeout(persist, id)
      if (timed.dropReplay) {
        void this.replay.remove(id)
      }
      entry?.reject(timed.error)
    }, timeoutMs)
    this.pendingTimers.set(id, timer)
    return true
  }

  private clearTimer(id: string): void {
    const timer = this.pendingTimers.get(id)
    if (timer) {
      clearTimeout(timer)
      this.pendingTimers.delete(id)
    }
  }

  private clearPending(id: string): void {
    this.clearTimer(id)
    this.pending.completeById(id)
  }

  private clearProtocolState(): void {
    connectionState.capabilities = undefined
    connectionState.protocolVersion = 0
    connectionState.hostVersion = ''
    connectionState.legacyHost = undefined
    clearMatchSnapshot()
    notifyExtractorHostDown('disconnect')
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
