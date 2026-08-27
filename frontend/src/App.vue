<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref, watch, defineAsyncComponent } from 'vue'
  import Sidebar from './components/layout/Sidebar.vue'
  import TitleBar from './components/layout/TitleBar.vue'
  import TaskList from './components/tasks/TaskList.vue'
  import DownloadGroupShell from './components/groups/DownloadGroupShell.vue'
  import SettingsPanel from './components/settings/SettingsPanel.vue'
  import { useUIStore } from './stores/ui'
  import { useConfigStore } from './stores/config'
  import { useTaskStore } from './stores/task'
  import { useDownloadGroupStore } from './stores/downloadGroups'
  import { subscribeToWindowEvents, unsubscribeFromWindowEvents } from './stores/events'
  import { Events } from '@wailsio/runtime'
  import { useSmartInput } from './composables/useSmartInput'
  import { Download } from '@lucide/vue'
  import { useI18n } from 'vue-i18n'

  const DebugPanel = import.meta.env.DEV
    ? defineAsyncComponent(() => import('./components/debug/DebugPanel.vue'))
    : null

  const TestSimulator = import.meta.env.DEV
    ? defineAsyncComponent(() => import('./components/debug/TestSimulator.vue'))
    : null

  const uiStore = useUIStore()
  const configStore = useConfigStore()
  const taskStore = useTaskStore()
  const downloadGroupStore = useDownloadGroupStore()
  const { t } = useI18n()
  const isReady = ref(false)
  const showTestSimulator = ref(window.location.hash.includes('test-simulator'))
  const unsubs: Array<() => void> = []

  const { isDragging, initSmartInput, cleanupSmartInput } = useSmartInput()

  const isDownloadGroupDetailVisible = computed(
    () =>
      Boolean(uiStore.selectedDownloadGroupKey) &&
      (uiStore.activeTab === 'downloads' || uiStore.activeTab === 'stopped'),
  )

  const activeContent = computed(() => {
    if (uiStore.activeTab === 'settings') return SettingsPanel
    if (isDownloadGroupDetailVisible.value) return DownloadGroupShell
    return TaskList
  })

  const getWindowTransparency = (): string => {
    const s = configStore.settings as unknown as { window_transparency?: string }
    return s.window_transparency || 'none'
  }

  const applyWindowTransparency = () => {
    document.documentElement.setAttribute('data-window-transparency', getWindowTransparency())
  }

  let stopWindowTransparencyWatch: (() => void) | null = null

  onMounted(async () => {
    // Initialize theme and skin from persisted state
    uiStore.initTheme()
    uiStore.initLocale()

    // Global initialization: fetch config from Go backend
    await configStore.fetchConfig()

    stopWindowTransparencyWatch = watch(
      () => [configStore.isHydrated, getWindowTransparency()] as const,
      ([hydrated]) => {
        if (hydrated) {
          applyWindowTransparency()
        }
      },
      { immediate: true },
    )

    // Small delay for smooth entrance animation
    setTimeout(() => {
      isReady.value = true
    }, 100)

    // Listen to page visibility changes (Browser tab switching)
    document.addEventListener('visibilitychange', handleVisibilityChange)

    // Listen to Wails Window events (Minimize, Hide to Tray, Restore)
    // Common events cover Windows/Mac/Linux
    unsubs.push(Events.On('common:WindowHide', () => taskStore.setWindowVisibility(false)))
    unsubs.push(Events.On('common:WindowMinimise', () => taskStore.setWindowVisibility(false)))
    unsubs.push(Events.On('common:WindowShow', () => taskStore.setWindowVisibility(true)))
    unsubs.push(Events.On('common:WindowUnMinimise', () => taskStore.setWindowVisibility(true)))
    unsubs.push(Events.On('common:WindowRestore', () => taskStore.setWindowVisibility(true)))

    // Listen for window creation events (for recovery from headless mode)
    subscribeToWindowEvents(async () => {
      // Window just created, sync state from backend snapshot
      await taskStore.syncFromSnapshot()
      await downloadGroupStore.syncAfterSnapshot(uiStore.selectedDownloadGroupKey)
    })

    // Init Smart Input (Clipboard & Drag-Drop logic)
    initSmartInput()

    // Reactively show/hide test simulator based on URL hash
    const updateSimulatorVisibility = () => {
      showTestSimulator.value = window.location.hash.includes('test-simulator')
    }
    window.addEventListener('hashchange', updateSimulatorVisibility)
    unsubs.push(() => window.removeEventListener('hashchange', updateSimulatorVisibility))

    // Start global polling for task status (required for Sidebar and Tray syncing)
    taskStore.startPolling(1000)
    downloadGroupStore.startAutoSync()

    void downloadGroupStore.fetchGroups()
  })

  onUnmounted(() => {
    cleanupSmartInput()
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    // Clean up Wails event listeners
    unsubs.forEach(unsub => unsub())
    // Clean up window lifecycle events
    unsubscribeFromWindowEvents()

    if (stopWindowTransparencyWatch) {
      stopWindowTransparencyWatch()
      stopWindowTransparencyWatch = null
    }

    downloadGroupStore.stopAutoSync()
    taskStore.stopPolling(true)
  })

  // Pause polling when window is hidden to save CPU and reduce log growth
  const handleVisibilityChange = () => {
    const isVisible = document.visibilityState === 'visible'
    taskStore.setWindowVisibility(isVisible)
  }
