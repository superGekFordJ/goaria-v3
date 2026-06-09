import type { DownloadGroup } from '../../../bindings/goaria-v3/internal/rpc/models'
import type { DownloadGroupOperationResult } from '../../../bindings/goaria-v3/internal/downloadgroups/models'
import type { DownloadGroupState } from './state'
import {
  cleanKey,
  cloneDownloadGroup,
  operationNoticeSeverity,
  primaryOperationCode,
  DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS,
  type DownloadGroupOperationAction,
  type DownloadGroupPlaceholderSource,
} from './utils'

export function useDownloadGroupActions(state: DownloadGroupState) {
  const {
    backendGroupKeys,
    operationInFlight,
    operationNotice,
    lastOperationResult,
    placeholders,
    currentDetail,
    currentDetailKey,
    detailError,
    isDetailLoading,
  } = state

  let operationNoticeSeq = 0

  // Callbacks to sync behavior to avoid circular dependencies
  let _pruneExpiredPlaceholders: (() => void) | null = null
  let _schedulePlaceholderPrune: (now: number) => void = () => {}
  let _schedulePendingNameRefetch: (() => void) | null = null
  let _onClearDetail: (() => void) | null = null

  function setSyncCallbacks(callbacks: {
    pruneExpiredPlaceholders: () => void
    schedulePlaceholderPrune: (now: number) => void
    schedulePendingNameRefetch: () => void
    onClearDetail: () => void
  }) {
    _pruneExpiredPlaceholders = callbacks.pruneExpiredPlaceholders
    _schedulePlaceholderPrune = callbacks.schedulePlaceholderPrune
    _schedulePendingNameRefetch = callbacks.schedulePendingNameRefetch
    _onClearDetail = callbacks.onClearDetail
  }

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

  function clearCurrentDetailForGroup(groupKey: string) {
    const normalizedKey = cleanKey(groupKey)
    if (!normalizedKey || currentDetailKey.value !== normalizedKey) return
    currentDetailKey.value = null
    currentDetail.value = null
    detailError.value = null
    isDetailLoading.value = false
    if (_onClearDetail) {
      _onClearDetail()
    }
  }

  function addPlaceholdersFromDownloadGroups(
    groups?: DownloadGroup[] | null,
    source: DownloadGroupPlaceholderSource = 'batch-add',
  ) {
    if (!groups?.length) {
      if (_pruneExpiredPlaceholders) _pruneExpiredPlaceholders()
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
    _schedulePlaceholderPrune(now)
    if (_schedulePendingNameRefetch) {
      _schedulePendingNameRefetch()
    }
  }

  return {
    setSyncCallbacks,
    hasBackendCard,
    operationKey,
    setOperationBusy,
    isGroupOperationBusy,
    clearOperationNotice,
    recordOperationNotice,
    clearCurrentDetailForGroup,
    addPlaceholdersFromDownloadGroups,
  }
}

export type DownloadGroupActions = ReturnType<typeof useDownloadGroupActions>
