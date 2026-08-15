import { TaskState } from './state'
import {
  GetTasks,
  GetActiveTasks,
  GetStoppedTasks,
  GetTaskMetadata,
  AddUri,
  BatchAddUri,
  PauseTask,
  ResumeTask,
  RemoveTask,
  OpenFolder,
  BatchPause,
  BatchResume,
  BatchRemove,
  GetFullSnapshot,
  MinimizeToTray,
} from '../../../bindings/goaria-v3/internal/wailsapp/app.js'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import type { BatchAddResult } from '../../../bindings/goaria-v3/internal/tasks/models'
import { cacheMetadata, applyMetadataFromCache } from './metadata'
import { mergeTasks, dedupByGid, applyLocalOrder } from './utils'
import { cloneTaskGroupMetadata, mergeTaskGroupMetadata } from './grouping'
import { useDownloadGroupStore } from '../downloadGroups'

export function setupActions(state: TaskState) {
  // Reuse sets to avoid GC - scoped to setupActions for better encapsulation
  const _activeGidSet = new Set<string>()
  const _waitingGidSet = new Set<string>()
  const _stoppedGidSet = new Set<string>()
  // GIDs suppressed by _stoppedGidSet on the previous fetchActiveTasks call.
  // Second consecutive backend sighting admits the GID (one-shot stale-snapshot defense).
  // Polling mode only; event-driven admits on first sighting.
  const _prevStoppedSuppressedGids = new Set<string>()
  const _currStoppedSuppressedGids = new Set<string>()
  const _admitFromStopped = new Set<string>()
  const metadataPending = new Set<string>()
  let metadataInFlight = false

  const {
    tasks,
    selectedGids,
    selectedGroupKeys,
    consecutiveErrors,
    pollingContextEnabled,
    isWindowVisible,
    throttledUpdateTrayIcon,
    immediateUpdateTrayIcon,
    pollingEnabled,
    syncMode,
  } = state

  const MAX_CONSECUTIVE_ERRORS = 3
  let lastStoppedFetchTime = 0
  let lastStoppedTasksRef: Task[] | null = null

  // Callback to restart polling if needed (will be injected by index.ts or polling.ts)
  let _restartPollingCallback: (() => void) | null = null
  let _stopPollingCallback: ((disableContext: boolean) => void) | null = null
  let _moveTasksToActive: ((gids: string[]) => { missing: boolean }) | null = null
  // Per-GID resume op generation: terminal events during IPC stamp superseded as -gen
  // so optimistic move does not overwrite a completed/error transition.
  let _resumeOpGen = 0
  const _resumePendingGids = new Map<string, number>()

  function setPollingCallbacks(restart: () => void, stop: (disable: boolean) => void) {
    _restartPollingCallback = restart
    _stopPollingCallback = stop
  }

  function setMoveTasksToActive(fn: (gids: string[]) => { missing: boolean }) {
    _moveTasksToActive = fn
  }

  function isResumePending(gid: string): boolean {
    return Boolean(gid) && _resumePendingGids.has(gid)
  }

  function markResumeSuperseded(gid: string) {
    if (!gid) return
    const pending = _resumePendingGids.get(gid)
    if (pending === undefined || pending < 0) return
    _resumePendingGids.set(gid, -pending)
  }

  function beginResumePending(gids: string[]): number {
    const gen = ++_resumeOpGen
    for (const gid of gids) {
      if (gid) _resumePendingGids.set(gid, gen)
    }
    return gen
  }

  function confirmedResumeGids(gids: string[], gen: number): string[] {
    return gids.filter(gid => Boolean(gid) && _resumePendingGids.get(gid) === gen)
  }

  function clearResumePending(gids: string[], gen: number): void {
    for (const gid of gids) {
      if (!gid) continue
      const pending = _resumePendingGids.get(gid)
      if (pending === gen || pending === -gen) _resumePendingGids.delete(gid)
    }
  }

  async function recoverResumeSnapshot(gids: string[], gen: number) {
    try {
      await fetchTasks()
    } finally {
      clearResumePending(gids, gen)
    }
  }

  function batchResumeOkGids(results: unknown, requested: string[]): string[] {
    if (!Array.isArray(results)) return requested
    const ok = new Set<string>()
    for (const item of results) {
      if (!item || typeof item !== 'object') continue
      const row = item as { gid?: string; ok?: boolean }
      if (row.ok === true && typeof row.gid === 'string' && row.gid) ok.add(row.gid)
    }
    return requested.filter(gid => ok.has(gid))
  }

  function clearStoppedSuppression(gid?: string) {
    if (gid) {
      _prevStoppedSuppressedGids.delete(gid)
      _currStoppedSuppressedGids.delete(gid)
      _admitFromStopped.delete(gid)
      return
    }
    _prevStoppedSuppressedGids.clear()
    _currStoppedSuppressedGids.clear()
    _admitFromStopped.clear()
  }

  // One-GID-one-list partition shared by fetchTasks and syncFromSnapshot.
  function partitionTaskLists(
    activeIn: Task[] | null | undefined,
    waitingIn: Task[] | null | undefined,
    stoppedIn: Task[] | null | undefined,
  ): { active: Task[]; waiting: Task[]; stopped: Task[] } {
    const active: Task[] = []
    _activeGidSet.clear()
    for (const t of activeIn || []) {
      const gid = t?.gid
      if (!gid || _activeGidSet.has(gid)) continue
      _activeGidSet.add(gid)
      active.push(t)
    }

    const waiting: Task[] = []
    _waitingGidSet.clear()
    for (const t of waitingIn || []) {
      const gid = t?.gid
      if (!gid || _activeGidSet.has(gid) || _waitingGidSet.has(gid)) continue
      _waitingGidSet.add(gid)
      waiting.push(t)
    }

    const stopped: Task[] = []
    _stoppedGidSet.clear()
    for (const t of stoppedIn || []) {
      const gid = t?.gid
      if (!gid || _activeGidSet.has(gid) || _waitingGidSet.has(gid) || _stoppedGidSet.has(gid))
        continue
      _stoppedGidSet.add(gid)
      stopped.push(t)
    }

    return { active, waiting, stopped }
  }

  function gidSeq(list: Task[]): string {
    return list.map(t => t.gid).join(',')
  }

  async function applyOptimisticResume(gids: string[]): Promise<'fetched' | 'ok' | 'needs-fetch'> {
    if (!_moveTasksToActive) {
      await fetchTasks()
      return 'fetched'
    }
    for (const gid of gids) clearStoppedSuppression(gid)
    const result = _moveTasksToActive(gids)
    return result.missing ? 'needs-fetch' : 'ok'
  }

  // --- Core Fetch Logic ---

  function handleFetchError(err: unknown) {
    console.error('Failed to fetch tasks:', err)
    consecutiveErrors.value++

    if (consecutiveErrors.value >= MAX_CONSECUTIVE_ERRORS) {
      console.warn(
        `Stopped polling after ${MAX_CONSECUTIVE_ERRORS} consecutive errors to prevent log spam.`,
      )
      if (_stopPollingCallback) _stopPollingCallback(true)
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
    return (
      value === undefined || value === null || (typeof value === 'string' && value.trim() === '')
    )
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

  function mergeRecoveredMetadata(existing: Task, meta: Task): Task {
    const merged = { ...existing }

    if (isNonEmptyString(meta.title)) merged.title = meta.title
    if (hasValidFiles(meta)) merged.files = meta.files
    if (isNonEmptyString(meta.dir)) merged.dir = meta.dir
    Object.assign(merged, mergeTaskGroupMetadata(existing, meta))

    merged.totalLength = preserveNonZeroValue(existing.totalLength, meta.totalLength)
    merged.completedLength = preserveNonZeroValue(existing.completedLength, meta.completedLength)
    merged.downloadSpeed = preserveNonZeroValue(existing.downloadSpeed, meta.downloadSpeed)

    if (meta.errorCode !== undefined) merged.errorCode = meta.errorCode
    if (meta.errorMessage !== undefined) merged.errorMessage = meta.errorMessage

    return merged
  }

  function applyRecoveredMetadata(metadata: Record<string, Task | undefined>) {
    let newActive = tasks.value.active
    let newWaiting = tasks.value.waiting
    let activeChanged = false
    let waitingChanged = false

    for (const gid of Object.keys(metadata)) {
      const meta = metadata[gid]
      if (!meta || (!hasValidFiles(meta) && !cloneTaskGroupMetadata(meta))) continue
      cacheMetadata(meta)

      const activeIdx = newActive.findIndex(t => t.gid === gid)
      if (activeIdx !== -1) {
        const existing = newActive[activeIdx]
        if (!existing) continue
        if (!activeChanged) {
          newActive = [...newActive]
          activeChanged = true
        }
        newActive[activeIdx] = mergeRecoveredMetadata(existing, meta)
      }

      const waitingIdx = newWaiting.findIndex(t => t.gid === gid)
      if (waitingIdx !== -1) {
        const existing = newWaiting[waitingIdx]
        if (!existing) continue
        if (!waitingChanged) {
          newWaiting = [...newWaiting]
          waitingChanged = true
        }
        newWaiting[waitingIdx] = mergeRecoveredMetadata(existing, meta)
      }
    }

    if (activeChanged || waitingChanged) {
      tasks.value = {
        ...tasks.value,
        active: newActive,
        waiting: newWaiting,
      }
    }
  }

  function drainMetadataRecovery() {
    if (metadataInFlight || metadataPending.size === 0) return

    metadataInFlight = true
    const batch = Array.from(metadataPending)
    metadataPending.clear()

    void GetTaskMetadata(batch)
      .then((metadata: Record<string, Task | undefined>) => {
        if (!metadata) return
        applyRecoveredMetadata(metadata)
      })
      .catch(err => {
        console.warn('Failed to recover task metadata:', err)
      })
      .finally(() => {
        metadataInFlight = false
        if (metadataPending.size > 0) drainMetadataRecovery()
      })
  }

  function queueMetadataRecovery(gids: string[] | string) {
    const batch = Array.isArray(gids) ? gids : [gids]
    for (const gid of batch) {
      if (gid) metadataPending.add(gid)
    }
    drainMetadataRecovery()
  }

  function queueMissingMetadataFromLists(...lists: Task[][]) {
    const gids: string[] = []
    for (const list of lists) {
      for (const task of list) {
        if (task.gid && !hasValidFiles(task)) gids.push(task.gid)
      }
    }
    if (gids.length > 0) queueMetadataRecovery(gids)
  }

  async function fetchActiveTasks(): Promise<{ hasActiveTasks: boolean; taskCompleted: boolean }> {
    try {
      const res = await GetActiveTasks()
      consecutiveErrors.value = 0

      // Deduplicate stopped tasks (skip rebuild if reference unchanged)
      if (tasks.value.stopped !== lastStoppedTasksRef) {
        _stoppedGidSet.clear()
        for (const t of tasks.value.stopped) {
          _stoppedGidSet.add(t.gid)
        }
        lastStoppedTasksRef = tasks.value.stopped
      }

      _currStoppedSuppressedGids.clear()
      _admitFromStopped.clear()

      const shouldAdmitStoppedGid = (gid: string): boolean => {
        if (!_stoppedGidSet.has(gid)) return true
        // Event-driven: no continuous active poll race — admit on first backend sighting.
        if (syncMode.value === 'event-driven') {
          _admitFromStopped.add(gid)
          return true
        }
        if (_prevStoppedSuppressedGids.has(gid)) {
          _admitFromStopped.add(gid)
          return true
        }
        _currStoppedSuppressedGids.add(gid)
        return false
      }

      const resumeHold = new Map<string, { bucket: 'waiting' | 'stopped'; task: Task }>()
      if (_resumePendingGids.size > 0) {
        for (const t of tasks.value.waiting) {
          if (t.gid && _resumePendingGids.has(t.gid)) {
            resumeHold.set(t.gid, { bucket: 'waiting', task: t })
          }
        }
        for (const t of tasks.value.stopped) {
          if (t.gid && _resumePendingGids.has(t.gid) && !resumeHold.has(t.gid)) {
            resumeHold.set(t.gid, { bucket: 'stopped', task: t })
          }
        }
      }

      const active: Task[] = []
      _activeGidSet.clear()
      for (const t of res.active || []) {
        const gid = t?.gid
        if (!gid || _activeGidSet.has(gid)) continue
        if (resumeHold.has(gid)) continue
        if (!shouldAdmitStoppedGid(gid)) continue
        _activeGidSet.add(gid)
        active.push(t)
      }

      const waiting: Task[] = []
      _waitingGidSet.clear()
      for (const t of res.waiting || []) {
        const gid = t?.gid
        if (!gid || _activeGidSet.has(gid) || _waitingGidSet.has(gid)) continue
        if (resumeHold.has(gid)) continue
        if (!shouldAdmitStoppedGid(gid)) continue
        _waitingGidSet.add(gid)
        waiting.push(t)
      }

      for (const t of [...active, ...waiting]) cacheMetadata(t)

      const oldCount = tasks.value.active.length + tasks.value.waiting.length

      const activeResult = mergeTasks(
        tasks.value.active,
        applyLocalOrder(tasks.value.active, active),
      )
      const waitingResult = mergeTasks(
        tasks.value.waiting,
        applyLocalOrder(tasks.value.waiting, waiting),
      )
      const stoppedChanged = _admitFromStopped.size > 0
      const admittedStopped = stoppedChanged
        ? tasks.value.stopped.filter(t => !_admitFromStopped.has(t.gid))
        : tasks.value.stopped

      let nextActive = activeResult.merged
      let nextWaiting = waitingResult.merged
      let nextStopped = admittedStopped

      if (resumeHold.size > 0) {
        const heldGids = new Set(resumeHold.keys())
        nextActive = nextActive.filter(t => !heldGids.has(t.gid))

        const mergedWaiting = new Map(nextWaiting.map(t => [t.gid, t]))
        const restoredWaiting: Task[] = []
        const localWaitingGids = new Set<string>()
        for (const t of tasks.value.waiting) {
          if (t.gid) localWaitingGids.add(t.gid)
        }
        for (const t of nextWaiting) {
          if (!t.gid || localWaitingGids.has(t.gid) || heldGids.has(t.gid)) continue
          restoredWaiting.push(t)
        }
        for (const t of tasks.value.waiting) {
          const hold = resumeHold.get(t.gid)
          if (hold?.bucket === 'waiting') {
            restoredWaiting.push(hold.task)
            continue
          }
          const merged = t.gid ? mergedWaiting.get(t.gid) : undefined
          if (merged && !heldGids.has(t.gid)) {
            restoredWaiting.push(merged)
          }
        }
        nextWaiting = restoredWaiting

        const mergedStopped = new Map(nextStopped.map(t => [t.gid, t]))
        const restoredStopped: Task[] = []
        const seenStopped = new Set<string>()
        for (const t of tasks.value.stopped) {
          const hold = resumeHold.get(t.gid)
          if (hold?.bucket === 'stopped') {
            restoredStopped.push(hold.task)
            seenStopped.add(t.gid)
            continue
          }
          const merged = t.gid ? mergedStopped.get(t.gid) : undefined
          if (merged && !heldGids.has(t.gid)) {
            restoredStopped.push(merged)
            seenStopped.add(t.gid)
          }
        }
        for (const t of nextStopped) {
          if (!t.gid || seenStopped.has(t.gid) || heldGids.has(t.gid)) continue
          restoredStopped.push(t)
          seenStopped.add(t.gid)
        }
        nextStopped = restoredStopped
      }

      const newCount = nextActive.length + nextWaiting.length
      const taskCompleted = newCount < oldCount

      const membershipUnchanged =
        gidSeq(nextActive) === gidSeq(tasks.value.active) &&
        gidSeq(nextWaiting) === gidSeq(tasks.value.waiting) &&
        gidSeq(nextStopped) === gidSeq(tasks.value.stopped)
      const freezeOnly =
        resumeHold.size > 0 &&
        membershipUnchanged &&
        nextWaiting.every((t, i) => t === tasks.value.waiting[i]) &&
        nextStopped.every((t, i) => t === tasks.value.stopped[i]) &&
        nextActive.every((t, i) => t === tasks.value.active[i])

      if (
        !freezeOnly &&
        (activeResult.changed ||
          waitingResult.changed ||
          stoppedChanged ||
          nextActive !== activeResult.merged ||
          nextWaiting !== waitingResult.merged ||
          nextStopped !== admittedStopped)
      ) {
        tasks.value = {
          active: nextActive,
          waiting: nextWaiting,
          stopped: nextStopped,
        }
      }

      _prevStoppedSuppressedGids.clear()
      for (const gid of _currStoppedSuppressedGids) {
        _prevStoppedSuppressedGids.add(gid)
      }
      _currStoppedSuppressedGids.clear()

      queueMissingMetadataFromLists(nextActive, nextWaiting)

      throttledUpdateTrayIcon()
      return { hasActiveTasks: nextActive.length > 0 || nextWaiting.length > 0, taskCompleted }
    } catch (err) {
      handleFetchError(err)
      return { hasActiveTasks: false, taskCompleted: false }
    }
  }

  async function fetchStoppedTasks() {
    try {
      const res = await GetStoppedTasks()
      const stopped = dedupByGid(res || [])

      _activeGidSet.clear()
      _waitingGidSet.clear()
      for (const t of tasks.value.active) _activeGidSet.add(t.gid)
      for (const t of tasks.value.waiting) _waitingGidSet.add(t.gid)

      const heldStopped = new Map<string, Task>()
      if (_resumePendingGids.size > 0) {
        for (const t of tasks.value.stopped) {
          if (
            t.gid &&
            _resumePendingGids.has(t.gid) &&
            !_activeGidSet.has(t.gid) &&
            !_waitingGidSet.has(t.gid)
          ) {
            heldStopped.set(t.gid, t)
          }
        }
      }

      const filteredStopped = stopped.filter(
        t => !_activeGidSet.has(t.gid) && !_waitingGidSet.has(t.gid),
      )

      const stoppedResult = mergeTasks(tasks.value.stopped, filteredStopped)
      let nextStopped = stoppedResult.merged

      if (heldStopped.size > 0) {
        const mergedByGid = new Map(nextStopped.map(t => [t.gid, t]))
        const restored: Task[] = []
        const seen = new Set<string>()
        for (const t of tasks.value.stopped) {
          const held = t.gid ? heldStopped.get(t.gid) : undefined
          if (held) {
            restored.push(held)
            seen.add(t.gid)
            continue
          }
          const merged = t.gid ? mergedByGid.get(t.gid) : undefined
          if (merged && !heldStopped.has(t.gid)) {
            restored.push(merged)
            seen.add(t.gid)
          }
        }
        for (const t of nextStopped) {
          if (!t.gid || seen.has(t.gid) || heldStopped.has(t.gid)) continue
          if (_activeGidSet.has(t.gid) || _waitingGidSet.has(t.gid)) continue
          restored.push(t)
          seen.add(t.gid)
        }
        nextStopped = restored
      }

      const unchanged =
        nextStopped.length === tasks.value.stopped.length &&
        nextStopped.every((t, i) => t === tasks.value.stopped[i])
      if (!unchanged) {
        tasks.value = { ...tasks.value, stopped: nextStopped }
      }
      lastStoppedFetchTime = Date.now()
    } catch (err) {
      console.warn('Failed to fetch stopped tasks:', err)
    }
  }

  async function fetchTasks(options?: { prependUnknownFrom?: Set<string> }) {
    try {
      const res = await GetTasks()
      consecutiveErrors.value = 0
      clearStoppedSuppression()

      const { active, waiting, stopped } = partitionTaskLists(res.active, res.waiting, res.stopped)

      for (const t of [...active, ...waiting, ...stopped]) cacheMetadata(t)

      let nextActive = applyLocalOrder(tasks.value.active, active).map(applyMetadataFromCache)
      let nextWaiting = applyLocalOrder(tasks.value.waiting, waiting).map(applyMetadataFromCache)
      const nextStopped = stopped.map(applyMetadataFromCache)

      // Prepend in THIS assign. await fetchTasks() then a second tasks.value=
      // (prependNewTasks) is two Vue flushes: TaskList capture/plays append,
      // then a bottom→top MOVE that looks like the pre-e2fd1dee enter swap.
      if (options?.prependUnknownFrom) {
        const known = options.prependUnknownFrom
        nextActive = prependUnknownGids(nextActive, known)
        nextWaiting = prependUnknownGids(nextWaiting, known)
      }

      const newTasks = {
        active: nextActive,
        waiting: nextWaiting,
        stopped: nextStopped,
      }

      // Assign tasks FIRST so the async metadata callback operates on current state
      tasks.value = newTasks
      lastStoppedTasksRef = newTasks.stopped

      queueMissingMetadataFromLists(newTasks.active, newTasks.waiting)
      immediateUpdateTrayIcon()
    } catch (err) {
      handleFetchError(err)
    }
  }

  // --- User Actions ---

  function prependUnknownGids(list: Task[], knownGids: Set<string>): Task[] {
    const incoming = list.filter(t => !knownGids.has(t.gid))
    if (incoming.length === 0) return list
    return [...incoming, ...list.filter(t => knownGids.has(t.gid))]
  }

  function snapshotKnownDownloadGids(): Set<string> {
    const knownGids = new Set<string>()
    for (const t of tasks.value.active) knownGids.add(t.gid)
    for (const t of tasks.value.waiting) knownGids.add(t.gid)
    return knownGids
  }

  async function addUri(uri: string) {
    try {
      const knownGids = snapshotKnownDownloadGids()
      const res = await AddUri(uri)
      await fetchTasks({ prependUnknownFrom: knownGids })
      immediateUpdateTrayIcon()

      if (
        pollingContextEnabled.value &&
        isWindowVisible.value &&
        _restartPollingCallback &&
        _stopPollingCallback
      ) {
        _stopPollingCallback(false)
        _restartPollingCallback()
      }
      return res
    } catch (err) {
      console.error('Failed to add URI:', err)
      throw err
    }
  }

  async function batchAddUri(uris: string[]): Promise<BatchAddResult> {
    try {
      const knownGids = snapshotKnownDownloadGids()
      const res = await BatchAddUri(uris)
      const downloadGroupStore = useDownloadGroupStore()
      downloadGroupStore.addPlaceholdersFromDownloadGroups(res.groups, 'batch-add')
      await fetchTasks({ prependUnknownFrom: knownGids })
      immediateUpdateTrayIcon()
      if (
        pollingContextEnabled.value &&
        isWindowVisible.value &&
        _restartPollingCallback &&
        _stopPollingCallback
      ) {
        _stopPollingCallback(false)
        _restartPollingCallback()
      }
      return res
    } catch (err) {
      console.error('Failed to batch add URIs:', err)
      throw err
    }
  }

  async function pause(gid: string) {
    try {
      await PauseTask(gid)
      await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error(`Failed to pause task ${gid}:`, err)
    }
  }

  async function resume(gid: string) {
    const requested = [gid]
    const gen = beginResumePending(requested)
    try {
      await ResumeTask(gid)
      const confirmed = confirmedResumeGids(requested, gen)
      if (confirmed.length === 0) {
        await recoverResumeSnapshot(requested, gen)
        return
      }
      const outcome = await applyOptimisticResume(confirmed)
      if (outcome === 'needs-fetch') {
        await recoverResumeSnapshot(requested, gen)
        return
      }
      clearResumePending(requested, gen)
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error(`Failed to resume task ${gid}:`, err)
      await recoverResumeSnapshot(requested, gen)
    }
  }

  async function remove(gid: string, deleteFile: boolean) {
    tasks.value = {
      active: tasks.value.active.filter(t => t.gid !== gid),
      waiting: tasks.value.waiting.filter(t => t.gid !== gid),
      stopped: tasks.value.stopped.filter(t => t.gid !== gid),
    }

    if (selectedGids.value.has(gid)) {
      selectedGids.value.delete(gid)
      selectedGids.value = new Set(selectedGids.value)
    }

    immediateUpdateTrayIcon()

    try {
      await RemoveTask(gid, deleteFile)
    } catch (err) {
      console.error(`Failed to remove task ${gid}:`, err)
      await fetchTasks()
    }
  }

  async function openTaskFolder(task: Task) {
    try {
      await OpenFolder(task)
    } catch (err) {
      console.error('Failed to open folder:', err)
    }
  }

  // --- Selection & Batch Actions ---

  function toggleSelect(gid: string) {
    if (selectedGids.value.has(gid)) {
      selectedGids.value.delete(gid)
    } else {
      selectedGids.value.add(gid)
    }
    selectedGids.value = new Set(selectedGids.value)
  }

  function toggleSelectGroup(groupKey: string) {
    const normalizedKey = groupKey.trim()
    if (!normalizedKey) return
    if (selectedGroupKeys.value.has(normalizedKey)) {
      selectedGroupKeys.value.delete(normalizedKey)
    } else {
      selectedGroupKeys.value.add(normalizedKey)
    }
    selectedGroupKeys.value = new Set(selectedGroupKeys.value)
  }

  function clearSelectedGroup(groupKey: string) {
    const normalizedKey = groupKey.trim()
    if (!normalizedKey || !selectedGroupKeys.value.has(normalizedKey)) return
    selectedGroupKeys.value.delete(normalizedKey)
    selectedGroupKeys.value = new Set(selectedGroupKeys.value)
  }

  function selectAll(gids: string[], groupKeys: string[] = []) {
    selectedGids.value = new Set(gids.map(gid => gid.trim()).filter(Boolean))
    selectedGroupKeys.value = new Set(groupKeys.map(key => key.trim()).filter(Boolean))
  }

  function clearSelection() {
    selectedGids.value = new Set()
    selectedGroupKeys.value = new Set()
  }

  async function batchPause(gids: string[]) {
    try {
      await BatchPause(gids)
      await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error('Batch pause failed:', err)
    }
  }

  async function batchResume(gids: string[]) {
    const gen = beginResumePending(gids)
    try {
      const results = await BatchResume(gids)
      const engineOk = batchResumeOkGids(results, gids)
      const confirmed = confirmedResumeGids(engineOk, gen)
      if (confirmed.length === 0) {
        await recoverResumeSnapshot(gids, gen)
        return
      }
      const outcome = await applyOptimisticResume(confirmed)
      if (outcome === 'needs-fetch') {
        await recoverResumeSnapshot(gids, gen)
        return
      }
      clearResumePending(gids, gen)
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error('Batch resume failed:', err)
      await recoverResumeSnapshot(gids, gen)
    }
  }

  // Optimistic removal for group delete: tasks are removed from UI state
  // before the backend RPC completes. On failure, fetchTasks() re-syncs
  // from Cache. For sg_ tasks, RemoveDownloadGroup delegates to BatchRemove
  // -> cleanupRemovedTask which calls Cache.RemoveTask, so fetchTasks won't
  // flash-back removed sg_ tasks. For ar_ tasks, tombstone + tick filter
  // handles delayed removal. This optimistic + fetch-recovery pattern is
  // preserved per AGENTS.md high-sensitivity rule.
  async function batchRemove(gids: string[], deleteFiles: boolean) {
    const gidSet = new Set(gids)
    tasks.value = {
      active: tasks.value.active.filter(t => !gidSet.has(t.gid)),
      waiting: tasks.value.waiting.filter(t => !gidSet.has(t.gid)),
      stopped: tasks.value.stopped.filter(t => !gidSet.has(t.gid)),
    }
    selectedGids.value = new Set()
    immediateUpdateTrayIcon()

    try {
      await BatchRemove(gids, deleteFiles)
    } catch (err) {
      console.error('Batch remove failed:', err)
      await fetchTasks()
    }
  }

  // --- System Actions ---

  async function syncFromSnapshot() {
    try {
      const snapshot = await GetFullSnapshot()
      clearStoppedSuppression()

      const { active, waiting, stopped } = partitionTaskLists(
        snapshot.tasks.active,
        snapshot.tasks.waiting,
        snapshot.tasks.stopped,
      )

      for (const t of [...active, ...waiting, ...stopped]) cacheMetadata(t)

      tasks.value = {
        active: applyLocalOrder(tasks.value.active, active).map(applyMetadataFromCache),
        waiting: applyLocalOrder(tasks.value.waiting, waiting).map(applyMetadataFromCache),
        stopped: stopped.map(applyMetadataFromCache),
      }
      lastStoppedTasksRef = tasks.value.stopped

      if (!pollingEnabled.value && _restartPollingCallback) {
        _restartPollingCallback() // This is actually startPolling
      }
    } catch (err) {
      console.error('[Snapshot] Sync failed:', err)
      await fetchTasks()
    }
  }

  async function minimizeToTray() {
    if (_stopPollingCallback) _stopPollingCallback(true)
    await MinimizeToTray()
  }

  return {
    fetchActiveTasks,
    fetchStoppedTasks,
    fetchTasks,
    addUri,
    batchAddUri,
    pause,
    resume,
    remove,
    openTaskFolder,
    toggleSelect,
    toggleSelectGroup,
    clearSelectedGroup,
    selectAll,
    clearSelection,
    batchPause,
    batchResume,
    batchRemove,
    syncFromSnapshot,
    minimizeToTray,
    setPollingCallbacks,
    setMoveTasksToActive,
    isResumePending,
    markResumeSuperseded,
    clearStoppedSuppression,
    metadataPending, // Shared with events
    metadataInFlight: () => metadataInFlight, // Getter
    setMetadataInFlight: (val: boolean) => (metadataInFlight = val),
    queueMetadataRecovery,
    getLastStoppedFetchTime: () => lastStoppedFetchTime,
  }
}

export type TaskActions = ReturnType<typeof setupActions>