</script>

<template>
  <!-- Debug Panel (dev-only; activate with #debug in URL) -->
  <component :is="DebugPanel" v-if="DebugPanel" />

  <!-- Test Simulator (dev-only; activate with #test-simulator in URL) -->
  <component :is="TestSimulator" v-if="TestSimulator && showTestSimulator" />

  <!-- Noise texture overlay for depth -->
  <div class="noise-overlay"></div>

  <!-- Drag-over overlay -->
  <Transition name="drop-overlay">
    <div v-if="isDragging" class="drop-overlay">
      <div class="drop-overlay-inner">
        <Download :size="32" class="drop-overlay-icon" />
        <p class="drop-overlay-text">{{ t('drop.releaseToAdd') }}</p>
        <p class="drop-overlay-hint">{{ t('drop.supportedFormats') }}</p>
      </div>
    </div>
  </Transition>

  <div class="flex h-screen bg-[var(--color-app-bg)] text-[var(--color-app-text)] overflow-hidden">
    <!-- Sidebar: Full Height -->
    <Sidebar />

    <!-- Main Layout Container (Right Column) -->
    <div class="flex-1 flex flex-col min-w-0 min-h-0">
      <!-- Custom Frameless Titlebar -->
      <TitleBar />

      <!-- Floating Glass Content Panel -->
      <main class="flex-1 flex flex-col min-w-0 px-3 pb-3 min-h-0">
        <div
          :class="[
            'flex-1 flex flex-col min-h-0 glass-panel rounded-[var(--radius-squircle-xl)] overflow-hidden',
            'transition-opacity transition-transform duration-700 ease-out',
            isReady ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4',
          ]"
        >
          <!-- Content Area with KeepAlive to prevent remount on tab switch -->
          <KeepAlive>
            <component :is="activeContent" :tab="uiStore.activeTab" />
          </KeepAlive>
        </div>
      </main>
    </div>
  </div>
</template>

<style>
  /* Panel transition animations */
  .panel-fade-enter-active {
    animation: fade-in-up 0.4s cubic-bezier(0.16, 1, 0.3, 1) both;
  }

  .panel-fade-leave-active {
    animation: fade-out 0.2s ease-out both;
  }

  @keyframes fade-out {
    from {
      opacity: 1;
    }
    to {
      opacity: 0;
    }
  }

  /* Global animation library for components */
  @keyframes shimmer {
    0% {
      transform: translateX(-100%);
    }
    100% {
      transform: translateX(100%);
    }
  }

  .animate-shimmer {
    animation: shimmer 2s infinite;
  }

  .animate-in {
    animation-fill-mode: both;
  }

  @keyframes fade-in {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  .fade-in {
    animation-name: fade-in;
  }

  @keyframes slide-in-bottom {
    from {
      transform: translateY(20px);
      opacity: 0;
    }
    to {
      transform: translateY(0);
      opacity: 1;
    }
  }

  .slide-in-from-bottom-4 {
    animation-name: slide-in-bottom;
  }

  @keyframes zoom-in-95 {
    from {
      transform: scale(0.95);
      opacity: 0;
    }
    to {
      transform: scale(1);
      opacity: 1;
    }
  }

  .zoom-in-95 {
    animation-name: zoom-in-95;
  }

  /* Drag-over overlay */
  .drop-overlay {
    position: fixed;
    inset: 0;
    z-index: 9999;
    display: flex;
    align-items: center;
    justify-content: center;
    background: color-mix(in srgb, var(--app-bg) 70%, transparent);
    backdrop-filter: blur(12px);
    pointer-events: none;
  }

  .drop-overlay-inner {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 12px;
    padding: 40px 60px;
    border-radius: var(--radius-squircle-xl);
    border: 2px dashed color-mix(in srgb, var(--neon-primary) 50%, transparent);
    background: color-mix(in srgb, var(--neon-primary) 5%, transparent);
    box-shadow: 0 0 40px color-mix(in srgb, var(--neon-primary) 15%, transparent);
  }

  .drop-overlay-icon {
    color: var(--neon-primary);
  }

  .drop-overlay-text {
    font-size: 16px;
    font-weight: 600;
    color: var(--app-text);
  }

  .drop-overlay-hint {
    font-size: 12px;
    color: var(--app-text-subtle);
  }

  /* Reduced effects: no blur, instant transition */
  [data-effects='reduced'] .drop-overlay {
    backdrop-filter: none;
  }

  /* Transition */
  .drop-overlay-enter-active {
    transition: opacity 0.25s ease;
  }
  .drop-overlay-leave-active {
    transition: opacity 0.15s ease;
  }
  .drop-overlay-enter-from,
  .drop-overlay-leave-to {
    opacity: 0;
  }
</style>
