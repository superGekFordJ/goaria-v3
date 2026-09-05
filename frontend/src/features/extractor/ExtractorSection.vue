<script setup lang="ts">
  import { computed, onMounted, onUnmounted } from 'vue'
  import { useI18n } from 'vue-i18n'
  import {
    Package,
    FolderArchive,
    FolderOpen,
    Globe,
    RefreshCw,
    Trash2,
    Loader2,
    AlertCircle,
    RotateCw,
  } from '@lucide/vue'
  import SectionCard from '../../components/settings/sections/SectionCard.vue'
  import { useExtractorState, mapErrorCodeToI18nKey } from './useExtractorState'

  const { t } = useI18n()

  const {
    state,
    loading,
    busy,
    error,
    remoteUrl,
    loadInitialState,
    loadPackFile,
    loadPackDirectory,
    loadPackURL,
    reloadSource,
    removeSource,
    dispose,
  } = useExtractorState()

  onMounted(() => {
    void loadInitialState()
  })

  onUnmounted(() => {
    dispose()
  })

  const isAvailable = computed(() => state.value.available)
  const actionsDisabled = computed(() => busy.value || !isAvailable.value)

  const shortFingerprint = (fp?: string) => {
    if (!fp) return ''
    return fp.length > 12 ? fp.slice(0, 12) : fp
  }
</script>

