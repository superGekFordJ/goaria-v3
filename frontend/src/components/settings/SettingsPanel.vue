<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref, watch, nextTick } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { useConfigStore } from '../../stores/config'
  import { AppConfig } from '../../../bindings/goaria-v3/internal/config/models.js'
  import { Settings as SettingsIcon, CheckCircle, Loader2, AlertCircle } from '@lucide/vue'

  import DownloadSection from './sections/DownloadSection.vue'
  import RPCSection from './sections/RPCSection.vue'
  import PerformanceSection from './sections/PerformanceSection.vue'
  import UASection from './sections/UASection.vue'
  import AppearanceSection from './sections/AppearanceSection.vue'
  import AdvancedSection from './sections/AdvancedSection.vue'
  import ExtensionSection from './sections/ExtensionSection.vue'
  import UpdateSection from './sections/UpdateSection.vue'

  const { t } = useI18n()
  const configStore = useConfigStore()

  const formData = ref({
    download_dir: '',
    rpc_port: '',
    rpc_secret: '',
    max_connections: '',
    max_concurrent_downloads: '',
    user_agent: '',
    show_history: false,
    window_transparency: 'none',
    smart_thread_mode: false,
  })

  type SaveStatus = 'idle' | 'saving' | 'saved' | 'error' | 'restart'
  const saveStatus = ref<SaveStatus>('idle')
  let saveTimeout: ReturnType<typeof setTimeout> | null = null
  let statusResetTimeout: ReturnType<typeof setTimeout> | null = null
  let isInitializedFromBackend = false
  let isApplyingCanonical = false
  let applyGeneration = 0
  let editGeneration = 0
  let disposed = false

  const editorsLocked = computed(() => !configStore.isHydrated)
  const showHydrationError = computed(
    () => !configStore.isHydrated && !configStore.isLoading && configStore.hydrateFailed,
  )

  const syncForm = (snapshot: AppConfig) => {
    const generation = ++applyGeneration
    isApplyingCanonical = true
    formData.value = {
      download_dir: snapshot.download_dir || '',
      rpc_port: String(snapshot.rpc_port || ''),
      rpc_secret: snapshot.rpc_secret || '',
      max_connections: String(snapshot.max_connections || ''),
      max_concurrent_downloads: String(snapshot.max_concurrent_downloads || ''),
      user_agent: snapshot.user_agent || '',
      show_history: Boolean(snapshot.show_history),
      window_transparency: snapshot.window_transparency || 'none',
      smart_thread_mode: Boolean(snapshot.smart_thread_mode),
    }
    void nextTick(() => {
      if (!disposed && generation === applyGeneration) {
        isApplyingCanonical = false
      }
    })
  }

  const hydrateFromStore = () => {
    syncForm(configStore.settings)
    isInitializedFromBackend = true
  }

  const previewPersistedSettings = () => {
    if (isInitializedFromBackend) return
    syncForm(configStore.settings)
  }

  onMounted(() => {
    if (configStore.isHydrated) {
      hydrateFromStore()
    } else {
      previewPersistedSettings()
    }
  })

  const stopHydrationWatch = watch(
    () => [configStore.isHydrated, configStore.isLoading, configStore.hydrateFailed] as const,
    ([hydrated]) => {
      if (hydrated) {
        if (!isInitializedFromBackend) {
          hydrateFromStore()
        }
        return
      }
      previewPersistedSettings()
    },
  )

  const clearPendingTimers = () => {
    if (saveTimeout) {
      clearTimeout(saveTimeout)
      saveTimeout = null
    }
    if (statusResetTimeout) {
      clearTimeout(statusResetTimeout)
      statusResetTimeout = null
    }
  }

  const buildCompleteSnapshot = () => {
    return new AppConfig({
      ...configStore.settings,
      ...formData.value,
    })
  }

  const scheduleReset = (generation: number) => {
    statusResetTimeout = setTimeout(() => {
      if (!disposed && generation === editGeneration) {
        saveStatus.value = 'idle'
      }
    }, 1500)
  }

  const triggerSave = () => {
    if (!isInitializedFromBackend || isApplyingCanonical || disposed) return

    const generation = ++editGeneration
    const snapshot = buildCompleteSnapshot()
    clearPendingTimers()
    saveStatus.value = 'saving'

    saveTimeout = setTimeout(async () => {
      try {
        const result = await configStore.updateConfig(snapshot)
        if (disposed || generation !== editGeneration) return
        const canonical = result?.config
        if (!canonical) {
          saveStatus.value = 'error'
          return
        }
        configStore.applyCanonicalConfig(canonical)
        syncForm(canonical)
        if (!result.success) {
          saveStatus.value = 'error'
          return
        }
        if (result.requires_app_restart) {
          saveStatus.value = 'restart'
          return
        }
        saveStatus.value = 'saved'
        scheduleReset(generation)
      } catch {
        if (import.meta.env.DEV) {
          console.warn('[Settings] save failed')
        }
        if (disposed || generation !== editGeneration) return
        saveStatus.value = 'error'
      }
    }, 800)
  }

  const handlePickDirectory = async () => {
    const selected = await configStore.pickDirectory()
    if (!selected || disposed || !isInitializedFromBackend) return
    formData.value.download_dir = selected
    triggerSave()
  }

  const retryHydration = () => {
    void configStore.fetchConfig()
  }

  onUnmounted(() => {
    disposed = true
    editGeneration++
    applyGeneration++
    clearPendingTimers()
    stopHydrationWatch()
  })

  const connectionOptions = ['1', '4', '8', '16', '24', '32']
