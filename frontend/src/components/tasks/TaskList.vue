<script setup lang="ts">
  import { ref, computed, onMounted, onUnmounted } from 'vue'
  import { RecycleScroller } from 'vue-virtual-scroller'
  import { useTaskStore } from '../../stores/task'
  import { useUIStore } from '../../stores/ui'
  import TaskCard from './TaskCard.vue'
  import TaskHeader from './TaskHeader.vue'
  import { Trash2, HardDrive, Download, CheckCircle2, AlertCircle } from 'lucide-vue-next'
  import { Task } from '../../../bindings/goaria-v3/internal/rpc/models'

  const taskStore = useTaskStore()
  const uiStore = useUIStore()

  // Modal State
  const showDelModal = ref(false)
  const delTarget = ref<Task | null>(null)
  const deleteLocalFile = ref(false)
  const isDeleting = ref(false)

  // Task filtering logic based on active tab
  const combinedDownloads = computed(() => [...taskStore.activeTasks, ...taskStore.waitingTasks])

  const displayTasks = computed(() => {
    return uiStore.activeTab === 'downloads' ? combinedDownloads.value : taskStore.stoppedTasks
  })

  // Empty state configuration
  const emptyStateConfig = computed(() => {
    if (uiStore.activeTab === 'downloads') {
      return {
        icon: Download,
        title: '暂无下载任务',
        description: '在上方粘贴链接开始下载',
        accent: '#06ffd5',
      }
    }
    return {
      icon: CheckCircle2,
      title: '暂无完成任务',
      description: '完成的下载任务将在此处显示',
      accent: '#22ff88',
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
    if (!delTarget.value?.files?.[0]?.path) return '未知任务'
    return delTarget.value.files[0].path.split(/[\\/]/).pop() || '未知任务'
  })

  // Performance: Start polling only when the list is visible
  onMounted(() => {
    taskStore.startPolling(500)
  })

  onUnmounted(() => {
    taskStore.stopPolling(true)
  })

  // Close modal on escape key
  const handleKeydown = (e: KeyboardEvent) => {
    if (e.key === 'Escape' && showDelModal.value) {
      cancelDelete()
    }
  }

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
              :style="{ background: `${emptyStateConfig.accent}08` }"
            >
              <!-- Subtle glow effect -->
              <div
                class="absolute inset-0 opacity-30"
                :style="{
                  background: `radial-gradient(circle at center, ${emptyStateConfig.accent}20 0%, transparent 70%)`,
                }"
              ></div>
              <component
                :is="emptyStateConfig.icon"
                :size="40"
                :style="{ color: `${emptyStateConfig.accent}60` }"
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
      <RecycleScroller
        v-if="displayTasks.length > 0"
        v-slot="{ item, index }"
        class="h-full px-5 py-4"
        :items="displayTasks"
        :item-size="160"
        key-field="gid"
        :buffer="400"
      >
        <div
          class="py-2 animate-spring-in"
          :style="{ animationDelay: `${Math.min(index * 50, 300)}ms` }"
        >
          <TaskCard :task="item" @confirm-delete="confirmDelete" />
        </div>
      </RecycleScroller>
    </div>

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
            <h3 class="text-xl font-bold text-center text-[var(--modal-text)] mb-2">确认删除任务？</h3>

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
                    deleteLocalFile ? 'text-red-400' : 'text-[var(--app-text-subtle)] group-hover:text-red-400/60'
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
                    同时删除本地文件
                  </span>
                  <span class="text-[10px] text-[var(--modal-text-subtle)]"> 此操作不可撤销 </span>
                </div>
              </div>
            </label>

            <!-- Action Buttons -->
            <div class="flex gap-3">
              <button
                class="flex-1 py-4 rounded-[var(--radius-squircle-md)] bg-[var(--btn-glass-bg)] border border-[var(--btn-glass-border)] text-[var(--modal-text-muted)] font-semibold text-sm transition-all duration-200 hover:bg-[var(--btn-glass-hover)] hover:text-[var(--modal-text)] active:scale-[0.98]"
                @click="cancelDelete"
              >
                取消
              </button>
              <button
                :disabled="isDeleting"
                class="flex-1 py-4 rounded-[var(--radius-squircle-md)] bg-[var(--status-error)] border border-red-400/20 text-white font-bold text-sm transition-all duration-200 hover:bg-red-400 active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed shadow-lg shadow-red-500/20"
                @click="handleDelete"
              >
                <span v-if="isDeleting" class="flex items-center justify-center gap-2">
                  <AlertCircle :size="16" class="animate-pulse" />
                  删除中...
                </span>
                <span v-else>确认删除</span>
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
                <span>取消</span>
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
