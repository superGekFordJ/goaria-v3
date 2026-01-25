<script setup lang="ts">
  import { computed } from 'vue'
  import { useTaskStore } from '../../stores/task'
  import { Pause, Play, Trash2, X, CheckSquare } from 'lucide-vue-next'

  const taskStore = useTaskStore()

  const emit = defineEmits<{
    (e: 'confirm-batch-delete'): void
  }>()

  const selectedCount = computed(() => taskStore.selectedCount)

  const handleBatchPause = () => {
    taskStore.batchPause(taskStore.getSelectedGids)
  }

  const handleBatchResume = () => {
    taskStore.batchResume(taskStore.getSelectedGids)
  }
</script>

<template>
  <Transition name="slide-up">
    <div v-if="selectedCount > 0" class="batch-action-bar">
      <div class="batch-action-bar-content glass-panel">
        <!-- Selection Info -->
        <div class="batch-info">
          <CheckSquare :size="16" class="text-[var(--neon-primary)]" />
          <span class="text-sm font-semibold text-[var(--app-text)]">
            已选择
            <span class="font-mono-data text-[var(--neon-primary)]">{{ selectedCount }}</span> 项
          </span>
        </div>

        <!-- Action Buttons (Icon-only) -->
        <div class="batch-actions">
          <button class="batch-btn-icon" title="批量暂停" @click="handleBatchPause">
            <Pause :size="18" />
          </button>
          <button class="batch-btn-icon" title="批量继续" @click="handleBatchResume">
            <Play :size="18" />
          </button>
          <button
            class="batch-btn-icon batch-btn-danger"
            title="批量删除"
            @click="emit('confirm-batch-delete')"
          >
            <Trash2 :size="18" />
          </button>
        </div>

        <!-- Clear Selection -->
        <button class="batch-clear-btn" title="取消选择 (Esc)" @click="taskStore.clearSelection">
          <X :size="16" />
        </button>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
  /* Component-specific styles are in style.css for consistency */
</style>
