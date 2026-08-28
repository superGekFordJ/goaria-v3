import { mount, tick, unmount } from 'svelte'
import { onMessage, sendMessage } from 'webext-bridge/content-script'
import browser from 'webextension-polyfill'
import ShadowRootApp from './ShadowRootApp.svelte'
import { popupQueue } from '../stores/popupQueue.svelte'
import {
  configState,
  EXTRACTOR_BATCH_WATCHDOG_MS,
  EXTRACTOR_RESOLVE_WATCHDOG_MS,
  STORAGE_KEY_EFFECTS,
} from '../stores/config.svelte'
import type {
  DownloadInterceptedMessage,
  ExtractorDetectedMessage,
  ExtractorHideMessage,
  ExtractorPickerCatalogMessage,
  ExtractorResultMessage,
  InterceptedReply,
  DomOpenMessage,
  DomOpenReply,
  DomCloseMessage,
  DomPingReply,
  DomScanReply,
} from '../utils/messaging'
import { pageTokenFromHref } from '../background/pageToken'
import { mintDirectBatchRequestId } from '../background/mintRequestId'
import { collectDomLinks, type DomScanRoot } from './domScan'
import { capsuleView } from './capsuleView.svelte'
import { pickerView } from './pickerView.svelte'
import { domPickerView } from './domPickerView.svelte'
import type { CapsuleEvent } from './capsuleUiState'
import { pickerEventForCapsuleUi, type PickerEvent } from './pickerUiState'
import { extractorBusyForDomMutex, type DomPickerEvent } from './domPickerUiState'
import glassCss from '../styles/index.css?inline'

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
    if (window !== window.top) return null
    const existing = document.getElementById('goaria-shadow-host')
    if (existing) {
      if (existing.shadowRoot) return existing.shadowRoot
      existing.remove()
    }
    const host = document.createElement('div')
    host.id = 'goaria-shadow-host'
    host.style.cssText =
      'position:fixed;bottom:20px;right:20px;z-index:2147483647;pointer-events:none'
    document.documentElement.appendChild(host)
    const shadowRoot = host.attachShadow({ mode: 'open' })
    if (!injectStylesIntoShadow(shadowRoot)) {
      // CSP blocked style injection; popup will render without glass CSS.
    }
    return shadowRoot
  } catch {
    return null
  }
}

function capsuleIsPainted(): boolean {
  const host = document.getElementById('goaria-shadow-host')
  const root = host?.shadowRoot
  if (!root) return false
  return !!root.querySelector('[data-extractor-capsule]')
}

let watchdogTimer: ReturnType<typeof setTimeout> | null = null
let leaseTimer: ReturnType<typeof setTimeout> | null = null

function clearWatchdog(): void {
  if (watchdogTimer) {
    clearTimeout(watchdogTimer)
    watchdogTimer = null
  }
}

function clearLeaseTimer(): void {
  if (leaseTimer) {
    clearTimeout(leaseTimer)
    leaseTimer = null
  }
}

function armWatchdog(ui: string): void {
  clearWatchdog()
  const ms =
    ui === 'resolving'
      ? EXTRACTOR_RESOLVE_WATCHDOG_MS
      : ui === 'committing'
        ? EXTRACTOR_BATCH_WATCHDOG_MS
        : 0
  if (!ms) return
  watchdogTimer = setTimeout(() => {
    applyCapsule({ type: 'watchdog' })
  }, ms)
}

function armLease(deadline?: number): void {
  clearLeaseTimer()
  if (typeof deadline !== 'number' || !Number.isFinite(deadline)) return
  const fire = (): void => {
    leaseTimer = null
    const shownToken = capsuleView.state.pageToken
    applyCapsule({ type: 'hide', reason: 'nav' })
    reportPageNav(shownToken)
  }
  const ms = deadline - Date.now()
  if (ms <= 0) {
    fire()
    return
  }
  leaseTimer = setTimeout(fire, ms)
}

