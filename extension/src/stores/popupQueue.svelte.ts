import type { DownloadInterceptedMessage } from '../utils/messaging'

// Max messages visible at once: 1 displayed + buffered, 3 total.
const MAX_TOTAL = 3

class PopupQueue {
  current = $state<DownloadInterceptedMessage | null>(null)
  private queue: DownloadInterceptedMessage[] = []

  push(msg: DownloadInterceptedMessage): void {
    // Enforce the total cap: current (1 if set) + buffered queue.
    const currentCount = this.current ? 1 : 0
    if (currentCount + this.queue.length >= MAX_TOTAL) {
      this.queue.shift()
    }
    this.queue.push(msg)
    if (!this.current) {
      this.current = this.queue.shift() ?? null
    }
  }

  dismiss(): void {
    this.current = this.queue.shift() ?? null
  }
}

export const popupQueue = new PopupQueue()
