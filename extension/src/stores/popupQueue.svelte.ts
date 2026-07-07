import type { DownloadInterceptedMessage } from '../utils/messaging'

const MAX_QUEUE = 3

class PopupQueue {
  current = $state<DownloadInterceptedMessage | null>(null)
  private queue: DownloadInterceptedMessage[] = []

  push(msg: DownloadInterceptedMessage): void {
    this.queue.push(msg)
    if (this.queue.length > MAX_QUEUE) this.queue.shift()
    if (!this.current) {
      this.current = this.queue.shift() ?? null
    }
  }

  dismiss(): void {
    this.current = this.queue.shift() ?? null
  }
}

export const popupQueue = new PopupQueue()
