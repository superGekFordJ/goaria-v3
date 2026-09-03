<script setup lang="ts">
  import { useI18n } from 'vue-i18n'
  import { Loader2, CheckCircle, AlertCircle } from '@lucide/vue'
  import LiquidGlassPanel from '../common/LiquidGlassPanel.vue'

  export type SaveStatus = 'idle' | 'saving' | 'saved' | 'error'

  defineProps<{
    visible: boolean
    status: SaveStatus
    errorKey: string
  }>()

  const { t } = useI18n()
</script>

<template>
  <Transition name="floating-capsule">
    <div
      v-if="visible && status !== 'idle'"
      class="absolute top-3 left-0 right-0 z-[150] flex justify-center pointer-events-none px-6"
      data-testid="floating-save-status"
    >
      <LiquidGlassPanel
        radius="rounded-full"
        base-color-class="bg-[color-mix(in_srgb,var(--app-bg)_65%,transparent)]"
        fallback-class="bg-[color-mix(in_srgb,var(--card-bg)_90%,transparent)] border border-[var(--glass-border)]"
        class="pointer-events-auto shadow-[var(--glass-shadow)] transition-all duration-200"
      >
        <div class="flex items-center gap-2 px-3.5 py-1.5">
          <template v-if="status === 'saving'">
            <Loader2 :size="12" class="animate-spin text-[var(--neon-primary)]" />
            <span
              class="text-[10px] font-mono-data text-[var(--app-text-muted)]"
              aria-live="polite"
            >
              {{ t('settings.saving') }}
            </span>
          </template>
          <template v-else-if="status === 'saved'">
            <CheckCircle :size="12" class="text-[var(--status-complete)]" />
            <span
              class="text-[10px] font-mono-data text-[var(--status-complete)]"
              aria-live="polite"
            >
              {{ t('settings.saved') }}
            </span>
          </template>
          <template v-else-if="status === 'error'">
            <AlertCircle :size="12" class="text-[var(--status-error)]" />
            <span
              class="text-[10px] font-mono-data text-[var(--status-error)]"
              aria-live="polite"
            >
              {{ t(errorKey) }}
            </span>
          </template>
        </div>
      </LiquidGlassPanel>
    </div>
  </Transition>
</template>

<style scoped>
  .floating-capsule-enter-active,
  .floating-capsule-leave-active {
    transition:
      opacity 0.25s cubic-bezier(0.16, 1, 0.3, 1),
      transform 0.25s cubic-bezier(0.16, 1, 0.3, 1);
  }
  .floating-capsule-enter-from,
  .floating-capsule-leave-to {
    opacity: 0;
    transform: translateY(-8px) scale(0.96);
  }
</style>
