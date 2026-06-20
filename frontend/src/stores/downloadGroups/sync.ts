import { watch, type WatchStopHandle } from 'vue'
import { GetDownloadGroups, GetDownloadGroupDetail } from '../../../bindings/goaria-v3/app.js'
import type {
  DownloadGroupCard,
  DownloadGroupDetailEnvelope,
  DownloadGroupListEnvelope,
  DownloadGroupWarning,
} from '../../../bindings/goaria-v3/internal/downloadgroups/models'
import { useTaskStore } from '../task'
import type { DownloadGroupActions } from './actions'
import type { DownloadGroupState } from './state'
import {
  cleanKey,
  cardsEqual,
  createEmptyDetailEnvelope,
  buildDownloadGroupTaskAutoSyncSignature,
  DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES,
  DOWNLOAD_GROUP_PENDING_NAME_REFETCH_MS,
  DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS,
  PLACEHOLDER_TIMER_FLOOR_MS,
  type DownloadGroupFetchOptions,
  type DownloadGroupPlaceholder,
} from './utils'

export function useDownloadGroupSync(state: DownloadGroupState, actions: DownloadGroupActions) {
  const {
    isLoading,
    error,
    currentDetail,
    currentDetailKey,
    isDetailLoading,
    detailError,
    placeholders,
    isAutoSyncActive,
    backendCards,
    updatedAt,
    degraded,
    warnings,
    backendGroupKeys,
    unreconciledPlaceholders,
  } = state

  let groupsRequestSeq = 0
  let visibleGroupsLoadingSeq = 0
  let detailRequestSeq = 0
  let detailVisibleLoadingSeq = 0
  let autoSyncStop: WatchStopHandle | null = null
  let autoSyncTimer: ReturnType<typeof setTimeout> | null = null
  let pendingNameTimer: ReturnType<typeof setTimeout> | null = null
  const pendingNameAttemptsByGroupKey = new Map<string, number>()
  let placeholderPruneTimer: ReturnType<typeof setTimeout> | null = null

  // Setup callbacks in actions to point here
  actions.setSyncCallbacks({
    pruneExpiredPlaceholders,
    schedulePlaceholderPrune,
    schedulePendingNameRefetch,
    onClearDetail: () => {
      detailRequestSeq++
    },
  })

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
    if (autoSyncTimer) return
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

  return {
    clearPlaceholderPruneTimer,
    schedulePlaceholderPrune,
    pruneExpiredPlaceholders,
    schedulePendingNameRefetch,
    clearPendingNameTimer,
    clearPendingNameRefetchState,
    fetchGroups,
    fetchGroupDetail,
    syncAfterSnapshot,
    startAutoSync,
    stopAutoSync,
  }
}

export type DownloadGroupSync = ReturnType<typeof useDownloadGroupSync>
