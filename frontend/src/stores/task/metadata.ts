import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import { cloneTaskGroupMetadata, isTaskGroupEqual } from './grouping'

// Global metadata cache - persists across list transitions
type CachedMetadata = {
  files?: Task['files']
  dir?: string
  totalLength?: string
  download_group?: NonNullable<Task['download_group']>
}
const metadataCache = new Map<string, CachedMetadata>()

export function cacheMetadata(task: Task) {
  if (!task.gid) return

  const existing = metadataCache.get(task.gid)
  const group = cloneTaskGroupMetadata(task)
  const hasFiles = Boolean(task.files?.length && task.files[0]?.path)

  if (!hasFiles && !group) return

  const next: CachedMetadata = existing ? { ...existing } : {}

  if (task.files?.length && task.files[0]?.path) {
    // Deep clone files to avoid reference corruption when task object is mutated
    next.files = task.files.map(f => ({ ...f, uris: f.uris ? [...f.uris] : [] }))
    next.dir = task.dir || ''
    next.totalLength = task.totalLength || '0'
  }

  if (group) next.download_group = group

  metadataCache.set(task.gid, next)
}

export function applyMetadataFromCache(task: Task): Task {
  const cached = metadataCache.get(task.gid)
  if (!cached) return task

  let next = task
  if ((!task.files || !task.files[0]?.path) && cached.files) {
    next = { ...next, files: cached.files, dir: cached.dir ?? next.dir }
  } else if (!task.dir && cached.dir) {
    next = { ...next, dir: cached.dir }
  }

  if (cached.download_group && !isTaskGroupEqual(next.download_group, cached.download_group)) {
    next = { ...next, download_group: { ...cached.download_group } }
  }

  return next
}

export function removeMetadata(gid: string) {
  metadataCache.delete(gid)
}

// For testing purposes
export function getMetadataCacheSize(): number {
  return metadataCache.size
}

export function clearMetadataCache() {
  metadataCache.clear()
}
