import { computed, ref } from 'vue'
import type {
  DownloadGroupCard,
  DownloadGroupDetailEnvelope,
  DownloadGroupOperationResult,
  DownloadGroupWarning,
} from '../../../bindings/goaria-v3/models'
import { useTaskStore } from '../task'
import {
  isDownloadGroupItemEligibleForTab,
  buildInlineTaskListEntries,
  type DownloadGroupPlaceholder,
  type DownloadGroupOperationNotice,
  type DownloadGroupMasterItem,
  type DownloadGroupPlaceholderMasterItem,
  type DownloadGroupBackendMasterItem,
} from './utils'

export function useDownloadGroupState() {
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

  const backendGroupKeys = computed(() => {
    const keys = new Set<string>()
    for (const card of backendCards.value) {
      keys.add(card.group_key)
    }
    return keys
  })

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
    backendGroupKeys,
    unreconciledPlaceholders,
    masterItems,
    downloadInlineGroupItems,
    completedInlineGroupItems,
    visibleGroupCount,
    inlineDownloadsCount,
    inlineCompletedCount,
  }
}

export type DownloadGroupState = ReturnType<typeof useDownloadGroupState>