function applyCapsule(event: CapsuleEvent): void {
  const next = capsuleView.apply(event)
  const teardown = pickerEventForCapsuleUi(next.ui)
  if (teardown) pickerView.apply(teardown)
  if (next.ui === 'hidden' || next.ui === 'success' || next.ui === 'error') {
    clearLeaseTimer()
  }
  if (next.ui === 'resolving' || next.ui === 'committing') {
    armWatchdog(next.ui)
  } else {
    clearWatchdog()
  }
}

function applyPicker(event: PickerEvent): void {
  if (domPickerView.state.phase !== 'closed') {
    if (
      event.type === 'open' ||
      event.type === 'catalog' ||
      event.type === 'submit' ||
      event.type === 'readyRestore'
    ) {
      return
    }
  }
  pickerView.apply(event)
}

function applyDomPicker(event: DomPickerEvent): void {
  domPickerView.apply(event)
  if (event.type === 'close' || event.type === 'not_found') {
    stopDomAlivePoll()
    domCatalogId = ''
    domOpenedHref = ''
  }
}

let documentNonce = mintDirectBatchRequestId()
let domCatalogId = ''
let domOpenedHref = ''
let domAliveTimer: ReturnType<typeof setInterval> | null = null

function stopDomAlivePoll(): void {
  if (domAliveTimer) {
    clearInterval(domAliveTimer)
    domAliveTimer = null
  }
}

function startDomAlivePoll(): void {
  stopDomAlivePoll()
  domAliveTimer = setInterval(() => {
    if (domPickerView.state.phase === 'closed' || !domCatalogId) {
      stopDomAlivePoll()
      return
    }
    if (location.href !== domOpenedHref) {
      const id = domCatalogId
      applyDomPicker({ type: 'close' })
      if (id) {
        void sendMessage('dom:cancel', { catalog_id: id }, 'background').catch(() => undefined)
      }
      return
    }
    void sendMessage('dom:alive', { catalog_id: domCatalogId }, 'background')
      .then(reply => {
        if (!reply?.ok) {
          teardownDomOverlay()
        }
      })
      .catch(() => {
        teardownDomOverlay()
      })
  }, 1000)
}

function recastDocumentNonce(): void {
  documentNonce = mintDirectBatchRequestId()
}

function teardownDomOverlay(): void {
  const id = domCatalogId || domPickerView.state.catalogId
  applyDomPicker({ type: 'close' })
  if (id) {
    void sendMessage('dom:cancel', { catalog_id: id }, 'background').catch(() => undefined)
  }
}

async function currentPageToken(): Promise<string | undefined> {
  return pageTokenFromHref(location.href)
}

capsuleView.onClick = () => {
  void (async () => {
    if (domPickerView.state.phase !== 'closed') return
    const ui = capsuleView.state.ui
    if (ui === 'resolving' || ui === 'committing') return
    const shownToken = capsuleView.state.pageToken
    const token = await currentPageToken()
    if (!token || token !== shownToken) {
      applyCapsule({ type: 'hide', reason: 'nav' })
      reportPageNav(shownToken)
      return
    }
    if (ui === 'ready' && capsuleView.state.count > 1) {
      try {
        const reply = await sendMessage('extractor:picker-open', { page_token: token }, 'background')
        if (reply?.ok && reply.items && reply.items.length > 0) {
          applyPicker({
            type: 'open',
            pageToken: token,
            items: reply.items,
            count: reply.count,
            lease_deadline: reply.lease_deadline,
          })
          armLease(reply.lease_deadline)
          return
        }
        if (reply?.error_code) {
          applyCapsule({
            type: 'result',
            pageToken: token,
            ui: 'error',
            error_code: reply.error_code,
          })
          applyPicker({ type: 'close' })
        }
      } catch {
        applyCapsule({
          type: 'result',
          pageToken: token,
          ui: 'error',
          error_code: 'disconnected',
        })
        applyPicker({ type: 'close' })
      }
      return
    }
    try {
      const reply = await sendMessage('extractor:click', { page_token: token }, 'background')
      if (reply?.accepted) {
        applyCapsule({ type: 'clickAccepted' })
        return
      }
      if (reply?.error_code) {
        applyCapsule({
          type: 'result',
          pageToken: token,
          ui: 'error',
          error_code: reply.error_code,
        })
      }
    } catch {
      applyCapsule({
        type: 'result',
        pageToken: token,
        ui: 'error',
        error_code: 'disconnected',
      })
    }
  })()
}

