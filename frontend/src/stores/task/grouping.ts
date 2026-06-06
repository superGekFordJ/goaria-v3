import type { DownloadGroup, Task } from '../../../bindings/goaria-v3/internal/rpc/models'

export type TaskGroupHint = {
  groupKey: string
  folderLabel?: string
  folderPath?: string
  totalCount?: number
  visibleCount?: number
  ordinal?: number
  isAutoFoldered: boolean
}

export type GroupSummary = {
  groupKey: string
  folderLabel?: string
  folderPath?: string
  totalCount?: number
  visibleCount: number
  isAutoFoldered: boolean
}

const secretSegmentPattern = /(?:token|secret|bearer|cookie|auth|account|password|key)[^\\/]*/i
const uriOrQueryPattern = /(?:https?:|ftp:|sftp:|magnet:|[?#&])/i

function cleanText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  return trimmed.length > 0 ? trimmed : undefined
}

function positiveNumber(value: unknown): number | undefined {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return undefined
  return value
}

function basename(value: string): string {
  const normalized = value.replace(/\\+$/, '')
  const index = Math.max(normalized.lastIndexOf('/'), normalized.lastIndexOf('\\'))
  return index >= 0 ? normalized.slice(index + 1) : normalized
}

function isSafeFolderPathHint(path: string): boolean {
  if (uriOrQueryPattern.test(path)) return false
  return !path.split(/[\\/]+/).some(segment => secretSegmentPattern.test(segment))
}

export function cloneTaskGroupMetadata(task?: Partial<Task>): DownloadGroup | undefined {
  const group = task?.download_group
  if (!group) return undefined
  const id = cleanText(group.id)
  if (!id) return undefined

  return {
    id,
    kind: cleanText(group.kind) ?? '',
    name: cleanText(group.name) ?? '',
    name_status: cleanText(group.name_status),
    folder_name: cleanText(group.folder_name) ?? '',
    dir: cleanText(group.dir) ?? '',
    item_count: positiveNumber(group.item_count) ?? 0,
    created_at:
      typeof group.created_at === 'number' && Number.isFinite(group.created_at)
        ? group.created_at
        : 0,
  }
}

export function hasTaskGroupMetadata(task?: Partial<Task>): boolean {
  return cloneTaskGroupMetadata(task) !== undefined
}

export function mergeTaskGroupMetadata(
  existing?: Partial<Task>,
  incoming?: Partial<Task>,
): Partial<Task> {
  const incomingGroup = cloneTaskGroupMetadata(incoming)
  if (incomingGroup) return { download_group: incomingGroup }

  const existingGroup = cloneTaskGroupMetadata(existing)
  if (existingGroup) return { download_group: existingGroup }

  return {}
}

export function applyTaskGroupMetadata<T extends Partial<Task>>(task: T, group?: DownloadGroup): T {
  if (!group) return task
  return { ...task, download_group: { ...group } as Task['download_group'] }
}

export function isTaskGroupEqual(a?: DownloadGroup | null, b?: DownloadGroup | null): boolean {
  const left = cloneTaskGroupMetadata({ download_group: a ?? undefined })
  const right = cloneTaskGroupMetadata({ download_group: b ?? undefined })
  if (!left && !right) return true
  if (!left || !right) return false
  return (
    left.id === right.id &&
    left.kind === right.kind &&
    left.name === right.name &&
    (left.name_status ?? '') === (right.name_status ?? '') &&
    left.folder_name === right.folder_name &&
    left.dir === right.dir &&
    left.item_count === right.item_count &&
    left.created_at === right.created_at
  )
}

export function getDownloadGroupHint(group?: DownloadGroup | null): TaskGroupHint | null {
  const cloned = cloneTaskGroupMetadata({ download_group: group ?? undefined })
  if (!cloned) return null

  const folderName = cleanText(cloned.folder_name)
  const dir = cleanText(cloned.dir)
  const folderLabel = folderName ?? (dir ? basename(dir) : cleanText(cloned.name))
  const safeFolderPath = dir && isSafeFolderPathHint(dir) ? dir : undefined

  return {
    groupKey: cloned.id,
    folderLabel,
    folderPath: safeFolderPath,
    totalCount: positiveNumber(cloned.item_count),
    isAutoFoldered: Boolean(folderName || dir),
  }
}

export function getTaskGroupHint(task: Task): TaskGroupHint | null {
  return getDownloadGroupHint(task.download_group)
}

export function buildBatchGroupResultHints(groups?: DownloadGroup[]): TaskGroupHint[] {
  if (!groups?.length) return []

  const hints: TaskGroupHint[] = []
  const seen = new Set<string>()
  for (const group of groups) {
    const hint = getDownloadGroupHint(group)
    if (!hint || seen.has(hint.groupKey)) continue
    seen.add(hint.groupKey)
    hints.push(hint)
  }
  return hints
}

export function buildVisibleTaskGroupHints(tasks: Task[]): Map<string, TaskGroupHint> {
  const summaries = new Map<string, GroupSummary>()
  const baseHintsByGid = new Map<string, TaskGroupHint>()

  for (const task of tasks) {
    const hint = getTaskGroupHint(task)
    if (!hint) continue

    baseHintsByGid.set(task.gid, hint)

    const summary = summaries.get(hint.groupKey)
    if (summary) {
      summary.visibleCount += 1
      if (!summary.folderLabel && hint.folderLabel) summary.folderLabel = hint.folderLabel
      if (!summary.folderPath && hint.folderPath) summary.folderPath = hint.folderPath
      if (!summary.totalCount && hint.totalCount) summary.totalCount = hint.totalCount
      summary.isAutoFoldered = summary.isAutoFoldered || hint.isAutoFoldered
    } else {
      summaries.set(hint.groupKey, {
        groupKey: hint.groupKey,
        folderLabel: hint.folderLabel,
        folderPath: hint.folderPath,
        totalCount: hint.totalCount,
        visibleCount: 1,
        isAutoFoldered: hint.isAutoFoldered,
      })
    }
  }

  const ordinalByGroup = new Map<string, number>()
  const hintsByGid = new Map<string, TaskGroupHint>()

  for (const [gid, hint] of baseHintsByGid) {
    const summary = summaries.get(hint.groupKey)
    if (!summary) continue
    const ordinal = (ordinalByGroup.get(hint.groupKey) ?? 0) + 1
    ordinalByGroup.set(hint.groupKey, ordinal)
    hintsByGid.set(gid, {
      groupKey: hint.groupKey,
      folderLabel: summary.folderLabel,
      folderPath: summary.folderPath,
      totalCount: summary.totalCount,
      visibleCount: summary.visibleCount,
      ordinal,
      isAutoFoldered: summary.isAutoFoldered,
    })
  }

  return hintsByGid
}
