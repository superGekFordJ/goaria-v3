<script setup lang="ts">
  import { ref, computed, onMounted, onUnmounted, watch, toRaw } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { RecycleScroller } from 'vue-virtual-scroller'
  import { useTaskStore } from '../../stores/task'
  import { useUIStore } from '../../stores/ui'
  import TaskCard from './TaskCard.vue'
  import TaskHeader from './TaskHeader.vue'
  import TaskSearch from './TaskSearch.vue'
  import BatchActionBar from './BatchActionBar.vue'
  import { Trash2, HardDrive, Download, CheckCircle2, AlertCircle, SearchX } from 'lucide-vue-next'
  import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'

  const { t } = useI18n()
  const taskStore = useTaskStore()
  const uiStore = useUIStore()

  // Modal State
  const showDelModal = ref(false)
  const delTarget = ref<Task | null>(null)
  const deleteLocalFile = ref(false)
  const isDeleting = ref(false)

  // Batch Delete Modal State
  const showBatchDelModal = ref(false)
  const isBatchDeleting = ref(false)

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
    // 性能优化：使用 toRaw 和 concat 避免 reactive proxy 迭代开销
    return toRaw(active).concat(toRaw(waiting))
  })

  const displayTasks = computed(() => {
    const base =
      uiStore.activeTab === 'downloads' ? combinedDownloads.value : taskStore.stoppedTasks
    if (uiStore.activeTab !== 'stopped' || !searchQuery.value.trim()) {
      return base
    }
    const q = searchQuery.value.toLowerCase()
    return base.filter(t => {
      const path = t.files?.[0]?.path
      if (!path) return false

      const lastSlashIndex = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\'))
      const filename = lastSlashIndex >= 0 ? path.slice(lastSlashIndex + 1) : path

      return filename.toLowerCase().includes(q)
    })
  })

  // Check if search returned no results
  const isSearchEmpty = computed(() => {
    return (
      uiStore.activeTab === 'stopped' &&
      searchQuery.value.trim() !== '' &&
      displayTasks.value.length === 0
    )
  })

  const useVirtualList = computed(() => displayTasks.value.length > 15)

  // Empty state configuration
  const emptyStateConfig = computed(() => {
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
      accent: 'var(--status-active)',
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

  watch(
    displayTasks,
    (newList, oldList) => {
      if (useVirtualList.value || !oldList) {
        prevOrderByGid.clear()
        newList.forEach((t, i) => prevOrderByGid.set(t.gid, i))
        return
      }

      // Find any task that moved significantly (index changed by >= 2)
      let movedGid: string | null = null
      let movedDirection: 'up' | 'down' = 'up'

      for (const [gid, oldIdx] of prevOrderByGid) {
        const newIdx = newList.findIndex(t => t.gid === gid)
        if (newIdx === -1) continue // task removed
        const delta = newIdx - oldIdx
        if (Math.abs(delta) >= 2) {
          movedGid = gid
          movedDirection = delta < 0 ? 'up' : 'down'
          break
        }
      }

      // Update order tracking
      prevOrderByGid.clear()
      newList.forEach((t, i) => prevOrderByGid.set(t.gid, i))

      if (!movedGid) return

      // Wait for TransitionGroup animation to complete (500ms is the animation duration)
      setTimeout(() => {
        scrollToTask(movedGid!, movedDirection === 'up' ? 'start' : 'end')
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
  const handleKeydown = (e: KeyboardEvent) => {
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
      const allGids = displayTasks.value.map(t => t.gid)
      taskStore.selectAll(allGids)
    }
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
    isBatchDeleting.value = true
    try {
      await taskStore.batchRemove(taskStore.getSelectedGids, deleteLocalFile.value)
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
      taskStore.clearSelection()

      // 切换到"已完成"时立即拉取 stopped 任务
      if (newTab === 'stopped') {
        taskStore.fetchStoppedTasks()
      }
    },
  )

  onMounted(() => {
    window.addEventListener('keydown', handleKeydown)
  })

  onUnmounted(() => {
    window.removeEventListener('keydown', handleKeydown)
  })
</script>

<template>
  <div class="flex-1 flex flex-col min-h-0">
    <!-- Task Addition Header (only in downloads tab) -->
    <TaskHeader v-if="uiStore.activeTab === 'downloads'" />

    <!-- Search Box (only in stopped tab) -->
    <TaskSearch v-if="uiStore.activeTab === 'stopped'" v-model="searchQuery" />

    <!-- Task List Container -->
    <div class="flex-1 min-h-0 relative">
      <!-- Empty State -->
      <Transition name="fade">
        <div
          v-if="displayTasks.length === 0"
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
        v-if="displayTasks.length > 0 && !useVirtualList"
        ref="listContainer"
        :key="uiStore.activeTab"
        class="h-full overflow-y-auto px-5 py-4"
      >
        <TransitionGroup name="task-list" tag="div" class="flex flex-col gap-4">
          <div
            v-for="(item, index) in displayTasks"
            :key="item.gid"
            :data-gid="item.gid"
            class="animate-spring-in"
            :style="{ animationDelay: `${Math.min(index * 50, 300)}ms` }"
          >
            <TaskCard :task="item" @confirm-delete="confirmDelete" />
          </div>
        </TransitionGroup>
      </div>

      <RecycleScroller
        v-else-if="displayTasks.length > 0"
        v-slot="{ item, index }"
        class="h-full px-5 py-4"
        :items="displayTasks"
        :item-size="176"
        key-field="gid"
        :buffer="200"
      >
        <div
          class="py-2 animate-spring-in"
          :style="{ animationDelay: `${Math.min(index * 50, 300)}ms` }"
        >
          <TaskCard :task="item" @confirm-delete="confirmDelete" />
        </div>
      </RecycleScroller>
    </div>

    <!-- Batch Action Bar -->
    <BatchActionBar @confirm-batch-delete="confirmBatchDelete" />

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
