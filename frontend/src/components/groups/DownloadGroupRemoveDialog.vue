<script setup lang="ts">
  import { computed, ref, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Trash2 } from 'lucide-vue-next'

  const props = defineProps<{
    open: boolean
    groupKey: string
    displayName: string
    busy?: boolean
  }>()

  const emit = defineEmits<{
    (e: 'cancel'): void
    (e: 'confirm', deleteFiles: boolean): void
  }>()

  const { t } = useI18n()
  const deleteFiles = ref(false)

  const targetName = computed(() => props.displayName || props.groupKey)

  watch(
    () => props.open,
    open => {
      if (open) deleteFiles.value = false
    },
  )

  function confirm() {
    if (props.busy) return
    emit('confirm', deleteFiles.value)
  }
</script>

<template>
  <Teleport to="body">
    <Transition name="modal">
      <div
        v-if="open"
        class="fixed inset-0 z-[100] flex items-center justify-center p-6"
        @click.self="emit('cancel')"
      >
        <div class="absolute inset-0 modal-backdrop animate-fade-in"></div>
        <div
          class="relative glass-panel rounded-[var(--radius-squircle-2xl)] w-full max-w-md p-7 animate-spring-in"
        >
          <div class="download-group-remove-icon rounded-[var(--radius-squircle-lg)]">
            <Trash2 :size="28" />
          </div>
          <h3 class="mt-5 text-xl font-black text-center text-[var(--modal-text)]">
            {{ t('downloadGroups.removeDialog.title') }}
          </h3>
          <p class="mt-3 text-sm text-center text-[var(--modal-text-muted)]">
            {{ t('downloadGroups.removeDialog.description', { name: targetName }) }}
          </p>
          <label class="download-group-delete-files rounded-[var(--radius-squircle-md)]">
            <input v-model="deleteFiles" type="checkbox" />
            <span>{{ t('downloadGroups.removeDialog.deleteFiles') }}</span>
          </label>
          <div class="mt-6 flex gap-3">
            <button
              type="button"
              class="flex-1 btn-glass rounded-[var(--radius-squircle-md)] px-4 py-3 text-sm font-bold text-[var(--modal-text-muted)]"
              @click="emit('cancel')"
            >
              {{ t('downloadGroups.removeDialog.cancel') }}
            </button>
            <button
              type="button"
              class="download-group-remove-confirm flex-1 rounded-[var(--radius-squircle-md)] px-4 py-3 text-sm font-black"
              :disabled="busy"
              @click="confirm"
            >
              <span v-if="busy">
                {{ t('downloadGroups.removeDialog.removing') }}
              </span>
              <span v-else>{{ t('downloadGroups.removeDialog.confirm') }}</span>
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
  .download-group-remove-icon {
    margin: 0 auto;
    display: flex;
    height: 4.5rem;
    width: 4.5rem;
    align-items: center;
    justify-content: center;
    border: 1px solid color-mix(in srgb, var(--status-error) 24%, transparent);
    background: color-mix(in srgb, var(--status-error) 9%, transparent);
    color: var(--status-error);
  }

  .download-group-delete-files {
    margin-top: 1.5rem;
    display: flex;
    cursor: pointer;
    align-items: center;
    gap: 0.75rem;
    border: 1px solid var(--glass-border);
    background: var(--btn-glass-bg);
    padding: 0.875rem 1rem;
    color: var(--modal-text-muted);
    font-size: 0.875rem;
    font-weight: 700;
  }

  .download-group-remove-confirm {
    border: 1px solid color-mix(in srgb, var(--status-error) 24%, transparent);
    background: var(--status-error);
    color: var(--neon-btn-text);
  }

  .download-group-remove-confirm:disabled {
    cursor: wait;
    opacity: 0.65;
  }
</style>