let popupReady = false
let capsuleMountFailed = false
let shadowHostCreated = false
let mountedApp: ReturnType<typeof mount> | undefined
let pendingDetect: ExtractorDetectedMessage | undefined
let ignoredPageToken: string | undefined
const canPaintCapsule = window === window.top

function reportPageNav(oldToken: string | undefined): void {
  if (!oldToken) return
  void sendMessage('extractor:nav', { page_token: oldToken }, 'background').catch(() => undefined)
}

function reportMountFallback(pageToken: string | undefined): void {
  if (!pageToken) return
  void sendMessage('extractor:fallback', { page_token: pageToken }, 'background').catch(() => undefined)
}

function failCapsuleMount(): void {
  capsuleMountFailed = true
  popupReady = false
  const pending = pendingDetect
  pendingDetect = undefined
  reportMountFallback(pending?.page_token)
}

async function replayPendingDetect(): Promise<void> {
  const pending = pendingDetect
  if (!pending) return
  try {
    const token = await currentPageToken()
    if (!token || token !== pending.page_token) {
      pendingDetect = undefined
      return
    }
    ignoredPageToken = undefined
    applyCapsule({
      type: 'detect',
      generation: pending.generation,
      pageToken: token,
    })
    pendingDetect = undefined
  } catch {
    pendingDetect = undefined
    reportMountFallback(pending.page_token)
  }
}

capsuleView.onIgnore = () => {
  void (async () => {
    const token = capsuleView.state.pageToken
    if (token) ignoredPageToken = token
    if (token) {
      try {
        await sendMessage('extractor:ignore', { page_token: token }, 'background')
      } catch {
        /* ignore */
      }
    }
    applyCapsule({ type: 'hide', pageToken: token || undefined })
  })()
}

pickerView.onCancel = () => {
  applyPicker({ type: 'close' })
}

domPickerView.onCancel = () => {
  teardownDomOverlay()
}

domPickerView.onSubmit = payload => {
  void (async () => {
    if (domPickerView.state.phase === 'closed') return
    const catalogId = domPickerView.state.catalogId
    if (!catalogId) return
    applyDomPicker({ type: 'submit' })
    try {
      const reply = await sendMessage(
        'dom:submit',
        {
          catalog_id: catalogId,
          indices: payload.indices,
          create_group: payload.create_group,
          folder_name: payload.folder_name,
        },
        'background',
      )
      if (reply?.accepted) {
        applyDomPicker({ type: 'close' })
        return
      }
      if (reply?.error_code === 'busy') {
        applyDomPicker({ type: 'busy' })
        return
      }
      if (reply?.error_code === 'pending') {
        applyDomPicker({ type: 'pending' })
        return
      }
      applyDomPicker({ type: 'close' })
    } catch {
      teardownDomOverlay()
    }
  })()
}

