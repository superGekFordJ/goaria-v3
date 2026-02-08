<script setup lang="ts">
  import { ref, onMounted, onUnmounted } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { useConfigStore } from '../../stores/config'
  import { Settings as SettingsIcon, CheckCircle, Loader2 } from 'lucide-vue-next'
  import ThemeIcon from '../common/ThemeIcon.vue'

  // Sections
  import DownloadSection from './sections/DownloadSection.vue'
  import RPCSection from './sections/RPCSection.vue'
  import PerformanceSection from './sections/PerformanceSection.vue'
  import UASection from './sections/UASection.vue'
  import AppearanceSection from './sections/AppearanceSection.vue'
  import AdvancedSection from './sections/AdvancedSection.vue'

  const { t } = useI18n()
  const configStore = useConfigStore()

  // Local form state - decoupled from store to prevent reactivity issues
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

  // Save status for UI feedback only
  const saveStatus = ref<'idle' | 'saving' | 'saved'>('idle')
  let saveTimeout: ReturnType<typeof setTimeout> | null = null
  let statusResetTimeout: ReturnType<typeof setTimeout> | null = null
  let isInitialized = false

  // Initialize form data from store
  onMounted(() => {
    const s = configStore.settings
    formData.value = {
      download_dir: s.download_dir || '',
      rpc_port: String(s.rpc_port || ''),
      rpc_secret: s.rpc_secret || '',
      max_connections: String(s.max_connections || ''),
      max_concurrent_downloads: String(s.max_concurrent_downloads || ''),
      user_agent: s.user_agent || '',
      show_history: Boolean(s.show_history),
      window_transparency: ((s as Record<string, unknown>).window_transparency as string) || 'none',
      smart_thread_mode: Boolean((s as Record<string, unknown>).smart_thread_mode),
    }
    // Mark as initialized after a tick to avoid triggering save on mount
    setTimeout(() => {
      isInitialized = true
    }, 100)
  })

  // Non-blocking background save function
  const triggerSave = () => {
    if (!isInitialized) return

    // Clear any pending operations
    if (saveTimeout) clearTimeout(saveTimeout)
    if (statusResetTimeout) clearTimeout(statusResetTimeout)

    // Show saving indicator
    saveStatus.value = 'saving'

    // Debounce - fire and forget
    saveTimeout = setTimeout(() => {
      // Copy form data to store
      Object.assign(configStore.settings, formData.value)

      // Save in background - completely non-blocking
      configStore
        .updateConfig()
        .then(() => {
          saveStatus.value = 'saved'
          statusResetTimeout = setTimeout(() => {
            saveStatus.value = 'idle'
          }, 1500)
        })
        .catch(() => {
          saveStatus.value = 'idle'
        })
    }, 800)
  }

  // Handle directory picker
  const handlePickDirectory = async () => {
    const selected = await configStore.pickDirectory()
    if (!selected) return
    // Sync the new value
    formData.value.download_dir = selected
    triggerSave()
  }

  // Cleanup timers on unmount
  onUnmounted(() => {
    if (saveTimeout) clearTimeout(saveTimeout)
    if (statusResetTimeout) clearTimeout(statusResetTimeout)
  })

  // Connection options
  const connectionOptions = ['1', '4', '8', '16']
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
        <div class="flex items-center gap-2">
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
              class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-[var(--status-active)]/10 border border-[var(--status-active)]/20"
            >
              <CheckCircle :size="12" class="text-[var(--status-active)]" />
              <span class="text-[10px] font-mono-data text-[var(--status-active)]">
                {{ t('settings.saved') }}
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

      <!-- Settings Cards Container -->
      <div class="space-y-4">
        <!-- Download Directory -->
        <DownloadSection v-model="formData.download_dir" @pick="handlePickDirectory" />

        <!-- RPC Configuration -->
        <RPCSection
          v-model:port="formData.rpc_port"
          v-model:secret="formData.rpc_secret"
          @change="triggerSave"
        />

        <!-- Performance Settings -->
        <PerformanceSection
          v-model:connections="formData.max_connections"
          v-model:concurrent-downloads="formData.max_concurrent_downloads"
          v-model:smart-thread-mode="formData.smart_thread_mode"
          :connection-options="connectionOptions"
          @change="triggerSave"
        />

        <!-- User Agent -->
        <UASection v-model="formData.user_agent" @change="triggerSave" />

        <!-- Appearance Settings -->
        <AppearanceSection />

        <!-- Advanced Settings (Transparency & History) -->
        <AdvancedSection
          v-model:transparency="formData.window_transparency"
          v-model:show-history="formData.show_history"
          @change="triggerSave"
        />

        <!-- About / Version Info -->
        <div class="glass-panel-subtle rounded-[var(--radius-squircle-lg)] p-5 mt-6">
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-3">
              <ThemeIcon :size="32" />
              <div>
                <span class="text-sm font-bold text-[var(--app-text)]/60">GoAria</span>
                <span class="text-[10px] text-[var(--app-text-subtle)] ml-2 font-mono-data"
                  >Luminous Edition</span
                >
              </div>
            </div>
            <div class="text-[10px] font-mono-data text-[var(--app-text-subtle)]/50">
              Powered by Aria2 + Wails
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
  /* Fade transition for save status */
  .fade-enter-active,
  .fade-leave-active {
    transition: opacity 0.2s ease;
  }

  .fade-enter-from,
  .fade-leave-to {
    opacity: 0;
  }
</style>
