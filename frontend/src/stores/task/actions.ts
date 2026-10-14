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
import { cloneTaskGroupMetadata } from './grouping'
import type { TaskMovePlacement } from './events'
import { useDownloadGroupStore } from '../downloadGroups'

export function setupActions(state: TaskState) {
  // Reuse sets to avoid GC - scoped to setupActions for better encapsulation
  const _activeGidSet = new Set<string>()
  const _waitingGidSet = new Set<string>()
  const _stoppedGidSet = new Set<string>()
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

  // Callback to restart polling if needed (will be injected by index.ts or polling.ts)
  let _restartPollingCallback: (() => void) | null = null
  let _stopPollingCallback: ((disableContext: boolean) => void) | null = null
  let _moveTasks: ((placements: TaskMovePlacement[]) => { missing: boolean }) | null = null

  function setPollingCallbacks(restart: () => void, stop: (disable: boolean) => void) {
    _restartPollingCallback = restart
    _stopPollingCallback = stop
  }

  function setTaskMover(fn: (placements: TaskMovePlacement[]) => { missing: boolean }) {
    _moveTasks = fn
  }

  // Fail closed: only explicit ok === true entries for requested GIDs count.
  // Non-array, malformed entries, unknown/missing GIDs and non-boolean truthy
  // values all mean "not a success".
  function batchResumeOkGids(results: unknown, requested: string[]): string[] {
    if (!Array.isArray(results)) return []
    const ok = new Set<string>()
    for (const item of results) {
      if (!item || typeof item !== 'object') continue
      const row = item as { gid?: unknown; ok?: unknown }
      if (row.ok === true && typeof row.gid === 'string' && row.gid) ok.add(row.gid)
    }
    return requested.filter(gid => ok.has(gid))
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

  // Static-field backfill only: a late metadata response fills empty
  // title/files/dir/group on the row the GID currently owns. Live fields
  // (status, counters, speed, errors, threads) stay owned by events/reads.
  // Returns null when there is nothing to fill.
  function mergeRecoveredMetadata(existing: Task, meta: Task): Task | null {
    let merged: Task | null = null
    const patch = () => (merged ??= { ...existing })

    if (!isNonEmptyString(existing.title) && isNonEmptyString(meta.title)) {
      patch().title = meta.title
    }
    if (!hasValidFiles(existing) && hasValidFiles(meta)) {
      patch().files = meta.files
    }
    if (!isNonEmptyString(existing.dir) && isNonEmptyString(meta.dir)) {
      patch().dir = meta.dir
    }
    if (!cloneTaskGroupMetadata(existing)) {
      const group = cloneTaskGroupMetadata(meta)
      if (group) patch().download_group = group
    }
    return merged
  }

  function applyRecoveredMetadata(metadata: Record<string, Task | undefined>) {
    let newActive = tasks.value.active
    let newWaiting = tasks.value.waiting
    let activeChanged = false
    let waitingChanged = false

    for (const gid of Object.keys(metadata)) {
      const meta = metadata[gid]
      if (!meta) continue

      // Fill on the row the GID currently owns; rows that moved away or were
      // deleted while the request was in flight are never revived.
      const activeIdx = newActive.findIndex(t => t.gid === gid)
      const waitingIdx = newWaiting.findIndex(t => t.gid === gid)
      const existing =
        activeIdx !== -1
          ? newActive[activeIdx]
          : waitingIdx !== -1
            ? newWaiting[waitingIdx]
            : undefined
      if (!existing) continue

      const merged = mergeRecoveredMetadata(existing, meta)
      if (!merged) continue
      cacheMetadata(merged)

      if (activeIdx !== -1) {
        if (!activeChanged) {
          newActive = [...newActive]
          activeChanged = true
        }
        newActive[activeIdx] = merged
      } else {
        if (!waitingChanged) {
          newWaiting = [...newWaiting]
          waitingChanged = true
        }
        newWaiting[waitingIdx] = merged
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

  async function fetchActiveTasks(): Promise<{
    hasActiveTasks: boolean
    taskCompleted: boolean
  }> {
    try {
      const res = await GetActiveTasks()
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

      // An incoming live row outranks a local stopped copy of the same GID.
      let nextStopped = tasks.value.stopped
      let stoppedChanged = false
      for (const t of tasks.value.stopped) {
        if (_activeGidSet.has(t.gid) || _waitingGidSet.has(t.gid)) {
          stoppedChanged = true
          break
        }
      }
      if (stoppedChanged) {
        nextStopped = tasks.value.stopped.filter(
          t => !_activeGidSet.has(t.gid) && !_waitingGidSet.has(t.gid),
        )
      }

      const nextActive = activeResult.merged
      const nextWaiting = waitingResult.merged
      const newCount = nextActive.length + nextWaiting.length
      const taskCompleted = newCount < oldCount

      if (activeResult.changed || waitingResult.changed || stoppedChanged) {
        tasks.value = {
          active: nextActive,
          waiting: nextWaiting,
          stopped: nextStopped,
        }
      }

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

      const filteredStopped = stopped.filter(
        t => !_activeGidSet.has(t.gid) && !_waitingGidSet.has(t.gid),
      )

      const stoppedResult = mergeTasks(tasks.value.stopped, filteredStopped)
      const nextStopped = stoppedResult.merged

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

      // Assign tasks FIRST so the async metadata callback operates on current state
      tasks.value = {
        active: nextActive,
        waiting: nextWaiting,
        stopped: nextStopped,
      }

      queueMissingMetadataFromLists(tasks.value.active, tasks.value.waiting)
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
    // PauseTask resolves void — the event stream is authoritative.
    try {
      await PauseTask(gid)
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error(`Failed to pause task ${gid}:`, err)
    }
  }

  async function resume(gid: string) {
    try {
      await ResumeTask(gid)
      // A resolved resume means queued, not started: the row waits in
      // 'waiting' until authoritative events move it again.
      const missing = !_moveTasks || _moveTasks([{ gid, dest: 'waiting' }]).missing
      if (missing) await fetchTasks()
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error(`Failed to resume task ${gid}:`, err)
      await fetchTasks()
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
      const results = await BatchResume(gids)
      const okGids = batchResumeOkGids(results, gids)
      if (okGids.length > 0) {
        const missing =
          !_moveTasks || _moveTasks(okGids.map(gid => ({ gid, dest: 'waiting' as const }))).missing
        if (missing) await fetchTasks()
      }
      immediateUpdateTrayIcon()
    } catch (err) {
      console.error('Batch resume failed:', err)
      await fetchTasks()
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
    setTaskMover,
    metadataPending, // Shared with events
    metadataInFlight: () => metadataInFlight, // Getter
    setMetadataInFlight: (val: boolean) => (metadataInFlight = val),
    queueMetadataRecovery,
    getLastStoppedFetchTime: () => lastStoppedFetchTime,
  }
}

export type TaskActions = ReturnType<typeof setupActions>
