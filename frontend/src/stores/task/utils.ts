import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
import { cacheMetadata, applyMetadataFromCache } from './metadata'
import { isTaskGroupEqual, mergeTaskGroupMetadata } from './grouping'

/**
 * 浅比对两个任务是否相等（仅比较高频变化字段）
 * 高频字段：completedLength, downloadSpeed, status
 * 低频字段：files, dir, errorCode 等变化极少
 */
export function isTaskEqual(a: Task, b: Task): boolean {
  if (!a || !b) return false
  return (
    a.gid === b.gid &&
    a.status === b.status &&
    a.completedLength === b.completedLength &&
    a.downloadSpeed === b.downloadSpeed &&
    a.totalLength === b.totalLength &&
    a.errorCode === b.errorCode &&
    isTaskGroupEqual(a.download_group, b.download_group)
  )
}

/**
 * 增量合并任务列表
 * @param oldList 当前列表
 * @param newList 新获取的列表
 * @returns { merged: Task[], changed: boolean }
 */
export function mergeTasks(oldList: Task[], newList: Task[]): { merged: Task[]; changed: boolean } {
  // 快速路径：两个列表都为空
  if (oldList.length === 0 && newList.length === 0) {
    return { merged: oldList, changed: false }
  }

  // 快速路径：旧列表为空，直接返回新列表（但先应用缓存）
  if (oldList.length === 0) {
    const merged = newList.map(t => {
      cacheMetadata(t)
      return applyMetadataFromCache(t)
    })
    return { merged, changed: true }
  }

  _mergeOldMap.clear()
  for (const t of oldList) _mergeOldMap.set(t.gid, t)

  // 检查是否有任务增减
  if (oldList.length !== newList.length) {
    const merged = newList.map(t => {
      cacheMetadata(t)
      return applyMetadataFromCache(t)
    })
    return { merged, changed: true }
  }

  // 检查 GID 集合是否一致
  _mergeOldGids.clear()
  for (const t of oldList) _mergeOldGids.add(t.gid)
  for (const newTask of newList) {
    if (!_mergeOldGids.has(newTask.gid)) {
      const merged = newList.map(t => {
        cacheMetadata(t)
        return applyMetadataFromCache(t)
      })
      return { merged, changed: true }
    }
  }

  // 逐个比对，保留未变化任务的引用
  let changed = false
  const merged = newList.map(newTask => {
    // Always cache valid metadata from new data
    cacheMetadata(newTask)

    // Apply cached metadata if new task is missing files - DO THIS EARLY
    if (!newTask.files?.length || !newTask.files[0]?.path) {
      newTask = applyMetadataFromCache(newTask)
    }

    const oldTask = _mergeOldMap.get(newTask.gid)
    if (oldTask) {
      // Check if we gained metadata (files appeared)
      const gainedMetadata =
        (!oldTask.files?.length || !oldTask.files[0]?.path) &&
        newTask.files?.length &&
        newTask.files[0]?.path

      if (isTaskEqual(oldTask, newTask) && !gainedMetadata) {
        return oldTask // 保持原引用
      }

      // Preserve old task's metadata if still missing in new task (and cache didn't help)
      if (
        (!newTask.files || newTask.files.length === 0) &&
        oldTask.files &&
        oldTask.files.length > 0
      ) {
        newTask.files = oldTask.files
      }
      if (!newTask.dir && oldTask.dir) {
        newTask.dir = oldTask.dir
      }
      Object.assign(newTask, mergeTaskGroupMetadata(oldTask, newTask))
    } else {
      // New task - already applied cache above
    }
    changed = true
    return newTask
  })

  // 开发模式日志
  if (import.meta.env.DEV && changed) {
    const changedCount = merged.filter((t, i) => t !== oldList[i]).length
    console.debug(`[Polling] Merged ${newList.length} tasks, ${changedCount} changed`)
  }

  return { merged, changed }
}

/**
 * 按 GID 去重（复用 Set 实例减少 GC 压力）
 */
// Reuse Map/Set instances to reduce GC pressure in hot paths
const _mergeOldMap = new Map<string, Task>()
const _mergeOldGids = new Set<string>()
const _dedupGidSet = new Set<string>()
const _localOrderGids = new Set<string>()
const _incomingKnown = new Map<string, Task>()
export function dedupByGid(list: Task[]): Task[] {
  _dedupGidSet.clear()
  return (list || []).filter(t => {
    const gid = t?.gid
    if (!gid || _dedupGidSet.has(gid)) return false
    _dedupGidSet.add(gid)
    return true
  })
}

/**
 * Keep local row order; never-seen GIDs lead in incoming order.
 * Membership still comes from `incoming` (local-only GIDs drop out).
 */
export function applyLocalOrder(localList: Task[], incoming: Task[]): Task[] {
  if (incoming.length === 0 || localList.length === 0) return incoming

  _localOrderGids.clear()
  for (const t of localList) {
    if (t?.gid) _localOrderGids.add(t.gid)
  }

  const ordered: Task[] = []
  _incomingKnown.clear()
  for (const t of incoming) {
    const gid = t?.gid
    if (!gid || !_localOrderGids.has(gid)) ordered.push(t)
    else _incomingKnown.set(gid, t)
  }
  if (_incomingKnown.size === 0) return incoming

  for (const t of localList) {
    const kept = t.gid ? _incomingKnown.get(t.gid) : undefined
    if (kept) ordered.push(kept)
  }
  const unchanged =
    ordered.length === incoming.length && ordered.every((t, i) => t === incoming[i])
  return unchanged ? incoming : ordered
}
