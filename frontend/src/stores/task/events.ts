import { TaskState } from './state'
import { TaskActions } from './actions'
import { TaskPolling } from './polling'
import { GetTaskMetadata } from '../../../bindings/goaria-v3/app.js'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import { cacheMetadata, applyMetadataFromCache, removeMetadata } from './metadata'
import { TaskMove, TaskDelta } from '../events'

export function setupEvents(state: TaskState, actions: TaskActions, _polling: TaskPolling) {
  const { tasks, selectedGids, throttledUpdateTrayIcon, immediateUpdateTrayIcon } = state
  const { fetchTasks } = actions
  const { metadataPending } = actions // Shared state from actions

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

    // Delayed metadata cleanup — stopped tasks no longer need cached metadata
    setTimeout(() => {
      if (tasks.value.stopped.some(t => t.gid === gid)) {
        removeMetadata(gid)
      }
    }, 5000)
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

  async function handleTaskDelta(delta: TaskDelta) {
    if (import.meta.env.DEV) {
      console.debug('[Events] Handling delta:', delta)
    }

    switch (delta.type) {
      case 'progress': {
        const payload = delta.payload as Partial<Task> | undefined
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
                   GetTaskMetadata(batch).then((metadata: Record<string, Task | undefined>) => {
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
        const MAX_RETRIES = 3
        const RETRY_DELAYS = [500, 1000, 2000]

        for (let attempt = 0; attempt <= MAX_RETRIES; attempt++) {
          try {
            const metadata = await GetTaskMetadata([delta.gid])
            const newTask = metadata?.[delta.gid]

            if (newTask && newTask.files?.length > 0 && newTask.files[0]?.path) {
              cacheMetadata(newTask)
              const existsInActive = tasks.value.active.some(t => t.gid === delta.gid)
              const existsInStopped = tasks.value.stopped.some(t => t.gid === delta.gid)
              if (!existsInActive && !existsInStopped) {
                const taskToAdd = applyMetadataFromCache(newTask)
                tasks.value = {
                  ...tasks.value,
                  active: [taskToAdd, ...tasks.value.active],
                  waiting: tasks.value.waiting.filter(t => t.gid !== delta.gid),
                }
              }
              break
            }

            if (attempt < MAX_RETRIES) {
              if (import.meta.env.DEV) {
                console.debug(`[Events] add: metadata incomplete for ${delta.gid}, retry ${attempt + 1}/${MAX_RETRIES}`)
              }
              await new Promise(r => setTimeout(r, RETRY_DELAYS[attempt]))
            } else {
              await fetchTasks()
            }
          } catch (e) {
            if (attempt >= MAX_RETRIES) {
              console.error('[Events] Failed to handle add event after retries, falling back:', e)
              await fetchTasks()
            } else {
              await new Promise(r => setTimeout(r, RETRY_DELAYS[attempt]))
            }
          }
        }
        break
      }

      case 'complete': {
        const payload = delta.payload as Partial<Task> | undefined

        // 对于已经进入 stopped 的任务，应用随后到来的带精确 payload 的 Pusher 推送
        const stoppedTask = tasks.value.stopped.find(t => t.gid === delta.gid)
        if (stoppedTask && payload) {
          let updated = false
          if (payload.completedLength !== undefined && stoppedTask.completedLength !== payload.completedLength) {
            stoppedTask.completedLength = payload.completedLength
            updated = true
          }
          if (payload.totalLength !== undefined && stoppedTask.totalLength !== payload.totalLength) {
            stoppedTask.totalLength = payload.totalLength
            updated = true
          }
          if (payload.downloadSpeed !== undefined && stoppedTask.downloadSpeed !== payload.downloadSpeed) {
            stoppedTask.downloadSpeed = payload.downloadSpeed
            updated = true
          }
          if (updated) tasks.value = { ...tasks.value }
          break
        }

        // 尝试在 active/waiting 中找到任务
        const existingTask = tasks.value.active.find(t => t.gid === delta.gid) ??
                             tasks.value.waiting.find(t => t.gid === delta.gid)

        if (existingTask) {
          // 正常路径：任务已在列表中，应用 payload 后移到 stopped
          if (payload) {
            if (payload.completedLength !== undefined) existingTask.completedLength = payload.completedLength
            if (payload.totalLength !== undefined) existingTask.totalLength = payload.totalLength
            if (payload.downloadSpeed !== undefined) existingTask.downloadSpeed = payload.downloadSpeed
          }
          moveTaskToStopped(delta.gid)
        } else if (!tasks.value.stopped.some(t => t.gid === delta.gid)) {
          // 竞态修复：小文件 complete 事件在 add 完成之前抵达
          // 任务不在任何列表中 → 直接获取数据并放入 stopped
          try {
            const metadata = await GetTaskMetadata([delta.gid])
            const taskData = metadata?.[delta.gid]
            if (taskData) {
              cacheMetadata(taskData)
              const taskToAdd = applyMetadataFromCache(taskData)
              // 用 payload 中的精确数据覆盖可能的 0B
              if (payload) {
                if (payload.completedLength !== undefined) taskToAdd.completedLength = payload.completedLength
                if (payload.totalLength !== undefined) taskToAdd.totalLength = payload.totalLength
                if (payload.downloadSpeed !== undefined) taskToAdd.downloadSpeed = payload.downloadSpeed
              }
              taskToAdd.status = 'complete'
              // 确保不重复添加（add handler 可能在此期间完成了）
              const nowInActive = tasks.value.active.some(t => t.gid === delta.gid)
              const nowInStopped = tasks.value.stopped.some(t => t.gid === delta.gid)
              if (nowInActive) {
                // add 已经完成，走正常移动路径
                moveTaskToStopped(delta.gid)
              } else if (!nowInStopped) {
                tasks.value = {
                  active: tasks.value.active.filter(t => t.gid !== delta.gid),
                  waiting: tasks.value.waiting.filter(t => t.gid !== delta.gid),
                  stopped: [taskToAdd, ...tasks.value.stopped],
                }
              }
            }
          } catch (e) {
            console.warn('[Events] Failed to fetch metadata for completing task:', delta.gid, e)
          }
        }
        // 如果已经在 stopped 中，忽略重复的 complete 事件
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