pickerView.onSubmit = payload => {
  void (async () => {
    if (pickerView.state.phase === 'closed') return
    const snapshot = pickerView.state
    const shownToken = snapshot.pageToken
    const token = await currentPageToken()
    if (!token || token !== shownToken) {
      applyCapsule({ type: 'hide', reason: 'nav' })
      reportPageNav(shownToken)
      return
    }
    applyPicker({ type: 'submit' })
    try {
      const reply = await sendMessage(
        'extractor:picker-submit',
        {
          page_token: token,
          indices: payload.indices,
          create_group: payload.create_group,
          folder_name: payload.folder_name,
        },
        'background',
      )
      if (reply?.accepted) return
      if (reply?.error_code === 'session_expired') {
        applyCapsule({
          type: 'result',
          pageToken: token,
          ui: 'error',
          error_code: 'session_expired',
        })
        applyPicker({ type: 'close' })
        return
      }
      applyPicker({
        type: 'open',
        pageToken: snapshot.pageToken,
        items: snapshot.items,
        count: snapshot.count,
        lease_deadline: snapshot.leaseDeadline,
        awaitingCatalog: snapshot.awaitingCatalog,
      })
      if (reply?.error_code && reply.error_code !== 'busy' && reply.error_code !== 'invalid_request') {
        applyCapsule({
          type: 'result',
          pageToken: token,
          ui: 'error',
          error_code: reply.error_code,
        })
        applyPicker({ type: 'close' })
      }
    } catch {
      applyCapsule({
        type: 'result',
        pageToken: token,
        ui: 'error',
        error_code: 'disconnected',
      })
      applyPicker({ type: 'close' })
    }
  })()
}

onMessage('download:intercepted', ({ data }): InterceptedReply => {
  if (popupReady) {
    popupQueue.push(data as DownloadInterceptedMessage)
    return 'shown'
  }
  return 'fallback'
})

onMessage(
  'extractor:detected',
  async ({ data }: { data: ExtractorDetectedMessage }): Promise<InterceptedReply> => {
    if (!popupReady) {
      if (!canPaintCapsule || capsuleMountFailed || !shadowHostCreated) {
        pendingDetect = undefined
        return 'fallback'
      }
      pendingDetect = data
      return 'pending'
    }
    try {
      const token = await currentPageToken()
      if (!token || token !== data.page_token) return 'fallback'
      ignoredPageToken = undefined
      applyCapsule({
        type: 'detect',
        generation: data.generation,
        pageToken: token,
      })
      await tick()
      return capsuleIsPainted() ? 'shown' : 'fallback'
    } catch {
      return 'fallback'
    }
  },
)

onMessage('extractor:hide', ({ data }: { data: ExtractorHideMessage }) => {
  void (async () => {
    if (!data.page_token) {
      pendingDetect = undefined
    } else if (pendingDetect && pendingDetect.page_token === data.page_token) {
      pendingDetect = undefined
    }
    if (data.page_token) {
      const token = await currentPageToken()
      if (!token || token !== data.page_token) return
    }
    applyCapsule({ type: 'hide', reason: data.reason, pageToken: data.page_token })
  })()
})

onMessage('extractor:picker-catalog', async ({ data }: { data: ExtractorPickerCatalogMessage }) => {
  if (domPickerView.state.phase !== 'closed') return
  const token = await currentPageToken()
  if (!token || token !== data.page_token) return
  applyPicker({
    type: 'catalog',
    pageToken: token,
    items: data.items,
    count: data.count,
    lease_deadline: data.lease_deadline,
  })
  armLease(data.lease_deadline)
})

onMessage('extractor:result', async ({ data }: { data: ExtractorResultMessage }) => {
  if (ignoredPageToken && ignoredPageToken === data.page_token) return
  const token = await currentPageToken()
  if (!token || token !== data.page_token) return
  applyCapsule({
    type: 'result',
    pageToken: data.page_token,
    ui: data.ui,
    count: data.count,
    filename: data.filename,
    error_code: data.error_code,
  })
  if (data.ui === 'committing') {
    applyPicker({ type: 'submit' })
    return
  }
  if (data.ui === 'success' || data.ui === 'error') {
    applyPicker({ type: 'close' })
    return
  }
  if (data.ui === 'ready') {
    armLease(data.lease_deadline)
    if (pickerView.state.phase === 'open' || pickerView.state.awaitingCatalog) {
      applyPicker({ type: 'readyRestore', pageToken: data.page_token })
    }
  }
})

