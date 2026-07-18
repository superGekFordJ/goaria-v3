<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Puzzle, Link2, Unlink, Loader2, CheckCircle, Radio, AlertCircle, Copy, Check, ExternalLink, RefreshCw, X } from 'lucide-vue-next'
  import SectionCard from './SectionCard.vue'
  import LiquidGlassPanel from '../../common/LiquidGlassPanel.vue'
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

    <!-- Connected Clients -->
    <div v-if="isListening" class="mb-4 flex items-center gap-2">
      <span class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]">
        {{ t('extension.connectedClients') }}
      </span>
      <span class="text-sm font-mono-data text-[var(--app-text)]/80">
        {{ extensionStore.connectedClients }}
      </span>
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

      <!-- Unpair Button -->
      <button
        v-if="extensionStore.paired"
        class="flex items-center gap-2 px-4 py-2.5 rounded-xl bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:border-red-500/30 transition-all duration-200 text-xs font-mono-data text-[var(--app-text-muted)] hover:text-red-400"
        @click="extensionStore.unpair()"
      >
        <Unlink :size="14" />
        {{ t('extension.unpair') }}
      </button>

      <!-- Paired Badge -->
      <div v-if="extensionStore.paired" class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--status-complete)]/10 border border-[var(--status-complete)]/20">
        <CheckCircle :size="12" class="text-[var(--status-complete)]" />
        <span class="text-[10px] font-mono-data text-[var(--status-complete)]">
          {{ t('extension.paired') }}
        </span>
      </div>
    </div>

    <!-- Inline Pairing Panel -->
    <Transition name="modal">
      <div
        v-if="extensionStore.pairingPanelOpen"
        class="mt-4 glass-panel-solid p-6 rounded-[var(--radius-squircle-md)] animate-spring-in"
      >
        <!-- Header Row -->
        <div class="flex items-center justify-between mb-4">
          <div class="flex items-center gap-3">
            <div
              class="w-8 h-8 rounded-[var(--radius-squircle-md)] flex items-center justify-center"
              :class="
                showStaleNotice
                  ? 'bg-[color-mix(in_srgb,var(--status-error)_12%,transparent)] border border-[color-mix(in_srgb,var(--status-error)_25%,transparent)]'
                  : 'bg-[color-mix(in_srgb,var(--neon-primary)_12%,transparent)] border border-[color-mix(in_srgb,var(--neon-primary)_25%,transparent)]'
              "
            >
              <Link2 :size="16" :class="showStaleNotice ? 'text-[var(--status-error)]' : 'text-[var(--neon-primary)]'" />
            </div>
            <h4 class="text-sm font-bold text-[var(--app-text)]">
              {{ t('extension.modal.title') }}
            </h4>
          </div>
          <button
            class="p-1.5 rounded-lg text-[var(--app-text-subtle)] hover:text-[var(--app-text)] transition-colors duration-200"
            @click="handleClose"
          >
            <X :size="16" />
          </button>
        </div>

        <!-- Help Text -->
        <p class="text-xs text-[var(--app-text-muted)] mb-3 leading-relaxed">
          {{ t('extension.modal.pairHelp') }}
        </p>

        <!-- Pairing URL Display -->
        <div
          class="mb-3 p-3 rounded-[var(--radius-squircle-md)] border bg-[var(--input-bg)] border-[var(--input-border)]"
        >
          <p class="text-xs font-mono-data text-[var(--app-text)] break-all leading-relaxed">
            {{ extensionStore.pairUrl || '—' }}
          </p>
        </div>

        <!-- Stale URL Notice -->
        <Transition name="modal">
          <div
            v-if="showStaleNotice"
            class="mb-3 flex items-start gap-2.5 p-3 rounded-[var(--radius-squircle-md)] bg-[color-mix(in_srgb,var(--status-error)_10%,transparent)] border border-[color-mix(in_srgb,var(--status-error)_20%,transparent)]"
          >
            <AlertCircle :size="14" class="shrink-0 mt-0.5 text-[var(--status-error)]" />
            <span class="text-xs text-[var(--app-text-muted)] leading-relaxed">
              {{ t('extension.modal.staleNotice') }}
            </span>
          </div>
        </Transition>

        <!-- Action Buttons -->
        <div class="flex flex-col gap-2">
          <!-- Primary Actions Row -->
          <div class="flex gap-2">
            <!-- Copy Button -->
            <LiquidGlassPanel
              as="button"
              :interactive="true"
              hover-effect="glow"
              base-color-class="bg-[var(--btn-glass-bg)]"
              fallback-class="btn-glass"
              class="flex-1 py-2.5 rounded-[var(--radius-squircle-md)] text-[var(--app-text-muted)] font-semibold text-xs transition-all duration-200 hover:text-[var(--app-text)] active:scale-[0.98]"
              @click="handleCopy"
            >
              <span class="flex items-center justify-center gap-2 w-full h-full">
                <Check v-if="copied" :size="14" class="text-[var(--status-complete)]" />
                <Copy v-else :size="14" />
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
              class="flex-1 py-2.5 rounded-[var(--radius-squircle-md)] text-[var(--app-text-muted)] font-semibold text-xs transition-all duration-200 hover:text-[var(--app-text)] active:scale-[0.98]"
              @click="extensionStore.openInBrowser(extensionStore.pairUrl)"
            >
              <span class="flex items-center justify-center gap-2 w-full h-full">
                <ExternalLink :size="14" />
                {{ t('extension.modal.openInBrowser') }}
              </span>
            </LiquidGlassPanel>
          </div>

          <!-- Secondary Actions Row -->
          <div class="flex gap-2">
            <!-- Regenerate Button -->
            <LiquidGlassPanel
              as="button"
              :interactive="!extensionStore.regenerating"
              hover-effect="glow"
              base-color-class="bg-[var(--btn-glass-bg)]"
              fallback-class="btn-glass"
              :disabled="extensionStore.regenerating"
              class="flex-1 py-2.5 rounded-[var(--radius-squircle-md)] text-[var(--app-text-muted)] font-semibold text-xs transition-all duration-200 hover:text-[var(--neon-primary)] active:scale-[0.98] disabled:opacity-50 disabled:cursor-not-allowed"
              @click="handleRegenerate"
            >
              <span class="flex items-center justify-center gap-2 w-full h-full">
                <Loader2 v-if="extensionStore.regenerating" :size="14" class="animate-spin" />
                <RefreshCw v-else :size="14" />
                {{ extensionStore.regenerating ? t('extension.modal.regenerating') : t('extension.modal.regenerate') }}
              </span>
            </LiquidGlassPanel>

            <!-- Close Button -->
            <LiquidGlassPanel
              as="button"
              :interactive="true"
              hover-effect="glow"
              base-color-class="bg-[var(--btn-glass-bg)]"
              fallback-class="btn-glass"
              class="flex-1 py-2.5 rounded-[var(--radius-squircle-md)] text-[var(--app-text-muted)] font-semibold text-xs transition-all duration-200 hover:text-[var(--app-text)] active:scale-[0.98]"
              @click="handleClose"
            >
              <span class="flex items-center justify-center gap-2 w-full h-full">
                {{ t('extension.modal.close') }}
              </span>
            </LiquidGlassPanel>
          </div>
        </div>
      </div>
    </Transition>
  </SectionCard>
</template>
