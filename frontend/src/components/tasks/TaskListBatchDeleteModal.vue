<script setup lang="ts">
  import { ref, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Trash2, HardDrive, AlertCircle } from '@lucide/vue'
  import LiquidGlassPanel from '../common/LiquidGlassPanel.vue'

  const props = defineProps<{
    show: boolean
    selectedCount: number
    isBatchDeleting: boolean
  }>()

  const emit = defineEmits<{
    (e: 'update:show', val: boolean): void
    (e: 'cancel'): void
    (e: 'confirm', deleteLocalFile: boolean): void
  }>()

  const { t } = useI18n()
  const deleteLocalFile = ref(false)

  // Reset deleteLocalFile when show becomes true
  watch(
    () => props.show,
    newVal => {
      if (newVal) {
        deleteLocalFile.value = false
      }
    },
  )

  const handleCancel = () => {
    emit('cancel')
    emit('update:show', false)
  }

  const handleConfirm = () => {
    emit('confirm', deleteLocalFile.value)
  }
</script>

<template>
  <Teleport to="body">
    <Transition name="modal">
      <div
        v-if="show"
        class="fixed inset-0 z-[100] flex items-center justify-center p-6"
        @click.self="handleCancel"
      >
        <!-- Backdrop -->
        <div class="absolute inset-0 modal-backdrop animate-fade-in"></div>

        <!-- Modal Content -->
        <div
          class="glass-panel-solid relative w-full max-w-md p-8 animate-spring-in rounded-[var(--radius-squircle-2xl)]"
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
            {{ t('taskList.confirmBatchDelete', { count: selectedCount }) }}
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
            <!-- Cancel Button -->
            <LiquidGlassPanel
              as="button"
              :interactive="true"
              hover-effect="glow"
              base-color-class="bg-[var(--btn-glass-bg)]"
              fallback-class="btn-glass"
              class="flex-1 py-4 rounded-[var(--radius-squircle-md)] text-[var(--modal-text-muted)] font-semibold text-sm transition-all duration-200 hover:text-[var(--modal-text)] active:scale-[0.98]"
              @click="handleCancel"
            >
              <span class="flex items-center justify-center w-full h-full">{{
                t('taskList.cancel')
              }}</span>
            </LiquidGlassPanel>
            <button
              :disabled="isBatchDeleting"
              class="flex-1 py-4 rounded-[var(--radius-squircle-md)] bg-[var(--status-error)] border border-red-400/20 text-white font-bold text-sm transition-all duration-200 hover:bg-red-400 active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed shadow-lg shadow-red-500/20"
              @click="handleConfirm"
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
</template>

<style scoped>
  /* Modal transitions */
  .modal-enter-active,
  .modal-leave-active {
    transition: opacity 0.3s ease;
  }

  .modal-enter-from,
  .modal-leave-to {
    opacity: 0;
  }

  .modal-enter-active .glass-panel-solid {
    animation: spring-in 0.4s cubic-bezier(0.34, 1.56, 0.64, 1) both;
  }

  .modal-leave-active .glass-panel-solid {
    animation: spring-out 0.2s ease-out both;
  }

  kbd {
    font-family: var(--font-family-mono);
  }
</style>
