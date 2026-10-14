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

export type TaskBucket = 'active' | 'waiting' | 'stopped'

export type TaskMovePlacement = {
  gid: string
  dest: 'active' | 'waiting'
}

// Recognized payload.status owns the destination bucket; delta type and the
// move's `to` field are only hints. Unknown/empty status returns null so the
// caller can recover via a read instead of guessing.
function bucketForStatusValue(status: unknown): TaskBucket | null {
  if (typeof status !== 'string') return null
  switch (status) {
    case 'active':
      return 'active'
    case 'waiting':
    case 'paused':
      return 'waiting'
    case 'complete':
    case 'error':
      return 'stopped'
    default:
      return null
  }
}

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
    ;(result as unknown as { threads?: string }).threads = (
      task as unknown as { threads?: string }
    ).threads
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
    ;(merged as unknown as { threads?: string }).threads = (
      incoming as unknown as { threads?: string }
    ).threads
  } else if ((existing as unknown as { threads?: string })?.threads !== undefined) {
    ;(merged as unknown as { threads?: string }).threads = (
      existing as unknown as { threads?: string }
    ).threads
  }
  Object.assign(merged, mergeTaskGroupMetadata(existing, incoming))

  return merged
}

export function setupEvents(state: TaskState, actions: TaskActions, _polling: TaskPolling) {
  const { tasks, selectedGids, throttledUpdateTrayIcon, immediateUpdateTrayIcon } = state
  const { fetchTasks } = actions

  function collectCopies(gid: string) {
    return {
      active: tasks.value.active.find(t => t.gid === gid),
      waiting: tasks.value.waiting.find(t => t.gid === gid),
      stopped: tasks.value.stopped.find(t => t.gid === gid),
    }
  }

  function scheduleStoppedMetadataCleanup(gid: string) {
    setTimeout(() => {
      if (tasks.value.stopped.some(t => t.gid === gid)) {
        removeMetadata(gid)
      }
    }, 5000)
  }

  function moveTaskToStopped(gid: string) {
    const { active: activeTask, waiting: waitingTask } = collectCopies(gid)
    const task = activeTask ?? waitingTask
    if (!task) return
    if (task.status !== 'error') task.status = 'complete'
    tasks.value = {
      active: tasks.value.active.filter(t => t.gid !== gid),
      waiting: tasks.value.waiting.filter(t => t.gid !== gid),
      stopped: [task, ...tasks.value.stopped],
    }
    scheduleStoppedMetadataCleanup(gid)
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

  type SurvivorPlan = {
    row: Task
    dest: 'active' | 'waiting'
    prepend: boolean
    patched: boolean
  }

  function prepareResumeSurvivor(gid: string, target: 'active' | 'waiting'): SurvivorPlan | null {
    const { active: activeTask, waiting: waitingTask, stopped: stoppedTask } = collectCopies(gid)

    if (target === 'waiting' && activeTask) {
      // Optimistic waiting never demotes an already-active row; stray
      // duplicate copies still get collapsed by the caller.
      if (waitingTask || stoppedTask) {
        backfillRichFields(activeTask, (waitingTask ?? stoppedTask) as Task)
      }
      if (hasValidFiles(activeTask) || hasTaskGroupMetadata(activeTask)) {
        cacheMetadata(activeTask)
      }
      return { row: activeTask, dest: 'active', prepend: false, patched: false }
    }

    const survivor =
      target === 'active'
        ? (activeTask ?? waitingTask ?? stoppedTask)
        : (waitingTask ?? stoppedTask)
    if (!survivor) return null

    if (activeTask && (waitingTask || stoppedTask)) {
      backfillRichFields(activeTask, (waitingTask ?? stoppedTask) as Task)
    } else if (waitingTask && stoppedTask) {
      backfillRichFields(waitingTask, stoppedTask)
    }

    let patched = false
    if (survivor.status !== target) {
      survivor.status = target
      patched = true
    }
    if (target === 'waiting' && !isNumericZero(survivor.downloadSpeed)) {
      // Waiting/paused rows carry no live speed; checkpoint bytes stay intact.
      survivor.downloadSpeed = '0'
      patched = true
    }
    if (stoppedTask && (survivor.errorCode || survivor.errorMessage)) {
      survivor.errorCode = ''
      survivor.errorMessage = ''
      patched = true
    }

    if (hasValidFiles(survivor) || hasTaskGroupMetadata(survivor)) {
      cacheMetadata(survivor)
    }

    const inDest = target === 'active' ? survivor === activeTask : survivor === waitingTask
    return { row: survivor, dest: target, prepend: !inDest, patched }
  }

  // Batched mover for optimistic waiting placements. One GID lands in
  // exactly one list; moved rows keep their identity and prepend in
  // source-relative order (waiting scan, then stopped).
  function moveTasks(placements: TaskMovePlacement[]): { missing: boolean } {
    const byGid = new Map<string, TaskMovePlacement>()
    for (const p of placements) {
      if (!p?.gid || byGid.has(p.gid)) continue
      byGid.set(p.gid, p)
    }
    if (byGid.size === 0) return { missing: false }

    const resolved = new Map<string, SurvivorPlan>()
    let patchedOnly = false
    for (const list of [tasks.value.waiting, tasks.value.stopped, tasks.value.active]) {
      for (const t of list) {
        const p = t.gid ? byGid.get(t.gid) : undefined
        if (!p || resolved.has(t.gid)) continue
        const plan = prepareResumeSurvivor(t.gid, p.dest)
        if (!plan) continue
        resolved.set(t.gid, plan)
        if (plan.patched && !plan.prepend) patchedOnly = true
      }
    }

    // Unresolved placements are unknown GIDs that arrived without a donor row.
    const missing = [...byGid.keys()].some(gid => !resolved.has(gid))
    if (resolved.size === 0) return { missing }

    const destByGid = new Map<string, 'active' | 'waiting'>()
    for (const [gid, plan] of resolved) destByGid.set(gid, plan.dest)

    const prependRows = (dest: 'active' | 'waiting') => {
      const rows: Task[] = []
      for (const plan of resolved.values()) {
        if (plan.dest === dest && plan.prepend) rows.push(plan.row)
      }
      return rows
    }
    const prependActive = prependRows('active')
    const prependWaiting = prependRows('waiting')

    let membershipChanged = prependActive.length > 0 || prependWaiting.length > 0
    if (!membershipChanged) {
      for (const [gid, dest] of destByGid) {
        const copies = collectCopies(gid)
        if (
          (dest === 'active' && (copies.waiting || copies.stopped)) ||
          (dest === 'waiting' && (copies.active || copies.stopped))
        ) {
          membershipChanged = true
          break
        }
      }
    }

    if (membershipChanged) {
      const keepIn = (list: Task[], dest: TaskBucket) =>
        list.filter(t => {
          const d = t.gid ? destByGid.get(t.gid) : undefined
          return d === undefined || d === dest
        })
      tasks.value = {
        active: [...prependActive, ...keepIn(tasks.value.active, 'active')],
        waiting: [...prependWaiting, ...keepIn(tasks.value.waiting, 'waiting')],
        stopped: keepIn(tasks.value.stopped, 'stopped'),
      }
    } else if (patchedOnly) {
      tasks.value = { ...tasks.value }
    }

    return { missing }
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

  // Fields the payload explicitly carries win over folded/kept values:
  // status and cleared errors anywhere, zero live speed off the active
  // bucket, and canonical totals/errors on terminal rows.
  function applyAuthoritativeIncoming(row: Task, incoming: Partial<Task>, dest: TaskBucket) {
    if (incoming.status !== undefined) row.status = incoming.status
    if (incoming.errorCode !== undefined) row.errorCode = incoming.errorCode
    if (incoming.errorMessage !== undefined) row.errorMessage = incoming.errorMessage
    const threads = (incoming as Record<string, unknown>).threads
    if (threads !== undefined) {
      ;(row as unknown as Record<string, unknown>).threads = threads
    }
    if (incoming.downloadSpeed !== undefined) {
      row.downloadSpeed =
        dest === 'active'
          ? preserveNonZeroValue(row.downloadSpeed, incoming.downloadSpeed)
          : incoming.downloadSpeed
    }
    if (dest === 'stopped') {
      if (!isEmptyLength(incoming.totalLength)) row.totalLength = String(incoming.totalLength)
      if (!isEmptyLength(incoming.completedLength)) {
        row.completedLength = String(incoming.completedLength)
      }
    } else {
      // Wire-carried progress wins over folded values; sparse/empty fields
      // still fall back to the existing row so Lite payloads never wipe data.
      row.totalLength = preserveNonZeroValue(row.totalLength, incoming.totalLength)
      row.completedLength = preserveNonZeroValue(row.completedLength, incoming.completedLength)
    }
  }

  // Fold all copies + incoming into the destination bucket. The destination
  // row keeps its identity; duplicate copies are purged without touching
  // unaffected lists. Returns false when there is no local row and no
  // meaningful payload — the caller then recovers via a read instead
  // of creating an empty placeholder.
  function foldIntoBucket(
    gid: string,
    dest: TaskBucket,
    incoming: Partial<Task> | undefined,
  ): boolean {
    const copies = collectCopies(gid)
    const copyList = [copies.active, copies.waiting, copies.stopped].filter(
      (t): t is Task => Boolean(t),
    )
    const destRow = copies[dest]
    const richIncoming = Boolean(
      incoming && (hasValidFiles(incoming) || hasTaskGroupMetadata(incoming)),
    )
    if (!destRow && copyList.length === 0 && !richIncoming) return false

    let acc: Task | undefined
    for (const copy of copyList) {
      if (copy === destRow) continue
      acc = acc ? mergeTaskPreservingRichData(acc, copy) : toTask(copy)
    }
    const merged = applyMetadataFromCache(
      mergeTaskPreservingRichData(acc ?? destRow, incoming ?? { gid }),
    )

    // A row landing on a non-terminal bucket sheds stale error state unless
    // the payload itself carries error fields.
    const clearStaleTerminalErrors = (row: Task) => {
      if (dest === 'stopped') return
      if (incoming?.errorCode === undefined && row.errorCode) row.errorCode = ''
      if (incoming?.errorMessage === undefined && row.errorMessage) row.errorMessage = ''
    }

    const purge = (list: Task[]) => list.filter(t => t.gid !== gid)
    // A duplicate twin that changes neither fields nor membership is a no-op:
    // it must not rebuild list identities.
    const membershipChanged = !destRow || copyList.length > 1
    let rowChanged = false
    let active = tasks.value.active
    let waiting = tasks.value.waiting
    let stopped = tasks.value.stopped
    if (destRow) {
      const before = { ...destRow }
      Object.assign(destRow, mergeTaskPreservingRichData(merged, destRow))
      clearStaleTerminalErrors(destRow)
      if (incoming) applyAuthoritativeIncoming(destRow, incoming, dest)
      rowChanged =
        before.status !== destRow.status ||
        before.title !== destRow.title ||
        before.totalLength !== destRow.totalLength ||
        before.completedLength !== destRow.completedLength ||
        before.downloadSpeed !== destRow.downloadSpeed ||
        before.errorCode !== destRow.errorCode ||
        before.errorMessage !== destRow.errorMessage ||
        before.dir !== destRow.dir ||
        before.files !== destRow.files ||
        (before as Record<string, unknown>).threads !==
          (destRow as unknown as Record<string, unknown>).threads ||
        !isTaskGroupEqual(before.download_group, destRow.download_group)
      if (membershipChanged) {
        if (dest !== 'active') active = purge(active)
        if (dest !== 'waiting') waiting = purge(waiting)
        if (dest !== 'stopped') stopped = purge(stopped)
      }
    } else {
      clearStaleTerminalErrors(merged)
      if (incoming) applyAuthoritativeIncoming(merged, incoming, dest)
      active = purge(active)
      waiting = purge(waiting)
      stopped = purge(stopped)
      if (dest === 'active') active = [merged, ...active]
      else if (dest === 'waiting') waiting = [merged, ...waiting]
      else stopped = [merged, ...stopped]
    }
    if (membershipChanged) {
      tasks.value = { active, waiting, stopped }
    } else if (rowChanged) {
      tasks.value = { ...tasks.value }
    }

    if (dest === 'stopped') {
      scheduleStoppedMetadataCleanup(gid)
    } else {
      const row = destRow ?? merged
      if (!hasValidFiles(row)) actions.queueMetadataRecovery(gid)
    }
    return true
  }

  // Status-less terminal notification: patch the fields that are present and
  // let a read settle status/membership. Never invent a destination
  // or a status field from the delta type alone.
  function patchStatuslessTerminal(gid: string, payload: TaskDelta['payload']) {
    const copies = collectCopies(gid)
    const row = copies.active ?? copies.waiting ?? copies.stopped
    let updated = false
    if (row && payload) {
      const assignField = (
        key: 'completedLength' | 'totalLength' | 'downloadSpeed' | 'errorCode' | 'errorMessage',
      ) => {
        const value = payload[key]
        if (value !== undefined && row[key] !== value) {
          ;(row as unknown as Record<string, unknown>)[key] = value
          updated = true
        }
      }
      assignField('completedLength')
      assignField('totalLength')
      assignField('downloadSpeed')
      assignField('errorCode')
      assignField('errorMessage')
      const threads = payload.threads
      if (threads !== undefined && (row as unknown as Record<string, unknown>).threads !== threads) {
        ;(row as unknown as Record<string, unknown>).threads = threads
        updated = true
      }
      const beforeGroup = cloneTaskGroupMetadata(row)
      Object.assign(row, mergeTaskGroupMetadata(row, payload))
      if (!isTaskGroupEqual(beforeGroup, cloneTaskGroupMetadata(row))) updated = true
      if (updated) tasks.value = { ...tasks.value }
    }
  }

  async function handleTaskDelta(delta: TaskDelta) {
    if (import.meta.env.DEV) {
      console.debug('[Events] Handling delta:', delta)
    }
    const gid = delta.gid
    const payload = delta.payload
    const incoming = payload ? { ...payload, gid: payload.gid || gid } : undefined
    const dest = bucketForStatusValue(payload?.status)

    switch (delta.type) {
      case 'progress': {
        if (dest && dest !== 'active') {
          if (!foldIntoBucket(gid, dest, incoming)) await fetchTasks()
          break
        }
        const task = tasks.value.active.find(t => t.gid === gid)
        if (task && payload) {
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
          const incomingThreads = payload.threads as string | undefined
          const currentThreads = (task as unknown as Record<string, unknown>).threads
          if (incomingThreads !== undefined && currentThreads !== incomingThreads) {
            ;(task as unknown as Record<string, unknown>).threads = incomingThreads
            hasUpdate = true
          }

          if (hasUpdate) tasks.value = { ...tasks.value }

          if (!hasValidFiles(task)) {
            actions.queueMetadataRecovery(gid)
          }
        } else if (!task && dest === 'active') {
          if (!foldIntoBucket(gid, 'active', incoming)) await fetchTasks()
        }
        break
      }

      case 'add': {
        if (incoming?.download_group && getActivePinia()) {
          const downloadGroupStore = useDownloadGroupStore()
          downloadGroupStore.addPlaceholdersFromDownloadGroups(
            [incoming.download_group],
            'websocket',
          )
        }
        if (incoming && (hasValidFiles(incoming) || hasTaskGroupMetadata(incoming))) {
          cacheMetadata(toTask(incoming))
        }

        if (dest) {
          if (!foldIntoBucket(gid, dest, incoming)) await fetchTasks()
          break
        }
        // Status-less add: merge into an existing non-stopped row, keep stopped
        // duplicate suppression, otherwise recover via a read.
        const copies = collectCopies(gid)
        if (copies.stopped) break
        const existing = copies.active ?? copies.waiting
        if (existing && incoming) {
          Object.assign(
            existing,
            applyMetadataFromCache(mergeTaskPreservingRichData(existing, incoming)),
          )
          tasks.value = { ...tasks.value }
          if (!hasValidFiles(existing)) actions.queueMetadataRecovery(gid)
        } else {
          await fetchTasks()
        }
        break
      }

      case 'resume': {
        if (dest) {
          if (!foldIntoBucket(gid, dest, incoming)) await fetchTasks()
        } else {
          await fetchTasks()
        }
        useDownloadGroupStore().scheduleAutoSyncImmediate('resume-delta')
        break
      }

      case 'pause': {
        if (dest) {
          if (!foldIntoBucket(gid, dest, incoming)) await fetchTasks()
        } else {
          // Status-less pause: a live row is demoted to waiting/paused with
          // zero speed; a stopped-only row is not reopened by it.
          const copies = collectCopies(gid)
          if (copies.active || copies.waiting) {
            foldIntoBucket(gid, 'waiting', { gid, status: 'paused', downloadSpeed: '0' })
          } else if (!copies.stopped) {
            await fetchTasks()
          }
        }
        useDownloadGroupStore().scheduleAutoSyncImmediate('pause-delta')
        break
      }

      case 'complete':
      case 'error': {
        if (dest === 'stopped') {
          if (!foldIntoBucket(gid, 'stopped', incoming)) await fetchTasks()
        } else if (dest) {
          if (!foldIntoBucket(gid, dest, incoming)) await fetchTasks()
        } else {
          patchStatuslessTerminal(gid, payload)
          if (!collectCopies(gid).stopped) await fetchTasks()
        }
        break
      }

      case 'remove': {
        removeTaskFromState(gid)
        break
      }
    }

    throttledUpdateTrayIcon()
  }

  function handleTaskMove(move: TaskMove) {
    const { gid, to, task: taskData } = move
    const payload = taskData as Partial<Task> | undefined
    const incoming = payload ? { ...payload, gid: payload.gid || gid } : undefined
    if (incoming && (hasValidFiles(incoming) || hasTaskGroupMetadata(incoming))) {
      cacheMetadata(toTask(incoming))
    }

    const statusDest = bucketForStatusValue(payload?.status)
    const wireDest = to === 'active' || to === 'waiting' || to === 'stopped' ? to : null
    const dest = statusDest ?? wireDest
    if (!dest) {
      // Malformed/mismatched destinations recover through a read.
      void fetchTasks()
      return
    }

    if (!foldIntoBucket(gid, dest, incoming)) {
      void fetchTasks()
      return
    }
    immediateUpdateTrayIcon()
  }

  return {
    handleTaskDelta,
    handleTaskMove,
    moveTasks,
    moveTaskToStopped, // Exposed for tests/polling
  }
}

export type TaskEvents = ReturnType<typeof setupEvents>
