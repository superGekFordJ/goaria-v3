<script setup lang="ts">
  import { ref, computed, onMounted, onUnmounted, watch, onActivated, onDeactivated, markRaw } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { RecycleScroller } from 'vue-virtual-scroller'
  import { useTaskStore } from '../../stores/task'
  import { useUIStore } from '../../stores/ui'
  import TaskCard from './TaskCard.vue'
  import TaskHeader from './TaskHeader.vue'
  import TaskSearch from './TaskSearch.vue'
  import BatchActionBar from './BatchActionBar.vue'
  import DownloadGroupCard from '../groups/DownloadGroupCard.vue'
  import DownloadGroupOperationNotice from '../groups/DownloadGroupOperationNotice.vue'
  import DownloadGroupRemoveDialog from '../groups/DownloadGroupRemoveDialog.vue'
  import { CheckCircle2, SearchX, Layers3 } from 'lucide-vue-next'
  import FileIcon from '../common/FileIcon.vue'
  import { useTaskKeyboard } from '../../composables/useTaskKeyboard'
  import TaskListEmptyState from './TaskListEmptyState.vue'
  import TaskListDeleteModal from './TaskListDeleteModal.vue'
  import TaskListBatchDeleteModal from './TaskListBatchDeleteModal.vue'
  import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
  import { buildVisibleTaskGroupHints } from '../../stores/task/grouping'
  import {
    buildInlineTaskListEntries,
    getDownloadGroupMasterItemDisplayName,
    type DownloadGroupOperationAction,
    type InlineTaskListEntry,
  } from '../../stores/downloadGroups'
  import { useDownloadGroupStore } from '../../stores/downloadGroups'

  const { t } = useI18n()
  const taskStore = useTaskStore()
  const uiStore = useUIStore()
  const downloadGroupStore = useDownloadGroupStore()

  const props = withDefaults(
    defineProps<{
      mode?: 'tab' | 'group-detail'
      detailTasks?: { active: Task[]; waiting: Task[]; stopped: Task[] }
      detailKey?: string
    }>(),
    {
      mode: 'tab',
      detailTasks: () => ({ active: [], waiting: [], stopped: [] }),
      detailKey: '',
    },
  )

  const isGroupDetailMode = computed(() => props.mode === 'group-detail')

  // Modal State
  const showDelModal = ref(false)
  const delTarget = ref<Task | null>(null)
  const isDeleting = ref(false)

  // Batch Delete Modal State
  const showBatchDelModal = ref(false)
  const isBatchDeleting = ref(false)
  const inlineRemoveTarget = ref<{
    groupKey: string
    displayName: string
  } | null>(null)

  // Search State
  const searchQuery = ref('')

  // Task filtering logic based on active tab
  // 优化：避免每次访问都创建新数组，直接使用 store 的响应式数据
  const combinedDownloads = computed(() => {
    const active = taskStore.activeTasks
    const waiting = taskStore.waitingTasks
    // 仅当有 waiting 任务时才合并，否则直接返回 active 避免创建新数组
    if (waiting.length === 0) return active
    if (active.length === 0) return waiting
    // 保留 task 对象的响应式代理，确保 TaskCard 字段 watcher 持续更新
    return active.concat(waiting)
  })

  const detailDisplayTasks = computed(() => {
    if (isGroupDetailMode.value) {
      return [
        ...(props.detailTasks?.active ?? []),
        ...(props.detailTasks?.waiting ?? []),
        ...(props.detailTasks?.stopped ?? []),
      ]
    }
    return []
  })

  type DisplayEntry = InlineTaskListEntry & { size: number }

  const displayEntries = computed<DisplayEntry[]>(() => {
    let rawEntries: InlineTaskListEntry[] = []
    if (isGroupDetailMode.value) {
      rawEntries = detailDisplayTasks.value.map(task => ({
        type: 'task',
        key: `task:${task.gid}`,
        task,
      }))
    } else if (uiStore.activeTab === 'downloads') {
      rawEntries = buildInlineTaskListEntries({
        tab: 'downloads',
        tasks: combinedDownloads.value,
        groupItems: downloadGroupStore.masterItems,
      })
    } else {
      rawEntries = buildInlineTaskListEntries({
        tab: 'stopped',
        tasks: taskStore.stoppedTasks,
        groupItems: downloadGroupStore.masterItems,
        searchQuery: searchQuery.value,
      })
    }

    return rawEntries.map(entry => {
      let size = 150 // completed / error / unknown tasks size
      if (entry.type === 'group') {
        const isComplete = entry.item.type === 'backend' && entry.item.card.status === 'complete'
        size = isComplete ? 250 : 268 // DownloadGroupCard size
      } else {
        const status = entry.task.status
        if (status === 'active') {
          size = 198 // TaskCard with active progress size (has speed/ETA)
        } else if (status === 'paused' || status === 'waiting') {
          size = 180 // TaskCard with inactive progress size (no speed/ETA)
        }
      }
      return {
        ...entry,
        size,
      }
    })
  })

  const displayTasks = computed(() =>
    displayEntries.value.flatMap(entry => (entry.type === 'task' ? [entry.task] : [])),
  )

  // Check if search returned no results
  const isSearchEmpty = computed(() => {
    return (
      !isGroupDetailMode.value &&
      uiStore.activeTab === 'stopped' &&
      searchQuery.value.trim() !== '' &&
      displayEntries.value.length === 0
    )
  })

  const useVirtualList = computed(() => displayEntries.value.length > 15)
  const groupHintsByGid = computed(() => buildVisibleTaskGroupHints(displayTasks.value))

  // Empty state configuration
  const emptyStateConfig = computed(() => {
    if (isGroupDetailMode.value) {
      return {
        icon: Layers3,
        title: t('downloadGroups.emptyTitle'),
        description: t('downloadGroups.detailNotFoundDescription'),
        accent: 'var(--neon-primary)',
      }
    }

    if (uiStore.activeTab === 'downloads') {
      return {
        icon: markRaw(FileIcon),
        iconProps: { fileName: null, tier: 'chipped', size: 40 },
        title: t('taskList.noDownloads'),
        description: t('taskList.pasteLink'),
        accent: 'var(--neon-primary)',
      }
    }
    // Search empty state
    if (isSearchEmpty.value) {
      return {
        icon: SearchX,
        title: t('taskList.noMatch'),
        description: t('taskList.tryOtherKeywords'),
        accent: 'var(--status-paused)',
      }
    }
    return {
      icon: CheckCircle2,
      title: t('taskList.noCompleted'),
      description: t('taskList.completedHere'),
      accent: 'var(--status-complete)',
    }
  })

  // Delete confirmation handlers
  const confirmDelete = (task: Task) => {
    delTarget.value = task
    showDelModal.value = true
  }

  const cancelDelete = () => {
    showDelModal.value = false
    delTarget.value = null
  }

  const handleDelete = async (deleteLocal: boolean) => {
    if (!delTarget.value || isDeleting.value) return

    isDeleting.value = true
    try {
      // Optimistic update handled in store
      await taskStore.remove(delTarget.value.gid, deleteLocal)
    } finally {
      isDeleting.value = false
      showDelModal.value = false
      delTarget.value = null
    }
  }

  // Extract filename for modal display
  const targetFileName = computed(() => {
    if (!delTarget.value?.files?.[0]?.path) return t('taskList.unknownTask')
    return delTarget.value.files[0].path.split(/[\\/]/).pop() || t('taskList.unknownTask')
  })

  const listContainer = ref<HTMLElement | null>(null)
  // 优化：复用 Map 实例减少 GC 压力
  const prevOrderByGid = new Map<string, number>()

  const scrollToTask = (gid: string, block: 'start' | 'center' | 'end' | 'nearest' = 'center') => {
    const container = listContainer.value
    if (!container) return
    const el = container.querySelector<HTMLElement>(`[data-gid="${gid}"]`)
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block })
  }

  const scrollToEntry = (
    entryKey: string,
    block: 'start' | 'center' | 'end' | 'nearest' = 'center',
  ) => {
    const container = listContainer.value
    if (!container) return
    const el = container.querySelector<HTMLElement>(`[data-entry-key="${entryKey}"]`)
    if (!el) return
    el.scrollIntoView({ behavior: 'smooth', block })
  }

  watch(
    displayEntries,
    (newList, oldList) => {
      if (useVirtualList.value || !oldList) {
        prevOrderByGid.clear()
        newList.forEach((entry, i) => prevOrderByGid.set(entry.key, i))
        return
      }

      // Find any task that moved significantly (index changed by >= 2)
      let movedKey: string | null = null
      let movedDirection: 'up' | 'down' = 'up'

      for (const [key, oldIdx] of prevOrderByGid) {
        const newIdx = newList.findIndex(entry => entry.key === key)
        if (newIdx === -1) continue // task removed
        const delta = newIdx - oldIdx
        if (Math.abs(delta) >= 2) {
          movedKey = key
          movedDirection = delta < 0 ? 'up' : 'down'
          break
        }
      }

      // Update order tracking
      prevOrderByGid.clear()
      newList.forEach((entry, i) => prevOrderByGid.set(entry.key, i))

      if (!movedKey) return

      // Wait for TransitionGroup animation to complete (500ms is the animation duration)
      setTimeout(() => {
        if (movedKey?.startsWith('task:')) {
          scrollToTask(movedKey.slice('task:'.length), movedDirection === 'up' ? 'start' : 'end')
        } else if (movedKey) {
          scrollToEntry(movedKey, movedDirection === 'up' ? 'start' : 'end')
        }
      }, 550)
    },
    { flush: 'post' },
  )

  // Performance: Polling is now managed globally in App.vue to support Sidebar status
  // onMounted / onUnmounted / onActivated logic removed from here

  const clearGroupDetailSelection = () => {
    if (isGroupDetailMode.value) {
      taskStore.clearSelection()
    }
  }

  const clearSelectionForMode = () => {
    taskStore.clearSelection()
  }

  // Keyboard shortcut integration using the new composable
  const { activateKeydown, deactivateKeydown } = useTaskKeyboard({
    shouldHandle: () => {
      return uiStore.activeTab === 'downloads' || uiStore.activeTab === 'stopped'
    },
    onEscape: () => {
      if (showDelModal.value) {
        cancelDelete()
        return
      }
      if (showBatchDelModal.value) {
        cancelBatchDelete()
        return
      }
      taskStore.clearSelection()
    },
    onSelectAll: () => {
      if (showDelModal.value || showBatchDelModal.value) return

      const allGids = displayEntries.value.flatMap(entry =>
        entry.type === 'task' ? [entry.task.gid] : [],
      )
      const allGroupKeys = isGroupDetailMode.value
        ? []
        : displayEntries.value.flatMap(entry =>
            entry.type === 'group' && entry.item.type === 'backend' ? [entry.group_key] : [],
          )
      taskStore.selectAll(allGids, allGroupKeys)
    },
  })

  function openInlineGroupDetail(groupKey: string) {
    uiStore.openDownloadGroupDetail(groupKey)
  }

  function pauseInlineGroup(groupKey: string) {
    void downloadGroupStore.pauseGroup(groupKey)
  }

  function resumeInlineGroup(groupKey: string) {
    void downloadGroupStore.resumeGroup(groupKey)
  }

  function openInlineGroupFolder(groupKey: string) {
    void downloadGroupStore.openGroupFolder(groupKey)
  }

  function inlineActionBusy(
    groupKey: string,
  ): Partial<Record<DownloadGroupOperationAction, boolean>> {
    return {
      pause: downloadGroupStore.isGroupOperationBusy(groupKey, 'pause'),
      resume: downloadGroupStore.isGroupOperationBusy(groupKey, 'resume'),
      remove: downloadGroupStore.isGroupOperationBusy(groupKey, 'remove'),
      open_folder: downloadGroupStore.isGroupOperationBusy(groupKey, 'open_folder'),
    }
  }

  function openInlineRemoveDialog(groupKey: string) {
    const item = downloadGroupStore.masterItems.find(entry => entry.group_key === groupKey)
    inlineRemoveTarget.value = {
      groupKey,
      displayName: item ? getDownloadGroupMasterItemDisplayName(item) : groupKey,
    }
  }

  async function confirmInlineRemoveGroup(deleteFiles: boolean) {
    const target = inlineRemoveTarget.value
    if (!target) return
    await downloadGroupStore.removeGroup(target.groupKey, deleteFiles)
    inlineRemoveTarget.value = null
  }

  // Batch delete handlers
  const confirmBatchDelete = () => {
    showBatchDelModal.value = true
  }

  const cancelBatchDelete = () => {
    showBatchDelModal.value = false
  }

  const handleBatchDelete = async (deleteLocal: boolean) => {
    if (isBatchDeleting.value) return
    const selectedTaskGids = [...taskStore.getSelectedGids]
    const selectedGroupKeys = [...(taskStore.getSelectedGroupKeys ?? [])]
    isBatchDeleting.value = true
    try {
      if (selectedTaskGids.length > 0) {
        await taskStore.batchRemove(selectedTaskGids, deleteLocal)
      }
      for (const groupKey of selectedGroupKeys) {
        await downloadGroupStore.removeGroup(groupKey, deleteLocal)
      }
      taskStore.clearSelection()
    } finally {
      isBatchDeleting.value = false
      showBatchDelModal.value = false
    }
  }

  // Clear selection when tab changes, and trigger stopped fetch when switching to stopped tab
  watch(
    () => uiStore.activeTab,
    newTab => {
      if (isGroupDetailMode.value) {
        if (newTab !== 'downloads' && newTab !== 'stopped') {
          taskStore.clearSelection()
          deactivateKeydown()
        } else {
          activateKeydown()
        }
        return
      }

      if (newTab !== 'downloads' && newTab !== 'stopped') {
        taskStore.clearSelection()
        deactivateKeydown()
        return
      }

      activateKeydown()

      taskStore.clearSelection()

      // 切换到"已完成"时立即拉取 stopped 任务
      if (newTab === 'stopped') {
        taskStore.fetchStoppedTasks()
      }
    },
  )

  watch(
    () => [props.mode, props.detailKey] as const,
    () => {
      clearSelectionForMode()
    },
    { immediate: true },
  )

  onMounted(() => {
    clearSelectionForMode()
    activateKeydown()
  })

  onActivated(() => {
    clearSelectionForMode()
    activateKeydown()
  })

  onDeactivated(() => {
    deactivateKeydown()
    clearGroupDetailSelection()
  })

  onUnmounted(() => {
    deactivateKeydown()
    clearGroupDetailSelection()
  })
