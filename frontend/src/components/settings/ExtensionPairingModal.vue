<script setup lang="ts">
  import { ref, watch, onUnmounted } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Link2, Copy, Check, ExternalLink, RefreshCw, X, Loader2, AlertCircle } from 'lucide-vue-next'
  import LiquidGlassPanel from '../common/LiquidGlassPanel.vue'
  import { useExtensionStore } from '../../stores/extension'

  const props = defineProps<{
    show: boolean
    url: string
    regenerating: boolean
  }>()

  const emit = defineEmits<{
    (e: 'update:show', val: boolean): void
    (e: 'close'): void
    (e: 'regenerate'): void
    (e: 'open-in-browser'): void
    (e: 'copied'): void
  }>()

  const { t } = useI18n()
  const extensionStore = useExtensionStore()
  const copied = ref(false)
  const showStaleNotice = ref(false)
  let staleTimer: ReturnType<typeof setTimeout> | null = null

  function clearStaleTimer() {
    if (staleTimer) {
      clearTimeout(staleTimer)
      staleTimer = null
    }
  }

  watch(
    () => props.show,
    newVal => {
      copied.value = false
      showStaleNotice.value = false
      clearStaleTimer()
      if (newVal) {
        staleTimer = setTimeout(() => {
          showStaleNotice.value = true
        }, 5 * 60 * 1000)
      }
    },
  )

  onUnmounted(() => {
    clearStaleTimer()
  })

  const handleCancel = () => {
    clearStaleTimer()
    emit('close')
    emit('update:show', false)
  }

  const handleCopy = async () => {
    const ok = await extensionStore.copyPairUrl()
    if (ok) {
      copied.value = true
      emit('copied')
    }
  }

  const handleRegenerate = () => {
    clearStaleTimer()
    showStaleNotice.value = false
    emit('regenerate')
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

        <div
          class="glass-panel-solid relative w-full max-w-md p-8 rounded-[var(--radius-squircle-2xl)] animate-spring-in"
        >
          <!-- Header Row -->
          <div class="flex items-center justify-between mb-6">
            <div class="flex items-center gap-3">
              <div
                class="w-10 h-10 rounded-[var(--radius-squircle-md)] flex items-center justify-center"
                :class="
                  showStaleNotice
                    ? 'bg-[color-mix(in_srgb,var(--status-error)_12%,transparent)] border border-[color-mix(in_srgb,var(--status-error)_25%,transparent)]'
                    : 'bg-[color-mix(in_srgb,var(--neon-primary)_12%,transparent)] border border-[color-mix(in_srgb,var(--neon-primary)_25%,transparent)]'
                "
              >
                <Link2 :size="20" :class="showStaleNotice ? 'text-[var(--status-error)]' : 'text-[var(--neon-primary)]'" />
              </div>
              <h3 class="text-lg font-bold text-[var(--modal-text)]">
                {{ t('extension.modal.title') }}
              </h3>
            </div>
            <button
              class="p-1.5 rounded-lg text-[var(--app-text-subtle)] hover:text-[var(--modal-text)] transition-colors duration-200"
              @click="handleCancel"
            >
              <X :size="18" />
            </button>
          </div>

          <!-- Help Text -->
          <p class="text-sm text-[var(--modal-text-muted)] mb-4 leading-relaxed">
            {{ t('extension.modal.pairHelp') }}
          </p>

          <!-- Pairing URL Display -->
          <div
            class="mb-4 p-4 rounded-[var(--radius-squircle-md)] border bg-[var(--input-bg)] border-[var(--input-border)]"
          >
            <p class="text-xs font-mono-data text-[var(--app-text)] break-all leading-relaxed">
              {{ url || '—' }}
            </p>
          </div>

          <!-- Stale URL Notice -->
          <Transition name="modal">
            <div
              v-if="showStaleNotice"
              class="mb-4 flex items-start gap-2.5 p-3 rounded-[var(--radius-squircle-md)] bg-[color-mix(in_srgb,var(--status-error)_10%,transparent)] border border-[color-mix(in_srgb,var(--status-error)_20%,transparent)]"
            >
              <AlertCircle :size="16" class="shrink-0 mt-0.5 text-[var(--status-error)]" />
              <span class="text-xs text-[var(--app-text-muted)] leading-relaxed">
                {{ t('extension.modal.staleNotice') }}
              </span>
            </div>
          </Transition>

          <!-- Action Buttons -->
          <div class="flex flex-col gap-2.5">
            <!-- Primary Actions Row -->
            <div class="flex gap-2.5">
              <!-- Copy Button -->
              <LiquidGlassPanel
                as="button"
                :interactive="true"
                hover-effect="glow"
                base-color-class="bg-[var(--btn-glass-bg)]"
                fallback-class="btn-glass"
                class="flex-1 py-3 rounded-[var(--radius-squircle-md)] text-[var(--modal-text-muted)] font-semibold text-sm transition-all duration-200 hover:text-[var(--modal-text)] active:scale-[0.98]"
                @click="handleCopy"
              >
                <span class="flex items-center justify-center gap-2 w-full h-full">
                  <Check v-if="copied" :size="16" class="text-[var(--status-complete)]" />
                  <Copy v-else :size="16" />
                  {{ copied ? t('extension.modal.copied') : t('extension.modal.copy') }}
                </span>
              </LiquidGlassPanel>

              <!-- Open in Browser Button -->
              <LiquidGlassPanel
                as="button"
                :interactive="true"
                hover-effect="glow"
                base-color-class="bg-[var(--btn-glass-bg)]"
                fallback-class="btn-glass"
                class="flex-1 py-3 rounded-[var(--radius-squircle-md)] text-[var(--modal-text-muted)] font-semibold text-sm transition-all duration-200 hover:text-[var(--modal-text)] active:scale-[0.98]"
                @click="emit('open-in-browser')"
              >
                <span class="flex items-center justify-center gap-2 w-full h-full">
                  <ExternalLink :size="16" />
                  {{ t('extension.modal.openInBrowser') }}
                </span>
              </LiquidGlassPanel>
            </div>

            <!-- Secondary Actions Row -->
            <div class="flex gap-2.5">
              <!-- Regenerate Button -->
              <LiquidGlassPanel
                as="button"
                :interactive="!regenerating"
                hover-effect="glow"
                base-color-class="bg-[var(--btn-glass-bg)]"
                fallback-class="btn-glass"
                :disabled="regenerating"
                class="flex-1 py-3 rounded-[var(--radius-squircle-md)] text-[var(--modal-text-muted)] font-semibold text-sm transition-all duration-200 hover:text-[var(--neon-primary)] active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
                @click="handleRegenerate"
              >
                <span class="flex items-center justify-center gap-2 w-full h-full">
                  <Loader2 v-if="regenerating" :size="16" class="animate-spin" />
                  <RefreshCw v-else :size="16" />
                  {{ regenerating ? t('extension.modal.regenerating') : t('extension.modal.regenerate') }}
                </span>
              </LiquidGlassPanel>

              <!-- Close Button -->
              <LiquidGlassPanel
                as="button"
                :interactive="true"
                hover-effect="glow"
                base-color-class="bg-[var(--btn-glass-bg)]"
                fallback-class="btn-glass"
                class="flex-1 py-3 rounded-[var(--radius-squircle-md)] text-[var(--modal-text-muted)] font-semibold text-sm transition-all duration-200 hover:text-[var(--modal-text)] active:scale-[0.98]"
                @click="handleCancel"
              >
                <span class="flex items-center justify-center gap-2 w-full h-full">
                  {{ t('extension.modal.close') }}
                </span>
              </LiquidGlassPanel>
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
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
</style>