onMessage('dom:ping', (): DomPingReply => ({
  document_nonce: documentNonce,
  page_href: location.href,
  extractor_picker_open: extractorBusyForDomMutex(
    pickerView.state.phase,
    pickerView.state.awaitingCatalog,
  ),
  dom_picker_open: domPickerView.state.phase !== 'closed',
}))

onMessage('dom:scan', (): DomScanReply => {
  const result = collectDomLinks(document as unknown as DomScanRoot)
  return {
    items: result.items.map(item => ({
      url: item.url,
      kind: item.kind,
      filename: item.filename,
      document_policy: item.documentPolicy,
      element_policy: item.elementPolicy,
      rel_noreferrer: item.relNoreferrer,
    })),
    truncated: result.truncated,
    title: result.title,
    document_nonce: documentNonce,
    page_href: location.href,
  }
})

onMessage('dom:open', ({ data }: { data: DomOpenMessage }): DomOpenReply => {
  if (extractorBusyForDomMutex(pickerView.state.phase, pickerView.state.awaitingCatalog)) {
    return { ok: false }
  }
  if (!data?.catalog_id || !Array.isArray(data.items) || data.items.length === 0) {
    return { ok: false }
  }
  applyDomPicker({
    type: 'open',
    catalogId: data.catalog_id,
    items: data.items,
    truncated: data.truncated,
    storeUnproven: data.store_unproven,
    folderPrefill: data.folder_prefill,
  })
  if (domPickerView.state.phase === 'closed' || domPickerView.state.catalogId !== data.catalog_id) {
    return { ok: false }
  }
  domCatalogId = data.catalog_id
  domOpenedHref = location.href
  startDomAlivePoll()
  return { ok: true }
})

onMessage('dom:close', ({ data }: { data: DomCloseMessage }) => {
  if (data?.catalog_id && domCatalogId && data.catalog_id !== domCatalogId) return
  applyDomPicker({ type: 'close' })
})

try {
  const shadowRoot = createShadowHost()
  if (shadowRoot) {
    shadowHostCreated = true
    for (const child of [...shadowRoot.children]) {
      if (child.tagName !== 'STYLE') child.remove()
    }
    void configState
      .loadEffects()
      .then(async () => {
        try {
          capsuleView.effects = configState.effects
          if (mountedApp) {
            await unmount(mountedApp)
            mountedApp = undefined
          }
          mountedApp = mount(ShadowRootApp, { target: shadowRoot })
          popupReady = true
          await replayPendingDetect()
        } catch {
          failCapsuleMount()
        }
      })
      .catch(() => {
        failCapsuleMount()
      })
  } else {
    capsuleMountFailed = true
  }
} catch {
  capsuleMountFailed = true
}

try {
  browser.storage.onChanged.addListener((changes, area) => {
    if (area !== 'local') return
    const change = changes[STORAGE_KEY_EFFECTS]
    if (!change) return
    const val = change.newValue
    if (val === 'full' || val === 'reduced') {
      configState.effects = val
      capsuleView.effects = val
    }
  })
} catch {
  /* storage.onChanged unavailable */
}

setInterval(() => {
  if (capsuleView.state.ui === 'hidden') return
  void currentPageToken().then(token => {
    const shownToken = capsuleView.state.pageToken
    if (!token || token !== shownToken) {
      applyCapsule({ type: 'hide', reason: 'nav' })
      reportPageNav(shownToken)
    }
  })
}, 1000)

window.addEventListener('pagehide', () => {
  teardownDomOverlay()
})

window.addEventListener('pageshow', event => {
  if (event.persisted) {
    recastDocumentNonce()
    teardownDomOverlay()
  }
})

window.addEventListener('hashchange', () => {
  teardownDomOverlay()
})
