import type { DownloadGroup, Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'

export type TaskGroupHint = {
  groupKey: string
  folderLabel?: string
  itemCount?: number
}

export type BatchGroupSummary = {
  groupKey: string
  folderLabel?: string
  itemCount?: number
}

function asGroupRecord(group: unknown): Partial<DownloadGroup> | undefined {
  if (!group || typeof group !== 'object') return undefined
  return group as Partial<DownloadGroup>
}

function normalizeText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  return trimmed ? trimmed : undefined
}

function normalizeCount(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value) && value > 0) return value
  return undefined
}

function normalizeTimestamp(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value) && value > 0) return value
  return 0
}

export function cloneDownloadGroup(group?: DownloadGroup | null): DownloadGroup | undefined {
  const source = asGroupRecord(group)
  const id = normalizeText(source?.id)
  if (!id) return undefined

  return {
    id,
    kind: normalizeText(source?.kind) ?? '',
    name: normalizeText(source?.name) ?? '',
    folder_name: normalizeText(source?.folder_name) ?? '',
    dir: normalizeText(source?.dir) ?? '',
    item_count: normalizeCount(source?.item_count) ?? 0,
    created_at: normalizeTimestamp(source?.created_at),
  }
}

export function cloneTaskDownloadGroup(task?: Partial<Task>): DownloadGroup | undefined {
  return cloneDownloadGroup((task as { download_group?: DownloadGroup | null } | undefined)?.download_group)
}

export function hasTaskGroupMetadata(task?: Partial<Task>): boolean {
  return cloneTaskDownloadGroup(task) !== undefined
}

export function mergeDownloadGroups(
  existing?: DownloadGroup | null,
  incoming?: DownloadGroup | null,
): DownloadGroup | undefined {
  const current = cloneDownloadGroup(existing)
  const next = cloneDownloadGroup(incoming)

  if (!next) return current
  if (!current) return next
  if (current.id !== next.id) return next

  return {
    id: next.id,
    kind: next.kind || current.kind,
    name: next.name || current.name,
    folder_name: next.folder_name || current.folder_name,
    dir: next.dir || current.dir,
    item_count: next.item_count || current.item_count,
    created_at: next.created_at || current.created_at,
  }
}

export function mergeTaskGroupMetadata(existing?: Partial<Task>, incoming?: Partial<Task>): Partial<Task> {
  const merged = mergeDownloadGroups(cloneTaskDownloadGroup(existing), cloneTaskDownloadGroup(incoming))
  return merged ? { download_group: merged } : {}
}

export function applyTaskGroupMetadata<T extends Partial<Task>>(task: T, group?: DownloadGroup): T {
  if (!group) return task
  return { ...task, download_group: cloneDownloadGroup(group) ?? group } as T
}

export function isTaskGroupEqual(a?: DownloadGroup | null, b?: DownloadGroup | null): boolean {
  const left = cloneDownloadGroup(a)
  const right = cloneDownloadGroup(b)

  if (!left && !right) return true
  if (!left || !right) return false

  return (
    left.id === right.id &&
    left.kind === right.kind &&
    left.name === right.name &&
    left.folder_name === right.folder_name &&
    left.dir === right.dir &&
    left.item_count === right.item_count &&
    left.created_at === right.created_at
  )
}

function toGroupSummary(group?: DownloadGroup | null): BatchGroupSummary | undefined {
  const normalized = cloneDownloadGroup(group)
  if (!normalized) return undefined

  return {
    groupKey: normalized.id,
    folderLabel: normalized.folder_name || normalized.name || undefined,
    itemCount: normalizeCount(normalized.item_count),
  }
}

export function getTaskGroupHint(task: Task): TaskGroupHint | null {
  const summary = toGroupSummary(cloneTaskDownloadGroup(task))
  return summary ? { ...summary } : null
}

export function buildBatchGroupSummaries(groups?: DownloadGroup[] | null): BatchGroupSummary[] {
  if (!groups?.length) return []

  const summariesByKey = new Map<string, BatchGroupSummary>()
  for (const group of groups) {
    const summary = toGroupSummary(group)
    if (!summary) continue

    const existing = summariesByKey.get(summary.groupKey)
    if (!existing) {
      summariesByKey.set(summary.groupKey, summary)
      continue
    }

    existing.folderLabel = existing.folderLabel || summary.folderLabel
    existing.itemCount = existing.itemCount || summary.itemCount
  }

  return Array.from(summariesByKey.values())
}

export function buildVisibleTaskGroupHints(tasks: Task[]): Map<string, TaskGroupHint> {
  const summariesByKey = new Map<string, TaskGroupHint>()
  const hintsByGid = new Map<string, TaskGroupHint>()

  for (const task of tasks) {
    const summary = getTaskGroupHint(task)
    if (!summary) continue

    const existing = summariesByKey.get(summary.groupKey)
    if (existing) {
      existing.folderLabel = existing.folderLabel || summary.folderLabel
      existing.itemCount = existing.itemCount || summary.itemCount
      hintsByGid.set(task.gid, existing)
      continue
    }

    summariesByKey.set(summary.groupKey, summary)
    hintsByGid.set(task.gid, summary)
  }

  return hintsByGid
}
