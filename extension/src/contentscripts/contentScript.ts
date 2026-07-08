import { mount } from 'svelte'
import { onMessage, sendMessage } from 'webext-bridge/content-script'
import ShadowDomPopup from './ShadowDomPopup.svelte'
import { popupQueue } from '../stores/popupQueue.svelte'
import { configState } from '../stores/config.svelte'
import type { DownloadInterceptedMessage, InterceptedReply } from '../utils/messaging'
import glassCss from '../styles/index.css?inline'

// Channel-verification helper kept from the scaffold phase.
export async function pingBackground() {
  try {
    await sendMessage('ping', { type: 'ping' }, 'background')
  } catch {
    // Background may not be ready yet.
  }
}

function injectStylesIntoShadow(shadowRoot: ShadowRoot): boolean {
  try {
    const sheet = new CSSStyleSheet()
    sheet.replaceSync(glassCss.replace(/:root/g, ':host'))
    shadowRoot.adoptedStyleSheets = [sheet]
    return true
  } catch {
    // Fallback: inject a <style> tag into the shadow root.
    try {
      const style = document.createElement('style')
      style.textContent = glassCss.replace(/:root/g, ':host')
      shadowRoot.appendChild(style)
      return true
    } catch {
      return false
    }
  }
}

function createShadowHost(): ShadowRoot | null {
  try {
    const host = document.createElement('div')
    host.id = 'goaria-shadow-host'
    host.style.cssText = 'position:fixed;bottom:20px;right:20px;z-index:2147483647;pointer-events:none'
    document.documentElement.appendChild(host)
    const shadowRoot = host.attachShadow({ mode: 'open' })
    if (!injectStylesIntoShadow(shadowRoot)) {
      // CSP blocked style injection; popup will render without glass CSS.
    }
    return shadowRoot
  } catch {
    // documentElement missing or attachShadow blocked (e.g. SVG document).
    return null
  }
}

// True once the Svelte toast mounted. While false the listener replies
// 'fallback' so the background degrades to a system notification.
let popupReady = false

// Register the listener first so messages are never lost to a late mount.
onMessage('download:intercepted', ({ data }): InterceptedReply => {
  if (popupReady) {
    popupQueue.push(data as DownloadInterceptedMessage)
    return 'shown'
  }
  return 'fallback'
})

// Best-effort mount. Any failure leaves popupReady false so the background
// falls back to browser.notifications.create.
try {
  const shadowRoot = createShadowHost()
  if (shadowRoot) {
    // Read the persisted effects setting before mounting so the Shadow DOM
    // popup respects the user's "高级材质" toggle.
    void configState.loadEffects().then(() => {
      try {
        mount(ShadowDomPopup, { target: shadowRoot, props: { effects: configState.effects } })
        popupReady = true
      } catch {
        // Mount threw; background will degrade to a notification.
      }
    })
  }
} catch {
  // Shadow host setup threw; background will degrade to a notification.
}
