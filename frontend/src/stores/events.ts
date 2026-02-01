import { Events } from '@wailsio/runtime'

type TaskDelta = {
  type: 'add' | 'update' | 'remove' | 'complete' | 'error' | 'pause' | 'progress'
  gid: string
  payload?: Record<string, unknown>
}

type DeltaHandler = (delta: TaskDelta) => void
type FullSyncHandler = () => void
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

  deltaUnsubscribe = Events.On('task:delta', (ev: any) => {
    const delta = (ev?.data ?? ev) as TaskDelta
    if (import.meta.env.DEV) {
      console.debug('[Events] task:delta', delta)
    }
    onDelta(delta)
  })

  batchDeltaUnsubscribe = Events.On('task:deltas', (ev: any) => {
    const deltas = (ev?.data ?? ev) as TaskDelta[]
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

// Task complete event subscription (backend-driven completion detection)
let taskCompleteUnsubscribe: (() => void) | null = null

export function subscribeToTaskCompleteEvent(onComplete: (gid: string) => void) {
  if (taskCompleteUnsubscribe) return

  taskCompleteUnsubscribe = Events.On('task:complete', (ev: any) => {
    const data = (ev?.data ?? ev) as { gid: string }
    if (import.meta.env.DEV) {
      console.debug('[Events] task:complete', data.gid)
    }
    onComplete(data.gid)
  })
}

export function unsubscribeFromTaskCompleteEvent() {
  taskCompleteUnsubscribe?.()
  taskCompleteUnsubscribe = null
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

  taskMoveUnsubscribe = Events.On('task:move', (ev: any) => {
    const data = (ev?.data ?? ev) as TaskMove
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
