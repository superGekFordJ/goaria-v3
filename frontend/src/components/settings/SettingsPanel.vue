<script setup lang="ts">
  import {
    computed,
    defineAsyncComponent,
    onMounted,
    onUnmounted,
    onActivated,
    onDeactivated,
    ref,
    watch,
    nextTick,
  } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { useConfigStore } from '../../stores/config'
  import { AppConfig } from '../../../bindings/goaria-v3/internal/config/models.js'
  import {
    Settings as SettingsIcon,
    AlertCircle,
    RotateCw,
    FolderOpen,
    Zap,
    Cpu,
    Globe,
    Palette,
    Layers,
    Puzzle,
    Package,
    RefreshCw,
  } from '@lucide/vue'

  import DownloadSection from './sections/DownloadSection.vue'
  import RPCSection from './sections/RPCSection.vue'
  import PerformanceSection from './sections/PerformanceSection.vue'
  import UASection from './sections/UASection.vue'
  import AppearanceSection from './sections/AppearanceSection.vue'
  import AdvancedSection from './sections/AdvancedSection.vue'
  import ExtensionSection from './sections/ExtensionSection.vue'
  import UpdateSection from './sections/UpdateSection.vue'
  import SettingsCommandCapsule, {
    type SettingsNavigationSection,
  } from './sections/SettingsCommandCapsule.vue'
  import { useUIStore } from '../../stores/ui'

  const isExtractorEnabled = import.meta.env.VITE_GOARIA_EXTRACTOR === 'true'
  const ExtractorSection = isExtractorEnabled
    ? defineAsyncComponent(() => import('../../features/extractor/ExtractorSection.vue'))
    : null

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

  type SaveStatus = 'idle' | 'saving' | 'saved' | 'error'
  const saveStatus = ref<SaveStatus>('idle')
  let saveTimeout: ReturnType<typeof setTimeout> | null = null
  let statusResetTimeout: ReturnType<typeof setTimeout> | null = null
  let isInitializedFromBackend = false
  let isApplyingCanonical = false
  let applyGeneration = 0
  let editGeneration = 0
  let disposed = false

  const editorsLocked = computed(() => !configStore.isHydrated)
  const showHydrationError = computed(() => !configStore.isHydrated && configStore.hydrateFailed)
  const retryBusy = computed(() => configStore.isLoading)
  const lastErrorCode = ref('')
  const saveErrorKey = computed(() => {
    switch (lastErrorCode.value) {
      case 'config_persist_failed':
        return 'settings.errors.persistFailed'
      case 'config_not_loaded':
        return 'settings.errors.notLoaded'
      case 'download_dir_unavailable':
        return 'settings.errors.downloadDirUnavailable'
      case 'rpc_extension_port_conflict':
        return 'settings.errors.rpcExtensionConflict'
      case 'aria2_restart_failed_rolled_back':
        return 'settings.errors.ariaRestartRolledBack'
      case 'aria2_readiness_failed_rolled_back':
        return 'settings.errors.ariaReadinessRolledBack'
      case 'config_rollback_failed':
        return 'settings.errors.configRollbackFailed'
      case 'aria2_rollback_failed':
        return 'settings.errors.ariaRollbackFailed'
      default:
        return 'settings.saveFailed'
    }
  })

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
    startNavigation()
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
    if (!configStore.isHydrated || !isInitializedFromBackend || isApplyingCanonical || disposed) {
      return
    }

    const generation = ++editGeneration
    const snapshot = buildCompleteSnapshot()
    clearPendingTimers()
    saveStatus.value = 'saving'

    saveTimeout = setTimeout(async () => {
      try {
        const result = await configStore.updateConfig(snapshot)
        if (disposed || generation !== editGeneration) return
        if (!result.success && result.error_code === 'config_not_loaded') {
          lastErrorCode.value = result.error_code
          saveStatus.value = 'error'
          return
        }
        const canonical = result?.config
        if (!canonical || (!result.success && !canonical.rpc_port)) {
          lastErrorCode.value = result?.error_code || ''
          saveStatus.value = 'error'
          return
        }
        configStore.applyCanonicalConfig(canonical)
        syncForm(canonical)
        if (!result.success) {
          lastErrorCode.value = result.error_code || ''
          saveStatus.value = 'error'
          return
        }
        lastErrorCode.value = ''
        saveStatus.value = 'saved'
        scheduleReset(generation)
      } catch {
        if (import.meta.env.DEV) {
          console.warn('[Settings] save failed')
        }
        if (disposed || generation !== editGeneration) return
        lastErrorCode.value = ''
        saveStatus.value = 'error'
      }
    }, 800)
  }

  const handlePickDirectory = async () => {
    const selected = await configStore.pickDirectory()
    if (!selected || disposed || !configStore.isHydrated || !isInitializedFromBackend) return
    formData.value.download_dir = selected
    triggerSave()
  }

  const retryHydration = () => {
    if (configStore.isLoading) return
    void configStore.fetchConfig()
  }

  onUnmounted(() => {
    disposed = true
    isApplyingCanonical = false
    editGeneration++
    applyGeneration++
    clearPendingTimers()
    stopHydrationWatch()
    stopNavigation()
  })

  const connectionOptions = ['1', '4', '8', '16', '24', '32']

  const uiStore = useUIStore()
  const sections: SettingsNavigationSection[] = [
    { id: 'download', labelKey: 'download.title', icon: FolderOpen },
    { id: 'rpc', labelKey: 'rpc.title', icon: Zap },
    { id: 'performance', labelKey: 'performance.title', icon: Cpu },
    { id: 'user-agent', labelKey: 'settings.navigation.userAgent', icon: Globe },
    { id: 'appearance', labelKey: 'appearance.title', icon: Palette },
    { id: 'advanced', labelKey: 'settings.navigation.advanced', icon: Layers },
    { id: 'extension', labelKey: 'extension.title', icon: Puzzle },
    ...(isExtractorEnabled
      ? [{ id: 'extractor', labelKey: 'extractor.title', icon: Package }]
      : []),
    { id: 'update', labelKey: 'settings.navigation.updates', icon: RefreshCw },
  ]
  const activeSection = ref(sections[0].id)
  const isFloating = ref(false)
  const capsuleTop = ref(32)
  const capsuleSpace = ref(360)
  const sentinelRef = ref<HTMLElement | null>(null)
  const contentRef = ref<HTMLElement | null>(null)
  const scrollContainerRef = ref<HTMLElement | null>(null)
  let sectionElements: HTMLElement[] = []
  let navigationObserver: ResizeObserver | null = null
  let scrollFrame = 0
  const landingOffset = 62

  function updateNavigation() {
    scrollFrame = 0
    const scroller = scrollContainerRef.value
    const sentinel = sentinelRef.value
    if (!scroller || !sentinel) return

    // 1. 批量读取 (Batch Read) - 集中读取布局尺寸，杜绝布局抖动与强制同步重排
    const viewport = scroller.getBoundingClientRect()
    const sentinelTop = sentinel.getBoundingClientRect().top - viewport.top
    const scrollTop = scroller.scrollTop

    const nextFloating = sentinelTop <= 12 && scrollTop > 0
    const nextTop = nextFloating ? 12 : Math.max(12, sentinelTop)
    const availableSpace = Math.max(34, scroller.clientHeight - nextTop - 12)

    let current = sections[0].id
    if (scrollTop > 0) {
      for (const element of sectionElements) {
        if (element.getBoundingClientRect().top - viewport.top > landingOffset + 2) break
        current = element.dataset.settingsSection ?? current
      }
      if (scrollTop + scroller.clientHeight >= scroller.scrollHeight - 2) {
        current = sections[sections.length - 1].id
      }
    }

    // 2. 批量写入 (Batch Write) - 仅在数值改变时才赋值，避免滚动期间逐帧触发 DOM Patch
    if (isFloating.value !== nextFloating) {
      isFloating.value = nextFloating
    }
    if (capsuleTop.value !== nextTop) {
      capsuleTop.value = nextTop
    }
    if (capsuleSpace.value !== availableSpace) {
      capsuleSpace.value = availableSpace
    }
    if (activeSection.value !== current) {
      activeSection.value = current
    }
  }

  function handleScroll() {
    if (!scrollFrame) scrollFrame = requestAnimationFrame(updateNavigation)
  }

  function startNavigation() {
    if (navigationObserver || !scrollContainerRef.value || !contentRef.value || !sentinelRef.value)
      return
    sectionElements = Array.from(
      contentRef.value.querySelectorAll<HTMLElement>('[data-settings-section]'),
    )
    navigationObserver = new ResizeObserver(handleScroll)
    for (const element of [
      scrollContainerRef.value,
      contentRef.value,
      sentinelRef.value,
      ...sectionElements,
    ]) {
      navigationObserver.observe(element)
    }
    updateNavigation()
  }

  function stopNavigation() {
    navigationObserver?.disconnect()
    navigationObserver = null
    cancelAnimationFrame(scrollFrame)
    scrollFrame = 0
    sectionElements = []
  }

  function navigateToSection(id: string) {
    const scroller = scrollContainerRef.value
    const target = sectionElements.find(element => element.dataset.settingsSection === id)
    if (!scroller || !target) return
    const top =
      target.getBoundingClientRect().top -
      scroller.getBoundingClientRect().top +
      scroller.scrollTop -
      landingOffset
    const reducedMotion =
      uiStore.effectsTier === 'reduced' ||
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
    target.focus({ preventScroll: true })
    scroller.scrollTo({ top: Math.max(0, top), behavior: reducedMotion ? 'instant' : 'smooth' })
    handleScroll()
  }

  onActivated(startNavigation)
  onDeactivated(stopNavigation)
