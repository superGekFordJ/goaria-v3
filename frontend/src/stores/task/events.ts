import { getActivePinia } from 'pinia'
import { useDownloadGroupStore } from '../downloadGroups'
import { TaskState } from './state'
import { TaskActions } from './actions'
import { TaskPolling } from './polling'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import { cacheMetadata, applyMetadataFromCache, removeMetadata } from './metadata'
import { TaskMove, TaskDelta } from '../events'
import {
  cloneTaskGroupMetadata,
  hasTaskGroupMetadata,
  isTaskGroupEqual,
  mergeTaskGroupMetadata,
} from './grouping'

function hasValidFiles(task?: Partial<Task>): boolean {
  const path = task?.files?.[0]?.path
  return typeof path === 'string' && path.trim().length > 0
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function isNonZeroLength(value: unknown): boolean {
  if (typeof value === 'number') return Number.isFinite(value) && value !== 0
  if (typeof value !== 'string') return false
  const trimmed = value.trim()
  if (!trimmed) return false
  if (/^0+(?:\.0+)?$/.test(trimmed)) return false
  const numeric = Number(trimmed)
  return !Number.isNaN(numeric)
}

function isEmptyLength(value: unknown): boolean {
  return value === undefined || value === null || (typeof value === 'string' && value.trim() === '')
}

function isNumericZero(value: unknown): boolean {
  if (typeof value === 'number') return Number.isFinite(value) && value === 0
  if (typeof value !== 'string') return false
  const trimmed = value.trim()
  if (!trimmed) return false
  if (/^0+(?:\.0+)?$/.test(trimmed)) return true
  const numeric = Number(trimmed)
  return Number.isFinite(numeric) && numeric === 0
}

function preserveNonZeroValue(
  existing: string | undefined,
  incoming: unknown,
  fallback = '0',
): string {
  const existingValue = existing ?? fallback
  if (isEmptyLength(incoming)) return existingValue
  if (isNumericZero(incoming) && isNonZeroLength(existingValue)) return existingValue
  return String(incoming)
}

function toTask(task: Partial<Task> | undefined): Task {
  const result: Task = {
    gid: task?.gid ?? '',
    status: task?.status ?? '',
    totalLength: task?.totalLength ?? '0',
    completedLength: task?.completedLength ?? '0',
    downloadSpeed: task?.downloadSpeed ?? '0',
    errorCode: task?.errorCode ?? '',
    errorMessage: task?.errorMessage ?? '',
    dir: task?.dir ?? '',
    files: task?.files ?? [],
  }
  if (task?.title !== undefined) result.title = task.title
  if ((task as unknown as { threads?: string })?.threads !== undefined) {
    ;(result as unknown as { threads?: string }).threads = (task as unknown as { threads?: string }).threads
  }
  const group = cloneTaskGroupMetadata(task)
  if (group) result.download_group = group
  return result
}

function mergeTaskPreservingRichData(
  existing: Task | undefined,
  incoming: Partial<Task> | undefined,
): Task {
  const merged = toTask(existing ?? incoming)

  if (!incoming) return merged

  if (incoming.gid !== undefined) merged.gid = incoming.gid
  if (incoming.status !== undefined) merged.status = incoming.status
  if (isNonEmptyString(incoming.title)) merged.title = incoming.title

  if (hasValidFiles(incoming)) {
    merged.files = incoming.files ?? []
  } else if (existing?.files?.length) {
    merged.files = existing.files
  }

  if (isNonEmptyString(incoming.dir)) {
    merged.dir = incoming.dir
  } else if (existing?.dir) {
    merged.dir = existing.dir
  }

  merged.totalLength = preserveNonZeroValue(existing?.totalLength, incoming.totalLength)
  merged.completedLength = preserveNonZeroValue(existing?.completedLength, incoming.completedLength)
  merged.downloadSpeed = preserveNonZeroValue(existing?.downloadSpeed, incoming.downloadSpeed)

  if (incoming.errorCode !== undefined) merged.errorCode = incoming.errorCode
  if (incoming.errorMessage !== undefined) merged.errorMessage = incoming.errorMessage
  if ((incoming as unknown as { threads?: string })?.threads !== undefined) {
    ;(merged as unknown as { threads?: string }).threads = (incoming as unknown as { threads?: string }).threads
  } else if ((existing as unknown as { threads?: string })?.threads !== undefined) {
    ;(merged as unknown as { threads?: string }).threads = (existing as unknown as { threads?: string }).threads
  }
  Object.assign(merged, mergeTaskGroupMetadata(existing, incoming))

  return merged
}

export function setupEvents(state: TaskState, actions: TaskActions, _polling: TaskPolling) {
  const { tasks, selectedGids, throttledUpdateTrayIcon, immediateUpdateTrayIcon } = state
  const { fetchTasks } = actions

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

  function backfillRichFields(retained: Task, donor: Task) {
    const backfill: Partial<Task> = {}
    if (!hasValidFiles(retained) && hasValidFiles(donor)) {
      backfill.files = donor.files
    }
    if (!isNonEmptyString(retained.dir) && isNonEmptyString(donor.dir)) {
      backfill.dir = donor.dir
    }
    if (!isNonEmptyString(retained.title) && isNonEmptyString(donor.title)) {
      backfill.title = donor.title
    }
    Object.assign(
      retained,
      mergeTaskPreservingRichData(retained, {
        ...backfill,
        ...mergeTaskGroupMetadata(retained, donor),
      }),
    )
  }

  function prepareResumeSurvivor(gid: string): { survivor: Task; alreadyActive: boolean } | null {
    const activeTask = tasks.value.active.find(t => t.gid === gid)
    const waitingTask = tasks.value.waiting.find(t => t.gid === gid)
    const stoppedTask = tasks.value.stopped.find(t => t.gid === gid)
    const sourceTask = waitingTask ?? stoppedTask
    const task = activeTask ?? sourceTask
    if (!task) return null

    if (activeTask && sourceTask) {
      backfillRichFields(activeTask, sourceTask)
    } else if (waitingTask && stoppedTask) {
      backfillRichFields(waitingTask, stoppedTask)
    }

    const surviving = activeTask ?? task
    surviving.status = 'active'
    if (stoppedTask) {
      surviving.errorCode = ''
      surviving.errorMessage = ''
    }

    if (hasValidFiles(surviving) || hasTaskGroupMetadata(surviving)) {
      cacheMetadata(surviving)
    }

    actions.clearStoppedSuppression(gid)
    return { survivor: surviving, alreadyActive: Boolean(activeTask) }
  }

  function moveTasksToActive(gids: string[]): { missing: boolean } {
    const requested: string[] = []
    const requestedSet = new Set<string>()
    for (const gid of gids) {
      if (!gid || requestedSet.has(gid)) continue
      requestedSet.add(gid)
      requested.push(gid)
    }

    const known = new Set<string>()
    for (const list of [tasks.value.active, tasks.value.waiting, tasks.value.stopped]) {
      for (const t of list) {
        if (t.gid) known.add(t.gid)
      }
    }
    const missing = requested.some(gid => !known.has(gid))

    const moverGids = new Set<string>()
    const prependBlock: Task[] = []
    let collapse = false

    const consider = (gid: string) => {
      if (moverGids.has(gid)) return
      const prepared = prepareResumeSurvivor(gid)
      if (!prepared) return
      moverGids.add(gid)
      if (prepared.alreadyActive) {
        collapse = true
        return
      }
      prependBlock.push(prepared.survivor)
    }

    for (const t of tasks.value.waiting) {
      if (t.gid && requestedSet.has(t.gid)) consider(t.gid)
    }
    for (const t of tasks.value.stopped) {
      if (t.gid && requestedSet.has(t.gid)) consider(t.gid)
    }

    if (prependBlock.length === 0 && !collapse) {
      return { missing }
    }

    const prependGids = new Set(prependBlock.map(t => t.gid))
    tasks.value = {
      active: [...prependBlock, ...tasks.value.active.filter(t => !prependGids.has(t.gid))],
      waiting: tasks.value.waiting.filter(t => !moverGids.has(t.gid)),
      stopped: tasks.value.stopped.filter(t => !moverGids.has(t.gid)),
    }
    return { missing }
  }

  function moveTaskToActive(gid: string) {
    moveTasksToActive([gid])
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
            if (
              payload.completedLength !== undefined &&
              task.completedLength !== payload.completedLength
            ) {
              task.completedLength = payload.completedLength
              hasUpdate = true
            }
            if (
              payload.downloadSpeed !== undefined &&
              task.downloadSpeed !== payload.downloadSpeed
            ) {
              task.downloadSpeed = payload.downloadSpeed
              hasUpdate = true
            }
            if (payload.totalLength !== undefined && task.totalLength !== payload.totalLength) {
              task.totalLength = payload.totalLength
              hasUpdate = true
            }
            if (payload.errorCode !== undefined) task.errorCode = payload.errorCode
            if (payload.errorMessage !== undefined) task.errorMessage = payload.errorMessage
            const incomingThreads = (payload as Record<string, unknown>).threads as
              string | undefined
            const currentThreads = (task as unknown as Record<string, unknown>).threads
            if (incomingThreads !== undefined && currentThreads !== incomingThreads) {
              ;(task as unknown as Record<string, unknown>).threads = incomingThreads
              hasUpdate = true
            }

            if (hasUpdate) tasks.value = { ...tasks.value }

            if (!hasValidFiles(task)) {
              actions.queueMetadataRecovery(delta.gid)
            }
          }
        }
        break
      }

      case 'add': {
        const payload = delta.payload as Partial<Task> | undefined
        const incoming = payload ? { ...payload, gid: payload.gid || delta.gid } : undefined

        if (incoming?.download_group && getActivePinia()) {
          const downloadGroupStore = useDownloadGroupStore()
          downloadGroupStore.addPlaceholdersFromDownloadGroups(
            [incoming.download_group],
            'websocket',
          )
        }

        if (hasValidFiles(incoming) || hasTaskGroupMetadata(incoming)) {
          cacheMetadata(toTask(incoming))
        }

        const activeIdx = tasks.value.active.findIndex(t => t.gid === delta.gid)
        const waitingIdx = tasks.value.waiting.findIndex(t => t.gid === delta.gid)
        const existsInStopped = tasks.value.stopped.some(t => t.gid === delta.gid)

        if (activeIdx !== -1) {
          const merged = applyMetadataFromCache(
            mergeTaskPreservingRichData(tasks.value.active[activeIdx], incoming),
          )
          const nextActive = [...tasks.value.active]
          nextActive[activeIdx] = merged
          tasks.value = {
            ...tasks.value,
            active: nextActive,
            waiting: tasks.value.waiting.filter(t => t.gid !== delta.gid),
          }
          if (!hasValidFiles(merged)) actions.queueMetadataRecovery(delta.gid)
        } else if (waitingIdx !== -1) {
          const merged = applyMetadataFromCache(
            mergeTaskPreservingRichData(tasks.value.waiting[waitingIdx], incoming),
          )
          const shouldMoveToActive = merged.status === 'active' || incoming?.status === 'active'
          tasks.value = {
            ...tasks.value,
            active: shouldMoveToActive ? [merged, ...tasks.value.active] : tasks.value.active,
            waiting: shouldMoveToActive
              ? tasks.value.waiting.filter(t => t.gid !== delta.gid)
              : tasks.value.waiting.map((t, index) => (index === waitingIdx ? merged : t)),
          }
          if (!hasValidFiles(merged)) actions.queueMetadataRecovery(delta.gid)
        } else if (existsInStopped) {
          // Keep stopped duplicate suppression semantics unchanged.
        } else if (hasValidFiles(incoming)) {
          const taskToAdd = applyMetadataFromCache(mergeTaskPreservingRichData(undefined, incoming))
          tasks.value = {
            ...tasks.value,
            active: [taskToAdd, ...tasks.value.active],
            waiting: tasks.value.waiting.filter(t => t.gid !== delta.gid),
          }
        } else {
          if (import.meta.env.DEV) {
            console.debug(
              `[Events] add: payload incomplete for ${delta.gid}, falling back to fetchTasks`,
            )
          }
          await fetchTasks()
        }
        break
      }

      case 'complete': {
        actions.markResumeSuperseded(delta.gid)
        const payload = delta.payload as Task | undefined

        // 对于已经进入 stopped 的任务，应用随后到来的带精确 payload 的 Pusher 推送
        const stoppedTask = tasks.value.stopped.find(t => t.gid === delta.gid)
        if (stoppedTask && payload) {
          let updated = false
          const beforeGroup = cloneTaskGroupMetadata(stoppedTask)
          const incomingPayload = { ...payload, gid: payload.gid || delta.gid }
          if (
            payload.completedLength !== undefined &&
            stoppedTask.completedLength !== payload.completedLength
          ) {
            stoppedTask.completedLength = payload.completedLength
            updated = true
          }
          if (
            payload.totalLength !== undefined &&
            stoppedTask.totalLength !== payload.totalLength
          ) {
            stoppedTask.totalLength = payload.totalLength
            updated = true
          }
          if (
            payload.downloadSpeed !== undefined &&
            stoppedTask.downloadSpeed !== payload.downloadSpeed
          ) {
            stoppedTask.downloadSpeed = payload.downloadSpeed
            updated = true
          }
          Object.assign(stoppedTask, mergeTaskGroupMetadata(stoppedTask, incomingPayload))
          if (!isTaskGroupEqual(beforeGroup, cloneTaskGroupMetadata(stoppedTask))) updated = true
          if (hasValidFiles(incomingPayload) || hasTaskGroupMetadata(incomingPayload)) {
            cacheMetadata(toTask(incomingPayload))
          }
          if (payload.errorCode !== undefined) stoppedTask.errorCode = payload.errorCode
          if (payload.errorMessage !== undefined) stoppedTask.errorMessage = payload.errorMessage
          if (updated) tasks.value = { ...tasks.value }
          break
        }

        // 尝试在 active/waiting 中找到任务
        const existingTask =
          tasks.value.active.find(t => t.gid === delta.gid) ??
          tasks.value.waiting.find(t => t.gid === delta.gid)

        if (existingTask) {
          // 正常路径：任务已在列表中，应用 payload 后移到 stopped
          if (payload) {
            if (payload.completedLength !== undefined)
              existingTask.completedLength = payload.completedLength
            if (payload.totalLength !== undefined) existingTask.totalLength = payload.totalLength
            if (payload.downloadSpeed !== undefined)
              existingTask.downloadSpeed = payload.downloadSpeed
            Object.assign(existingTask, mergeTaskGroupMetadata(existingTask, payload))
          }
          moveTaskToStopped(delta.gid)
        } else if (!tasks.value.stopped.some(t => t.gid === delta.gid)) {
          // 小文件竞态路径：任务不在任何列表中，直接从 payload 构建 stopped 任务
          if (payload && (payload.files?.[0]?.path || hasTaskGroupMetadata(payload))) {
            cacheMetadata(payload)
            const taskToAdd = applyMetadataFromCache(payload)
            taskToAdd.status = 'complete'
            tasks.value = {
              active: tasks.value.active.filter(t => t.gid !== delta.gid),
              waiting: tasks.value.waiting.filter(t => t.gid !== delta.gid),
              stopped: [taskToAdd, ...tasks.value.stopped],
            }
          } else {
            if (import.meta.env.DEV) {
              console.debug(
                `[Events] complete: payload incomplete for ${delta.gid}, falling back to fetchTasks`,
              )
            }
            await fetchTasks()
          }
        }
        // 如果已经在 stopped 中，忽略重复的 complete 事件
        break
      }

      case 'pause': {
        patchTaskStatus(delta.gid, 'paused')
        useDownloadGroupStore().scheduleAutoSyncImmediate('pause-delta')
        break
      }

      case 'resume': {
        if (!actions.isResumePending(delta.gid)) {
          moveTaskToActive(delta.gid)
        }
        useDownloadGroupStore().scheduleAutoSyncImmediate('resume-delta')
        break
      }

      case 'error': {
        actions.markResumeSuperseded(delta.gid)
        const errorPayload = delta.payload as Task | undefined

        // 尝试在 active/waiting 中找到任务
        const errorTask =
          tasks.value.active.find(t => t.gid === delta.gid) ??
          tasks.value.waiting.find(t => t.gid === delta.gid)

        if (errorTask) {
          // 正常路径：任务在列表中，应用 payload 后移到 stopped
          if (errorPayload) {
            if (errorPayload.completedLength !== undefined)
              errorTask.completedLength = errorPayload.completedLength
            if (errorPayload.totalLength !== undefined)
              errorTask.totalLength = errorPayload.totalLength
            if (errorPayload.errorCode !== undefined) errorTask.errorCode = errorPayload.errorCode
            if (errorPayload.errorMessage !== undefined)
              errorTask.errorMessage = errorPayload.errorMessage
            Object.assign(errorTask, mergeTaskGroupMetadata(errorTask, errorPayload))
          }
          patchTaskStatus(delta.gid, 'error')
          moveTaskToStopped(delta.gid)
        } else if (!tasks.value.stopped.some(t => t.gid === delta.gid)) {
          // 小文件竞态路径：任务不在任何列表中，直接从 payload 构建 stopped 任务
          if (
            errorPayload &&
            (errorPayload.files?.[0]?.path || hasTaskGroupMetadata(errorPayload))
          ) {
            cacheMetadata(errorPayload)
            const taskToAdd = applyMetadataFromCache(errorPayload)
            taskToAdd.status = 'error'
            tasks.value = {
              active: tasks.value.active.filter(t => t.gid !== delta.gid),
              waiting: tasks.value.waiting.filter(t => t.gid !== delta.gid),
              stopped: [taskToAdd, ...tasks.value.stopped],
            }
          } else {
            if (import.meta.env.DEV) {
              console.debug(
                `[Events] error: payload incomplete for ${delta.gid}, falling back to fetchTasks`,
              )
            }
            await fetchTasks()
          }
        } else {
          const stoppedTask = tasks.value.stopped.find(t => t.gid === delta.gid)
          if (stoppedTask && errorPayload) {
            const beforeGroup = cloneTaskGroupMetadata(stoppedTask)
            const incomingPayload = { ...errorPayload, gid: errorPayload.gid || delta.gid }
            Object.assign(stoppedTask, mergeTaskGroupMetadata(stoppedTask, incomingPayload))
            if (errorPayload.errorCode !== undefined) stoppedTask.errorCode = errorPayload.errorCode
            if (errorPayload.errorMessage !== undefined)
              stoppedTask.errorMessage = errorPayload.errorMessage
            if (hasValidFiles(incomingPayload) || hasTaskGroupMetadata(incomingPayload)) {
              cacheMetadata(toTask(incomingPayload))
            }
            if (!isTaskGroupEqual(beforeGroup, cloneTaskGroupMetadata(stoppedTask))) {
              tasks.value = { ...tasks.value }
            }
          }
        }
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
    const payload = taskData as Partial<Task> | undefined
    const incoming = payload ? { ...payload, gid: payload.gid || gid } : undefined
    if (hasValidFiles(incoming) || hasTaskGroupMetadata(incoming)) {
      cacheMetadata(toTask(incoming))
    }

    if (to === 'active' && actions.isResumePending(gid)) {
      return
    }
    if (to === 'stopped') {
      actions.markResumeSuperseded(gid)
    }

    const byName = {
      active: tasks.value.active,
      waiting: tasks.value.waiting,
      stopped: tasks.value.stopped,
    } as const

    const copies: Task[] = []
    for (const name of ['active', 'waiting', 'stopped'] as const) {
      const copy = byName[name].find(t => t.gid === gid)
      if (copy) copies.push(copy)
    }

    // from is a hint for the preferred non-dest base; fall back across lists
    let movedTask: Task | undefined
    if (from === 'active' || from === 'waiting' || from === 'stopped') {
      movedTask = byName[from].find(t => t.gid === gid)
    }
    if (!movedTask) {
      movedTask = copies[0]
    }

    const destRow =
      (to === 'active' && tasks.value.active.find(t => t.gid === gid)) ||
      (to === 'waiting' && tasks.value.waiting.find(t => t.gid === gid)) ||
      (to === 'stopped' && tasks.value.stopped.find(t => t.gid === gid)) ||
      undefined

    // Fold every non-destination copy + incoming, then prefer dest fields last
    let acc: Task | undefined
    for (const copy of copies) {
      if (destRow && copy === destRow) continue
      acc = acc ? mergeTaskPreservingRichData(acc, copy) : toTask(copy)
    }
    if (!acc && movedTask && movedTask !== destRow) {
      acc = toTask(movedTask)
    }
    const merged = applyMetadataFromCache(
      mergeTaskPreservingRichData(acc ?? destRow ?? movedTask, incoming ?? { gid }),
    )

    const purge = (list: Task[]) => list.filter(t => t.gid !== gid)
    let active: Task[]
    let waiting: Task[]
    let stopped: Task[]

    if (destRow) {
      Object.assign(destRow, mergeTaskPreservingRichData(merged, destRow))
      // Dest-last merge keeps rich files/lengths; payload still owns terminal fields.
      if (incoming?.status !== undefined) destRow.status = incoming.status
      if (incoming?.errorCode !== undefined) destRow.errorCode = incoming.errorCode
      if (incoming?.errorMessage !== undefined) destRow.errorMessage = incoming.errorMessage
      active = to === 'active' ? tasks.value.active : purge(tasks.value.active)
      waiting = to === 'waiting' ? tasks.value.waiting : purge(tasks.value.waiting)
      stopped = to === 'stopped' ? tasks.value.stopped : purge(tasks.value.stopped)
    } else {
      active = purge(tasks.value.active)
      waiting = purge(tasks.value.waiting)
      stopped = purge(tasks.value.stopped)
      if (to === 'active') active = [merged, ...active]
      else if (to === 'waiting') waiting = [merged, ...waiting]
      else if (to === 'stopped') stopped = [merged, ...stopped]
    }

    if (to === 'stopped') {
      setTimeout(() => {
        if (tasks.value.stopped.some(t => t.gid === gid)) {
          removeMetadata(gid)
        }
      }, 5000)
    }

    tasks.value = { active, waiting, stopped }
    if (to === 'active' || to === 'waiting') {
      actions.clearStoppedSuppression(gid)
    }
    immediateUpdateTrayIcon()
  }

  return {
    handleTaskDelta,
    handleTaskMove,
    moveTaskToActive,
    moveTasksToActive,
    moveTaskToStopped, // Exposed for tests/polling
  }
}

export type TaskEvents = ReturnType<typeof setupEvents>