</script>

<template>
  <div class="flex-1 flex flex-col min-h-0">
    <!-- Task Addition Header (only in downloads tab) -->
    <TaskHeader v-if="!isGroupDetailMode && uiStore.activeTab === 'downloads'" />

    <!-- Search Box (only in stopped tab) -->
    <TaskSearch
      v-if="!isGroupDetailMode && uiStore.activeTab === 'stopped'"
      v-model="searchQuery"
    />

    <!-- Task List Container -->
    <div class="flex-1 min-h-0 relative">
      <!-- Empty State -->
      <TaskListEmptyState :show="displayEntries.length === 0" :config="emptyStateConfig" />

      <!-- Virtual Scrolling Task List -->
      <div
        v-if="displayEntries.length > 0 && !useVirtualList"
        ref="listContainer"
        :key="isGroupDetailMode ? `group-detail:${props.detailKey}` : uiStore.activeTab"
        class="h-full overflow-y-auto px-5 py-4"
      >
        <TransitionGroup name="task-list" tag="div" class="flex flex-col gap-4">
          <div
            v-for="(entry, index) in displayEntries"
            :key="entry.key"
            :data-gid="entry.type === 'task' ? entry.task.gid : undefined"
            :data-entry-key="entry.key"
            class="animate-spring-in"
            :style="{ animationDelay: `${Math.min(index * 50, 300)}ms` }"
          >
            <TaskCard
              v-if="entry.type === 'task'"
              :task="entry.task"
              :group-hint="groupHintsByGid.get(entry.task.gid)"
              @confirm-delete="confirmDelete"
            />
            <DownloadGroupCard
              v-else
              :item="entry.item"
              :operation-busy="inlineActionBusy(entry.group_key)"
              @open="openInlineGroupDetail"
              @pause="pauseInlineGroup"
              @resume="resumeInlineGroup"
              @open-folder="openInlineGroupFolder"
              @remove="openInlineRemoveDialog"
            />
          </div>
        </TransitionGroup>
      </div>

      <RecycleScroller
        v-else-if="displayEntries.length > 0"
        v-slot="{ item }"
        class="h-full px-5 py-4"
        :items="displayEntries"
        :item-size="null"
        size-field="size"
        key-field="key"
        :buffer="200"
      >
        <div class="task-list-virtual-row" :data-entry-key="item.key">
          <TaskCard
            v-if="item.type === 'task'"
            :task="item.task"
            :group-hint="groupHintsByGid.get(item.task.gid)"
            @confirm-delete="confirmDelete"
          />
          <DownloadGroupCard
            v-else
            :item="item.item"
            :operation-busy="inlineActionBusy(item.group_key)"
            @open="openInlineGroupDetail"
            @pause="pauseInlineGroup"
            @resume="resumeInlineGroup"
            @open-folder="openInlineGroupFolder"
            @remove="openInlineRemoveDialog"
          />
        </div>
      </RecycleScroller>
    </div>

    <DownloadGroupOperationNotice
      v-if="!isGroupDetailMode && downloadGroupStore.operationNotice"
      class="mx-5 mb-3"
      :notice="downloadGroupStore.operationNotice"
      @dismiss="downloadGroupStore.clearOperationNotice()"
    />

    <!-- Batch Action Bar -->
    <BatchActionBar @confirm-batch-delete="confirmBatchDelete" />

    <DownloadGroupRemoveDialog
      :open="Boolean(inlineRemoveTarget)"
      :group-key="inlineRemoveTarget?.groupKey || ''"
      :display-name="inlineRemoveTarget?.displayName || ''"
      :busy="
        inlineRemoveTarget
          ? downloadGroupStore.isGroupOperationBusy(inlineRemoveTarget.groupKey, 'remove')
          : false
      "
      @cancel="inlineRemoveTarget = null"
      @confirm="confirmInlineRemoveGroup"
    />

    <!-- Delete Confirmation Modal -->
    <TaskListDeleteModal
      v-model:show="showDelModal"
      :target-file-name="targetFileName"
      :is-deleting="isDeleting"
      @cancel="cancelDelete"
      @confirm="handleDelete"
    />

    <!-- Batch Delete Confirmation Modal -->
    <TaskListBatchDeleteModal
      v-model:show="showBatchDelModal"
      :selected-count="taskStore.selectedCount"
      :is-batch-deleting="isBatchDeleting"
      @cancel="cancelBatchDelete"
      @confirm="handleBatchDelete"
    />
  </div>
</template>

<style scoped>
  /* List transition animations */
  .task-list-move {
    transition: all 0.5s cubic-bezier(0.34, 1.56, 0.64, 1);
  }

  .task-list-enter-active {
    animation: spring-in 0.5s cubic-bezier(0.34, 1.56, 0.64, 1) both;
  }

  .task-list-leave-active {
    animation: spring-out 0.3s ease-out both;
    position: absolute;
    width: calc(100% - 40px);
  }

  /* All extracted styles moved to respective sub-components */

  /* Virtual scroller customization */
  :deep(.vue-recycle-scroller__item-wrapper) {
    overflow: visible !important;
  }
</style>
