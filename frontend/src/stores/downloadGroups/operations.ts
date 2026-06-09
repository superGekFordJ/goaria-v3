import {
  OpenDownloadGroupFolder,
  PauseDownloadGroup,
  RemoveDownloadGroup,
  ResumeDownloadGroup,
} from '../../../bindings/goaria-v3/app.js'
import type { DownloadGroupOperationResult } from '../../../bindings/goaria-v3/models'
import { useTaskStore } from '../task'
import type { DownloadGroupActions } from './actions'
import type { DownloadGroupState } from './state'
import type { DownloadGroupSync } from './sync'
import {
  cleanKey,
  normalizeRefreshHint,
  createRejectedOperationResult,
  getTaskDownloadGroupId,
  type DownloadGroupOperationAction,
} from './utils'

export function useDownloadGroupOperations(
  state: DownloadGroupState,
  actions: DownloadGroupActions,
  sync: DownloadGroupSync,
) {
  const { currentDetail, currentDetailKey, detailError } = state

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
      await sync.fetchGroups().catch(() => null)
    }
    if (
      normalizedKey &&
      currentDetailKey.value === normalizedKey &&
      (refresh.detail || result.found === false)
    ) {
      const detailAtRefresh = currentDetail.value
      const detailErrorAtRefresh = detailError.value
      const detail = await sync.fetchGroupDetail(normalizedKey).catch(() => null)
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

    actions.setOperationBusy(action, normalizedKey, true)
    try {
      let result: DownloadGroupOperationResult
      try {
        result = await operation(normalizedKey)
      } catch {
        result = createRejectedOperationResult(action, normalizedKey)
      }
      result.refresh = normalizeRefreshHint(result.refresh)
      actions.recordOperationNotice(action, normalizedKey, result)
      if (action === 'remove') {
        actions.clearCurrentDetailForGroup(normalizedKey)
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
      actions.setOperationBusy(action, normalizedKey, false)
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

  return {
    pauseGroup,
    resumeGroup,
    removeGroup,
    openGroupFolder,
  }
}

export type DownloadGroupOperations = ReturnType<typeof useDownloadGroupOperations>
