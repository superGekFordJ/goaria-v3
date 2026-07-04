import browser from 'webextension-polyfill'
import { DownloadLinkInterceptor } from './DownloadLinkInterceptor'
import type { InterceptionContext } from './LinkGrabberResponse'
import { SMALL_FILE_THRESHOLD_BYTES } from '../stores/config.svelte'
import {
  getAllPendingDecisions,
  removePendingDecision,
  savePendingDecision,
  updatePendingStatus,
  type PendingDecision,
} from '../background/pendingDecisionStore'

// onDeterminingFilename is Chrome-specific and absent from the
// webextension-polyfill type definitions. The polyfill forwards it to
// chrome.downloads.onDeterminingFilename at runtime, so we declare a minimal
// typed surface here.
type FilenameSuggestion = {
  filename?: string
  conflictAction?: 'uniquify' | 'overwrite' | 'prompt'
}
type SuggestFn = (suggestion?: FilenameSuggestion) => void
type OnDeterminingFilenameItem = Pick<
  browser.Downloads.DownloadItem,
  'id' | 'url' | 'filename' | 'fileSize' | 'totalBytes' | 'referrer' | 'byExtensionId' | 'mime'
>
type OnDeterminingFilenameListener = (
  item: OnDeterminingFilenameItem,
  suggest: SuggestFn,
) => boolean | void

interface DownloadsDeterminingFilenameEvent {
  addListener(listener: OnDeterminingFilenameListener): void
  hasListener(listener: OnDeterminingFilenameListener): boolean
  removeListener(listener: OnDeterminingFilenameListener): void
}

function getOnDeterminingFilename(): DownloadsDeterminingFilenameEvent | null {
  const downloads = browser.downloads as unknown as {
    onDeterminingFilename?: DownloadsDeterminingFilenameEvent
  }
  return downloads.onDeterminingFilename ?? null
}

/**
 * Chrome MV3 interceptor — path B (IDM strategy). The browser starts the
 * download, we pause it, ask GoAria to accept the handoff, then either cancel
 * + erase the browser entry (success) or resume it (failure). onDeterminingFilename
 * delays filename finalization so cancel/resume can run before the file lands.
 *
 * In-memory mirrors of the pending-decision status let onDeterminingFilename
 * make a synchronous decision (the listener must return true synchronously to
 * delay filename finalization; storage.session reads are async).
 */
export class ChromeDownloadsApiInterceptor extends DownloadLinkInterceptor {
  // Download ids we have paused, for synchronous onDeterminingFilename checks.
  private pausedIds = new Set<number>()
  // In-memory mirror of the persisted status so the synchronous filename
  // listener doesn't need to await storage.session.
  private statusMirror = new Map<number, PendingDecision['status']>()

  private onCreatedListener = (item: browser.Downloads.DownloadItem): void => {
    void this.onDownloadCreated(item)
  }
  private onDeterminingListener: OnDeterminingFilenameListener = (item, suggest) => {
    return this.onDeterminingFilename(item, suggest)
  }

  register(): void {
    browser.downloads.onCreated.addListener(this.onCreatedListener)
    const ev = getOnDeterminingFilename()
    if (ev) ev.addListener(this.onDeterminingListener)
  }

  async recoverPendingDecisions(): Promise<void> {
    const pending = await getAllPendingDecisions()
    for (const [downloadId, decision] of pending) {
      try {
        const items = await browser.downloads.search({ id: downloadId })
        const item = items[0]
        if (!item || item.state === 'complete') {
          await removePendingDecision(downloadId)
          continue
        }
        if (item.state === 'in_progress' && item.paused) {
          // Still paused from before the SW died — re-run the decision.
          this.pausedIds.add(downloadId)
          this.statusMirror.set(downloadId, 'pending')
          const ctx = this.contextFromDecision(decision)
          void this.handlePausedDownload(downloadId, ctx)
        } else {
          // SW death let the download auto-resume; abandon takeover.
          await removePendingDecision(downloadId)
        }
      } catch {
        await removePendingDecision(downloadId)
      }
    }
  }

