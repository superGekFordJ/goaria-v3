import browser from 'webextension-polyfill'
import { wsClient } from './wsClient'
import { getCookiesForUrl } from './cookieCapture'
import { getDownloadPageUrl } from './refererCapture'
import { getScheme } from '../interceptors/DownloadLinkInterceptor'
import type { DownloadHandoffMessage } from '../utils/messaging'
import { t } from '../lib/i18n'
import { urlPathIsM3uPlaylist } from './domCanonicalUrl'
import { handleCollectPageLinks } from './domFlow'
import { hasCapability } from './capabilities'
import { CAP_DOWNLOAD_BATCH } from '../stores/config.svelte'
import { connectionState } from '../stores/connection.svelte'

const MENU_ID = 'goaria-download-link'
const COLLECT_MENU_ID = 'goaria-collect-page-links'

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
      browser.contextMenus.create({
        id: COLLECT_MENU_ID,
        title: t('context_menu_collect_page_links'),
        contexts: ['page'],
      })
    })
  })
  browser.contextMenus.onClicked.addListener(handleContextMenuClick)
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

async function handleContextMenuClick(
  info: browser.Menus.OnClickData,
  tab: browser.Tabs.Tab | undefined,
): Promise<void> {
  if (info.menuItemId === COLLECT_MENU_ID) {
    if (!hasCapability(connectionState.capabilities, CAP_DOWNLOAD_BATCH)) {
      void browser.notifications
        .create({
          type: 'basic',
          iconUrl: browser.runtime.getURL('icons/icon-48.png'),
          title: t('dom_missing_cap_title'),
          message: t('dom_missing_cap_body'),
        })
        .catch(() => undefined)
      return
    }
    await handleCollectPageLinks(info, tab)
    return
  }

  const url = info.linkUrl || info.srcUrl || ''
  if (!url) return

  const scheme = getScheme(url)
  if (scheme !== 'http:' && scheme !== 'https:') return

  if (urlPathIsM3uPlaylist(url)) {
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
