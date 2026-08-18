export type PendingKind = 'download' | 'rpc'

export type PendingEntry<T = unknown> = {
  id: string
  kind: PendingKind
  resolve: (value: T) => void
  reject: (err: Error) => void
}

export type RoutedMessage = {
  type?: unknown
  request_id?: unknown
  [key: string]: unknown
}

export type RouteResult<T> =
  | { kind: 'download_ack'; entry: PendingEntry<T> }
  | { kind: 'protocol_error'; entry: PendingEntry<T> }
  | { kind: 'typed_ack'; entry: PendingEntry<T> }
  | { kind: 'ignored' }

const MSG_DOWNLOAD_ACK = 'download_ack'
const MSG_PROTOCOL_ERROR = 'protocol_error'
const MSG_EXTRACTOR_RESOLVE_ACK = 'extractor_resolve_ack'
const MSG_BATCH_DOWNLOAD_ACK = 'batch_download_ack'

export function createPendingMap<T = unknown>() {
  const pending = new Map<string, PendingEntry<T>>()

  function add(entry: PendingEntry<T>): void {
    pending.set(entry.id, entry)
  }

  function completeById(id: string): PendingEntry<T> | undefined {
    const entry = pending.get(id)
    if (!entry) return undefined
    pending.delete(id)
    return entry
  }

  function completeFifo(): PendingEntry<T> | undefined {
    for (const entry of pending.values()) {
      if (entry.kind !== 'download') continue
      pending.delete(entry.id)
      return entry
    }
    return undefined
  }

  function failAll(reason: string): void {
    for (const entry of pending.values()) {
      entry.reject(new Error(reason))
    }
    pending.clear()
  }

  function routeMessage(msg: RoutedMessage): RouteResult<T> {
    const type = typeof msg.type === 'string' ? msg.type : ''
    const id = typeof msg.request_id === 'string' && msg.request_id !== '' ? msg.request_id : ''

    if (type === MSG_DOWNLOAD_ACK) {
      if (id) {
        const entry = pending.get(id)
        if (!entry || entry.kind !== 'download') {
          return { kind: 'ignored' }
        }
        pending.delete(id)
        return { kind: 'download_ack', entry }
      }
      const entry = completeFifo()
      if (!entry) {
        return { kind: 'ignored' }
      }
      return { kind: 'download_ack', entry }
    }

    if (type === MSG_PROTOCOL_ERROR) {
      if (!id) return { kind: 'ignored' }
      const entry = completeById(id)
      if (!entry) return { kind: 'ignored' }
      return { kind: 'protocol_error', entry }
    }

    if (type === MSG_EXTRACTOR_RESOLVE_ACK || type === MSG_BATCH_DOWNLOAD_ACK) {
      if (!id) return { kind: 'ignored' }
      const entry = completeById(id)
      if (!entry) return { kind: 'ignored' }
      return { kind: 'typed_ack', entry }
    }

    return { kind: 'ignored' }
  }

  return {
    add,
    completeById,
    completeFifo,
    failAll,
    routeMessage,
    size: () => pending.size,
  }
}

export type PendingMap<T = unknown> = ReturnType<typeof createPendingMap<T>>
