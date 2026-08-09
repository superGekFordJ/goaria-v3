import { ref, computed } from 'vue'
import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import { UpdateTrayState } from '../../../bindings/goaria-v3/internal/wailsapp/app.js'

type UriLookup = Iterable<string> & {
  readonly size: number
  has(value: string): boolean
  forEach(
    callbackfn: (value: string, value2: string, set: UriLookup) => void,
    thisArg?: unknown,
  ): void
  entries(): IterableIterator<[string, string]>
  keys(): IterableIterator<string>
  values(): IterableIterator<string>
}

function collectTaskUris(list: Task[]): Set<string> {
  const uris = new Set<string>()
  for (const task of list) {
    if (!task.files) continue
    for (const file of task.files) {
      if (!file.uris) continue
      for (const uri of file.uris) {
        if (uri?.uri) uris.add(uri.uri)
      }
    }
  }
  return uris
}

class CombinedUriSet implements UriLookup {
  readonly [Symbol.toStringTag] = 'Set'

  constructor(private readonly sets: readonly ReadonlySet<string>[]) {}

  get size(): number {
    const uniqueUris = new Set<string>()
    for (const set of this.sets) {
      for (const uri of set) uniqueUris.add(uri)
    }
    return uniqueUris.size
  }

  has(value: string): boolean {
    return this.sets.some(set => set.has(value))
  }

  forEach(
    callbackfn: (value: string, value2: string, set: UriLookup) => void,
    thisArg?: unknown,
  ): void {
    for (const value of this) {
      callbackfn.call(thisArg, value, value, this)
    }
  }

  *entries(): IterableIterator<[string, string]> {
    for (const value of this) {
      yield [value, value]
    }
  }

  keys(): IterableIterator<string> {
    return this.values()
  }

  *values(): IterableIterator<string> {
    const emitted = new Set<string>()
    for (const set of this.sets) {
      for (const uri of set) {
        if (emitted.has(uri)) continue
        emitted.add(uri)
        yield uri
      }
    }
  }

  [Symbol.iterator](): IterableIterator<string> {
    return this.values()
  }
}

export function setupState() {
  // State
  const tasks = ref<Record<string, Task[]>>({
    active: [],
    waiting: [],
    stopped: [],
  })

  // Selection State for batch operations
  const selectedGids = ref<Set<string>>(new Set())
  const selectedGroupKeys = ref<Set<string>>(new Set())

  // Polling & App Flags
  const syncMode = ref<'polling' | 'event-driven'>('event-driven')
  const pollingEnabled = ref(false)
  const pollingContextEnabled = ref(false)
  const isFetching = ref(false)
  const isWindowVisible = ref(true)
  const preferredInterval = ref(1000)
  const consecutiveErrors = ref(0)

  // Getters
  const activeTasks = computed(() => tasks.value.active || [])
  const waitingTasks = computed(() => tasks.value.waiting || [])
  const stoppedTasks = computed(() => tasks.value.stopped || [])

  const allTasksCount = computed(() => {
    return activeTasks.value.length + waitingTasks.value.length + stoppedTasks.value.length
  })

  const activeUris = computed(() => collectTaskUris(activeTasks.value))
  const waitingUris = computed(() => collectTaskUris(waitingTasks.value))
  const stoppedUris = computed(() => collectTaskUris(stoppedTasks.value))
  const allUris = computed<UriLookup>(
    () => new CombinedUriSet([activeUris.value, waitingUris.value, stoppedUris.value]),
  )

  // Selection Getters
  const selectedTaskCount = computed(() => selectedGids.value.size)
  const selectedGroupCount = computed(() => selectedGroupKeys.value.size)
  const selectedCount = computed(() => selectedTaskCount.value + selectedGroupCount.value)
  const isSelected = (gid: string) => selectedGids.value.has(gid)
  const isGroupSelected = (groupKey: string) => selectedGroupKeys.value.has(groupKey.trim())
  const getSelectedGids = computed(() => Array.from(selectedGids.value))
  const getSelectedGroupKeys = computed(() => Array.from(selectedGroupKeys.value))

  // Tray State (UI Logic related to state)
  let lastTrayState = { hasActive: false, hasPaused: false, hasError: false }
  let trayUpdateTimer: ReturnType<typeof setTimeout> | null = null
  const TRAY_UPDATE_DEBOUNCE = 500

  function computeTrayState() {
    const hasActive = tasks.value.active.length > 0
    const hasPaused =
      tasks.value.waiting.some(t => t.status === 'paused') ||
      tasks.value.active.some(t => t.status === 'paused')
    const hasError =
      tasks.value.active.some(t => t.status === 'error') ||
      tasks.value.waiting.some(t => t.status === 'error')
    return { hasActive, hasPaused, hasError }
  }

  function throttledUpdateTrayIcon() {
    if (trayUpdateTimer) return
    trayUpdateTimer = setTimeout(() => {
      trayUpdateTimer = null
      const newState = computeTrayState()
      if (
        newState.hasActive !== lastTrayState.hasActive ||
        newState.hasPaused !== lastTrayState.hasPaused ||
        newState.hasError !== lastTrayState.hasError
      ) {
        lastTrayState = newState
        UpdateTrayState(newState.hasActive, newState.hasPaused, newState.hasError)
      }
    }, TRAY_UPDATE_DEBOUNCE)
  }

  function immediateUpdateTrayIcon() {
    if (trayUpdateTimer) {
      clearTimeout(trayUpdateTimer)
      trayUpdateTimer = null
    }
    const newState = computeTrayState()
    if (
      newState.hasActive !== lastTrayState.hasActive ||
      newState.hasPaused !== lastTrayState.hasPaused ||
      newState.hasError !== lastTrayState.hasError
    ) {
      lastTrayState = newState
      UpdateTrayState(newState.hasActive, newState.hasPaused, newState.hasError)
    }
  }

  return {
    tasks,
    selectedGids,
    selectedGroupKeys,
    syncMode,
    pollingEnabled,
    pollingContextEnabled,
    isFetching,
    isWindowVisible,
    preferredInterval,
    consecutiveErrors,
    activeTasks,
    waitingTasks,
    stoppedTasks,
    allTasksCount,
    allUris,
    selectedTaskCount,
    selectedGroupCount,
    selectedCount,
    isSelected,
    isGroupSelected,
    getSelectedGids,
    getSelectedGroupKeys,
    throttledUpdateTrayIcon,
    immediateUpdateTrayIcon,
  }
}

export type TaskState = ReturnType<typeof setupState>
