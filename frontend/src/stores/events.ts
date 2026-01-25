import { Events } from '@wailsio/runtime'

type TaskDelta = {
  type: 'add' | 'update' | 'remove' | 'complete' | 'error' | 'pause'
  gid: string
  payload?: Record<string, unknown>
}

type DeltaHandler = (delta: TaskDelta) => void
type FullSyncHandler = () => void
type ConnectionHandler = (connected: boolean) => void

let deltaUnsubscribe: (() => void) | null = null
let fullSyncUnsubscribe: (() => void) | null = null
let connectionUnsubscribe: (() => void) | null = null

export function subscribeToTaskEvents(
  onDelta: DeltaHandler,
  onFullSync: FullSyncHandler,
  onConnectionChange?: ConnectionHandler,
) {
  if (deltaUnsubscribe || fullSyncUnsubscribe || connectionUnsubscribe) {
    return
  }

  deltaUnsubscribe = Events.On('task:delta', (ev: any) => {
    const delta = (ev?.data ?? ev) as TaskDelta
    if (import.meta.env.DEV) {
      console.debug('[Events] task:delta', delta)
    }
    onDelta(delta)
  })

  fullSyncUnsubscribe = Events.On('task:fullsync', () => {
    if (import.meta.env.DEV) {
      console.debug('[Events] task:fullsync triggered')
    }
    onFullSync()
  })

  if (onConnectionChange) {
    connectionUnsubscribe = Events.On('aria2:connection', (ev: any) => {
      const data = (ev?.data ?? ev) as { connected: boolean }
      if (import.meta.env.DEV) {
        console.debug('[Events] aria2:connection', data.connected)
      }
      onConnectionChange(data.connected)
    })
  }
}

export function unsubscribeFromTaskEvents() {
  deltaUnsubscribe?.()
  fullSyncUnsubscribe?.()
  connectionUnsubscribe?.()

  deltaUnsubscribe = null
  fullSyncUnsubscribe = null
  connectionUnsubscribe = null
}

// Window lifecycle event subscriptions
let windowCreatedUnsubscribe: (() => void) | null = null

export function subscribeToWindowEvents(onWindowCreated: () => void) {
  if (windowCreatedUnsubscribe) return

  windowCreatedUnsubscribe = Events.On('window:created', () => {
    if (import.meta.env.DEV) {
      console.debug('[Events] window:created')
    }
    onWindowCreated()
  })
}

export function unsubscribeFromWindowEvents() {
  windowCreatedUnsubscribe?.()
  windowCreatedUnsubscribe = null
}
