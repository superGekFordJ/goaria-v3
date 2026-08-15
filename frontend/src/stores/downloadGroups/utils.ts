import type {
  DownloadGroupCard,
  DownloadGroupDetailEnvelope,
  DownloadGroupMemberCounts,
  DownloadGroupOperationItemResult,
  DownloadGroupOperationRefreshHint,
  DownloadGroupOperationResult,
  DownloadGroupWarning,
} from '../../../bindings/goaria-v3/internal/downloadgroups/models'
import type { DownloadGroup, Task } from '../../../bindings/goaria-v3/internal/rpc/models'

export const DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS = 15_000
export const DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS = 300
export const DOWNLOAD_GROUP_PENDING_NAME_REFETCH_MS = 1500
export const DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES = 5
export const PLACEHOLDER_TIMER_FLOOR_MS = 1

export type DownloadGroupPlaceholderSource = 'batch-add' | 'websocket'

export type DownloadGroupOperationAction = 'pause' | 'resume' | 'remove' | 'open_folder'
export type DownloadGroupOperationNoticeSeverity = 'success' | 'info' | 'warning' | 'error'

export type DownloadGroupOperationNotice = {
  id: number
  group_key: string
  action: DownloadGroupOperationAction
  severity: DownloadGroupOperationNoticeSeverity
  code: string
  succeeded: number
  skipped: number
  failed: number
  noop: boolean
  updated_at: number
  result: DownloadGroupOperationResult
}

export type DownloadGroupWarningSeverity = 'info' | 'warning' | 'error'

export type DownloadGroupWarningSummary = {
  code: string
  severity: DownloadGroupWarningSeverity
  count: number
  labelKey: string
  descriptionKey: string
}

export type DownloadGroupPlaceholder = {
  group_key: string
  download_group: DownloadGroup
  created_at: number
  expires_at: number
  source: DownloadGroupPlaceholderSource
}

export type DownloadGroupBackendMasterItem = {
  type: 'backend'
  group_key: string
  card: DownloadGroupCard
}

export type DownloadGroupPlaceholderMasterItem = {
  type: 'placeholder'
  group_key: string
  placeholder: DownloadGroupPlaceholder
}

export type DownloadGroupMasterItem =
  DownloadGroupBackendMasterItem | DownloadGroupPlaceholderMasterItem

export type InlineTaskListTab = 'downloads' | 'stopped'

export type InlineTaskListEntry =
  | {
      type: 'task'
      key: string
      task: Task
    }
  | {
      type: 'group'
      key: string
      group_key: string
      item: DownloadGroupMasterItem
    }

export type BuildInlineTaskListEntriesOptions = {
  tab: InlineTaskListTab
  tasks: Task[]
  groupItems: DownloadGroupMasterItem[]
  searchQuery?: string
  errorOnly?: boolean
}

export type DownloadGroupFetchOptions = {
  silent?: boolean
  reason?: string
}

const WARNING_SEVERITY_RANK: Record<DownloadGroupWarningSeverity, number> = {
  info: 1,
  warning: 2,
  error: 3,
}

const READ_WARNING_DEFAULT_SEVERITY: Record<string, DownloadGroupWarningSeverity> = {
  mixed_status: 'info',
  partial_error: 'error',
  missing_members: 'warning',
  missing_metadata: 'warning',
  history_only: 'info',
  stale_group: 'warning',
  name_pending: 'info',
  name_degraded: 'warning',
  group_not_found: 'warning',
}

export const READ_MODEL_WARNING_CODES = Object.keys(READ_WARNING_DEFAULT_SEVERITY)

export const OPERATION_WARNING_CODES = [
  'group_not_found',
  'empty_group',
  'no_actionable_members',
  'stale_member',
  'missing_member',
  'partial_failure',
  'rpc_error',
  'paused',
  'already_paused',
  'terminal_state',
  'history_only',
  'resumed',
  'already_active',
  'not_paused',
  'removed',
  'removed_stale_metadata',
  'remove_accepted',
  'opened',
  'folder_unavailable',
  'folder_unsafe',
  'open_failed',
]

const OPERATION_CODE_SET = new Set(OPERATION_WARNING_CODES)

export function normalizeWarningSeverity(value: unknown, fallback: DownloadGroupWarningSeverity) {
  return value === 'info' || value === 'warning' || value === 'error' ? value : fallback
}

