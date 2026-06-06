import { Events } from '@wailsio/runtime'

export type TaskDelta = {
  type: 'add' | 'update' | 'remove' | 'complete' | 'error' | 'pause' | 'progress'
  gid: string
  payload?: Record<string, unknown>
}

type DeltaHandler = (delta: TaskDelta) => void
type FullSyncHandler = () => void | Promise<void>
type ConnectionHandler = (connected: boolean) => void

let deltaUnsubscribe: (() => void) | null = null
let batchDeltaUnsubscribe: (() => void) | null = null
let fullSyncUnsubscribe: (() => void) | null = null
let connectionUnsubscribe: (() => void) | null = null

export function subscribeToTaskEvents(
  onDelta: DeltaHandler,
  onFullSync: FullSyncHandler,
  onConnectionChange?: ConnectionHandler,
) {
  if (deltaUnsubscribe || batchDeltaUnsubscribe || fullSyncUnsubscribe || connectionUnsubscribe) {
    return
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  deltaUnsubscribe = Events.On('task:delta', (ev: any) => {
    const delta = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as TaskDelta
    if (import.meta.env.DEV) {
      console.debug('[Events] task:delta', delta)
    }
    onDelta(delta)
  })

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  batchDeltaUnsubscribe = Events.On('task:deltas', (ev: any) => {
    const deltas = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as TaskDelta[]
    if (import.meta.env.DEV) {
      // console.debug('[Events] task:deltas', deltas.length)
    }
    deltas.forEach(onDelta)
  })

  fullSyncUnsubscribe = Events.On('task:fullsync', () => {
    if (import.meta.env.DEV) {
      console.debug('[Events] task:fullsync triggered')
    }
    onFullSync()
  })

  if (onConnectionChange) {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    connectionUnsubscribe = Events.On('aria2:connection', (ev: any) => {
      const data = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as { connected: boolean }
      if (import.meta.env.DEV) {
        console.debug('[Events] aria2:connection', data.connected)
      }
      onConnectionChange(data.connected)
    })
  }
}

export function unsubscribeFromTaskEvents() {
  deltaUnsubscribe?.()
  batchDeltaUnsubscribe?.()
  fullSyncUnsubscribe?.()
  connectionUnsubscribe?.()

  deltaUnsubscribe = null
  batchDeltaUnsubscribe = null
  fullSyncUnsubscribe = null
  connectionUnsubscribe = null
}

// Window lifecycle event subscriptions
let windowCreatedUnsubscribe: (() => void) | null = null

export function subscribeToWindowEvents(onWindowCreated: () => void | Promise<void>) {
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

// Task move event subscription (cross-list metadata preservation)
export type TaskMove = {
  gid: string
  from: 'active' | 'waiting' | 'stopped'
  to: 'active' | 'waiting' | 'stopped'
  task: Record<string, unknown>
}

let taskMoveUnsubscribe: (() => void) | null = null

export function subscribeToTaskMoveEvent(onMove: (move: TaskMove) => void) {
  if (taskMoveUnsubscribe) return

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  taskMoveUnsubscribe = Events.On('task:move', (ev: any) => {
    const data = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as TaskMove
    if (import.meta.env.DEV) {
      console.debug('[Events] task:move', data.gid, data.from, '->', data.to)
    }
    onMove(data)
  })
}

export function unsubscribeFromTaskMoveEvent() {
  taskMoveUnsubscribe?.()
  taskMoveUnsubscribe = null
}
