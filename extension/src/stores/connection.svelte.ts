import { onMessage, sendMessage } from 'webext-bridge/window'
import type { WsStatusMessage } from '../utils/messaging'

export type ConnectionStatus = 'disconnected' | 'connecting' | 'connected'

class ConnectionState {
  status = $state<ConnectionStatus>('disconnected')
  wsPort = $state(0)
  paired = $state(false)
  lastError = $state('')

  get isConnected() {
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

let statusListenerBound = false

/**
 * Register the ws:status listener and query the background for the current
 * state. Intended to run once when the popup opens. Idempotent so repeated
 * mounts don't stack handlers.
 */
export async function initStatusListener(): Promise<void> {
  if (!statusListenerBound) {
    onMessage('ws:status', ({ data }) => {
      connectionState.updateFromStatus(data as WsStatusMessage)
    })
    statusListenerBound = true
  }
  // One-shot query covers the gap between popup open and the next push.
  try {
    const status = await sendMessage<WsStatusMessage>('ws:getStatus', {}, 'background')
    connectionState.updateFromStatus(status)
  } catch {
    // Background may be mid-restart; the listener will catch the next push.
  }
}