export function warningSummaryForCode(
  code: string,
  severity: DownloadGroupWarningSeverity,
  count: number,
): DownloadGroupWarningSummary {
  return {
    code,
    severity,
    count,
    labelKey: `downloadGroups.warning.code.${code}.label`,
    descriptionKey: `downloadGroups.warning.code.${code}.description`,
  }
}

export function cleanKey(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function normalizeDownloadGroupWarningSummaries(
  warnings?: DownloadGroupWarning[] | null,
  nameStatus?: string | null,
  degraded = false,
): DownloadGroupWarningSummary[] {
  const byCode = new Map<string, DownloadGroupWarningSummary>()

  function add(code: string, severity: DownloadGroupWarningSeverity, count = 1) {
    if (!code) return
    const existing = byCode.get(code)
    if (!existing) {
      byCode.set(code, warningSummaryForCode(code, severity, Math.max(count, 1)))
      return
    }

    existing.count += Math.max(count, 1)
    if (WARNING_SEVERITY_RANK[severity] > WARNING_SEVERITY_RANK[existing.severity]) {
      existing.severity = severity
    }
  }

  for (const warning of warnings ?? []) {
    const code = cleanKey(warning?.code)
    if (!code || !READ_WARNING_DEFAULT_SEVERITY[code]) continue
    add(
      code,
      normalizeWarningSeverity(warning?.severity, READ_WARNING_DEFAULT_SEVERITY[code] ?? 'warning'),
      typeof warning?.count === 'number' && Number.isFinite(warning.count) ? warning.count : 1,
    )
  }

  if (nameStatus === 'pending') {
    add('name_pending', 'info', 1)
  } else if (nameStatus === 'degraded') {
    add('name_degraded', 'warning', 1)
  }

  if (degraded && byCode.size === 0) {
    add('stale_group', 'warning', 1)
  }

  return Array.from(byCode.values()).sort((a, b) => {
    const severityDelta = WARNING_SEVERITY_RANK[b.severity] - WARNING_SEVERITY_RANK[a.severity]
    if (severityDelta !== 0) return severityDelta
    return a.code.localeCompare(b.code)
  })
}

export function getTaskDownloadGroupId(task?: Task | null): string {
  return cleanKey(task?.download_group?.id)
}

export function snapshotGroupResumeHoldGids(
  groupKey: string,
  waitingTasks: Task[] | null | undefined,
  detailKey?: string | null,
  detailWaiting?: Task[] | null,
): string[] {
  const key = cleanKey(groupKey)
  const seen = new Set<string>()
  const gids: string[] = []

  function appendPausedWaiting(list: Task[] | null | undefined, requireGroupKey: boolean) {
    if (!list) return
    for (const task of list) {
      const gid = cleanKey(task?.gid)
      if (!gid || seen.has(gid)) continue
      if (task.status !== 'paused') continue
      if (requireGroupKey && getTaskDownloadGroupId(task) !== key) continue
      seen.add(gid)
      gids.push(gid)
    }
  }

  appendPausedWaiting(waitingTasks, true)
  if (key && cleanKey(detailKey) === key) {
    appendPausedWaiting(detailWaiting, false)
  }
  return gids
}

export function succeededOperationItemGids(
  result?: Pick<DownloadGroupOperationResult, 'items'> | null,
): string[] {
  const gids: string[] = []
  for (const item of result?.items ?? []) {
    const gid = cleanKey(item?.gid)
    if (!gid || item.status !== 'succeeded') continue
    gids.push(gid)
  }
  return gids
}

export function isTerminalDownloadGroupCard(card?: DownloadGroupCard | null): boolean {
  if (!card) return false
  const isTerminalStatus = card.status === 'complete' || card.status === 'error'
  if (!isTerminalStatus) return false
  const counts = card.counts
  return (counts.active ?? 0) + (counts.waiting ?? 0) + (counts.paused ?? 0) === 0
}

export function isDownloadGroupItemEligibleForTab(
  item: DownloadGroupMasterItem,
  tab: InlineTaskListTab,
): boolean {
  if (tab === 'downloads') {
    return item.type === 'placeholder' || !isTerminalDownloadGroupCard(item.card)
  }
  return item.type === 'backend' && isTerminalDownloadGroupCard(item.card)
}

export function getDownloadGroupMasterItemDisplayName(item: DownloadGroupMasterItem): string {
  if (item.type === 'backend') {
    return item.card.display_name || item.card.fallback_name || item.group_key
  }
  return (
    item.placeholder.download_group.name ||
    item.placeholder.download_group.folder_name ||
    item.group_key
  )
}

export function getDownloadGroupMasterItemSearchText(item: DownloadGroupMasterItem): string {
  if (item.type === 'backend') {
    const group = item.card.download_group
    return [
      item.card.display_name,
      item.card.fallback_name,
      item.card.folder_label,
      item.card.folder_path_hint,
      group?.name,
      group?.folder_name,
      group?.dir,
    ]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
  }

  const group = item.placeholder.download_group
  return [group.name, group.folder_name, group.dir].filter(Boolean).join(' ').toLowerCase()
}

export function taskFilename(task: Task): string {
  const path = task.files?.[0]?.path || task.title || task.gid
  const lastSlashIndex = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return lastSlashIndex >= 0 ? path.slice(lastSlashIndex + 1) : path
}

export function taskMatchesSearch(task: Task, query: string): boolean {
  return taskFilename(task).toLowerCase().includes(query)
}

export function taskAutoSyncPart(value: unknown): string {
  if (value === undefined || value === null) return ''
  return String(value).replace(/[|\n\r]/g, ' ')
}

export function appendDownloadGroupTaskSignatureEntries(
  entries: string[],
  bucket: 'active' | 'waiting' | 'stopped',
  tasks: Task[],
) {
  for (const task of tasks) {
    const groupId = getTaskDownloadGroupId(task)
    if (!groupId) continue
    entries.push(
      [
        bucket,
        taskAutoSyncPart(task.gid),
        taskAutoSyncPart(groupId),
        taskAutoSyncPart(task.status),
        taskAutoSyncPart(task.completedLength),
        taskAutoSyncPart(task.totalLength),
        taskAutoSyncPart(task.downloadSpeed),
        taskAutoSyncPart(task.errorCode),
        taskAutoSyncPart(task.download_group?.name_status),
        taskAutoSyncPart(task.download_group?.name),
        taskAutoSyncPart(task.download_group?.folder_name),
      ].join('|'),
    )
  }
}

export function buildDownloadGroupTaskAutoSyncSignature(
  activeTasks: Task[] = [],
  waitingTasks: Task[] = [],
  stoppedTasks: Task[] = [],
): string {
  const entries: string[] = []
  appendDownloadGroupTaskSignatureEntries(entries, 'active', activeTasks)
  appendDownloadGroupTaskSignatureEntries(entries, 'waiting', waitingTasks)
  appendDownloadGroupTaskSignatureEntries(entries, 'stopped', stoppedTasks)
  entries.sort()
  return entries.join('\n')
}

export function buildInlineTaskListEntries({
  tab,
  tasks,
  groupItems,
  searchQuery = '',
  errorOnly = false,
}: BuildInlineTaskListEntriesOptions): InlineTaskListEntry[] {
  const eligibleItems = groupItems.filter(item => isDownloadGroupItemEligibleForTab(item, tab))
  const eligibleByKey = new Map<string, DownloadGroupMasterItem>()
  const knownGroupKeys = new Set<string>()
  for (const item of groupItems) {
    if (item.group_key) knownGroupKeys.add(item.group_key)
  }
  for (const item of eligibleItems) {
    if (!item.group_key || eligibleByKey.has(item.group_key)) continue
    eligibleByKey.set(item.group_key, item)
  }

  const emittedGroups = new Set<string>()
  const entries: InlineTaskListEntry[] = []

  for (const task of tasks) {
    const groupKey = getTaskDownloadGroupId(task)
    const groupItem = groupKey ? eligibleByKey.get(groupKey) : undefined
    if (!groupKey || !knownGroupKeys.has(groupKey)) {
      entries.push({ type: 'task', key: `task:${task.gid}`, task })
      continue
    }

    if (groupItem && !emittedGroups.has(groupKey)) {
      entries.push({
        type: 'group',
        key: `group:${groupKey}`,
        group_key: groupKey,
        item: groupItem,
      })
      emittedGroups.add(groupKey)
    }
  }

  for (const item of eligibleItems) {
    if (emittedGroups.has(item.group_key)) continue
    entries.push({ type: 'group', key: `group:${item.group_key}`, group_key: item.group_key, item })
    emittedGroups.add(item.group_key)
  }

  const query = searchQuery.trim().toLowerCase()
  const applyErrorOnly = errorOnly && tab === 'stopped'
  if (!query && !applyErrorOnly) return entries

  // Error filter: groups with error members (aligned with search's group-member match pattern)
  const errorGroupKeys = new Set<string>()
  if (applyErrorOnly) {
    for (const item of eligibleItems) {
      if (item.type === 'backend' && item.card.counts.error > 0) {
        errorGroupKeys.add(item.group_key)
      }
    }
  }

  // Search filter: groups whose display name or any member matches the query
  const stoppedMemberSearchMatchByGroupKey = new Set<string>()
  if (query) {
    for (const task of tasks) {
      const groupKey = getTaskDownloadGroupId(task)
      if (!groupKey || !eligibleByKey.has(groupKey)) continue
      if (taskMatchesSearch(task, query)) stoppedMemberSearchMatchByGroupKey.add(groupKey)
    }
  }

  return entries.filter(entry => {
    if (entry.type === 'task') {
      if (applyErrorOnly && entry.task.status !== 'error') return false
      if (query && !taskMatchesSearch(entry.task, query)) return false
      return true
    }
    // Group entry: AND of error filter and search filter
    if (applyErrorOnly && !errorGroupKeys.has(entry.group_key)) return false
    if (
      query &&
      !getDownloadGroupMasterItemSearchText(entry.item).includes(query) &&
      !stoppedMemberSearchMatchByGroupKey.has(entry.group_key)
    )
      return false
    return true
  })
}

export function cloneDownloadGroup(group: DownloadGroup): DownloadGroup {
  return {
    id: cleanKey(group.id),
    kind: typeof group.kind === 'string' ? group.kind : '',
    name: typeof group.name === 'string' ? group.name : '',
    name_status: typeof group.name_status === 'string' ? group.name_status : undefined,
    folder_name: typeof group.folder_name === 'string' ? group.folder_name : '',
    dir: typeof group.dir === 'string' ? group.dir : '',
    item_count:
      typeof group.item_count === 'number' && Number.isFinite(group.item_count)
        ? group.item_count
        : 0,
    created_at:
      typeof group.created_at === 'number' && Number.isFinite(group.created_at)
        ? group.created_at
        : 0,
  } as DownloadGroup
}

export function downloadGroupEqual(a?: DownloadGroup | null, b?: DownloadGroup | null): boolean {
  if (!a && !b) return true
  if (!a || !b) return false
  return (
    a.id === b.id &&
    a.kind === b.kind &&
    a.name === b.name &&
    (a.name_status ?? '') === (b.name_status ?? '') &&
    a.folder_name === b.folder_name &&
    a.dir === b.dir &&
    a.item_count === b.item_count &&
    a.created_at === b.created_at
  )
}

export function warningsEqual(a?: DownloadGroupWarning[], b?: DownloadGroupWarning[]): boolean {
  const left = a ?? []
  const right = b ?? []
  if (left.length !== right.length) return false
  return left.every((warning, index) => {
    const other = right[index]
    return (
      other &&
      warning.code === other.code &&
      warning.severity === other.severity &&
      warning.count === other.count
    )
  })
}

export function countsEqual(a?: DownloadGroupMemberCounts, b?: DownloadGroupMemberCounts): boolean {
  if (!a && !b) return true
  if (!a || !b) return false
  return (
    a.expected === b.expected &&
    a.resolved === b.resolved &&
    a.missing === b.missing &&
    a.active === b.active &&
    a.waiting === b.waiting &&
    a.paused === b.paused &&
    a.complete === b.complete &&
    a.error === b.error &&
    a.history_only === b.history_only
  )
}

export function cardsEqual(a: DownloadGroupCard, b: DownloadGroupCard): boolean {
  return (
    a.group_key === b.group_key &&
    downloadGroupEqual(a.download_group, b.download_group) &&
    a.kind === b.kind &&
    a.display_name === b.display_name &&
    a.fallback_name === b.fallback_name &&
    a.name_status === b.name_status &&
    a.status === b.status &&
    a.degraded === b.degraded &&
    warningsEqual(a.warnings, b.warnings) &&
    countsEqual(a.counts, b.counts) &&
    a.total_length === b.total_length &&
    a.completed_length === b.completed_length &&
    a.download_speed === b.download_speed &&
    a.progress === b.progress &&
    a.created_at === b.created_at &&
    a.updated_at === b.updated_at &&
    (a.folder_label ?? '') === (b.folder_label ?? '') &&
    (a.folder_path_hint ?? '') === (b.folder_path_hint ?? '') &&
    a.has_folder === b.has_folder
  )
}

export function createEmptyDetailEnvelope(groupKey: string): DownloadGroupDetailEnvelope {
  return {
    group_key: groupKey,
    found: false,
    group: {
      group_key: groupKey,
      kind: '',
      display_name: '',
      fallback_name: '',
      name_status: 'degraded',
      status: 'unknown',
      degraded: true,
      warnings: [{ code: 'group_not_found', severity: 'warning' }],
      counts: {
        expected: 0,
        resolved: 0,
        missing: 0,
        active: 0,
        waiting: 0,
        paused: 0,
        complete: 0,
        error: 0,
        history_only: 0,
      },
      total_length: '0',
      completed_length: '0',
      download_speed: '0',
      progress: 0,
      created_at: 0,
      updated_at: Date.now(),
      has_folder: false,
    },
    tasks: {
      active: [],
      waiting: [],
      stopped: [],
    },
    updated_at: Date.now(),
    degraded: true,
    warnings: [{ code: 'group_not_found', severity: 'warning' }],
  } as DownloadGroupDetailEnvelope
}

export function normalizeRefreshHint(
  refresh?: DownloadGroupOperationRefreshHint | null,
): DownloadGroupOperationRefreshHint {
  return {
    tasks: refresh?.tasks === true,
    groups: refresh?.groups === true,
    detail: refresh?.detail === true,
    reason: typeof refresh?.reason === 'string' ? refresh.reason : undefined,
  }
}

export function getOperationResultCodes(result: DownloadGroupOperationResult): Set<string> {
  const codes = new Set<string>()
  for (const warning of result.warnings ?? []) {
    const code = cleanKey(warning?.code)
    if (code && OPERATION_CODE_SET.has(code)) codes.add(code)
  }
  for (const item of result.items ?? []) {
    if (item?.status !== 'failed' && item?.status !== 'skipped') continue
    const code = cleanKey(item.code)
    if (code && OPERATION_CODE_SET.has(code)) codes.add(code)
  }
  return codes
}

export function primaryOperationCode(result: DownloadGroupOperationResult): string {
  const codes = getOperationResultCodes(result)
  if (codes.has('partial_failure')) return 'partial_failure'
  if (codes.has('rpc_error')) return 'rpc_error'
  if (codes.has('group_not_found')) return 'group_not_found'
  if (codes.has('no_actionable_members')) return 'no_actionable_members'
  if (codes.has('history_only')) return 'history_only'
  const firstOperationItemCode = result.items?.find(item =>
    OPERATION_CODE_SET.has(cleanKey(item?.code)),
  )?.code
  return firstOperationItemCode || result.action || 'unknown'
}

export function operationNoticeSeverity(
  result: DownloadGroupOperationResult,
): DownloadGroupOperationNoticeSeverity {
  const codes = getOperationResultCodes(result)
  const action = cleanKey(result.action)
  const attempted = Math.max(result.succeeded + result.failed, result.total_targets)

  if (result.failed > 0 && result.succeeded > 0) {
    return 'warning'
  }

  if (
    result.failed > 0 ||
    codes.has('rpc_error') ||
    codes.has('folder_unavailable') ||
    codes.has('folder_unsafe') ||
    codes.has('open_failed') ||
    (action === 'open_folder' && !result.ok)
  ) {
    return attempted > 0 && result.succeeded === 0 ? 'error' : 'warning'
  }

  if (
    codes.has('partial_failure') ||
    codes.has('missing_member') ||
    codes.has('stale_member') ||
    codes.has('group_not_found')
  ) {
    return 'warning'
  }

  if (
    result.noop ||
    codes.has('no_actionable_members') ||
    codes.has('history_only') ||
    codes.has('empty_group')
  ) {
    return 'info'
  }

  if (result.ok && result.failed === 0) return 'success'
  return 'info'
}

export function createRejectedOperationResult(
  action: DownloadGroupOperationAction,
  groupKey: string,
): DownloadGroupOperationResult {
  return {
    group_key: groupKey,
    action,
    ok: false,
    found: false,
    noop: false,
    total_targets: 1,
    succeeded: 0,
    skipped: 0,
    failed: 1,
    items: [
      {
        status: 'failed',
        code: 'rpc_error',
      } as DownloadGroupOperationItemResult,
    ],
    warnings: [{ code: 'rpc_error', severity: 'error', count: 1 }],
    refresh: { tasks: true, groups: true, detail: true, reason: 'rpc_error' },
    updated_at: Date.now(),
  } as DownloadGroupOperationResult
}
