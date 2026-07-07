import { mount } from 'svelte'
import { onMessage, sendMessage } from 'webext-bridge/content-script'
import ShadowDomPopup from './ShadowDomPopup.svelte'
import { popupQueue } from '../stores/popupQueue.svelte'
import { configState } from '../stores/config.svelte'
import type { DownloadInterceptedMessage } from '../utils/messaging'
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
    sheet.replaceSync(glassCss)
    shadowRoot.adoptedStyleSheets = [sheet]
    return true
  } catch {
    // Fallback: inject a <style> tag into the shadow root.
    try {
      const style = document.createElement('style')
      style.textContent = glassCss
      shadowRoot.appendChild(style)
      return true
    } catch {
      return false
    }
  }
}

function createShadowHost(): ShadowRoot | null {
  const host = document.createElement('div')
  host.id = 'goaria-shadow-host'
  host.style.cssText = 'position:fixed;bottom:20px;right:20px;z-index:2147483647;pointer-events:none'
  document.documentElement.appendChild(host)
  const shadowRoot = host.attachShadow({ mode: 'open' })
  if (!injectStylesIntoShadow(shadowRoot)) {
    // CSP blocked style injection; popup will render without glass CSS.
  }
  return shadowRoot
}

const shadowRoot = createShadowHost()
if (shadowRoot) {
  // Read the persisted effects setting before mounting so the Shadow DOM
  // popup respects the user's "高级材质" toggle instead of always rendering
  // full SVG refraction.
  void configState.loadEffects().then(() => {
    mount(ShadowDomPopup, { target: shadowRoot, props: { effects: configState.effects } })
  })
}

onMessage('download:intercepted', ({ data }) => {
  popupQueue.push(data as DownloadInterceptedMessage)
})