</script>

<template>
  <div class="settings-shell relative flex-1 flex flex-col min-h-0 overflow-hidden">
    <!-- Floating Save Status Capsule (Top-Center Dynamic Island) -->
    <SettingsCommandCapsule
      :sections="sections"
      :active-section="activeSection"
      :floating="isFloating"
      :status="showHydrationError ? 'error' : editorsLocked ? 'loading' : saveStatus"
      :error-key="showHydrationError ? 'settings.loadFailed' : saveErrorKey"
      :style="{ '--capsule-top': `${capsuleTop}px`, '--capsule-space': `${capsuleSpace}px` }"
      @navigate="navigateToSection"
    />

    <div
      ref="scrollContainerRef"
      class="relative z-0 flex-1 overflow-y-auto p-6"
      @scroll.passive="handleScroll"
    >
      <div ref="contentRef" class="max-w-2xl mx-auto">
        <!-- Header -->
        <div class="settings-header mb-8">
          <div class="settings-heading flex items-center gap-4">
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
          <div ref="sentinelRef" class="settings-capsule-sentinel" aria-hidden="true"></div>
        </div>

        <div
          v-if="showHydrationError"
          class="mb-4 flex items-center justify-between gap-3 px-4 py-3 rounded-xl bg-[var(--status-error)]/10 border border-[var(--status-error)]/20 animate-fade-in"
          aria-live="polite"
        >
          <div class="flex items-center gap-2.5 min-w-0">
            <AlertCircle :size="15" class="text-[var(--status-error)] shrink-0" />
            <p class="text-xs text-[var(--status-error)] font-medium tracking-tight">
              {{ t('settings.loadFailed') }}
            </p>
          </div>
          <button
            type="button"
            class="retry-hydrate flex items-center gap-1.5 shrink-0 px-3 py-1.5 rounded-lg text-[10px] font-mono-data text-[var(--app-text)] bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:bg-[var(--glass-border)] transition-all duration-200 disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="retryBusy"
            @click="retryHydration"
          >
            <RotateCw
              :size="11"
              class="transition-transform duration-500"
              :class="{ 'animate-spin': retryBusy }"
            />
            <span>{{ t('settings.retry') }}</span>
          </button>
        </div>

        <div class="flex flex-col gap-4">
          <fieldset
            class="min-w-0 flex flex-col gap-4 border-0 p-0 m-0"
            :disabled="editorsLocked"
            :class="{ 'pointer-events-none opacity-60': editorsLocked }"
          >
            <div
              id="settings-section-download"
              data-settings-section="download"
              tabindex="-1"
              :aria-label="t('download.title')"
            >
              <DownloadSection v-model="formData.download_dir" @pick="handlePickDirectory" />
            </div>

            <div
              id="settings-section-rpc"
              data-settings-section="rpc"
              tabindex="-1"
              :aria-label="t('rpc.title')"
            >
              <RPCSection
                v-model:port="formData.rpc_port"
                v-model:secret="formData.rpc_secret"
                @change="triggerSave"
              />
            </div>

            <div
              id="settings-section-performance"
              data-settings-section="performance"
              tabindex="-1"
              :aria-label="t('performance.title')"
            >
              <PerformanceSection
                v-model:connections="formData.max_connections"
                v-model:concurrent-downloads="formData.max_concurrent_downloads"
                v-model:smart-thread-mode="formData.smart_thread_mode"
                :connection-options="connectionOptions"
                @change="triggerSave"
              />
            </div>

            <div
              id="settings-section-user-agent"
              data-settings-section="user-agent"
              tabindex="-1"
              :aria-label="t('settings.navigation.userAgent')"
            >
              <UASection v-model="formData.user_agent" @change="triggerSave" />
            </div>
          </fieldset>

          <div
            id="settings-section-appearance"
            data-settings-section="appearance"
            tabindex="-1"
            :aria-label="t('appearance.title')"
          >
            <AppearanceSection />
          </div>

          <fieldset
            class="min-w-0 flex flex-col gap-4 border-0 p-0 m-0"
            :disabled="editorsLocked"
            :class="{ 'pointer-events-none opacity-60': editorsLocked }"
          >
            <div
              id="settings-section-advanced"
              data-settings-section="advanced"
              tabindex="-1"
              :aria-label="t('settings.navigation.advanced')"
            >
              <AdvancedSection
                v-model:transparency="formData.window_transparency"
                v-model:show-history="formData.show_history"
                @change="triggerSave"
              />
            </div>
          </fieldset>

          <div
            id="settings-section-extension"
            data-settings-section="extension"
            tabindex="-1"
            :aria-label="t('extension.title')"
          >
            <ExtensionSection />
          </div>

          <div
            v-if="ExtractorSection"
            id="settings-section-extractor"
            data-settings-section="extractor"
            tabindex="-1"
            :aria-label="t('extractor.title')"
          >
            <component :is="ExtractorSection" />
          </div>

          <div
            id="settings-section-update"
            data-settings-section="update"
            tabindex="-1"
            :aria-label="t('settings.navigation.updates')"
          >
            <UpdateSection />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
  .settings-shell {
    container-type: inline-size;
  }

  .settings-header {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 164px minmax(0, 1fr);
    align-items: center;
    column-gap: 16px;
  }

  .settings-heading > div:first-child {
    flex-shrink: 0;
  }

  .settings-capsule-sentinel {
    width: 164px;
    height: 34px;
    justify-self: center;
  }

  [data-settings-section] {
    scroll-margin-top: 62px;
    border-radius: var(--radius-squircle-lg);
  }

  [data-settings-section]:focus-visible {
    outline: 2px solid color-mix(in srgb, var(--neon-primary) 40%, transparent);
    outline-offset: 3px;
  }

  @container (max-width: 620px) {
    .settings-header {
      grid-template-columns: 1fr;
      row-gap: 18px;
    }
  }
</style>
