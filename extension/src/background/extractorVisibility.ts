import { sendMessage } from 'webext-bridge/background'
import browser from 'webextension-polyfill'
import { t } from '../lib/i18n'
import type {
  ExtractorHideReason,
  ExtractorPickerCatalogMessage,
  ExtractorResultMessage,
  InterceptedReply,
} from '../utils/messaging'
import { pageTokenFromHref } from './pageToken'
import { createExtractorSessionStore, type ExtractorSessionStore } from './extractorSessionStore'
import { cancelAllClicks, cancelTabClick } from './extractorClickLock'
import type { ReplayStorage } from './replayStore'

const HIDE_TAB_CAP = 64
const CONFIRM_RENDER_MS = 1000

function browserSessionStorage(): ReplayStorage {
  return {
    async get(key) {
      try {
        const result = await browser.storage.session.get(key)
        return result[key]
      } catch {
        return undefined
      }
    },
    async set(key, value) {
      await browser.storage.session.set({ [key]: value })
    },
    async remove(key) {
      await browser.storage.session.remove(key)
    },
    async getAll() {
      try {
        return (await browser.storage.session.get(null)) as Record<string, unknown>
      } catch {
        return undefined
      }
    },
  }
}

let store: ExtractorSessionStore | undefined

export function getExtractorSessionStore(): ExtractorSessionStore {
  if (!store) {
    store = createExtractorSessionStore(browserSessionStorage())
  }
  return store
}

export async function deliverExtractorDetected(
  tabId: number,
  generation: number,
  tabUrl: string | undefined,
): Promise<void> {
  const token = await pageTokenFromHref(tabUrl ?? '')
  if (!token) return
  const sessions = getExtractorSessionStore()
  if (await sessions.isIgnored(tabId, token)) return
  const existing = await sessions.getSession(tabId)
  if (existing && existing.pageToken !== token) {
    cancelTabClick(tabId)
    await sessions.deleteSession(tabId)
  }
  try {
    const sendPromise = sendMessage(
      'extractor:detected',
      { generation, page_token: token },
      `content-script@${tabId}`,
    ) as Promise<InterceptedReply | undefined>
    const sendResult = sendPromise.then(
      reply => ({ reply }),
      () => ({ reply: undefined as InterceptedReply | undefined }),
    )
    let timeoutId: ReturnType<typeof setTimeout> | undefined
    const raced = await Promise.race([
      sendResult.then(value => ({ timedOut: false as const, reply: value.reply })),
      new Promise<{ timedOut: true; reply: undefined }>(resolve => {
        timeoutId = setTimeout(() => resolve({ timedOut: true, reply: undefined }), CONFIRM_RENDER_MS)
      }),
    ])
    if (timeoutId !== undefined) clearTimeout(timeoutId)
    if (raced.timedOut) {
      await showExtractorFallbackNotification(tabId, token)
      return
    }
    const reply = raced.reply
    if (reply !== 'shown' && reply !== 'pending') {
      await showExtractorFallbackNotification(tabId, token)
    }
  } catch {
    await showExtractorFallbackNotification(tabId, token)
  }
}

export async function broadcastHide(reason: ExtractorHideReason, pageToken?: string): Promise<void> {
  let httpTabs: browser.Tabs.Tab[]
  try {
    httpTabs = await browser.tabs.query({ url: ['http://*/*', 'https://*/*'] })
  } catch {
    httpTabs = []
  }
  const payload = pageToken ? { reason, page_token: pageToken } : { reason }
  let sent = 0
  for (const tab of httpTabs) {
    if (sent >= HIDE_TAB_CAP) break
    if (typeof tab.id !== 'number') continue
    void sendMessage('extractor:hide', payload, `content-script@${tab.id}`).catch(() => undefined)
    sent++
  }
}

export function notifyExtractorHostDown(reason: 'disconnect'): void {
  // Cancel in-flight resolve/batch so a late ack cannot commit. A later user click may still show disconnected.
  cancelAllClicks()
  void getExtractorSessionStore().clearAll()
  void broadcastHide(reason)
}

export function notifyExtractorMatchCleared(): void {
  cancelAllClicks()
  void getExtractorSessionStore().clearSessions()
  void broadcastHide('generation')
}

export async function pushExtractorResult(tabId: number, payload: ExtractorResultMessage): Promise<void> {
  try {
    await sendMessage('extractor:result', payload, `content-script@${tabId}`)
  } catch {
    /* tab gone */
  }
}

export async function pushPickerCatalog(tabId: number, payload: ExtractorPickerCatalogMessage): Promise<void> {
  try {
    await sendMessage('extractor:picker-catalog', payload, `content-script@${tabId}`)
  } catch {
    /* tab gone */
  }
}

export async function showExtractorFallbackNotification(tabId: number, pageToken: string): Promise<void> {
  const sessions = getExtractorSessionStore()
  if (!(await sessions.shouldNotifyFallback(tabId, pageToken))) return
  try {
    void browser.notifications.create({
      type: 'basic',
      iconUrl: browser.runtime.getURL('icons/icon-128.png'),
      title: t('capsule_notif_title'),
      message: t('capsule_notif_body'),
    })
  } catch {
    /* notifications API unavailable */
  }
}
