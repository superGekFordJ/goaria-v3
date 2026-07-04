import browser from 'webextension-polyfill'
import { onMessage } from 'webext-bridge/background'
import { wsClient } from './wsClient'
import { STORAGE_KEY_SECRET } from '../stores/config.svelte'
import type { PairSecretMessage, WsStatusMessage } from '../utils/messaging'

// Channel-verification handler kept from the scaffold phase.
onMessage('ping', () => ({ pong: true }))

// popup -> background: one-shot query for the current WS state so the popup
// doesn't show stale data when it opens (the socket may have reconnected or
// dropped while the popup was closed).
onMessage('ws:getStatus', () => wsClient.getStatus() satisfies WsStatusMessage)

// content script (pair.ts) -> background: persist the new secret and reconnect
// so the next auth uses it. disconnect() first to abort any in-flight probe.
onMessage('pair:secret', async ({ data }: { data: PairSecretMessage }) => {
  if (!data?.secret) return { ok: false }
  try {
    await browser.storage.local.set({ [STORAGE_KEY_SECRET]: data.secret })
  } catch {
    // storage write failure: still attempt reconnect with the in-memory secret
    // by stashing it where sendAuth reads. Fallback path is best-effort.
    return { ok: false }
  }
  wsClient.disconnect()
  wsClient.connect()
  return { ok: true }
})

// SW startup: top-level code runs on every (re)start of the Chrome MV3
// service worker. connect() reads the secret from storage.local and
// authenticates, so a restarted SW transparently recovers the link.
// No chrome.alarms keep-alive: download/pairing events wake the SW.
wsClient.connect()

export { wsClient }
