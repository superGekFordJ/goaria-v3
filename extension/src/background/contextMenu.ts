import browser from 'webextension-polyfill'
import { wsClient } from './wsClient'
import { getCookiesForUrl } from './cookieCapture'
import { getDownloadPageUrl } from './refererCapture'
import { getScheme } from '../interceptors/DownloadLinkInterceptor'
import type { DownloadHandoffMessage } from '../utils/messaging'
import { t } from '../lib/i18n'

const MENU_ID = 'goaria-download-link'

/**
 * Register the right-click "Download with GoAria" context menu. The menu item
 * is created on install/update via runtime.onInstalled + removeAll (idempotent
 * across SW restarts — Chrome/Firefox persist the registration). The click
 * listener is bound at top level so it re-attaches on every SW restart.
 *
 * The title is resolved once via getMessage() at creation time. A browser UI
 * language change mid-session won't refresh it until the next onInstalled
 * (extension update/reload). This is an accepted trade-off to avoid extra
 * language-change plumbing.
 */
export function initContextMenu(): void {
  browser.runtime.onInstalled.addListener(() => {
    void browser.contextMenus.removeAll().then(() => {
      browser.contextMenus.create({
        id: MENU_ID,
        title: t('context_menu_download_with'),
        contexts: ['link', 'video', 'audio'],
      })
    })
  })
  browser.contextMenus.onClicked.addListener(handleContextMenuClick)
}

/** Detect HLS playlist URLs by pathname suffix (case-insensitive). */
function isM3u8Url(url: string): boolean {
  try {
    const path = new URL(url).pathname.toLowerCase()
    return path.endsWith('.m3u8') || path.endsWith('.m3u')
  } catch {
    return false
  }
}

/** Show a system notification that HLS download is not yet supported. */
function showHlsUnsupportedPrompt(): void {
  void browser.notifications
    .create({
      type: 'basic',
      iconUrl: browser.runtime.getURL('icons/icon-48.png'),
      title: t('context_hls_unsupported_title'),
      message: t('context_hls_unsupported_body'),
    })
    .catch(() => {
      // notifications API may be unavailable in some environments.
    })
}

/**
 * Context menu click handler: extract the link/media URL, gracefully block
 * m3u8 URLs (HLS engine not ready), otherwise forward a normal download
 * request through the WS pipeline. Honors user intent — no content-type or
 * MIME filtering (the user explicitly chose "Download with GoAria").
 */
async function handleContextMenuClick(
  info: browser.Menus.OnClickData,
  tab: browser.Tabs.Tab | undefined,
): Promise<void> {
  const url = info.linkUrl || info.srcUrl || ''
  if (!url) return

  const scheme = getScheme(url)
  if (scheme !== 'http:' && scheme !== 'https:') return

  if (isM3u8Url(url)) {
    showHlsUnsupportedPrompt()
    return
  }

  try {
    const [headers, downloadPage] = await Promise.all([
      getCookiesForUrl(url),
      getDownloadPageUrl({ tabId: tab?.id, referrer: info.pageUrl }),
    ])
    const req: DownloadHandoffMessage = {
      type: 'download',
      url,
      finalUrl: '',
      headers,
      fileSize: 0,
      skipHeadProbe: false,
      dedupKey: '',
      filename: '',
      downloadPage,
    }
    await wsClient.sendDownloadRequest(req)
  } catch (err) {
    // No UI to surface errors from a context menu click; degrade silently.
    console.warn('GoAria context menu download failed:', err)
  }
}
