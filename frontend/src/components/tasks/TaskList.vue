<script setup lang="ts">
  import {
    ref,
    computed,
    onMounted,
    onUnmounted,
    watch,
    onActivated,
    onDeactivated,
    nextTick,
  } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { RecycleScroller } from 'vue-virtual-scroller'
  import { useTaskStore } from '../../stores/task'
  import { useUIStore } from '../../stores/ui'
  import TaskCard from './TaskCard.vue'
  import TaskHeader from './TaskHeader.vue'
  import TaskSearch from './TaskSearch.vue'
  import ErrorFilterTag from './ErrorFilterTag.vue'
  import BatchActionBar from './BatchActionBar.vue'
  import DownloadGroupCard from '../groups/DownloadGroupCard.vue'
  import DownloadGroupOperationNotice from '../groups/DownloadGroupOperationNotice.vue'
  import DownloadGroupRemoveDialog from '../groups/DownloadGroupRemoveDialog.vue'
  import { CheckCircle2, SearchX, Layers3, Download, CheckCircle } from '@lucide/vue'
  import { useTaskKeyboard } from '../../composables/useTaskKeyboard'
  import { useFLIPAnimation } from '../../composables/useFLIPAnimation'
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

  // Clear selection when searching to prevent operating on hidden items
  watch(searchQuery, () => {
    taskStore.clearSelection()
  })

  // Error Filter State
  const errorFilterActive = ref(false)

  const errorCount = computed(() => taskStore.stoppedTasks.filter(t => t.status === 'error').length)

  const isErrorFilterActive = computed(
    () => !isGroupDetailMode.value && uiStore.activeTab === 'stopped' && errorFilterActive.value,
  )

  // Error filter toggle — additive (AND) with search query, no dismissal
  const toggleErrorFilter = () => {
    errorFilterActive.value = !errorFilterActive.value
    taskStore.clearSelection()
  }

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
    let rawEntries: InlineTaskListEntry[]
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
        errorOnly: errorFilterActive.value,
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
        icon: Download,
        title: t('taskList.noDownloads'),
        description: t('taskList.pasteLink'),
        accent: 'var(--neon-primary)',
      }
    }
    // Error filter active but no results (e.g. combined with search, or errors resolved)
    if (isErrorFilterActive.value && displayEntries.value.length === 0) {
      return {
        icon: CheckCircle,
        title: t('taskList.noErrors'),
        description: t('taskList.allErrorsResolved'),
        accent: 'var(--status-complete)',
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

  const taskContainer = ref<HTMLElement | null>(null)
  const { capture, play, clear } = useFLIPAnimation(taskContainer)

  // One-shot guard consumed by the post-watcher below. Every guard only ever
  // raises it, never lowers it, so guards firing in the same tick cannot cancel
  // each other and the behaviour does not depend on watcher registration order.
  const skipNextFlip = ref(false)

  // While <KeepAlive> holds this panel deactivated the subtree is detached:
  // every rect measures 0, so capture()/play() would only pollute lastRects and
  // force layout for content nobody can see.
  const isPanelActive = ref(true)

  function sameDisplayIdentity(
    next: DisplayEntry[] | undefined,
    prev: DisplayEntry[] | undefined,
  ): boolean {
    if (!next || !prev || next.length !== prev.length) return false
    for (let i = 0; i < next.length; i++) {
      if (next[i].key !== prev[i].key || next[i].size !== prev[i].size) return false
    }
    return true
  }

  // Downloads-only: same key+size sequence is a new array, not a reorder.
  // A second capture/play on that identity replays Invert on every yielder
  // (viewport-bottom looks like a late drop; the enterer clips above the port).
  // Stopped tab is excluded so error-tag header growth still FLIPs.
  function isDownloadsIdentitySkip(
    next: DisplayEntry[] | undefined,
    prev: DisplayEntry[] | undefined,
  ): boolean {
    return (
      !isGroupDetailMode.value &&
      uiStore.activeTab === 'downloads' &&
      sameDisplayIdentity(next, prev)
    )
  }

  // Pre-watcher captures First rects before Vue patches DOM
  watch(
    displayEntries,
    (newList, oldList) => {
      if (!isPanelActive.value) return
      if (isDownloadsIdentitySkip(newList, oldList)) return
      capture(oldList?.map(entry => entry.key))
    },
    { flush: 'pre' },
  )

  // Tag appear/disappear changes sticky header height, shifting card positions.
  // Appearing is safe for FLIP: the tag is in flow while it fades in, so play()
  // measures the grown header and the cards glide down by the exact delta.
  // Disappearing also deactivates the error filter, so the full stopped list
  // replaces the error-only one in the same tick — and the destination branch
  // decides whether FLIP can cope:
  // 1. Destination is the plain <div> (e.g. 15 error → 15 full): every
  //    non-error row was filtered out during capture(), so FLIP treats the
  //    whole list as "entering" (oldTop = newTop - height) and it snaps up then
  //    bounces back. Skip FLIP; the tag leave animation carries the change.
  // 2. Destination is <RecycleScroller> (e.g. 15 error → 100 full): only
  //    viewport+buffer rows exist, so the same entering logic slides a handful
  //    of rows down into place, which reads as a natural settle. Let FLIP play;
  //    the boundary watcher below still vetoes it when the user is scrolled.
  watch(
    () => errorCount.value > 0,
    hasErrors => {
      if (hasErrors) return
      errorFilterActive.value = false
      // Reading useVirtualList after the mutation re-evaluates the computed
      // chain, so this reflects the destination list, not the error-only one.
      if (!useVirtualList.value) {
        skipNextFlip.value = true
      }
    },
  )

  // Boundary crossing: when the list grows past 15 or shrinks below 16, the
  // DOM switches between <div> and <RecycleScroller>. FLIP can animate this
  // transition IF the user is at the top (scrollTop ≈ 0) — both branches render
  // the same leading rows there, so First/Last comparison stays valid.
  // But if the user has scrolled down in RecycleScroller, off-screen items
  // were recycled (not in DOM), so capture() can't record them. When crossing
  // to <div> (scrollTop resets to 0, all items render), FLIP assigns huge
  // deltaY to ex-viewport items and treats ex-off-screen items as entering —
  // causing visual tearing. So only allow cross-boundary FLIP at the top;
  // otherwise skip it (hard cut, no animation).
  // Watchers are pre-flush, so this still measures the outgoing scroller.
  watch(useVirtualList, () => {
    const scroller = taskContainer.value?.querySelector<HTMLElement>('[data-task-scroll-root]')
    if (scroller && scroller.scrollTop > 4) {
      skipNextFlip.value = true
    }
  })

  // Post-watcher executes FLIP Play phase after Vue updates DOM
  watch(
    displayEntries,
    (newList, oldList) => {
      // The guard flag is one-shot: consume it before any early return, or a
      // cycle that had nothing to animate would leak it into the next one.
      const skip = skipNextFlip.value
      skipNextFlip.value = false
      if (skip || !isPanelActive.value) return
      if (!oldList || oldList.length === 0) return
      // Reduced effects: snap to new positions instantly, no FLIP transition.
      if (uiStore.effectsTier === 'reduced') return
      if (isDownloadsIdentitySkip(newList, oldList)) return
      nextTick(() => {
        play()
      })
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
      } else {
        // Leaving stopped tab: clear error filter state
        errorFilterActive.value = false
      }
    },
  )

  // Auto-clear error filter when no errors remain (e.g. all errors deleted)
  watch(
    () => displayEntries.value.length,
    len => {
      if (errorFilterActive.value && len === 0 && errorCount.value === 0) {
        errorFilterActive.value = false
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
    isPanelActive.value = true
    // KeepAlive restore: the subtree was detached, so the scroller's scrollTop
    // was reset and RecycleScroller needs a frame or two to recompute its
    // transform layout. Anything captured before that settles is stale, and
    // play() would turn it into huge deltaY values — cards fly in from nowhere.
    // Drop the stale rects and skip the first cycle after activation; the list
    // just needs to appear correctly, not animate.
    clear()
    skipNextFlip.value = true
  })

  onDeactivated(() => {
    isPanelActive.value = false
    deactivateKeydown()
    clearGroupDetailSelection()
    // Reset error filter when leaving the page — prevents ghost empty state on return
    errorFilterActive.value = false
  })

  onUnmounted(() => {
    deactivateKeydown()
    clearGroupDetailSelection()
    errorFilterActive.value = false
  })
</script>

<template>
  <div class="flex-1 flex flex-col min-h-0">
    <!-- Task Addition Header (only in downloads tab) -->
    <TaskHeader v-if="!isGroupDetailMode && uiStore.activeTab === 'downloads'" />

    <!-- Search Box (only in stopped tab) — outside scroll container -->
    <TaskSearch
      v-if="!isGroupDetailMode && uiStore.activeTab === 'stopped'"
      v-model="searchQuery"
    />

    <!-- Task List Container -->
    <div ref="taskContainer" class="flex-1 min-h-0 relative">
      <!-- Empty State -->
      <TaskListEmptyState :show="displayEntries.length === 0" :config="emptyStateConfig" />

      <!-- Empty State Filter Tag (Absolute Overlay to keep it visible when list is empty) -->
      <div
        v-if="displayEntries.length === 0"
        class="absolute top-0 left-0 w-full px-5 z-20 pointer-events-none"
      >
        <!-- Padding-free positioning context: the leaving tag goes position:absolute,
             which anchors to the containing block's padding box — without this the
             tag would jump out of the px-5 inset when it starts to fade. -->
        <div class="relative">
          <Transition name="filter-chips-fade">
            <div
              v-if="!isGroupDetailMode && uiStore.activeTab === 'stopped' && errorCount > 0"
              class="filter-chips-row"
            >
              <ErrorFilterTag
                :error-count="errorCount"
                :active="errorFilterActive"
                @toggle="toggleErrorFilter"
              />
            </div>
          </Transition>
        </div>
      </div>

      <!-- Non-virtual path -->
      <div
        v-if="displayEntries.length > 0 && !useVirtualList"
        :key="isGroupDetailMode ? `group-detail:${props.detailKey}` : uiStore.activeTab"
        data-task-scroll-root
        class="h-full overflow-y-auto px-5 pb-4"
        :class="{ 'pt-4': isGroupDetailMode || uiStore.activeTab !== 'stopped' }"
      >
        <!-- Sticky header area -->
        <div class="sticky top-0 z-40 pointer-events-none">
          <!-- Sticky filter chips row (stopped tab only) -->
          <Transition name="filter-chips-fade">
            <div
              v-if="!isGroupDetailMode && uiStore.activeTab === 'stopped' && errorCount > 0"
              class="filter-chips-row"
            >
              <ErrorFilterTag
                :error-count="errorCount"
                :active="errorFilterActive"
                @toggle="toggleErrorFilter"
              />
            </div>
          </Transition>
          <!-- Permanent gap above first card (transparent, cards scroll behind it visibly) -->
          <div v-if="!isGroupDetailMode && uiStore.activeTab === 'stopped'" class="h-4"></div>
        </div>

        <div class="flex flex-col gap-4">
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
        </div>
      </div>

      <!-- Virtual path -->
      <RecycleScroller
        v-else-if="displayEntries.length > 0"
        data-task-scroll-root
        class="h-full px-5 pb-4"
        :class="{ 'pt-4': isGroupDetailMode || uiStore.activeTab !== 'stopped' }"
        :items="displayEntries"
        :item-size="null"
        size-field="size"
        key-field="key"
        :buffer="200"
      >
        <template #before>
          <!-- Sticky filter chips row (stopped tab only) -->
          <Transition name="filter-chips-fade">
            <div
              v-if="!isGroupDetailMode && uiStore.activeTab === 'stopped' && errorCount > 0"
              class="filter-chips-row"
            >
              <ErrorFilterTag
                :error-count="errorCount"
                :active="errorFilterActive"
                @toggle="toggleErrorFilter"
              />
            </div>
          </Transition>
          <!-- Permanent gap for the first card that cards can visibly scroll behind -->
          <div v-if="!isGroupDetailMode && uiStore.activeTab === 'stopped'" class="h-4"></div>
        </template>

        <template #default="{ item }">
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
        </template>
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
  /* All extracted styles moved to respective sub-components */

  /* Virtual scroller customization */
  :deep(.vue-recycle-scroller__item-wrapper) {
    overflow: visible !important;
  }

  /* Sticky header for virtual scroller — applied to the slot wrapper div itself */
  :deep(.vue-recycle-scroller > .vue-recycle-scroller__slot:first-child) {
    position: sticky;
    top: 0;
    z-index: 40;
    pointer-events: none; /* Let clicks pass through the transparent gap */
  }

  /* Filter chips row — left-aligned to search content (icon position),
     tight spacing to feel like an extension of the search bar */
  .filter-chips-row {
    display: flex;
    align-items: center;
    gap: 8px;
    justify-content: flex-start;
    padding-left: 32px;
    padding-top: 4px;
    padding-bottom: 6px;
    overflow: hidden;
    pointer-events: auto; /* Re-enable clicks for the tag */
  }

  /* Fade transition for tag — opacity-only to avoid virtual scroller height bugs.
     Enter uses spring overshoot, leave uses accelerate curve.
     Leave uses position:absolute so the tag exits layout flow immediately —
     the sticky header collapses to its final height in the same frame FLIP
     captures newTop, so deltaY accurately reflects the full header collapse.
     Without this, the tag occupies space during its opacity fade, FLIP
     measures a partial deltaY, and the header collapse after fade-end causes
     a secondary hard jump that conflicts with the FLIP transition.
     Leave also adds translateY + scale to match the FLIP cards' upward motion,
     so the tag fades out "in flow" with the cards instead of floating in place. */
  .filter-chips-fade-enter-active {
    transition: opacity 0.3s cubic-bezier(0.34, 1.56, 0.64, 1);
  }

  .filter-chips-fade-leave-active {
    transition:
      opacity 0.3s cubic-bezier(0.2, 0.8, 0.2, 1),
      transform 0.3s cubic-bezier(0.2, 0.8, 0.2, 1);
    position: absolute;
    left: 0;
    right: 0;
  }

  .filter-chips-fade-enter-from {
    opacity: 0;
  }

  .filter-chips-fade-leave-to {
    opacity: 0;
    transform: translateY(-20px) scale(0.95);
  }
</style>
