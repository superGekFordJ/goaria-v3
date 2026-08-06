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
} from '../../../bindings/goaria-v3/app.js'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import type { BatchAddResult } from '../../../bindings/goaria-v3/internal/tasks/models'
import { cacheMetadata, applyMetadataFromCache } from './metadata'
import { mergeTasks, dedupByGid } from './utils'
import { cloneTaskGroupMetadata, mergeTaskGroupMetadata } from './grouping'
import { useDownloadGroupStore } from '../downloadGroups'

export function setupActions(state: TaskState) {
  // Reuse sets to avoid GC - scoped to setupActions for better encapsulation
  const _activeGidSet = new Set<string>()
  const _waitingGidSet = new Set<string>()
  const _stoppedGidSet = new Set<string>()
  // GIDs suppressed by _stoppedGidSet on the previous fetchActiveTasks call.
  // Second consecutive backend sighting admits the GID (one-shot stale-snapshot defense).
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
  } = state

  const MAX_CONSECUTIVE_ERRORS = 3
  let lastStoppedFetchTime = 0
  let lastStoppedTasksRef: Task[] | null = null

  // Callback to restart polling if needed (will be injected by index.ts or polling.ts)
  let _restartPollingCallback: (() => void) | null = null
  let _stopPollingCallback: ((disableContext: boolean) => void) | null = null

  function setPollingCallbacks(restart: () => void, stop: (disable: boolean) => void) {
    _restartPollingCallback = restart
    _stopPollingCallback = stop
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
        if (_prevStoppedSuppressedGids.has(gid)) {
          _admitFromStopped.add(gid)
          return true
        }
        _currStoppedSuppressedGids.add(gid)
        return false
      }

      const active: Task[] = []
      _activeGidSet.clear()
      for (const t of res.active || []) {
        const gid = t?.gid
        if (!gid || _activeGidSet.has(gid)) continue
        if (!shouldAdmitStoppedGid(gid)) continue
        _activeGidSet.add(gid)
        active.push(t)
      }

      const waiting: Task[] = []
      _waitingGidSet.clear()
      for (const t of res.waiting || []) {
        const gid = t?.gid
        if (!gid || _activeGidSet.has(gid) || _waitingGidSet.has(gid)) continue
        if (!shouldAdmitStoppedGid(gid)) continue
        _waitingGidSet.add(gid)
        waiting.push(t)
      }

      for (const t of [...active, ...waiting]) cacheMetadata(t)

      const oldCount = tasks.value.active.length + tasks.value.waiting.length
      const newCount = active.length + waiting.length
      const taskCompleted = newCount < oldCount

      const activeResult = mergeTasks(tasks.value.active, active)
      const waitingResult = mergeTasks(tasks.value.waiting, waiting)
      const stoppedChanged = _admitFromStopped.size > 0
      const nextStopped = stoppedChanged
        ? tasks.value.stopped.filter(t => !_admitFromStopped.has(t.gid))
        : tasks.value.stopped

      if (activeResult.changed || waitingResult.changed || stoppedChanged) {
        tasks.value = {
          active: activeResult.merged,
          waiting: waitingResult.merged,
          stopped: nextStopped,
        }
      }

      _prevStoppedSuppressedGids.clear()
      for (const gid of _currStoppedSuppressedGids) {
        _prevStoppedSuppressedGids.add(gid)
      }
      _currStoppedSuppressedGids.clear()

      queueMissingMetadataFromLists(activeResult.merged, waitingResult.merged)

      throttledUpdateTrayIcon()
      return { hasActiveTasks: active.length > 0 || waiting.length > 0, taskCompleted }
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

      const filteredStopped = stopped.filter(
        t => !_activeGidSet.has(t.gid) && !_waitingGidSet.has(t.gid),
      )

      const stoppedResult = mergeTasks(tasks.value.stopped, filteredStopped)
      if (stoppedResult.changed) {
        tasks.value = { ...tasks.value, stopped: stoppedResult.merged }
      }
      lastStoppedFetchTime = Date.now()
    } catch (err) {
      console.warn('Failed to fetch stopped tasks:', err)
    }
  }

  async function fetchTasks() {
    try {
      const res = await GetTasks()
      consecutiveErrors.value = 0

      const active: Task[] = []
      _activeGidSet.clear()
      for (const t of res.active || []) {
        const gid = t?.gid
        if (!gid || _activeGidSet.has(gid)) continue
        _activeGidSet.add(gid)
        active.push(t)
      }

      const waiting: Task[] = []
      _waitingGidSet.clear()
      for (const t of res.waiting || []) {
        const gid = t?.gid
        if (!gid || _activeGidSet.has(gid) || _waitingGidSet.has(gid)) continue
        _waitingGidSet.add(gid)
        waiting.push(t)
      }

      const stopped: Task[] = []
      _stoppedGidSet.clear()
      for (const t of res.stopped || []) {
        const gid = t?.gid
        if (!gid || _activeGidSet.has(gid) || _waitingGidSet.has(gid) || _stoppedGidSet.has(gid))
          continue
        _stoppedGidSet.add(gid)
        stopped.push(t)
      }

      for (const t of [...active, ...waiting, ...stopped]) cacheMetadata(t)

      const activeWithMeta = active.map(applyMetadataFromCache)
      const waitingWithMeta = waiting.map(applyMetadataFromCache)
      const stoppedWithMeta = stopped.map(applyMetadataFromCache)

      const newTasks = {
        active: activeWithMeta,
        waiting: waitingWithMeta,
        stopped: stoppedWithMeta,
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

  // Reorder newly-added tasks to the front so fetchTasks snapshots match the event-path prepend.
  function prependNewTasks(knownGids: Set<string>) {
    const active = tasks.value.active
    const waiting = tasks.value.waiting

    const newActive = active.filter(t => !knownGids.has(t.gid))
    const oldActive = active.filter(t => knownGids.has(t.gid))
    const newWaiting = waiting.filter(t => !knownGids.has(t.gid))
    const oldWaiting = waiting.filter(t => knownGids.has(t.gid))

    if (newActive.length > 0 || newWaiting.length > 0) {
      tasks.value = {
        ...tasks.value,
        active: [...newActive, ...oldActive],
        waiting: [...newWaiting, ...oldWaiting],
      }
    }
  }

  async function addUri(uri: string) {
    try {
      const res = await AddUri(uri)
      const knownGids = new Set<string>()
      for (const t of tasks.value.active) knownGids.add(t.gid)
      for (const t of tasks.value.waiting) knownGids.add(t.gid)
      await fetchTasks()
      prependNewTasks(knownGids)
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
      const res = await BatchAddUri(uris)
      const downloadGroupStore = useDownloadGroupStore()
      downloadGroupStore.addPlaceholdersFromDownloadGroups(res.groups, 'batch-add')
      const knownGids = new Set<string>()
      for (const t of tasks.value.active) knownGids.add(t.gid)
      for (const t of tasks.value.waiting) knownGids.add(t.gid)
      await fetchTasks()
      prependNewTasks(knownGids)
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
    try {
      await ResumeTask(gid)
      await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error(`Failed to resume task ${gid}:`, err)
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
    try {
      await BatchResume(gids)
      await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error('Batch resume failed:', err)
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
      for (const t of [
        ...(snapshot.tasks.active || []),
        ...(snapshot.tasks.waiting || []),
        ...(snapshot.tasks.stopped || []),
      ]) {
        cacheMetadata(t)
      }
      tasks.value = {
        active: (snapshot.tasks.active || []).map(applyMetadataFromCache),
        waiting: (snapshot.tasks.waiting || []).map(applyMetadataFromCache),
        stopped: (snapshot.tasks.stopped || []).map(applyMetadataFromCache),
      }
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
    metadataPending, // Shared with events
    metadataInFlight: () => metadataInFlight, // Getter
    setMetadataInFlight: (val: boolean) => (metadataInFlight = val),
    queueMetadataRecovery,
    getLastStoppedFetchTime: () => lastStoppedFetchTime,
  }
}

export type TaskActions = ReturnType<typeof setupActions>
