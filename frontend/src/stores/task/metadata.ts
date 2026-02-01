import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'

// Global metadata cache - persists across list transitions
// Key: gid, Value: { files, dir, totalLength }
type CachedMetadata = {
  files: Task['files']
  dir: string
  totalLength: string
}
const metadataCache = new Map<string, CachedMetadata>()

export function cacheMetadata(task: Task) {
  if (task.files?.length && task.files[0]?.path) {
    // Deep clone files to avoid reference corruption when task object is mutated
    metadataCache.set(task.gid, {
      files: task.files.map(f => ({ ...f, uris: f.uris ? [...f.uris] : [] })),
      dir: task.dir || '',
      totalLength: task.totalLength || '0',
    })
  }
}

export function applyMetadataFromCache(task: Task): Task {
  if ((!task.files || !task.files[0]?.path) && metadataCache.has(task.gid)) {
    const cached = metadataCache.get(task.gid)!
    return { ...task, files: cached.files, dir: cached.dir }
  }
  return task
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
