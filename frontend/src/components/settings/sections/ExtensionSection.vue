<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Puzzle, Link2, Unlink, Loader2, CheckCircle, Radio, AlertCircle, Copy, Check, ExternalLink, RefreshCw, X } from 'lucide-vue-next'
  import SectionCard from './SectionCard.vue'
  import { useExtensionStore } from '../../../stores/extension'
  import { clearClipboardIfMatches } from '../../../utils/clipboard'

  const { t } = useI18n()
  const extensionStore = useExtensionStore()

  const statusText = computed(() => {
    if (extensionStore.status === 'listening') {
      return t('extension.status.listening', { port: extensionStore.wsPort })
    }
    if (extensionStore.status === 'paired') {
      return t('extension.status.paired', { count: extensionStore.connectedClients })
    }
    return t('extension.status.disconnected')
  })

  const isListening = computed(() => extensionStore.status === 'listening' || extensionStore.status === 'paired')

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
    () => extensionStore.pairingPanelOpen,
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

  onMounted(() => {
    extensionStore.subscribeToEvents()
    extensionStore.refreshStatus()
  })

  onUnmounted(() => {
    extensionStore.unsubscribeFromEvents()
    clearAuthFailedTimer()
    clearUnpairRotatedTimer()
    clearStaleTimer()
  })

  let authFailedTimer: ReturnType<typeof setTimeout> | null = null
  let unpairRotatedTimer: ReturnType<typeof setTimeout> | null = null

  function clearAuthFailedTimer() {
    if (authFailedTimer) {
      clearTimeout(authFailedTimer)
      authFailedTimer = null
    }
  }

  function clearUnpairRotatedTimer() {
    if (unpairRotatedTimer) {
      clearTimeout(unpairRotatedTimer)
      unpairRotatedTimer = null
    }
  }

  // Auto-dismiss the auth-failed notice after 8 seconds.
  watch(
    () => extensionStore.authFailedNotice,
    val => {
      clearAuthFailedTimer()
      if (val) {
        authFailedTimer = setTimeout(() => {
          extensionStore.authFailedNotice = false
          authFailedTimer = null
        }, 8000)
      }
    },
  )

  watch(
    () => extensionStore.unpairRotatedNotice,
    val => {
      clearUnpairRotatedTimer()
      if (val) {
        unpairRotatedTimer = setTimeout(() => {
          extensionStore.unpairRotatedNotice = false
          unpairRotatedTimer = null
        }, 8000)
      }
    },
  )

  const handleClose = () => {
    clearStaleTimer()
    const consumedUrl = extensionStore.pairUrl
    extensionStore.pairUrl = ''
    extensionStore.pairingPanelOpen = false
    if (consumedUrl) {
      void clearClipboardIfMatches(consumedUrl)
    }
  }

  const handleCopy = async () => {
    const ok = await extensionStore.copyPairUrl()
    if (ok) {
      copied.value = true
    }
  }

  const handleRegenerate = () => {
    clearStaleTimer()
    showStaleNotice.value = false
    copied.value = false
    extensionStore.regenerate()
  }
</script>

