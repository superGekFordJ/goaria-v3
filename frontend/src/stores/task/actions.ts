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
import { mergeTasks, dedupByGid, applyLocalOrder, markStoppedTaskRaw } from './utils'
import { cloneTaskGroupMetadata } from './grouping'
import type { TaskBucket, TaskMovePlacement } from './events'
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
    const membershipAtSend = liveBucketByGid()
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

      const merged = {
        active: activeResult.merged,
        waiting: waitingResult.merged,
        stopped: nextStopped,
      }
      // Rows events re-seated mid-flight keep their event-side bucket even
      // when that bucket is one this read does not rewrite (e.g. stopped).
      const next = preserveInflightMoves(membershipAtSend, merged)

      const newCount = next.active.length + next.waiting.length
      const taskCompleted = newCount < oldCount

      if (activeResult.changed || waitingResult.changed || stoppedChanged || next !== merged) {
        tasks.value = next
      }

      queueMissingMetadataFromLists(next.active, next.waiting)

      throttledUpdateTrayIcon()
      return { hasActiveTasks: next.active.length > 0 || next.waiting.length > 0, taskCompleted }
    } catch (err) {
      handleFetchError(err)
      return { hasActiveTasks: false, taskCompleted: false }
    }
  }

  async function fetchStoppedTasks() {
    const membershipAtSend = liveBucketByGid()
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

      // Active/waiting pass through untouched; the reconcile only re-homes
      // rows events folded mid-flight (e.g. a completion landing in stopped
      // that a pre-move stopped snapshot does not know yet).
      const { merged: mergedStopped, changed: stoppedChanged } = mergeTasks(
        tasks.value.stopped,
        filteredStopped,
      )
      const merged = {
        active: tasks.value.active,
        waiting: tasks.value.waiting,
        stopped: stoppedChanged ? mergedStopped.map(markStoppedTaskRaw) : mergedStopped,
      }
      const next = preserveInflightMoves(membershipAtSend, merged)

      const unchanged =
        next.active === tasks.value.active &&
        next.waiting === tasks.value.waiting &&
        next.stopped.length === tasks.value.stopped.length &&
        next.stopped.every((t, i) => t === tasks.value.stopped[i])
      if (!unchanged) {
        tasks.value = next
      }
      lastStoppedFetchTime = Date.now()
    } catch (err) {
      console.warn('Failed to fetch stopped tasks:', err)
    }
  }

  type LiveSeat = { bucket: TaskBucket; row: Task }

  // Bucket+row ownership per GID at call time; compared after an awaited read
  // to detect rows event folds re-seated while the snapshot was in flight.
  function liveBucketByGid(): Map<string, LiveSeat> {
    const map = new Map<string, LiveSeat>()
    for (const t of tasks.value.active) if (t?.gid) map.set(t.gid, { bucket: 'active', row: t })
    for (const t of tasks.value.waiting) if (t?.gid) map.set(t.gid, { bucket: 'waiting', row: t })
    for (const t of tasks.value.stopped) if (t?.gid) map.set(t.gid, { bucket: 'stopped', row: t })
    return map
  }

  // GIDs re-seated by event folds during a read are newer facts than the
  // snapshot: strip their snapshot seats and re-home the live row in the
  // bucket the events chose. Everything else lands verbatim.
  // Seats are (bucket, row identity): re-seating folds always swap the row
  // object, so a same-bucket round trip still counts. Same-bucket in-place
  // field patches keep the row and let the snapshot seat land (transparent).
  // With overlapping reads a later landing is judged against the seats the
  // earlier one left — a bounded approximation the next authoritative
  // event or read converges.
  function preserveInflightMoves(
    before: Map<string, LiveSeat>,
    next: { active: Task[]; waiting: Task[]; stopped: Task[] },
  ): { active: Task[]; waiting: Task[]; stopped: Task[] } {
    const after = liveBucketByGid()
    const reseat = new Map<string, LiveSeat>()
    const dropped = new Set<string>()
    for (const [gid, cur] of after) {
      const prev = before.get(gid)
      if (!prev || prev.bucket !== cur.bucket || prev.row !== cur.row) reseat.set(gid, cur)
    }
    for (const gid of before.keys()) {
      if (!after.has(gid)) dropped.add(gid)
    }
    if (reseat.size === 0 && dropped.size === 0) return next

    const moved = new Set<string>([...reseat.keys(), ...dropped])
    // Untouched buckets keep their array identity so partial writers can
    // tell a reconciled list from an unchanged one by reference.
    const strip = (list: Task[]) => {
      const kept = list.filter(t => !moved.has(t.gid))
      return kept.length === list.length ? list : kept
    }
    const nextLists: Record<TaskBucket, Task[]> = {
      active: strip(next.active),
      waiting: strip(next.waiting),
      stopped: strip(next.stopped),
    }
    const currentLists: Record<TaskBucket, Task[]> = {
      active: tasks.value.active,
      waiting: tasks.value.waiting,
      stopped: tasks.value.stopped,
    }
    for (const seat of reseat.values()) {
      nextLists[seat.bucket] = applyLocalOrder(currentLists[seat.bucket], [
        ...nextLists[seat.bucket],
        seat.row,
      ])
    }
    if (
      nextLists.active === next.active &&
      nextLists.waiting === next.waiting &&
      nextLists.stopped === next.stopped
    ) {
      return next
    }
    return nextLists
  }

  async function fetchTasks(options?: { prependUnknownFrom?: Set<string> }) {
    const membershipAtSend = liveBucketByGid()
    try {
      const res = await GetTasks()
      consecutiveErrors.value = 0

      const { active, waiting, stopped } = partitionTaskLists(res.active, res.waiting, res.stopped)

      for (const t of [...active, ...waiting, ...stopped]) cacheMetadata(t)

      let nextActive = applyLocalOrder(tasks.value.active, active).map(applyMetadataFromCache)
      let nextWaiting = applyLocalOrder(tasks.value.waiting, waiting).map(applyMetadataFromCache)
      const nextStopped = stopped.map(t => markStoppedTaskRaw(applyMetadataFromCache(t)))

      // Prepend in THIS assign. await fetchTasks() then a second tasks.value=
      // (prependNewTasks) is two Vue flushes: TaskList capture/plays append,
      // then a bottom→top MOVE that looks like the pre-e2fd1dee enter swap.
      if (options?.prependUnknownFrom) {
        const known = options.prependUnknownFrom
        nextActive = prependUnknownGids(nextActive, known)
        nextWaiting = prependUnknownGids(nextWaiting, known)
      }

      // Assign tasks FIRST so the async metadata callback operates on current state
      const next = preserveInflightMoves(membershipAtSend, {
        active: nextActive,
        waiting: nextWaiting,
        stopped: nextStopped,
      })
      const unchanged =
        next.active.length === tasks.value.active.length &&
        next.active.every((t, i) => t === tasks.value.active[i]) &&
        next.waiting.length === tasks.value.waiting.length &&
        next.waiting.every((t, i) => t === tasks.value.waiting[i]) &&
        next.stopped.length === tasks.value.stopped.length &&
        next.stopped.every((t, i) => t === tasks.value.stopped[i])
      if (!unchanged) {
        tasks.value = next
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
    const membershipAtSend = liveBucketByGid()
    try {
      const snapshot = await GetFullSnapshot()
      const { active, waiting, stopped } = partitionTaskLists(
        snapshot.tasks.active,
        snapshot.tasks.waiting,
        snapshot.tasks.stopped,
      )

      for (const t of [...active, ...waiting, ...stopped]) cacheMetadata(t)

      tasks.value = preserveInflightMoves(membershipAtSend, {
        active: applyLocalOrder(tasks.value.active, active).map(applyMetadataFromCache),
        waiting: applyLocalOrder(tasks.value.waiting, waiting).map(applyMetadataFromCache),
        stopped: stopped.map(t => markStoppedTaskRaw(applyMetadataFromCache(t))),
      })

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
    metadataPending, // Test-only seam; production writes go through queueMetadataRecovery
    metadataInFlight: () => metadataInFlight, // Test-only
    setMetadataInFlight: (val: boolean) => (metadataInFlight = val), // Test-only
    queueMetadataRecovery,
    getLastStoppedFetchTime: () => lastStoppedFetchTime,
  }
}

export type TaskActions = ReturnType<typeof setupActions>
