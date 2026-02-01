import { TaskState } from './state'
import {
  GetTasks,
  GetActiveTasks,
  GetStoppedTasks,
  GetTaskMetadata,
  AddUri,
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
import { cacheMetadata, applyMetadataFromCache, removeMetadata } from './metadata'
import { mergeTasks, dedupByGid } from './utils'

// Reuse sets to avoid GC
const _dedupGidSet = new Set<string>()
const _activeGidSet = new Set<string>()
const _waitingGidSet = new Set<string>()
const _stoppedGidSet = new Set<string>()
const metadataPending = new Set<string>()
let metadataInFlight = false

export function setupActions(state: TaskState) {
  const {
    tasks,
    selectedGids,
    consecutiveErrors,
    pollingContextEnabled,
    isWindowVisible,
    preferredInterval,
    throttledUpdateTrayIcon,
    immediateUpdateTrayIcon,
    pollingEnabled,
    isFetching,
  } = state

  const MAX_CONSECUTIVE_ERRORS = 3
  let lastStoppedFetchTime = 0

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

  async function fetchActiveTasks(): Promise<{ hasActiveTasks: boolean; taskCompleted: boolean }> {
    try {
      const res = await GetActiveTasks()
      consecutiveErrors.value = 0

      // Deduplicate stopped tasks
      _stoppedGidSet.clear()
      for (const t of tasks.value.stopped) {
        _stoppedGidSet.add(t.gid)
      }

      const active = dedupByGid((res.active || []).filter((t: Task) => !_stoppedGidSet.has(t.gid)))
      _activeGidSet.clear()
      for (const t of active) {
        _activeGidSet.add(t.gid)
      }
      const waiting = dedupByGid(
        (res.waiting || []).filter(
          (t: Task) => !_activeGidSet.has(t.gid) && !_stoppedGidSet.has(t.gid),
        ),
      )

      for (const t of [...active, ...waiting]) cacheMetadata(t)

      // Fetch missing metadata
      const tasksNeedingMetadata: string[] = []
      for (const t of active) if (!t.files?.[0]?.path) tasksNeedingMetadata.push(t.gid)
      for (const t of waiting) if (!t.files?.[0]?.path) tasksNeedingMetadata.push(t.gid)

      if (tasksNeedingMetadata.length > 0 && !metadataInFlight) {
        for (const gid of tasksNeedingMetadata) metadataPending.add(gid)
        metadataInFlight = true
        const batch = Array.from(metadataPending)
        metadataPending.clear()

        GetTaskMetadata(batch)
          .then((metadata: Record<string, Task>) => {
            if (!metadata) return
            let updated = false
            for (const gid of Object.keys(metadata)) {
              const meta = metadata[gid]
              if (!meta?.files?.[0]?.path) continue
              cacheMetadata(meta)
              for (const list of [tasks.value.active, tasks.value.waiting]) {
                const idx = list.findIndex(t => t.gid === gid)
                if (idx !== -1) {
                  list[idx] = { ...list[idx], ...meta }
                  updated = true
                }
              }
            }
            if (updated) tasks.value = { ...tasks.value }
          })
          .finally(() => (metadataInFlight = false))
      }

      const oldCount = tasks.value.active.length + tasks.value.waiting.length
      const newCount = active.length + waiting.length
      const taskCompleted = newCount < oldCount

      const activeResult = mergeTasks(tasks.value.active, active)
      const waitingResult = mergeTasks(tasks.value.waiting, waiting)

      if (activeResult.changed || waitingResult.changed) {
        tasks.value = {
          active: activeResult.merged,
          waiting: waitingResult.merged,
          stopped: tasks.value.stopped,
        }
      }

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

      const active = dedupByGid(res.active || [])
      _activeGidSet.clear()
      for (const t of active) _activeGidSet.add(t.gid)

      const waiting = dedupByGid((res.waiting || []).filter((t: Task) => !_activeGidSet.has(t.gid)))
      _waitingGidSet.clear()
      for (const t of waiting) _waitingGidSet.add(t.gid)

      const stopped = dedupByGid(
        (res.stopped || []).filter(
          (t: Task) => !_activeGidSet.has(t.gid) && !_waitingGidSet.has(t.gid),
        ),
      )

      for (const t of [...active, ...waiting, ...stopped]) cacheMetadata(t)

      const activeWithMeta = active.map(applyMetadataFromCache)
      const waitingWithMeta = waiting.map(applyMetadataFromCache)
      const stoppedWithMeta = stopped.map(applyMetadataFromCache)

      const newTasks = { active: activeWithMeta, waiting: waitingWithMeta, stopped: stoppedWithMeta }

      // Metadata fetching logic similar to fetchActiveTasks...
      const tasksNeedingMetadata: string[] = []
      for (const t of newTasks.active) if (!t.files?.[0]?.path) tasksNeedingMetadata.push(t.gid)
      for (const t of newTasks.waiting) if (!t.files?.[0]?.path) tasksNeedingMetadata.push(t.gid)

      if (tasksNeedingMetadata.length > 0) {
         for (const gid of tasksNeedingMetadata) metadataPending.add(gid)
         if (!metadataInFlight) {
            metadataInFlight = true
            const batch = Array.from(metadataPending)
            metadataPending.clear()
            GetTaskMetadata(batch).then((metadata: Record<string, Task>) => {
               if (!metadata) return
               let updated = false
               for (const gid of Object.keys(metadata)) {
                  const meta = metadata[gid]
                  if (!meta?.files?.[0]?.path) continue
                  for (const list of [tasks.value.active, tasks.value.waiting]) {
                     const idx = list.findIndex(t => t.gid === gid)
                     if (idx !== -1) {
                        list[idx] = { ...list[idx], ...meta }
                        updated = true
                     }
                  }
               }
               if (updated) tasks.value = { ...tasks.value }
            }).finally(() => metadataInFlight = false)
         }
      }

      tasks.value = newTasks
      immediateUpdateTrayIcon()
    } catch (err) {
      handleFetchError(err)
    }
  }

  // --- User Actions ---

  async function addUri(uri: string) {
    try {
      const res = await AddUri(uri)
      await fetchTasks()
      immediateUpdateTrayIcon()

      if (pollingContextEnabled.value && isWindowVisible.value && _restartPollingCallback && _stopPollingCallback) {
        _stopPollingCallback(false)
        _restartPollingCallback()
      }
      return res
    } catch (err) {
      console.error('Failed to add URI:', err)
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

  function selectAll(gids: string[]) {
    selectedGids.value = new Set(gids)
  }

  function clearSelection() {
    selectedGids.value = new Set()
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

  async function batchRemove(gids: string[], deleteFiles: boolean) {
    const gidSet = new Set(gids)
    tasks.value = {
      active: tasks.value.active.filter(t => !gidSet.has(t.gid)),
      waiting: tasks.value.waiting.filter(t => !gidSet.has(t.gid)),
      stopped: tasks.value.stopped.filter(t => !gidSet.has(t.gid)),
    }
    clearSelection()
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
      tasks.value = {
        active: snapshot.tasks.active || [],
        waiting: snapshot.tasks.waiting || [],
        stopped: snapshot.tasks.stopped || [],
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
    pause,
    resume,
    remove,
    openTaskFolder,
    toggleSelect,
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
    getLastStoppedFetchTime: () => lastStoppedFetchTime,
  }
}

export type TaskActions = ReturnType<typeof setupActions>
