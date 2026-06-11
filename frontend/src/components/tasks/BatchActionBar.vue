<script setup lang="ts">
  import { computed } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { useTaskStore } from '../../stores/task'
  import { useDownloadGroupStore } from '../../stores/downloadGroups'
  import { Pause, Play, Trash2, X, CheckSquare } from 'lucide-vue-next'
  import StaticGlassPanel from '../common/StaticGlassPanel.vue'

  const { t } = useI18n()
  const taskStore = useTaskStore()
  const downloadGroupStore = useDownloadGroupStore()

  const emit = defineEmits<{
    (e: 'confirm-batch-delete'): void
  }>()

  const selectedCount = computed(() => taskStore.selectedCount)

  const handleBatchPause = async () => {
    const selectedTaskGids = [...taskStore.getSelectedGids]
    const selectedGroupKeys = [...(taskStore.getSelectedGroupKeys ?? [])]
    if (selectedTaskGids.length > 0) {
      await taskStore.batchPause(selectedTaskGids)
    }
    for (const groupKey of selectedGroupKeys) {
      await downloadGroupStore.pauseGroup(groupKey)
    }
  }

  const handleBatchResume = async () => {
    const selectedTaskGids = [...taskStore.getSelectedGids]
    const selectedGroupKeys = [...(taskStore.getSelectedGroupKeys ?? [])]
    if (selectedTaskGids.length > 0) {
      await taskStore.batchResume(selectedTaskGids)
    }
    for (const groupKey of selectedGroupKeys) {
      await downloadGroupStore.resumeGroup(groupKey)
    }
  }
</script>

<template>
  <Transition name="slide-up">
    <div v-if="selectedCount > 0" class="batch-action-bar">
      <StaticGlassPanel radius="rounded-[var(--radius-squircle-xl)]">
        <div class="batch-action-bar-content">
          <!-- Selection Info -->
          <div class="batch-info">
            <CheckSquare :size="16" class="text-[var(--neon-primary)]" />
            <span class="text-sm font-semibold text-[var(--app-text)]">
              {{ t('batch.selected', { count: selectedCount }) }}
            </span>
          </div>

          <!-- Action Buttons (Icon-only) -->
          <div class="batch-actions">
            <button class="batch-btn-icon" :title="t('batch.pauseAll')" @click="handleBatchPause">
              <Pause :size="18" />
            </button>
            <button class="batch-btn-icon" :title="t('batch.resumeAll')" @click="handleBatchResume">
              <Play :size="18" />
            </button>
            <button
              class="batch-btn-icon batch-btn-danger"
              :title="t('batch.deleteAll')"
              @click="emit('confirm-batch-delete')"
            >
              <Trash2 :size="18" />
            </button>
          </div>

          <!-- Clear Selection -->
          <button
            class="batch-clear-btn"
            :title="t('batch.clearSelection')"
            @click="taskStore.clearSelection"
          >
            <X :size="16" />
          </button>
        </div>
      </StaticGlassPanel>
    </div>
  </Transition>
</template>

<style scoped>
  /* Component-specific styles are in style.css for consistency */
</style>