  private async onDownloadCreated(item: browser.Downloads.DownloadItem): Promise<void> {
    // Skip downloads triggered by other extensions (defensive — avoids loops).
    if (item.byExtensionId) return

    const ctx: InterceptionContext = {
      url: item.url,
      tabId: -1,
      mimeType: item.mime ?? '',
      contentDisposition: '',
      fileSize: item.fileSize > 0 ? item.fileSize : item.totalBytes > 0 ? item.totalBytes : 0,
      filename: item.filename || '',
      referrer: item.referrer ?? '',
    }

    const decision = this.shouldIntercept(ctx)
    if (decision !== 'intercept') return

    try {
      await browser.downloads.pause(item.id)
    } catch {
      // pause may fail for completed/interrupted/small-file downloads.
      try {
        const fresh = await browser.downloads.search({ id: item.id })
        const state = fresh[0]?.state
        if (state === 'complete' || state === 'interrupted') return
      } catch {
        return
      }
      // Still in_progress despite pause error — onDeterminingFilename will
      // delay completion; proceed with the handoff attempt.
    }
    this.pausedIds.add(item.id)
    this.statusMirror.set(item.id, 'pending')
    void this.handlePausedDownload(item.id, ctx)
  }

  private async handlePausedDownload(
    downloadId: number,
    ctx: InterceptionContext,
  ): Promise<void> {
    const decision: PendingDecision = {
      url: ctx.url,
      filename: ctx.filename,
      fileSize: ctx.fileSize,
      startTime: Date.now(),
      status: 'pending',
    }
    await savePendingDecision(downloadId, decision)

    try {
      const req = await this.constructDownloadRequest(ctx)
      const resp = await this.requestAddDownload(req)
      if (resp.success) {
        this.statusMirror.set(downloadId, 'canceling')
        await updatePendingStatus(downloadId, 'canceling')
        await this.cancelAndErase(downloadId)
        this.cleanupMemory(downloadId)
        await removePendingDecision(downloadId)
        this.notifyIntercepted(ctx, true)
      } else {
        this.statusMirror.set(downloadId, 'resuming')
        await updatePendingStatus(downloadId, 'resuming')
        await this.resumeDownload(downloadId)
        this.cleanupMemory(downloadId)
        await removePendingDecision(downloadId)
        this.notifyIntercepted(ctx, false, resp.error)
      }
    } catch (err) {
      // WS dropped or timeout — restore the browser download.
      this.statusMirror.set(downloadId, 'resuming')
      await this.resumeDownload(downloadId)
      this.cleanupMemory(downloadId)
      await removePendingDecision(downloadId)
      const msg = err instanceof Error ? err.message : String(err)
      this.notifyIntercepted(ctx, false, msg)
    }
  }

  /**
   * Synchronous filename listener. Must return true to keep the suggest
   * callback alive (delaying filename finalization) when we intend to
   * cancel/resume the download ourselves.
   */
  private onDeterminingFilename(item: OnDeterminingFilenameItem, suggest: SuggestFn): boolean | void {
    const size = item.fileSize > 0 ? item.fileSize : item.totalBytes
    // Small files: pause is ineffective and takeover adds little value.
    if (size > 0 && size < SMALL_FILE_THRESHOLD_BYTES) {
      suggest()
      return
    }
    if (!this.pausedIds.has(item.id)) {
      // Not our paused download — let the browser proceed normally.
      suggest()
      return
    }
    const status = this.statusMirror.get(item.id) ?? 'pending'
    if (status === 'resuming') {
      // Resume path — let the browser finalize the filename.
      suggest()
      return
    }
    // pending or canceling — hold suggest; cancel/resume will resolve the download.
    return true
  }

  private async cancelAndErase(downloadId: number): Promise<void> {
    try {
      await browser.downloads.cancel(downloadId)
    } catch {
      // download may already be gone — ignore
    }
    try {
      await browser.downloads.erase({ id: downloadId })
    } catch {
      // erase failure is non-fatal
    }
  }

  private async resumeDownload(downloadId: number): Promise<void> {
    try {
      await browser.downloads.resume(downloadId)
    } catch {
      // download may already be complete/interrupted — ignore
    }
  }

  private cleanupMemory(downloadId: number): void {
    this.pausedIds.delete(downloadId)
    this.statusMirror.delete(downloadId)
  }

  private contextFromDecision(decision: PendingDecision): InterceptionContext {
    return {
      url: decision.url,
      tabId: -1,
      mimeType: '',
      contentDisposition: '',
      fileSize: decision.fileSize,
      filename: decision.filename,
      referrer: '',
    }
  }
}