<template>
  <SectionCard
    :title="t('extension.title')"
    :description="t('extension.pairHelp')"
    :icon="Puzzle"
    icon-class="bg-cyan-500/10 text-cyan-400"
  >
    <!-- Status Row -->
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-2">
        <div
          class="w-2 h-2 rounded-full transition-all duration-300"
          :class="[
            isListening
              ? 'bg-[var(--status-complete)] shadow-[0_0_6px_var(--status-complete)]'
              : 'bg-[var(--app-text-subtle)]',
          ]"
        ></div>
        <span class="text-[10px] font-mono-data text-[var(--app-text-muted)]">
          {{ statusText }}
        </span>
      </div>
      <div v-if="isListening" class="flex items-center gap-1.5 px-2 py-1 rounded-lg bg-[var(--btn-glass-bg)]">
        <Radio :size="10" class="text-[var(--neon-primary)]" />
        <span class="text-[10px] font-mono-data text-[var(--app-text-subtle)]">
          {{ extensionStore.wsPort }}
        </span>
      </div>
    </div>

    <!-- Auth Failed Notice -->
    <Transition name="modal">
      <div
        v-if="extensionStore.authFailedNotice"
        class="mb-4 flex items-start gap-2.5 p-3 rounded-[var(--radius-squircle-md)] bg-[color-mix(in_srgb,var(--status-error)_10%,transparent)] border border-[color-mix(in_srgb,var(--status-error)_20%,transparent)]"
      >
        <AlertCircle :size="16" class="shrink-0 mt-0.5 text-[var(--status-error)]" />
        <span class="text-xs text-[var(--app-text-muted)] leading-relaxed">
          {{ t('extension.toast.authFailed') }}
        </span>
      </div>
    </Transition>

    <!-- Unpair Rotated Notice -->
    <Transition name="modal">
      <div
        v-if="extensionStore.unpairRotatedNotice"
        class="mb-4 flex items-start gap-2.5 p-3 rounded-[var(--radius-squircle-md)] bg-[color-mix(in_srgb,var(--neon-primary)_10%,transparent)] border border-[color-mix(in_srgb,var(--neon-primary)_20%,transparent)]"
      >
        <CheckCircle :size="16" class="shrink-0 mt-0.5 text-[var(--neon-primary)]" />
        <span class="text-xs text-[var(--app-text-muted)] leading-relaxed">
          {{ t('extension.toast.unpairRotated') }}
        </span>
      </div>
    </Transition>

    <!-- Two-stage cross-fade: idle vs pairing -->
    <!-- Grid overlap ensures the container is sized by the tallest stage, providing true Zero Layout Shift -->
    <div class="grid grid-cols-1 grid-rows-1 mt-2">
      <!-- Stage 1: Idle -->
      <Transition name="fade">
        <div v-if="!extensionStore.pairingPanelOpen" data-testid="pairing-stage-idle" class="col-start-1 row-start-1 flex flex-col justify-start">
          <!-- Connected Clients -->
          <div v-if="isListening" class="mb-4 flex items-center gap-2">
            <span class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]">
              {{ t('extension.connectedClients') }}
            </span>
            <span class="text-sm font-mono-data text-[var(--app-text)]/80">
              {{ extensionStore.connectedClients }}
            </span>
          </div>

          <!-- Action Buttons -->
          <div class="flex items-center gap-3">
            <!-- Pair Button -->
            <button
              :disabled="extensionStore.pairing"
              class="flex items-center gap-2 px-4 py-2.5 rounded-xl bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:border-[var(--neon-primary)]/30 transition-all duration-200 text-xs font-mono-data text-[var(--app-text-muted)] hover:text-[var(--neon-primary)] disabled:opacity-50 disabled:cursor-not-allowed"
              @click="extensionStore.pair()"
            >
              <Loader2 v-if="extensionStore.pairing" :size="14" class="animate-spin" />
              <Link2 v-else :size="14" />
              {{ extensionStore.pairing ? t('extension.pairing') : t('extension.pair') }}
            </button>

            <!-- Unpair Button + Paired Badge / In-place Confirmation -->
            <Transition v-if="extensionStore.paired" mode="out-in" name="fade">
              <div
                v-if="!extensionStore.showUnpairConfirm"
                key="unpair-idle"
                class="flex items-center gap-3"
              >
                <button
                  data-testid="unpair-btn"
                  class="flex items-center gap-2 px-4 py-2.5 rounded-xl bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:border-red-500/30 transition-all duration-200 text-xs font-mono-data text-[var(--app-text-muted)] hover:text-red-400"
                  @click="extensionStore.requestUnpair()"
                >
                  <Unlink :size="14" />
                  {{ t('extension.unpair') }}
                </button>
                <div class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--status-complete)]/10 border border-[var(--status-complete)]/20">
                  <CheckCircle :size="12" class="text-[var(--status-complete)]" />
                  <span class="text-[10px] font-mono-data text-[var(--status-complete)]">
                    {{ t('extension.paired') }}
                  </span>
                </div>
              </div>
              <div
                v-else
                key="unpair-confirm"
                data-testid="unpair-confirm"
                class="flex items-center gap-2"
              >
                <span class="text-[10px] text-[var(--app-text-subtle)] leading-tight max-w-[180px]">
                  {{ t('extension.unpairConfirm.message') }}
                </span>
                <button
                  data-testid="unpair-confirm-btn"
                  class="flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg bg-red-500/15 border border-red-500/30 text-[10px] font-mono-data text-red-400 hover:bg-red-500/25 transition-all"
                  @click="extensionStore.unpair()"
                >
                  <Unlink :size="12" />
                  {{ t('extension.unpairConfirm.confirm') }}
                </button>
                <button
                  data-testid="unpair-cancel-btn"
                  class="flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] text-[10px] font-mono-data text-[var(--app-text-muted)] hover:text-[var(--app-text)] transition-all"
                  @click="extensionStore.cancelUnpair()"
                >
                  {{ t('extension.unpairConfirm.cancel') }}
                </button>
              </div>
            </Transition>
          </div>
        </div>
      </Transition>

      <!-- Stage 2: Pairing -->
      <Transition name="fade">
        <div v-if="extensionStore.pairingPanelOpen" data-testid="pairing-stage-pairing" class="col-start-1 row-start-1 flex flex-col justify-start w-full z-10">
          <!-- Header: Help text & Close -->
          <div class="flex items-start justify-between gap-4 mb-3">
            <p class="text-xs text-[var(--app-text-muted)] leading-relaxed flex-1">
              {{ t('extension.modal.pairHelp') }}
            </p>
            <button
              data-testid="pairing-close-btn"
              class="p-1 rounded-md text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:bg-[var(--btn-glass-bg)] transition-all shrink-0 mt-[-2px]"
              :aria-label="t('extension.modal.close')"
              @click="handleClose"
            >
              <X :size="16" />
            </button>
          </div>

          <!-- URL Input & Actions -->
          <div class="flex items-center gap-2">
            <!-- Unified Input Group -->
            <div class="flex-1 flex items-center bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl h-[38px] overflow-hidden focus-within:border-[var(--neon-primary)]/50 transition-colors">
              <input
                data-testid="pairing-url-input"
                readonly
                :value="extensionStore.pairUrl || '—'"
                class="flex-1 bg-transparent px-3 text-[11px] font-mono-data text-[var(--app-text)] focus:outline-none w-0 truncate"
                :aria-label="t('extension.pairUrl')"
              />
              <!-- Inline Actions -->
              <div class="flex items-center gap-1 pr-1.5 pl-1.5 border-l border-[var(--glass-border)] bg-[var(--btn-glass-bg)]/30 h-full">
                <button
                  data-testid="pairing-copy-btn"
                  class="p-1.5 rounded-lg text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:bg-[var(--btn-glass-bg)] transition-all"
                  :title="t('extension.modal.copy')"
                  @click="handleCopy"
                >
                  <Check v-if="copied" :size="14" class="text-[var(--status-complete)]" />
                  <Copy v-else :size="14" />
                </button>
                <button
                  data-testid="pairing-open-btn"
                  class="p-1.5 rounded-lg text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:bg-[var(--btn-glass-bg)] transition-all"
                  :title="t('extension.modal.openInBrowser')"
                  @click="extensionStore.openInBrowser(extensionStore.pairUrl)"
                >
                  <ExternalLink :size="14" />
                </button>
              </div>
            </div>
            
            <!-- Regenerate Button -->
            <button
              data-testid="pairing-regenerate-btn"
              :disabled="extensionStore.regenerating"
              class="flex items-center justify-center h-[38px] px-3.5 rounded-xl bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] text-[var(--app-text-muted)] hover:text-[var(--neon-primary)] hover:border-[var(--neon-primary)]/30 transition-all disabled:opacity-50 shrink-0"
              :title="t('extension.modal.regenerateHelp')"
              @click="handleRegenerate"
            >
              <Loader2 v-if="extensionStore.regenerating" :size="14" class="animate-spin" />
              <RefreshCw v-else :size="14" />
            </button>
          </div>

          <!-- Stale URL Notice -->
          <Transition name="fade">
            <div
              v-if="showStaleNotice"
              class="mt-3 flex items-center gap-2 p-2.5 rounded-xl bg-[color-mix(in_srgb,var(--status-error)_10%,transparent)] border border-[color-mix(in_srgb,var(--status-error)_20%,transparent)]"
            >
              <AlertCircle :size="14" class="shrink-0 text-[var(--status-error)]" />
              <span class="text-xs text-[var(--app-text-muted)]">
                {{ t('extension.modal.staleNotice') }}
              </span>
            </div>
          </Transition>
        </div>
      </Transition>
    </div>
  </SectionCard>
</template>

<style scoped>
  .fade-enter-active,
  .fade-leave-active {
    transition: opacity 0.25s ease;
  }
  .fade-enter-from,
  .fade-leave-to {
    opacity: 0;
  }
</style>
