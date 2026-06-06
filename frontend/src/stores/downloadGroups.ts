import { defineStore } from 'pinia'
import { computed, ref, watch, type WatchStopHandle } from 'vue'
import {
  GetDownloadGroupDetail,
  GetDownloadGroups,
  OpenDownloadGroupFolder,
  PauseDownloadGroup,
  RemoveDownloadGroup,
  ResumeDownloadGroup,
} from '../../bindings/goaria-v3/app.js'
import type {
  DownloadGroupCard,
  DownloadGroupDetailEnvelope,
  DownloadGroupListEnvelope,
  DownloadGroupMemberCounts,
  DownloadGroupOperationItemResult,
  DownloadGroupOperationRefreshHint,
  DownloadGroupOperationResult,
  DownloadGroupWarning,
} from '../../bindings/goaria-v3/models'
import type { DownloadGroup, Task } from '../../bindings/goaria-v3/internal/rpc/models'
import { useTaskStore } from './task'

export const DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS = 15_000
export const DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS = 300
export const DOWNLOAD_GROUP_PENDING_NAME_REFETCH_MS = 1500
export const DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES = 5
const PLACEHOLDER_TIMER_FLOOR_MS = 1

export type DownloadGroupPlaceholderSource = 'batch-add'

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
  | DownloadGroupBackendMasterItem
  | DownloadGroupPlaceholderMasterItem

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

function normalizeWarningSeverity(value: unknown, fallback: DownloadGroupWarningSeverity) {
  return value === 'info' || value === 'warning' || value === 'error' ? value : fallback
}