<template>
  <SectionCard
    :title="t('extractor.title')"
    :description="t('extractor.description')"
    :icon="Package"
    icon-class="bg-[var(--neon-primary)]/10 text-[var(--neon-primary)]"
  >
    <div v-if="loading" data-testid="loading-indicator" class="flex items-center justify-center gap-2 py-6 text-xs font-mono-data text-[var(--app-text-subtle)]">
      <Loader2 class="animate-spin text-[var(--neon-primary)]" :size="16" />
      <span>{{ t('extractor.actions.loading') }}</span>
    </div>

    <div v-else class="flex flex-col gap-4">
      <!-- Unavailable banner when compile-gated runtime reports available: false -->
      <div
        v-if="!isAvailable"
        data-testid="unavailable-banner"
        class="flex items-center gap-2 px-3 py-2 rounded-xl bg-[var(--status-error)]/10 border border-[var(--status-error)]/20 text-xs text-[var(--status-error)]"
      >
        <AlertCircle :size="14" class="shrink-0" />
        <span>{{ t('extractor.state.unavailable') }}</span>
      </div>

      <!-- Recovery warnings -->
      <div
        v-if="state.recovery_errors && state.recovery_errors.length > 0"
        data-testid="recovery-warning"
        class="flex items-center gap-2 px-3 py-2 rounded-xl bg-[var(--status-error)]/10 border border-[var(--status-error)]/20 text-xs text-[var(--status-error)]"
        aria-live="polite"
      >
        <AlertCircle :size="14" class="shrink-0" />
        <span>{{ t('extractor.notices.recoveryWarning') }}</span>
      </div>

      <!-- Operation / Transport error notice with optional retry -->
      <div
        v-if="error"
        data-testid="error-notice"
        class="flex items-center justify-between gap-2 px-3 py-2 rounded-xl bg-[var(--status-error)]/10 border border-[var(--status-error)]/20 text-xs text-[var(--status-error)]"
        aria-live="polite"
      >
        <div class="flex items-center gap-2 min-w-0">
          <AlertCircle :size="14" class="shrink-0" />
          <span class="truncate">{{ t(error) }}</span>
        </div>
        <button
          v-if="!state.sources.length"
          type="button"
          data-testid="retry-btn"
          class="shrink-0 flex items-center gap-1 px-2 py-1 rounded bg-[var(--btn-glass-bg)] hover:bg-[var(--glass-border)] text-[10px] font-mono-data text-[var(--app-text)] transition-colors"
          @click="loadInitialState"
        >
          <RotateCw :size="11" />
          <span>{{ t('extractor.actions.retry') }}</span>
        </button>
      </div>

      <!-- Equal Load Actions: Load ZIP, Load Unpacked, Load from URL -->
      <div class="flex flex-col gap-3">
        <div class="flex flex-wrap items-center gap-2">
          <button
            type="button"
            data-testid="load-zip-btn"
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-[var(--app-text)] bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:bg-[var(--glass-border)] transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="actionsDisabled"
            @click="loadPackFile"
          >
            <FolderArchive :size="14" />
            <span>{{ t('extractor.actions.loadZip') }}</span>
          </button>

          <button
            type="button"
            data-testid="load-directory-btn"
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-[var(--app-text)] bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:bg-[var(--glass-border)] transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="actionsDisabled"
            @click="loadPackDirectory"
          >
            <FolderOpen :size="14" />
            <span>{{ t('extractor.actions.loadDirectory') }}</span>
          </button>
        </div>

        <div class="flex items-center gap-2">
          <div class="relative flex-1">
            <input
              v-model="remoteUrl"
              type="url"
              data-testid="url-input"
              :placeholder="t('extractor.urlInput.placeholder')"
              :disabled="actionsDisabled"
              class="w-full px-3 py-1.5 rounded-lg text-xs font-mono-data text-[var(--app-text)] bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] placeholder:text-[var(--app-text-subtle)]/50 focus:outline-none focus:border-[var(--neon-primary)] disabled:opacity-50 disabled:cursor-not-allowed"
              @keydown.enter.prevent="loadPackURL"
            />
          </div>
          <button
            type="button"
            data-testid="load-url-btn"
            class="shrink-0 flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-[var(--app-text)] bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:bg-[var(--glass-border)] transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="actionsDisabled || !remoteUrl.trim()"
            @click="loadPackURL"
          >
            <Globe :size="14" />
            <span>{{ t('extractor.actions.loadUrl') }}</span>
          </button>
        </div>
      </div>

      <!-- Sources List / Empty state -->
      <div
        v-if="state.sources.length === 0"
        data-testid="empty-state"
        class="py-6 text-center text-xs text-[var(--app-text-subtle)]"
      >
        {{ t('extractor.source.empty') }}
      </div>

      <div v-else class="flex flex-col gap-2">
        <div
          v-for="source in state.sources"
          :key="source.source_id"
          data-testid="source-row"
          class="flex flex-col gap-1.5 p-3 rounded-xl bg-[var(--btn-glass-bg)]/40 border border-[var(--glass-border)]/50"
        >
          <div class="flex items-center justify-between gap-2">
            <div class="flex items-center gap-2 min-w-0">
              <!-- Light is status: ready vs unavailable -->
              <span
                v-if="source.status === 'ready'"
                data-testid="status-light-ready"
                class="w-2 h-2 rounded-full bg-[var(--status-complete)] shadow-[0_0_6px_var(--status-complete)] shrink-0"
                :title="t('extractor.source.status.ready')"
              />
              <span
                v-else
                data-testid="status-light-unavailable"
                class="w-2 h-2 rounded-full bg-[var(--status-error)] shadow-[0_0_6px_var(--status-error)] shrink-0"
                :title="t('extractor.source.status.unavailable')"
              />

              <span class="text-xs font-medium text-[var(--app-text)] truncate">
                {{ source.display_name }}
              </span>

              <span class="text-[10px] px-1.5 py-0.5 rounded bg-[var(--glass-border)]/40 text-[var(--app-text-subtle)]">
                {{ t(`extractor.source.kind.${source.kind}`) }}
              </span>
            </div>

            <div class="flex items-center gap-1 shrink-0">
              <button
                type="button"
                :data-testid="`reload-btn-${source.source_id}`"
                class="p-1 rounded-md text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:bg-[var(--glass-border)] transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
                :disabled="busy"
                :title="t('extractor.actions.reload')"
                @click="reloadSource(source.source_id)"
              >
                <RefreshCw :size="13" />
              </button>

              <button
                type="button"
                :data-testid="`remove-btn-${source.source_id}`"
                class="p-1 rounded-md text-[var(--app-text-subtle)] hover:text-[var(--status-error)] hover:bg-[var(--glass-border)] transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
                :disabled="busy"
                :title="t('extractor.actions.remove')"
                @click="removeSource(source.source_id)"
              >
                <Trash2 :size="13" />
              </button>
            </div>
          </div>

          <!-- Metadata: Pack ID, Version, Signer Fingerprint -->
          <div class="flex items-center gap-3 text-[10px] font-mono-data text-[var(--app-text-subtle)] flex-wrap">
            <span>{{ source.pack_id }}</span>
            <span>{{ 'v' + source.pack_version }}</span>
            <span v-if="source.signer_fingerprint" class="text-[var(--app-text-subtle)]/70">
              {{ t('extractor.source.fingerprint') }}: {{ shortFingerprint(source.signer_fingerprint) }}
            </span>
          </div>

          <!-- Source-local error message if unavailable -->
          <div
            v-if="source.status === 'unavailable' && source.error_code"
            class="mt-1 text-[10px] text-[var(--status-error)] flex items-center gap-1"
          >
            <AlertCircle :size="11" class="shrink-0" />
            <span>{{ t(mapErrorCodeToI18nKey(source.error_code)) }}</span>
          </div>
        </div>
      </div>
    </div>
  </SectionCard>
</template>
