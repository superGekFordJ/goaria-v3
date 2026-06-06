<script setup lang="ts">
  import { ref, computed, onMounted, onUnmounted, watch, onActivated, onDeactivated } from 'vue'
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
  import {
    Trash2,
    HardDrive,
    Download,
    CheckCircle2,
    AlertCircle,
    SearchX,
    Layers3,
  } from 'lucide-vue-next'
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
  const deleteLocalFile = ref(false)
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

  const displayEntries = computed<InlineTaskListEntry[]>(() => {
    if (isGroupDetailMode.value) {
      return detailDisplayTasks.value.map(task => ({
        type: 'task',
        key: `task:${task.gid}`,
        task,
      }))
    }

    if (uiStore.activeTab === 'downloads') {
      return buildInlineTaskListEntries({
        tab: 'downloads',
        tasks: combinedDownloads.value,
        groupItems: downloadGroupStore.masterItems,
      })
    }

    return buildInlineTaskListEntries({
      tab: 'stopped',
      tasks: taskStore.stoppedTasks,
      groupItems: downloadGroupStore.masterItems,
      searchQuery: searchQuery.value,
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
    deleteLocalFile.value = false
    showDelModal.value = true
  }

  const cancelDelete = () => {
    showDelModal.value = false
    delTarget.value = null
    deleteLocalFile.value = false
  }

  const handleDelete = async () => {
    if (!delTarget.value || isDeleting.value) return

    isDeleting.value = true
    try {
      // Optimistic update handled in store
      await taskStore.remove(delTarget.value.gid, deleteLocalFile.value)
    } finally {
      isDeleting.value = false
      showDelModal.value = false
      delTarget.value = null
      deleteLocalFile.value = false
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

  const isEditableTarget = (target: EventTarget | null) => {
    let el = target as HTMLElement | null
    while (el) {
      const tag = (el.tagName || '').toLowerCase()
      if (tag === 'input' || tag === 'textarea' || el.isContentEditable) return true
      el = el.parentElement
    }
    return false
  }

  // Keyboard shortcuts handler
  const isKeydownActive = ref(false)

  const shouldHandleKeydown = computed(() => {
    if (isGroupDetailMode.value) {
      return uiStore.activeTab === 'downloads' || uiStore.activeTab === 'stopped'
    }
    return uiStore.activeTab === 'downloads' || uiStore.activeTab === 'stopped'
  })

  const activateKeydown = () => {
    if (isKeydownActive.value || !shouldHandleKeydown.value) return
    window.addEventListener('keydown', handleKeydown)
    isKeydownActive.value = true
  }

  const deactivateKeydown = () => {
    if (!isKeydownActive.value) return
    window.removeEventListener('keydown', handleKeydown)
    isKeydownActive.value = false
  }

  const clearGroupDetailSelection = () => {
    if (isGroupDetailMode.value) {
      taskStore.clearSelection()
    }
  }

  const clearSelectionForMode = () => {
    taskStore.clearSelection()
  }

  const handleKeydown = (e: KeyboardEvent) => {
    if (!shouldHandleKeydown.value) return

    // Escape: Close modal or clear selection
    if (e.key === 'Escape') {
      if (showDelModal.value) {
        cancelDelete()
        return
      }
      if (showBatchDelModal.value) {
        cancelBatchDelete()
        return
      }
      // Clear selection when no modal is open
      taskStore.clearSelection()
      return
    }

    // Ctrl+A / Cmd+A: Select all visible tasks
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') {
      // If user is typing in an input/search box, keep native select-all behavior
      if (isEditableTarget(e.target) || isEditableTarget(document.activeElement)) return
      // Do not trigger task-level shortcuts when modals are open
      if (showDelModal.value || showBatchDelModal.value) return

      e.preventDefault()
      const allGids = displayEntries.value.flatMap(entry =>
        entry.type === 'task' ? [entry.task.gid] : [],
      )
      const allGroupKeys = isGroupDetailMode.value
        ? []
        : displayEntries.value.flatMap(entry =>
            entry.type === 'group' && entry.item.type === 'backend' ? [entry.group_key] : [],
          )
      taskStore.selectAll(allGids, allGroupKeys)
    }
  }

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
    deleteLocalFile.value = false
    showBatchDelModal.value = true
  }

  const cancelBatchDelete = () => {
    showBatchDelModal.value = false
    deleteLocalFile.value = false
  }

  const handleBatchDelete = async () => {
    if (isBatchDeleting.value) return
    const selectedTaskGids = [...taskStore.getSelectedGids]
    const selectedGroupKeys = [...(taskStore.getSelectedGroupKeys ?? [])]
    isBatchDeleting.value = true
    try {
      if (selectedTaskGids.length > 0) {
        await taskStore.batchRemove(selectedTaskGids, deleteLocalFile.value)
      }
      for (const groupKey of selectedGroupKeys) {
        await downloadGroupStore.removeGroup(groupKey, deleteLocalFile.value)
      }
      taskStore.clearSelection()
    } finally {
      isBatchDeleting.value = false
      showBatchDelModal.value = false
      deleteLocalFile.value = false
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
      <Transition name="fade">
        <div
          v-if="displayEntries.length === 0"
          class="absolute inset-0 flex flex-col items-center justify-center p-8"
        >
          <div class="empty-state animate-fade-in-up">
            <!-- Animated Icon Container -->
            <div
              class="w-24 h-24 rounded-[var(--radius-squircle-xl)] flex items-center justify-center mb-6 relative overflow-hidden"
              :style="{ background: `color-mix(in srgb, ${emptyStateConfig.accent} 3%, transparent)` }"
            >
              <!-- Subtle glow effect -->
              <div
                class="absolute inset-0 opacity-30"
                :style="{
                  background: `radial-gradient(circle at center, color-mix(in srgb, ${emptyStateConfig.accent} 12%, transparent) 0%, transparent 70%)`,
                }"
              ></div>
              <component
                :is="emptyStateConfig.icon"
                :size="40"
                :style="{ color: `color-mix(in srgb, ${emptyStateConfig.accent} 38%, transparent)` }"
                class="animate-float"
              />
            </div>

            <h3 class="text-lg font-bold text-[var(--app-text-muted)] mb-2">
              {{ emptyStateConfig.title }}
            </h3>
            <p class="text-sm text-[var(--app-text-subtle)]">
              {{ emptyStateConfig.description }}
            </p>
          </div>
        </div>
      </Transition>

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
        :item-size="208"
        key-field="key"
        :buffer="200"
      >
        <div class="py-2 task-list-virtual-row" :data-entry-key="item.key">
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
    <Teleport to="body">
      <Transition name="modal">
        <div
          v-if="showDelModal"
          class="fixed inset-0 z-[100] flex items-center justify-center p-6"
          @click.self="cancelDelete"
        >
          <!-- Backdrop -->
          <div class="absolute inset-0 modal-backdrop animate-fade-in"></div>

          <!-- Modal Content -->
          <div
            class="relative glass-panel rounded-[var(--radius-squircle-2xl)] w-full max-w-md p-8 animate-spring-in"
          >
            <!-- Warning Icon -->
            <div class="flex justify-center mb-6">
              <div
                class="w-20 h-20 rounded-[var(--radius-squircle-lg)] bg-red-500/10 border border-red-500/20 flex items-center justify-center"
              >
                <Trash2 :size="36" class="text-red-400" />
              </div>
            </div>

            <!-- Title -->
            <h3 class="text-xl font-bold text-center text-[var(--modal-text)] mb-2">
              {{ t('taskList.confirmDelete') }}
            </h3>

            <!-- Filename -->
            <p
              class="text-sm text-[var(--modal-text-muted)] text-center mb-8 px-4 truncate font-mono-data"
              :title="targetFileName"
            >
              {{ targetFileName }}
            </p>

            <!-- Delete Local File Option -->
            <label
              class="flex items-center gap-4 p-4 mb-8 rounded-[var(--radius-squircle-md)] cursor-pointer transition-all duration-200 group"
              :class="
                deleteLocalFile
                  ? 'bg-red-500/10 border border-red-500/20'
                  : 'bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:bg-red-500/5 hover:border-red-500/10'
              "
            >
              <input v-model="deleteLocalFile" type="checkbox" class="shrink-0" />
              <div class="flex items-center gap-3">
                <HardDrive
                  :size="18"
                  :class="
                    deleteLocalFile
                      ? 'text-red-400'
                      : 'text-[var(--app-text-subtle)] group-hover:text-red-400/60'
                  "
                />
                <div class="flex flex-col">
                  <span
                    :class="[
                      'text-sm font-semibold transition-colors',
                      deleteLocalFile
                        ? 'text-red-400'
                        : 'text-[var(--modal-text-muted)] group-hover:text-red-400/80',
                    ]"
                  >
                    {{ t('taskList.deleteFile') }}
                  </span>
                  <span class="text-[10px] text-[var(--modal-text-subtle)]">
                    {{ t('taskList.irreversible') }}
                  </span>
                </div>
              </div>
            </label>

            <!-- Action Buttons -->
            <div class="flex gap-3">
              <button
                class="flex-1 py-4 rounded-[var(--radius-squircle-md)] bg-[var(--btn-glass-bg)] border border-[var(--btn-glass-border)] text-[var(--modal-text-muted)] font-semibold text-sm transition-all duration-200 hover:bg-[var(--btn-glass-hover)] hover:text-[var(--modal-text)] active:scale-[0.98]"
                @click="cancelDelete"
              >
                {{ t('taskList.cancel') }}
              </button>
              <button
                :disabled="isDeleting"
                class="flex-1 py-4 rounded-[var(--radius-squircle-md)] bg-[var(--status-error)] border border-red-400/20 text-white font-bold text-sm transition-all duration-200 hover:bg-red-400 active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed shadow-lg shadow-red-500/20"
                @click="handleDelete"
              >
                <span v-if="isDeleting" class="flex items-center justify-center gap-2">
                  <AlertCircle :size="16" class="animate-pulse" />
                  {{ t('taskList.deleting') }}
                </span>
                <span v-else>{{ t('taskList.confirm') }}</span>
              </button>
            </div>

            <!-- Keyboard Shortcut Hint -->
            <div class="flex justify-center mt-6">
              <div class="flex items-center gap-2 text-[10px] text-[var(--kbd-text)]">
                <kbd
                  class="px-1.5 py-0.5 rounded bg-[var(--kbd-bg)] border border-[var(--kbd-border)] font-mono text-[9px]"
                >
                  Esc
                </kbd>
                <span>{{ t('taskList.cancel') }}</span>
              </div>
            </div>
          </div>
        </div>
      </Transition>

      <!-- Batch Delete Confirmation Modal -->
      <Transition name="modal">
        <div
          v-if="showBatchDelModal"
          class="fixed inset-0 z-[100] flex items-center justify-center p-6"
          @click.self="cancelBatchDelete"
        >
          <!-- Backdrop -->
          <div class="absolute inset-0 modal-backdrop animate-fade-in"></div>

          <!-- Modal Content -->
          <div
            class="relative glass-panel rounded-[var(--radius-squircle-2xl)] w-full max-w-md p-8 animate-spring-in"
          >
            <!-- Warning Icon -->
            <div class="flex justify-center mb-6">
              <div
                class="w-20 h-20 rounded-[var(--radius-squircle-lg)] bg-red-500/10 border border-red-500/20 flex items-center justify-center"
              >
                <Trash2 :size="36" class="text-red-400" />
              </div>
            </div>

            <!-- Title -->
            <h3 class="text-xl font-bold text-center text-[var(--modal-text)] mb-2">
              {{ t('taskList.confirmBatchDelete', { count: taskStore.selectedCount }) }}
            </h3>

            <!-- Description -->
            <p class="text-sm text-[var(--modal-text-muted)] text-center mb-8 px-4">
              {{ t('taskList.batchDeleteDesc') }}
            </p>

            <!-- Delete Local File Option -->
            <label
              class="flex items-center gap-4 p-4 mb-8 rounded-[var(--radius-squircle-md)] cursor-pointer transition-all duration-200 group"
              :class="
                deleteLocalFile
                  ? 'bg-red-500/10 border border-red-500/20'
                  : 'bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:bg-red-500/5 hover:border-red-500/10'
              "
            >
              <input v-model="deleteLocalFile" type="checkbox" class="shrink-0" />
              <div class="flex items-center gap-3">
                <HardDrive
                  :size="18"
                  :class="
                    deleteLocalFile
                      ? 'text-red-400'
                      : 'text-[var(--app-text-subtle)] group-hover:text-red-400/60'
                  "
                />
                <div class="flex flex-col">
                  <span
                    :class="[
                      'text-sm font-semibold transition-colors',
                      deleteLocalFile
                        ? 'text-red-400'
                        : 'text-[var(--modal-text-muted)] group-hover:text-red-400/80',
                    ]"
                  >
                    {{ t('taskList.deleteFile') }}
                  </span>
                  <span class="text-[10px] text-[var(--modal-text-subtle)]">
                    {{ t('taskList.irreversible') }}
                  </span>
                </div>
              </div>
            </label>

            <!-- Action Buttons -->
            <div class="flex gap-3">
              <button
                class="flex-1 py-4 rounded-[var(--radius-squircle-md)] bg-[var(--btn-glass-bg)] border border-[var(--btn-glass-border)] text-[var(--modal-text-muted)] font-semibold text-sm transition-all duration-200 hover:bg-[var(--btn-glass-hover)] hover:text-[var(--modal-text)] active:scale-[0.98]"
                @click="cancelBatchDelete"
              >
                {{ t('taskList.cancel') }}
              </button>
              <button
                :disabled="isBatchDeleting"
                class="flex-1 py-4 rounded-[var(--radius-squircle-md)] bg-[var(--status-error)] border border-red-400/20 text-white font-bold text-sm transition-all duration-200 hover:bg-red-400 active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed shadow-lg shadow-red-500/20"
                @click="handleBatchDelete"
              >
                <span v-if="isBatchDeleting" class="flex items-center justify-center gap-2">
                  <AlertCircle :size="16" class="animate-pulse" />
                  {{ t('taskList.deleting') }}
                </span>
                <span v-else>{{ t('taskList.confirm') }}</span>
              </button>
            </div>

            <!-- Keyboard Shortcut Hint -->
            <div class="flex justify-center mt-6">
              <div class="flex items-center gap-2 text-[10px] text-[var(--kbd-text)]">
                <kbd
                  class="px-1.5 py-0.5 rounded bg-[var(--kbd-bg)] border border-[var(--kbd-border)] font-mono text-[9px]"
                >
                  Esc
                </kbd>
                <span>{{ t('taskList.cancel') }}</span>
              </div>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>
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

  /* Modal transitions */
  .modal-enter-active,
  .modal-leave-active {
    transition: opacity 0.3s ease;
  }

  .modal-enter-from,
  .modal-leave-to {
    opacity: 0;
  }

  .modal-enter-active .glass-panel {
    animation: spring-in 0.4s cubic-bezier(0.34, 1.56, 0.64, 1) both;
  }

  .modal-leave-active .glass-panel {
    animation: spring-out 0.2s ease-out both;
  }

  /* Fade transition for empty state */
  .fade-enter-active,
  .fade-leave-active {
    transition: opacity 0.3s ease;
  }

  .fade-enter-from,
  .fade-leave-to {
    opacity: 0;
  }

  /* Keyboard shortcut styling */
  kbd {
    font-family: var(--font-family-mono);
  }

  /* Virtual scroller customization */
  :deep(.vue-recycle-scroller__item-wrapper) {
    overflow: visible !important;
  }

  /* Empty state centering fix */
  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
  }
</style>
