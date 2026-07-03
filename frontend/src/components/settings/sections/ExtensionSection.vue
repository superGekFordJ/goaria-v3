<script setup lang="ts">
  import { computed, onMounted, onUnmounted } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Puzzle, Link2, Unlink, Loader2, CheckCircle, Radio } from 'lucide-vue-next'
  import SectionCard from './SectionCard.vue'
  import { useExtensionStore } from '../../../stores/extension'

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

  onMounted(() => {
    extensionStore.subscribeToEvents()
    extensionStore.refreshStatus()
  })

  onUnmounted(() => {
    extensionStore.unsubscribeFromEvents()
  })
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

    <!-- Pairing URL Display -->
    <div v-if="extensionStore.pairUrl && extensionStore.pairing" class="mb-4">
      <label class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-2">
        <Link2 :size="10" />
        {{ t('extension.pairUrl') }}
      </label>
      <div class="bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-xs font-mono-data text-[var(--app-text)]/60 break-all">
        {{ extensionStore.pairUrl }}
      </div>
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
  </SectionCard>
</template>
