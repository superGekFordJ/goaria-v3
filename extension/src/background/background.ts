import browser from 'webextension-polyfill'
import { onMessage } from 'webext-bridge/background'
import { wsClient } from './wsClient'
import { configState, STORAGE_KEY_SECRET } from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'
import { isFirefox } from '../utils/extensionInfo'
import { FirefoxBlockingInterceptor } from '../interceptors/FirefoxBlockingInterceptor'
import { ChromeDownloadsApiInterceptor } from '../interceptors/ChromeDownloadsApiInterceptor'
import { initContextMenu } from './contextMenu'
import type {
  InterceptionToggleMessage,
  PairSecretMessage,
  WsStatusMessage,
} from '../utils/messaging'

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
    // storage write failure (e.g. incognito split mode): cannot persist the
    // secret, so reconnect would use a stale/empty secret. Surface the error
    // to pair.ts so the user can retry from GoAria settings.
    return { ok: false }
  }
  wsClient.disconnect()
  wsClient.connect()
  return { ok: true }
})

// popup -> background: toggle interception on/off + persist.
onMessage('interception:toggle', async ({ data }: { data: InterceptionToggleMessage }) => {
  configState.autoCapture = data.enabled
  await configState.persistAutoCapture()
  return { ok: true }
})

// popup -> background: unpair — remove secret + disconnect WS.
onMessage('pair:unpair', async () => {
  try {
    await browser.storage.local.remove(STORAGE_KEY_SECRET)
  } catch {
    // storage failure: proceed to disconnect anyway.
  }
  wsClient.disconnect()
  connectionState.paired = false
  return { ok: true }
})

// Right-click "Download with GoAria" context menu. Registered at top level
// (before the async IIFE) so the onClicked wake-up listener is bound
// synchronously on SW restart — contextMenus.onClicked is an MV3 wake-up
// event and must be registered before any await to avoid missing dispatches.
initContextMenu()

// SW startup: load persisted state before connecting so the interceptor
// uses the user's saved autoCapture setting. Awaited before connect() and
// interceptor registration to close the race where a freshly restarted SW
// reads the default autoCapture before storage.local resolves.
void (async () => {
  await Promise.all([configState.loadEffects(), configState.loadAutoCapture()])

  // top-level code runs on every (re)start of the Chrome MV3 service worker.
  // connect() reads the secret from storage.local and authenticates, so a
  // restarted SW transparently recovers the link. No chrome.alarms keep-alive:
  // download/pairing events wake the SW.
  wsClient.connect()

  // Download interception: fork by build target. Firefox MV3 uses
  // webRequestBlocking (synchronous cancel); Chrome MV3 uses the downloads API
  // path B (pause → handoff → cancel/resume). shouldIntercept checks the WS
  // connection state, so registering before the socket is up is safe — events
  // simply pass through until the link is established.
  const interceptor = isFirefox()
    ? new FirefoxBlockingInterceptor()
    : new ChromeDownloadsApiInterceptor()
  interceptor.register()
  // Chrome path B persists pending decisions in storage.session so a SW restart
  // can resume an in-flight cancel/resume. Firefox is a no-op here.
  void interceptor.recoverPendingDecisions()
})()

export { wsClient }
