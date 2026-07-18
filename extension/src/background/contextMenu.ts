import browser from 'webextension-polyfill'
import { wsClient } from './wsClient'
import { getCookiesForUrl } from './cookieCapture'
import { getDownloadPageUrl } from './refererCapture'
import { getScheme } from '../interceptors/DownloadLinkInterceptor'
import type { DownloadHandoffMessage } from '../utils/messaging'

// Bilingual menu title (structured for future i18n migration).
const MENU_TITLE_ZH = '用 GoAria 下载'
const MENU_ID = 'goaria-download-link'

// Bilingual HLS unsupported prompt text.
const HLS_UNSUPPORTED_TITLE = 'GoAria — HLS 暂不支持 / HLS Not Yet Supported'
const HLS_UNSUPPORTED_BODY =
  'HLS 视频流下载即将推出，暂不支持。请等待后续版本。' +
  ' / HLS video stream download is coming soon, not supported yet.'

/**
 * Register the right-click "Download with GoAria" context menu. The menu item
 * is created on install/update via runtime.onInstalled + removeAll (idempotent
 * across SW restarts — Chrome/Firefox persist the registration). The click
 * listener is bound at top level so it re-attaches on every SW restart.
 */
export function initContextMenu(): void {
  browser.runtime.onInstalled.addListener(() => {
    void browser.contextMenus.removeAll().then(() => {
      browser.contextMenus.create({
        id: MENU_ID,
        title: MENU_TITLE_ZH,
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
      title: HLS_UNSUPPORTED_TITLE,
      message: HLS_UNSUPPORTED_BODY,
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
