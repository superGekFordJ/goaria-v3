import { TaskState } from './state'
import { TaskActions } from './actions'
import { TaskPolling } from './polling'
import { GetTaskMetadata } from '../../../bindings/goaria-v3/app.js'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import { cacheMetadata, applyMetadataFromCache, removeMetadata } from './metadata'
import { TaskMove } from '../events'

export function setupEvents(state: TaskState, actions: TaskActions, polling: TaskPolling) {
  const { tasks, selectedGids, throttledUpdateTrayIcon, immediateUpdateTrayIcon } = state
  const { fetchActiveTasks, fetchTasks } = actions
  const { metadataPending, setMetadataInFlight } = actions // Shared state from actions

  function moveTaskToStopped(gid: string) {
    const activeIdx = tasks.value.active.findIndex(t => t.gid === gid)
    const waitingIdx = activeIdx === -1 ? tasks.value.waiting.findIndex(t => t.gid === gid) : -1

    if (activeIdx !== -1) {
      const task = tasks.value.active[activeIdx]
      if (task.status !== 'error') task.status = 'complete'
      tasks.value = {
        active: tasks.value.active.filter(t => t.gid !== gid),
        waiting: tasks.value.waiting,
        stopped: [task, ...tasks.value.stopped],
      }
    } else if (waitingIdx !== -1) {
      const task = tasks.value.waiting[waitingIdx]
      if (task.status !== 'error') task.status = 'complete'
      tasks.value = {
        active: tasks.value.active,
        waiting: tasks.value.waiting.filter(t => t.gid !== gid),
        stopped: [task, ...tasks.value.stopped],
      }
    }
  }

  function removeTaskFromState(gid: string) {
    const inActive = tasks.value.active.some(t => t.gid === gid)
    const inWaiting = tasks.value.waiting.some(t => t.gid === gid)
    const inStopped = tasks.value.stopped.some(t => t.gid === gid)

    if (inActive || inWaiting || inStopped) {
      tasks.value = {
        active: inActive ? tasks.value.active.filter(t => t.gid !== gid) : tasks.value.active,
        waiting: inWaiting ? tasks.value.waiting.filter(t => t.gid !== gid) : tasks.value.waiting,
        stopped: inStopped ? tasks.value.stopped.filter(t => t.gid !== gid) : tasks.value.stopped,
      }
    }
    if (selectedGids.value.has(gid)) {
      selectedGids.value.delete(gid)
      selectedGids.value = new Set(selectedGids.value)
    }
    removeMetadata(gid)
  }

  function patchTaskStatus(gid: string, status: string) {
    for (const list of [tasks.value.active, tasks.value.waiting]) {
      const task = list.find(t => t.gid === gid)
      if (task && task.status !== status) {
        task.status = status
        tasks.value = { ...tasks.value }
        return
      }
    }
  }

  async function handleTaskDelta(delta: { type: string; gid: string; payload?: any }) {
    if (import.meta.env.DEV) {
      console.debug('[Events] Handling delta:', delta)
    }

    switch (delta.type) {
      case 'progress': {
        const payload = delta.payload as any
        if (payload) {
          const task = tasks.value.active.find(t => t.gid === delta.gid)
          if (task) {
            let hasUpdate = false
            if (payload.completedLength !== undefined && task.completedLength !== payload.completedLength) {
              task.completedLength = payload.completedLength
              hasUpdate = true
            }
            if (payload.downloadSpeed !== undefined && task.downloadSpeed !== payload.downloadSpeed) {
              task.downloadSpeed = payload.downloadSpeed
              hasUpdate = true
            }
            if (payload.totalLength !== undefined && task.totalLength !== payload.totalLength) {
              task.totalLength = payload.totalLength
              hasUpdate = true
            }
            if (payload.errorCode !== undefined) task.errorCode = payload.errorCode
            if (payload.errorMessage !== undefined) task.errorMessage = payload.errorMessage

            if (hasUpdate) tasks.value = { ...tasks.value }

            // Metadata self-healing
            if (!task.files?.[0]?.path && !metadataPending.has(delta.gid)) {
               metadataPending.add(delta.gid)
               // Access metadataInFlight via getter? No, we need to check shared state.
               // Since `metadataInFlight` is a let variable in actions scope, we need the getter/setter exposed.
               if (!actions.metadataInFlight()) {
                 actions.setMetadataInFlight(true)
                 setTimeout(() => {
                   const batch = Array.from(metadataPending)
                   metadataPending.clear()
                   GetTaskMetadata(batch).then((metadata: Record<string, Task>) => {
                      if (!metadata) return
                      let updated = false
                      for (const gid of Object.keys(metadata)) {
                        const meta = metadata[gid]
                        if (!meta?.files?.[0]?.path) continue
                        cacheMetadata(meta)
                        const t = tasks.value.active.find(x => x.gid === gid) || tasks.value.waiting.find(x => x.gid === gid)
                        if (t) {
                           t.files = meta.files
                           t.dir = meta.dir
                           updated = true
                        }
                      }
                      if (updated) tasks.value = { ...tasks.value }
                   }).finally(() => actions.setMetadataInFlight(false))
                 }, 50)
               }
            }
          }
        }
        break
      }

      case 'add': {
        try {
          // Attempt to fetch metadata for the new task immediately
          const metadata = await GetTaskMetadata([delta.gid])
          const newTask = metadata?.[delta.gid]

          if (newTask && newTask.files && newTask.files.length > 0) {
            // If we have valid task data, add it to the state optimistically
            cacheMetadata(newTask)
            const existsInActive = tasks.value.active.some(t => t.gid === delta.gid)
            const existsInWaiting = tasks.value.waiting.some(t => t.gid === delta.gid)

            if (!existsInActive) {
              const taskToAdd = applyMetadataFromCache(newTask)
              tasks.value = {
                ...tasks.value,
                active: [taskToAdd, ...tasks.value.active],
                waiting: existsInWaiting
                  ? tasks.value.waiting.filter(t => t.gid !== delta.gid)
                  : tasks.value.waiting,
              }
            }
          } else {
            // Task not found or metadata incomplete, fallback to full sync
            // This handles the case where the task might be in a state not yet visible to TellStatus
            await fetchTasks()
          }
        } catch (e) {
             console.error('[Events] Failed to handle add event, falling back to full fetch:', e)
             await fetchTasks()
        }
        break
      }

      case 'complete': {
        moveTaskToStopped(delta.gid)
        break
      }

      case 'pause': {
        patchTaskStatus(delta.gid, 'paused')
        break
      }

      case 'error': {
        patchTaskStatus(delta.gid, 'error')
        moveTaskToStopped(delta.gid)
        break
      }

      case 'remove': {
        removeTaskFromState(delta.gid)
        break
      }
    }

    throttledUpdateTrayIcon()
  }

  function handleTaskMove(move: TaskMove) {
    const { gid, from, to, task: taskData } = move
    const fullTask = taskData as unknown as Task | undefined
    if (fullTask?.files?.length) {
      cacheMetadata(fullTask)
    }

    let movedTask: Task | undefined
    if (from === 'active') {
      movedTask = tasks.value.active.find(t => t.gid === gid)
      tasks.value.active = tasks.value.active.filter(t => t.gid !== gid)
    } else if (from === 'waiting') {
      movedTask = tasks.value.waiting.find(t => t.gid === gid)
      tasks.value.waiting = tasks.value.waiting.filter(t => t.gid !== gid)
    } else if (from === 'stopped') {
      movedTask = tasks.value.stopped.find(t => t.gid === gid)
      tasks.value.stopped = tasks.value.stopped.filter(t => t.gid !== gid)
    }

    let taskToAdd: Task
    if (fullTask?.gid) {
      taskToAdd = applyMetadataFromCache(fullTask)
    } else if (movedTask) {
      taskToAdd = movedTask
    } else {
      taskToAdd = applyMetadataFromCache({ gid } as Task)
    }

    if (to === 'active' && !tasks.value.active.some(t => t.gid === gid)) {
      tasks.value.active = [taskToAdd, ...tasks.value.active]
    } else if (to === 'waiting' && !tasks.value.waiting.some(t => t.gid === gid)) {
      tasks.value.waiting = [taskToAdd, ...tasks.value.waiting]
    } else if (to === 'stopped' && !tasks.value.stopped.some(t => t.gid === gid)) {
      tasks.value.stopped = [taskToAdd, ...tasks.value.stopped]
    }

    if (to === 'stopped') {
      setTimeout(() => {
        if (tasks.value.stopped.some(t => t.gid === gid)) {
          removeMetadata(gid)
        }
      }, 5000)
    }

    tasks.value = { ...tasks.value }
    immediateUpdateTrayIcon()
  }

  return {
    handleTaskDelta,
    handleTaskMove,
    moveTaskToStopped, // Exposed for tests/polling
  }
}

export type TaskEvents = ReturnType<typeof setupEvents>