function warningSummaryForCode(
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

function cleanKey(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function getTaskDownloadGroupId(task?: Task | null): string {
  return cleanKey(task?.download_group?.id)
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

function getDownloadGroupMasterItemSearchText(item: DownloadGroupMasterItem): string {
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

function taskFilename(task: Task): string {
  const path = task.files?.[0]?.path || task.title || task.gid
  const lastSlashIndex = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
  return lastSlashIndex >= 0 ? path.slice(lastSlashIndex + 1) : path
}

function taskMatchesSearch(task: Task, query: string): boolean {
  return taskFilename(task).toLowerCase().includes(query)
}

function taskAutoSyncPart(value: unknown): string {
  if (value === undefined || value === null) return ''
  return String(value).replace(/[|\n\r]/g, ' ')
}

function appendDownloadGroupTaskSignatureEntries(
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
  if (tab !== 'stopped' || !query) return entries

  const stoppedMemberSearchMatchByGroupKey = new Set<string>()
  for (const task of tasks) {
    const groupKey = getTaskDownloadGroupId(task)
    if (!groupKey || !eligibleByKey.has(groupKey)) continue
    if (taskMatchesSearch(task, query)) stoppedMemberSearchMatchByGroupKey.add(groupKey)
  }

  return entries.filter(entry => {
    if (entry.type === 'task') return taskMatchesSearch(entry.task, query)
    return (
      getDownloadGroupMasterItemSearchText(entry.item).includes(query) ||
      stoppedMemberSearchMatchByGroupKey.has(entry.group_key)
    )
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

function downloadGroupEqual(a?: DownloadGroup | null, b?: DownloadGroup | null): boolean {
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

function warningsEqual(a?: DownloadGroupWarning[], b?: DownloadGroupWarning[]): boolean {
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

function countsEqual(a?: DownloadGroupMemberCounts, b?: DownloadGroupMemberCounts): boolean {
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

function cardsEqual(a: DownloadGroupCard, b: DownloadGroupCard): boolean {
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

function createEmptyDetailEnvelope(groupKey: string): DownloadGroupDetailEnvelope {
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

function normalizeRefreshHint(
  refresh?: DownloadGroupOperationRefreshHint | null,
): DownloadGroupOperationRefreshHint {
  return {
    tasks: refresh?.tasks === true,
    groups: refresh?.groups === true,
    detail: refresh?.detail === true,
    reason: typeof refresh?.reason === 'string' ? refresh.reason : undefined,
  }
}

function getOperationResultCodes(result: DownloadGroupOperationResult): Set<string> {
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

function primaryOperationCode(result: DownloadGroupOperationResult): string {
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

function operationNoticeSeverity(
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

function createRejectedOperationResult(
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

export const useDownloadGroupStore = defineStore('downloadGroups', () => {
  const backendCards = ref<DownloadGroupCard[]>([])
  const updatedAt = ref(0)
  const degraded = ref(false)
  const warnings = ref<DownloadGroupWarning[]>([])
  const isLoading = ref(false)
  const error = ref<string | null>(null)

  const currentDetail = ref<DownloadGroupDetailEnvelope | null>(null)
  const currentDetailKey = ref<string | null>(null)
  const isDetailLoading = ref(false)
  const detailError = ref<string | null>(null)

  const placeholders = ref<DownloadGroupPlaceholder[]>([])
  const operationInFlight = ref<Set<string>>(new Set())
  const operationNotice = ref<DownloadGroupOperationNotice | null>(null)
  const lastOperationResult = ref<DownloadGroupOperationResult | null>(null)
  const isAutoSyncActive = ref(false)
  let groupsRequestSeq = 0
  let visibleGroupsLoadingSeq = 0
  let detailRequestSeq = 0
  let detailVisibleLoadingSeq = 0
  let autoSyncStop: WatchStopHandle | null = null
  let autoSyncTimer: ReturnType<typeof setTimeout> | null = null
  let pendingNameTimer: ReturnType<typeof setTimeout> | null = null
  const pendingNameAttemptsByGroupKey = new Map<string, number>()
  let placeholderPruneTimer: ReturnType<typeof setTimeout> | null = null
  let operationNoticeSeq = 0

  const backendGroupKeys = computed(() => new Set(backendCards.value.map(card => card.group_key)))

  const unreconciledPlaceholders = computed(() =>
    placeholders.value.filter(placeholder => !backendGroupKeys.value.has(placeholder.group_key)),
  )

  const masterItems = computed<DownloadGroupMasterItem[]>(() => {
    const placeholderItems: DownloadGroupPlaceholderMasterItem[] = [
      ...unreconciledPlaceholders.value,
    ]
      .sort((a, b) => b.created_at - a.created_at)
      .map(placeholder => ({
        type: 'placeholder',
        group_key: placeholder.group_key,
        placeholder,
      }))

    const backendItems: DownloadGroupBackendMasterItem[] = backendCards.value.map(card => ({
      type: 'backend',
      group_key: card.group_key,
      card,
    }))

    return [...placeholderItems, ...backendItems]
  })

  const downloadInlineGroupItems = computed(() =>
    masterItems.value.filter(item => isDownloadGroupItemEligibleForTab(item, 'downloads')),
  )

  const completedInlineGroupItems = computed(() =>
    masterItems.value.filter(item => isDownloadGroupItemEligibleForTab(item, 'stopped')),
  )

  const visibleGroupCount = computed(
    () => backendCards.value.length + unreconciledPlaceholders.value.length,
  )

  const inlineDownloadsCount = computed(() => {
    const taskStore = useTaskStore()
    return buildInlineTaskListEntries({
      tab: 'downloads',
      tasks: taskStore.activeTasks.concat(taskStore.waitingTasks),
      groupItems: masterItems.value,
    }).length
  })

  const inlineCompletedCount = computed(() => {
    const taskStore = useTaskStore()
    return buildInlineTaskListEntries({
      tab: 'stopped',
      tasks: taskStore.stoppedTasks,
      groupItems: masterItems.value,
    }).length
  })

  function hasBackendCard(groupKey: string): boolean {
    return backendGroupKeys.value.has(groupKey)
  }

  function operationKey(action: DownloadGroupOperationAction, groupKey: string): string {
    return `${action}:${groupKey}`
  }

  function setOperationBusy(action: DownloadGroupOperationAction, groupKey: string, busy: boolean) {
    const next = new Set(operationInFlight.value)
    const key = operationKey(action, groupKey)
    if (busy) {
      next.add(key)
    } else {
      next.delete(key)
    }
    operationInFlight.value = next
  }

  function isGroupOperationBusy(groupKey: string, action?: DownloadGroupOperationAction): boolean {
    const normalizedKey = cleanKey(groupKey)
    if (!normalizedKey) return false
    if (action) return operationInFlight.value.has(operationKey(action, normalizedKey))
    for (const key of operationInFlight.value) {
      if (key.endsWith(`:${normalizedKey}`)) return true
    }
    return false
  }

  function clearOperationNotice() {
    operationNotice.value = null
  }

  function recordOperationNotice(
    action: DownloadGroupOperationAction,
    groupKey: string,
    result: DownloadGroupOperationResult,
  ) {
    lastOperationResult.value = result
    operationNotice.value = {
      id: ++operationNoticeSeq,
      group_key: groupKey,
      action,
      severity: operationNoticeSeverity(result),
      code: primaryOperationCode(result),
      succeeded: result.succeeded ?? 0,
      skipped: result.skipped ?? 0,
      failed: result.failed ?? 0,
      noop: result.noop === true,
      updated_at: result.updated_at || Date.now(),
      result,
    }
  }

  function clearPlaceholderPruneTimer() {
    if (!placeholderPruneTimer) return
    clearTimeout(placeholderPruneTimer)
    placeholderPruneTimer = null
  }

  function schedulePlaceholderPrune(now = Date.now()) {
    clearPlaceholderPruneTimer()

    if (placeholders.value.length === 0) return

    const nextExpiry = placeholders.value.reduce(
      (earliest, placeholder) => Math.min(earliest, placeholder.expires_at),
      Number.POSITIVE_INFINITY,
    )
    if (!Number.isFinite(nextExpiry)) return

    const delay = Math.max(
      nextExpiry - now + PLACEHOLDER_TIMER_FLOOR_MS,
      PLACEHOLDER_TIMER_FLOOR_MS,
    )
    placeholderPruneTimer = setTimeout(() => {
      placeholderPruneTimer = null
      pruneExpiredPlaceholders()
    }, delay)
  }

  function pruneExpiredPlaceholders(now = Date.now()) {
    placeholders.value = placeholders.value.filter(placeholder => placeholder.expires_at > now)
    schedulePlaceholderPrune(now)
    schedulePendingNameRefetch()
  }

  function reconcilePlaceholders() {
    const keys = backendGroupKeys.value
    placeholders.value = placeholders.value.filter(placeholder => !keys.has(placeholder.group_key))
  }

  function mergeBackendCards(incoming: DownloadGroupCard[]): DownloadGroupCard[] {
    const existingByKey = new Map(backendCards.value.map(card => [card.group_key, card]))
    const seen = new Set<string>()
    const merged: DownloadGroupCard[] = []

    for (const card of incoming) {
      const groupKey = cleanKey(card.group_key)
      if (!groupKey || seen.has(groupKey)) continue
      seen.add(groupKey)

      const normalizedCard =
        groupKey === card.group_key ? card : ({ ...card, group_key: groupKey } as DownloadGroupCard)
      const existing = existingByKey.get(groupKey)
      merged.push(existing && cardsEqual(existing, normalizedCard) ? existing : normalizedCard)
    }

    return merged
  }

  function hasPendingNameWarning(warningsToCheck?: DownloadGroupWarning[] | null): boolean {
    return (warningsToCheck ?? []).some(warning => cleanKey(warning?.code) === 'name_pending')
  }

  function pendingNameGroupKeyFromCard(card: DownloadGroupCard): string {
    return cleanKey(card.group_key || card.download_group?.id)
  }

  function pendingNameGroupKeyFromPlaceholder(placeholder: DownloadGroupPlaceholder): string {
    return cleanKey(placeholder.group_key || placeholder.download_group.id)
  }

  function hasPendingNameInCard(card: DownloadGroupCard): boolean {
    return (
      card.name_status === 'pending' ||
      card.download_group?.name_status === 'pending' ||
      hasPendingNameWarning(card.warnings)
    )
  }

  function hasPendingNameInDetail(detail?: DownloadGroupDetailEnvelope | null): boolean {
    return Boolean(
      detail &&
      (detail.group?.name_status === 'pending' ||
        detail.group?.download_group?.name_status === 'pending' ||
        hasPendingNameWarning(detail.group?.warnings) ||
        hasPendingNameWarning(detail.warnings)),
    )
  }

  function collectPendingNameGroupKeys(): Set<string> {
    const pendingKeys = new Set<string>()

    for (const card of backendCards.value) {
      if (!hasPendingNameInCard(card)) continue
      const groupKey = pendingNameGroupKeyFromCard(card)
      if (groupKey) pendingKeys.add(groupKey)
    }

    for (const placeholder of unreconciledPlaceholders.value) {
      if (placeholder.download_group.name_status !== 'pending') continue
      const groupKey = pendingNameGroupKeyFromPlaceholder(placeholder)
      if (groupKey) pendingKeys.add(groupKey)
    }

    if (hasPendingNameInDetail(currentDetail.value)) {
      const groupKey = cleanKey(currentDetail.value?.group_key || currentDetailKey.value)
      if (groupKey) pendingKeys.add(groupKey)
    }

    return pendingKeys
  }

  function clearPendingNameTimer() {
    if (!pendingNameTimer) return
    clearTimeout(pendingNameTimer)
    pendingNameTimer = null
  }

  function clearPendingNameRefetchState() {
    clearPendingNameTimer()
    pendingNameAttemptsByGroupKey.clear()
  }

  function resetSettledPendingNameAttempts(pendingKeys: Set<string>) {
    for (const groupKey of Array.from(pendingNameAttemptsByGroupKey.keys())) {
      if (!pendingKeys.has(groupKey)) pendingNameAttemptsByGroupKey.delete(groupKey)
    }
  }

  function schedulePendingNameRefetch() {
    if (!isAutoSyncActive.value) return

    const pendingKeys = collectPendingNameGroupKeys()
    resetSettledPendingNameAttempts(pendingKeys)

    if (pendingKeys.size === 0) {
      clearPendingNameTimer()
      return
    }

    const eligibleKeys = Array.from(pendingKeys).filter(
      groupKey =>
        (pendingNameAttemptsByGroupKey.get(groupKey) ?? 0) <
        DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES,
    )
    if (eligibleKeys.length === 0 || pendingNameTimer) return

    pendingNameTimer = setTimeout(() => {
      pendingNameTimer = null
      if (!isAutoSyncActive.value) return

      const nextPendingKeys = collectPendingNameGroupKeys()
      const runnableKeys = Array.from(nextPendingKeys).filter(
        groupKey =>
          (pendingNameAttemptsByGroupKey.get(groupKey) ?? 0) <
          DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES,
      )
      if (runnableKeys.length === 0) {
        resetSettledPendingNameAttempts(nextPendingKeys)
        return
      }

      for (const groupKey of runnableKeys) {
        pendingNameAttemptsByGroupKey.set(
          groupKey,
          (pendingNameAttemptsByGroupKey.get(groupKey) ?? 0) + 1,
        )
      }

      void runAutoSync('pending-name')
    }, DOWNLOAD_GROUP_PENDING_NAME_REFETCH_MS)
  }

  function clearAutoSyncTimer() {
    if (!autoSyncTimer) return
    clearTimeout(autoSyncTimer)
    autoSyncTimer = null
  }

  function scheduleAutoSync(reason = 'task-signature') {
    if (!isAutoSyncActive.value) return
    clearAutoSyncTimer()
    autoSyncTimer = setTimeout(() => {
      autoSyncTimer = null
      void runAutoSync(reason)
    }, DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS)
  }

  async function runAutoSync(reason = 'task-signature') {
    if (!isAutoSyncActive.value) return

    await fetchGroups({ silent: true, reason })
    if (!isAutoSyncActive.value) return

    const detailKey = cleanKey(currentDetailKey.value)
    if (!detailKey) return
    if (currentDetailKey.value !== detailKey) return

    await fetchGroupDetail(detailKey, { silent: true, reason })
    if (currentDetailKey.value !== detailKey) return
  }

  async function fetchGroups(
    options: DownloadGroupFetchOptions = {},
  ): Promise<DownloadGroupListEnvelope | null> {
    const silent = options.silent === true
    const requestSeq = ++groupsRequestSeq
    const visibleSeq = silent ? 0 : ++visibleGroupsLoadingSeq

    pruneExpiredPlaceholders()
    if (!silent) {
      isLoading.value = true
      error.value = null
    }

    try {
      const envelope = await GetDownloadGroups()
      if (requestSeq !== groupsRequestSeq) return null

      backendCards.value = mergeBackendCards(envelope.groups ?? [])
      updatedAt.value = envelope.updated_at ?? 0
      degraded.value = envelope.degraded ?? false
      warnings.value = envelope.warnings ?? []
      error.value = null
      reconcilePlaceholders()
      pruneExpiredPlaceholders()
      schedulePendingNameRefetch()
      return envelope
    } catch (err) {
      if (requestSeq !== groupsRequestSeq) return null
      if (!silent) {
        error.value = err instanceof Error ? err.message : String(err)
      }
      pruneExpiredPlaceholders()
      return null
    } finally {
      if (!silent && visibleSeq === visibleGroupsLoadingSeq) {
        isLoading.value = false
      }
    }
  }

  async function fetchGroupDetail(
    groupKey: string,
    options: DownloadGroupFetchOptions = {},
  ): Promise<DownloadGroupDetailEnvelope | null> {
    const normalizedKey = cleanKey(groupKey)
    const silent = options.silent === true
    const requestSeq = ++detailRequestSeq
    const visibleSeq = silent ? 0 : ++detailVisibleLoadingSeq

    if (!silent || normalizedKey) {
      currentDetailKey.value = normalizedKey || null
    }
    if (!silent) {
      detailError.value = null
    }

    if (!normalizedKey) {
      if (!silent) {
        currentDetail.value = createEmptyDetailEnvelope(normalizedKey)
        isDetailLoading.value = false
      }
      return currentDetail.value
    }

    if (!silent && currentDetail.value?.group_key !== normalizedKey) {
      currentDetail.value = null
    }

    if (!silent) {
      isDetailLoading.value = true
    }
    try {
      const envelope = await GetDownloadGroupDetail(normalizedKey)
      if (requestSeq !== detailRequestSeq || currentDetailKey.value !== normalizedKey) {
        return null
      }
      currentDetail.value = envelope
      currentDetailKey.value = normalizedKey
      detailError.value = null
      schedulePendingNameRefetch()
      return envelope
    } catch (err) {
      if (requestSeq !== detailRequestSeq || currentDetailKey.value !== normalizedKey) {
        return null
      }
      if (!silent) {
        detailError.value = err instanceof Error ? err.message : String(err)
        currentDetailKey.value = normalizedKey
        currentDetail.value = createEmptyDetailEnvelope(normalizedKey)
      }
      return null
    } finally {
      if (
        !silent &&
        visibleSeq === detailVisibleLoadingSeq &&
        currentDetailKey.value === normalizedKey
      ) {
        isDetailLoading.value = false
      }
    }
  }

  async function syncAfterSnapshot(selectedGroupKey?: string | null) {
    await fetchGroups()
    const groupKey = cleanKey(selectedGroupKey)
    if (groupKey) {
      await fetchGroupDetail(groupKey)
    }
  }

  function startAutoSync() {
    if (autoSyncStop) return
    const taskStore = useTaskStore()
    isAutoSyncActive.value = true
    autoSyncStop = watch(
      () =>
        buildDownloadGroupTaskAutoSyncSignature(
          taskStore.activeTasks,
          taskStore.waitingTasks,
          taskStore.stoppedTasks,
        ),
      (_signature, previousSignature) => {
        if (previousSignature === undefined) return
        scheduleAutoSync('task-signature')
      },
      { flush: 'post' },
    )
    schedulePendingNameRefetch()
  }

  function stopAutoSync() {
    isAutoSyncActive.value = false
    if (autoSyncStop) {
      autoSyncStop()
      autoSyncStop = null
    }
    clearAutoSyncTimer()
    clearPendingNameRefetchState()
  }

  function clearCurrentDetailForGroup(groupKey: string) {
    const normalizedKey = cleanKey(groupKey)
    if (!normalizedKey || currentDetailKey.value !== normalizedKey) return
    currentDetailKey.value = null
    currentDetail.value = null
    detailError.value = null
    detailRequestSeq++
    isDetailLoading.value = false
  }

  async function applyOperationRefreshHints(
    groupKey: string,
    result: DownloadGroupOperationResult,
  ) {
    const refresh = normalizeRefreshHint(result.refresh)
    const taskStore = useTaskStore()
    const normalizedKey = cleanKey(groupKey)

    if (refresh.tasks) {
      await taskStore.fetchTasks().catch(() => undefined)
    }
    if (refresh.groups) {
      await fetchGroups().catch(() => null)
    }
    if (
      normalizedKey &&
      currentDetailKey.value === normalizedKey &&
      (refresh.detail || result.found === false)
    ) {
      const detailAtRefresh = currentDetail.value
      const detailErrorAtRefresh = detailError.value
      const detail = await fetchGroupDetail(normalizedKey).catch(() => null)
      if (
        !detail &&
        currentDetailKey.value === normalizedKey &&
        detailAtRefresh?.group_key === normalizedKey
      ) {
        currentDetail.value = detailAtRefresh
        currentDetailKey.value = normalizedKey
        detailError.value = detailErrorAtRefresh
      }
    }
  }

  async function runGroupOperation(
    action: DownloadGroupOperationAction,
    groupKey: string,
    operation: (normalizedKey: string) => Promise<DownloadGroupOperationResult>,
  ): Promise<DownloadGroupOperationResult | null> {
    const normalizedKey = cleanKey(groupKey)
    if (!normalizedKey) return null

    setOperationBusy(action, normalizedKey, true)
    try {
      let result: DownloadGroupOperationResult
      try {
        result = await operation(normalizedKey)
      } catch {
        result = createRejectedOperationResult(action, normalizedKey)
      }
      result.refresh = normalizeRefreshHint(result.refresh)
      recordOperationNotice(action, normalizedKey, result)
      if (action === 'remove') {
        clearCurrentDetailForGroup(normalizedKey)
        const taskStore = useTaskStore()
        taskStore.clearSelectedGroup(normalizedKey)

        const gidsToRemove = [
          ...taskStore.activeTasks,
          ...taskStore.waitingTasks,
          ...taskStore.stoppedTasks,
        ]
          .filter(t => getTaskDownloadGroupId(t) === normalizedKey)
          .map(t => t.gid)

        let changed = false
        for (const gid of gidsToRemove) {
          if (taskStore.selectedGids.has(gid)) {
            taskStore.selectedGids.delete(gid)
            changed = true
          }
        }
        if (changed) {
          taskStore.selectedGids = new Set(taskStore.selectedGids)
        }
      }
      await applyOperationRefreshHints(normalizedKey, result)
      return result
    } finally {
      setOperationBusy(action, normalizedKey, false)
    }
  }

  function pauseGroup(groupKey: string) {
    return runGroupOperation('pause', groupKey, normalizedKey => PauseDownloadGroup(normalizedKey))
  }

  function resumeGroup(groupKey: string) {
    return runGroupOperation('resume', groupKey, normalizedKey =>
      ResumeDownloadGroup(normalizedKey),
    )
  }

  function removeGroup(groupKey: string, deleteFiles: boolean) {
    return runGroupOperation('remove', groupKey, normalizedKey =>
      RemoveDownloadGroup(normalizedKey, deleteFiles),
    )
  }

  function openGroupFolder(groupKey: string) {
    return runGroupOperation('open_folder', groupKey, normalizedKey =>
      OpenDownloadGroupFolder(normalizedKey),
    )
  }

  function addPlaceholdersFromDownloadGroups(
    groups?: DownloadGroup[] | null,
    source: DownloadGroupPlaceholderSource = 'batch-add',
  ) {
    if (!groups?.length) {
      pruneExpiredPlaceholders()
      return
    }

    const now = Date.now()
    const byKey = new Map(
      placeholders.value.map(placeholder => [placeholder.group_key, placeholder]),
    )

    for (const group of groups) {
      const groupKey = cleanKey(group?.id)
      if (!groupKey || hasBackendCard(groupKey)) continue

      byKey.set(groupKey, {
        group_key: groupKey,
        download_group: cloneDownloadGroup({ ...group, id: groupKey } as DownloadGroup),
        created_at: now,
        expires_at: now + DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS,
        source,
      })
    }

    placeholders.value = Array.from(byKey.values()).filter(
      placeholder => placeholder.expires_at > now,
    )
    schedulePlaceholderPrune(now)
    schedulePendingNameRefetch()
  }

  function $dispose() {
    stopAutoSync()
    clearPlaceholderPruneTimer()
  }

  return {
    backendCards,
    updatedAt,
    degraded,
    warnings,
    isLoading,
    error,
    currentDetail,
    currentDetailKey,
    isDetailLoading,
    detailError,
    placeholders,
    operationInFlight,
    operationNotice,
    lastOperationResult,
    isAutoSyncActive,
    masterItems,
    downloadInlineGroupItems,
    completedInlineGroupItems,
    visibleGroupCount,
    inlineDownloadsCount,
    inlineCompletedCount,
    fetchGroups,
    fetchGroupDetail,
    syncAfterSnapshot,
    startAutoSync,
    stopAutoSync,
    addPlaceholdersFromDownloadGroups,
    pruneExpiredPlaceholders,
    clearCurrentDetailForGroup,
    clearOperationNotice,
    isGroupOperationBusy,
    pauseGroup,
    resumeGroup,
    removeGroup,
    openGroupFolder,
    $dispose,
  }
})
