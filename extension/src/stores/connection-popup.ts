// Popup-only: imports webext-bridge/popup, which must never load in the
// background service worker — it creates a 'popup' message endpoint that
// causes persistent high CPU in the Sw context.
import { onMessage, sendMessage } from 'webext-bridge/popup'
import { connectionState } from './connection.svelte'
import type { WsStatusMessage, PairStatusMessage } from '../utils/messaging'

let statusListenerBound = false

// Register ws:status + pair:status listeners and query the current state.
// Idempotent so repeated popup mounts don't stack handlers.
export async function initStatusListener(): Promise<void> {
  if (!statusListenerBound) {
    onMessage('ws:status', ({ data }) => {
      connectionState.updateFromStatus(data as WsStatusMessage)
    })
    onMessage('pair:status', ({ data }) => {
      const msg = data as PairStatusMessage
      connectionState.paired = msg.paired
      connectionState.status = msg.status
      connectionState.wsPort = msg.wsPort
      if (msg.status !== 'connected') connectionState.legacyHost = undefined
    })
    statusListenerBound = true
  }
  try {
    const status = await sendMessage<WsStatusMessage>('ws:getStatus', {}, 'background')
    connectionState.updateFromStatus(status)
  } catch {
    // Background may be mid-restart; the listener will catch the next push.
  }
}

// Popup-only unpair: clears local state + tells background to remove secret
// and disconnect the WebSocket. Must never be called from the background Sw.
export async function unpairFromPopup(): Promise<void> {
  connectionState.paired = false
  connectionState.status = 'disconnected'
  connectionState.lastError = 'Unpaired'
  connectionState.legacyHost = undefined
  try {
    await sendMessage('pair:unpair', {}, 'background')
  } catch {
    // Background may be mid-restart; local state is already cleared.
  }
}
