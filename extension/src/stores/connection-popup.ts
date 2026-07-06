// Popup-only: imports webext-bridge/popup, which must never load in the
// background service worker — it creates a 'popup' message endpoint that
// causes persistent high CPU in the SW context.
import { onMessage, sendMessage } from 'webext-bridge/popup'
import { connectionState } from './connection.svelte'
import type { WsStatusMessage } from '../utils/messaging'

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
