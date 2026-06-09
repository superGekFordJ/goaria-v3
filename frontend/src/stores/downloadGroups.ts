import { defineStore } from 'pinia'
import { useDownloadGroupState } from './downloadGroups/state'
import { useDownloadGroupActions } from './downloadGroups/actions'
import { useDownloadGroupSync } from './downloadGroups/sync'
import { useDownloadGroupOperations } from './downloadGroups/operations'

export {
  DOWNLOAD_GROUP_PLACEHOLDER_TTL_MS,
  DOWNLOAD_GROUP_AUTO_SYNC_DEBOUNCE_MS,
  DOWNLOAD_GROUP_PENDING_NAME_REFETCH_MS,
  DOWNLOAD_GROUP_PENDING_NAME_MAX_REFETCHES,
  READ_MODEL_WARNING_CODES,
  OPERATION_WARNING_CODES,
  normalizeDownloadGroupWarningSummaries,
  getTaskDownloadGroupId,
  isTerminalDownloadGroupCard,
  isDownloadGroupItemEligibleForTab,
  getDownloadGroupMasterItemDisplayName,
  buildDownloadGroupTaskAutoSyncSignature,
  buildInlineTaskListEntries,
  cloneDownloadGroup,
} from './downloadGroups/utils'

export type {
  DownloadGroupPlaceholderSource,
  DownloadGroupOperationAction,
  DownloadGroupOperationNoticeSeverity,
  DownloadGroupOperationNotice,
  DownloadGroupWarningSeverity,
  DownloadGroupWarningSummary,
  DownloadGroupPlaceholder,
  DownloadGroupBackendMasterItem,
  DownloadGroupPlaceholderMasterItem,
  DownloadGroupMasterItem,
  InlineTaskListTab,
  InlineTaskListEntry,
  BuildInlineTaskListEntriesOptions,
  DownloadGroupFetchOptions,
} from './downloadGroups/utils'

export const useDownloadGroupStore = defineStore('downloadGroups', () => {
  // 1. Initialize State
  const state = useDownloadGroupState()

  // 2. Initialize Actions (simple manipulation / state writes)
  const actions = useDownloadGroupActions(state)

  // 3. Initialize Sync logic (fetching, auto-sync, timers)
  const sync = useDownloadGroupSync(state, actions)

  // 4. Initialize Operations (Wails IPC callers)
  const operations = useDownloadGroupOperations(state, actions, sync)

  // 5. Store Cleanup Function
  function $dispose() {
    sync.stopAutoSync()
    sync.clearPlaceholderPruneTimer()
  }

  // 6. Return unified API surface matching the original store exactly
  return {
    // State
    backendCards: state.backendCards,
    updatedAt: state.updatedAt,
    degraded: state.degraded,
    warnings: state.warnings,
    isLoading: state.isLoading,
    error: state.error,
    currentDetail: state.currentDetail,
    currentDetailKey: state.currentDetailKey,
    isDetailLoading: state.isDetailLoading,
    detailError: state.detailError,
    placeholders: state.placeholders,
    operationInFlight: state.operationInFlight,
    operationNotice: state.operationNotice,
    lastOperationResult: state.lastOperationResult,
    isAutoSyncActive: state.isAutoSyncActive,

    // Getters
    masterItems: state.masterItems,
    downloadInlineGroupItems: state.downloadInlineGroupItems,
    completedInlineGroupItems: state.completedInlineGroupItems,
    visibleGroupCount: state.visibleGroupCount,
    inlineDownloadsCount: state.inlineDownloadsCount,
    inlineCompletedCount: state.inlineCompletedCount,

    // Sync Actions
    fetchGroups: sync.fetchGroups,
    fetchGroupDetail: sync.fetchGroupDetail,
    syncAfterSnapshot: sync.syncAfterSnapshot,
    startAutoSync: sync.startAutoSync,
    stopAutoSync: sync.stopAutoSync,
    pruneExpiredPlaceholders: sync.pruneExpiredPlaceholders,

    // Actions
    addPlaceholdersFromDownloadGroups: actions.addPlaceholdersFromDownloadGroups,
    clearCurrentDetailForGroup: actions.clearCurrentDetailForGroup,
    clearOperationNotice: actions.clearOperationNotice,
    isGroupOperationBusy: actions.isGroupOperationBusy,

    // Operations (IPC wrappers)
    pauseGroup: operations.pauseGroup,
    resumeGroup: operations.resumeGroup,
    removeGroup: operations.removeGroup,
    openGroupFolder: operations.openGroupFolder,

    // Store disposal hook
    $dispose,
  }
})
