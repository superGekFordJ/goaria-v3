import type { WsStatusMessage } from '../utils/messaging'

export type ConnectionStatus = 'disconnected' | 'connecting' | 'connected'

class ConnectionState {
  status = $state<ConnectionStatus>('disconnected')
  wsPort = $state(0)
  paired = $state(false)
  lastError = $state('')
  capabilities = $state<string[]>([])
  protocolVersion = $state(0)
  hostVersion = $state('')

  get isConnected() {
    return this.status === 'connected'
  }

  // Interception only takes effect while the WS link is up; otherwise the
  // browser download would be cancelled/paused with no backend to hand off to.
  get interceptionEnabled() {
    return this.status === 'connected'
  }

  /** Apply a status snapshot pushed by the background (ws:status). */
  updateFromStatus(msg: WsStatusMessage): void {
    this.status = msg.status
    this.wsPort = msg.wsPort
    this.paired = msg.paired
    this.lastError = msg.lastError
  }
}

export const connectionState = new ConnectionState()