</script>

<template>
  <div class="flex-1 overflow-y-auto p-6 animate-fade-in-up">
    <div class="max-w-2xl mx-auto">
      <!-- Header -->
      <div class="flex items-center justify-between mb-8">
        <div class="flex items-center gap-4">
          <div
            class="w-12 h-12 rounded-[var(--radius-squircle-md)] bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] flex items-center justify-center"
          >
            <SettingsIcon :size="22" class="text-[var(--app-text-muted)]" />
          </div>
          <div>
            <h2 class="text-2xl font-bold text-[var(--app-text)] tracking-tight">
              {{ t('settings.title') }}
            </h2>
            <p class="text-xs text-[var(--app-text-subtle)] mt-0.5">
              {{ t('settings.description') }}
            </p>
          </div>
        </div>

        <!-- Save Status Indicator -->
        <div class="flex items-center gap-2" aria-live="polite">
          <Transition name="fade" mode="out-in">
            <div
              v-if="saveStatus === 'saving'"
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)]"
            >
              <Loader2 :size="12" class="animate-spin text-[var(--neon-primary)]" />
              <span class="text-[10px] font-mono-data text-[var(--app-text-muted)]">
                {{ t('settings.saving') }}
              </span>
            </div>
            <div
              v-else-if="saveStatus === 'saved'"
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--status-complete)]/10 border border-[var(--status-complete)]/20"
            >
              <CheckCircle :size="12" class="text-[var(--status-complete)]" />
              <span class="text-[10px] font-mono-data text-[var(--status-complete)]">
                {{ t('settings.saved') }}
              </span>
            </div>
            <div
              v-else-if="saveStatus === 'restart'"
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--status-complete)]/10 border border-[var(--status-complete)]/20"
            >
              <AlertCircle :size="12" class="text-[var(--neon-primary)]" />
              <span class="text-[10px] font-mono-data text-[var(--neon-primary)]">
                {{ t('settings.requiresAppRestart') }}
              </span>
            </div>
            <div
              v-else-if="saveStatus === 'error'"
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--status-error)]/10 border border-[var(--status-error)]/20"
            >
              <AlertCircle :size="12" class="text-[var(--status-error)]" />
              <span class="text-[10px] font-mono-data text-[var(--status-error)]">
                {{ t('settings.saveFailed') }}
              </span>
            </div>
            <div
              v-else
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)]"
            >
              <div class="w-1.5 h-1.5 rounded-full bg-[var(--app-text-subtle)]"></div>
              <span class="text-[10px] font-mono-data text-[var(--app-text-subtle)]">
                {{ t('settings.autoSave') }}
              </span>
            </div>
          </Transition>
        </div>
      </div>

      <div
        v-if="showHydrationError"
        class="mb-4 flex items-center justify-between gap-3 px-4 py-3 rounded-xl bg-[var(--status-error)]/10 border border-[var(--status-error)]/20"
      >
        <p class="text-xs text-[var(--status-error)]">{{ t('settings.loadFailed') }}</p>
        <button
          type="button"
          class="retry-hydrate shrink-0 px-3 py-1.5 rounded-lg text-[10px] font-mono-data text-[var(--app-text)] bg-[var(--btn-glass-bg)] border border-[var(--glass-border)]"
          @click="retryHydration"
        >
          {{ t('settings.retry') }}
        </button>
      </div>

      <!-- Settings Cards Container -->
      <fieldset
        class="min-w-0 space-y-4 border-0 p-0 m-0"
        :disabled="editorsLocked"
        :class="{ 'pointer-events-none opacity-60': editorsLocked }"
      >
        <DownloadSection v-model="formData.download_dir" @pick="handlePickDirectory" />

        <RPCSection
          v-model:port="formData.rpc_port"
          v-model:secret="formData.rpc_secret"
          @change="triggerSave"
        />

        <PerformanceSection
          v-model:connections="formData.max_connections"
          v-model:concurrent-downloads="formData.max_concurrent_downloads"
          v-model:smart-thread-mode="formData.smart_thread_mode"
          :connection-options="connectionOptions"
          @change="triggerSave"
        />

        <UASection v-model="formData.user_agent" @change="triggerSave" />

        <AppearanceSection />

        <AdvancedSection
          v-model:transparency="formData.window_transparency"
          v-model:show-history="formData.show_history"
          @change="triggerSave"
        />

        <ExtensionSection />

        <UpdateSection />
      </fieldset>
    </div>
  </div>
</template>

<style scoped>
  .fade-enter-active,
  .fade-leave-active {
    transition: opacity 0.2s ease;
  }
  .fade-enter-from,
  .fade-leave-to {
    opacity: 0;
  }

  fieldset:disabled {
    opacity: 0.6;
  }
</style>
