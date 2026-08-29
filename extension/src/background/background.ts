import browser from 'webextension-polyfill'
import { onMessage } from 'webext-bridge/background'
import { wsClient } from './wsClient'
import { configState, STORAGE_KEY_SECRET } from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'
import { isFirefox } from '../utils/extensionInfo'
import { setBootReady } from './bootState'
import { FirefoxBlockingInterceptor } from '../interceptors/FirefoxBlockingInterceptor'
import { ChromeDownloadsApiInterceptor } from '../interceptors/ChromeDownloadsApiInterceptor'
import { initContextMenu } from './contextMenu'
import { initTabMatcher } from './tabMatcher'
import { initExtractorFlow, onExtractorUnpair } from './extractorFlow'
import { initDomFlow, onDomUnpair } from './domFlow'
import { initBurstFlow } from './burstFlow'
import { notifyCaptureHostDown, onCaptureUnpair } from './captureHostDown'
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
  if (!data.enabled) await notifyCaptureHostDown()
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
  await onExtractorUnpair()
  await onDomUnpair()
  await onCaptureUnpair()
  return { ok: true }
})

// Right-click "Download with GoAria" context menu. Registered at top level
// (before the async IIFE) so the onClicked wake-up listener is bound
// synchronously on SW restart — contextMenus.onClicked is an MV3 wake-up
// event and must be registered before any await to avoid missing dispatches.
initContextMenu()
initTabMatcher()
initExtractorFlow()
initDomFlow()
initBurstFlow()

// Download interception: fork by build target and register before any await so
// cold-wake downloads/webRequest events are not missed. shouldIntercept still
// passes until bootReady (config loaded) and the WS link is up.
const interceptor = isFirefox()
  ? new FirefoxBlockingInterceptor()
  : new ChromeDownloadsApiInterceptor()
interceptor.register()

void (async () => {
  await Promise.all([configState.loadEffects(), configState.loadAutoCapture()])
  setBootReady(true)

  // connect() reads the secret from storage.local and authenticates, so a
  // restarted SW transparently recovers the link. No chrome.alarms keep-alive:
  // download/pairing events wake the SW.
  wsClient.connect()

  // Chrome path B persists pending decisions in storage.session so a SW restart
  // can resume an in-flight cancel/resume. Firefox is a no-op here. Recover only
  // after boot so autoCapture/effects are loaded.
  void interceptor.recoverPendingDecisions()
})()

export { wsClient }
