<script setup lang="ts">
  import { onMounted, onUnmounted, ref, watch, defineAsyncComponent } from 'vue'
  import Sidebar from './components/layout/Sidebar.vue'
  import TitleBar from './components/layout/TitleBar.vue'
  import TaskList from './components/tasks/TaskList.vue'
  import SettingsPanel from './components/settings/SettingsPanel.vue'
  import { useUIStore } from './stores/ui'
  import { useConfigStore } from './stores/config'
  import { useTaskStore } from './stores/task'
  import { subscribeToWindowEvents, unsubscribeFromWindowEvents } from './stores/events'
  import { Clipboard, Events } from '@wailsio/runtime'

  const DebugPanel = import.meta.env.DEV
    ? defineAsyncComponent(() => import('./components/debug/DebugPanel.vue'))
    : null

  const TestSimulator = import.meta.env.DEV
    ? defineAsyncComponent(() => import('./components/debug/TestSimulator.vue'))
    : null

  const uiStore = useUIStore()
  const configStore = useConfigStore()
  const taskStore = useTaskStore()
  const isReady = ref(false)
  const showTestSimulator = ref(window.location.hash.includes('test-simulator'))
  const unsubs: Array<() => void> = []

  let lastClipboardCandidate = ''
  let lastClipboardCandidateAt = 0
  let lastTasksRefreshAt = 0

  const isValidUrl = (text: string): boolean => {
    return /^(https?|ftp|sftp|magnet):/i.test(text)
  }

  const isDuplicateUri = (uri: string): boolean => {
    const needle = uri.trim()
    if (!needle) return false
    for (const list of [taskStore.activeTasks, taskStore.waitingTasks, taskStore.stoppedTasks]) {
      for (const t of list) {
        for (const f of t.files || []) {
          for (const u of f.uris || []) {
            if (u?.uri === needle) return true
          }
        }
      }
    }
    return false
  }

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

    // Global initialization: fetch config from Go backend
    await configStore.fetchConfig()

    // Apply window transparency state to CSS (required for acrylic/mica to be visible)
    applyWindowTransparency()

    // Reactively apply on change (settings panel saves async)
    stopWindowTransparencyWatch = watch(
      () => getWindowTransparency(),
      () => {
        applyWindowTransparency()
      },
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
    subscribeToWindowEvents(() => {
      // Window just created, sync state from backend snapshot
      taskStore.syncFromSnapshot()
    })

    // Centralized Clipboard Processing Logic
    const processClipboard = async (trigger: 'auto' | 'manual') => {
      try {
        const text = (await Clipboard.Text()).trim()
        if (!text) return
        if (!isValidUrl(text)) return

        // 1. Check against history (Deduplication for Auto-Trigger)
        // If triggered automatically (Focus), we ONLY act if the content has changed.
        // If triggered manually (Tab Switch), we allow re-processing (user might want to paste what they have).
        if (trigger === 'auto' && text === lastClipboardCandidate) {
          return
        }

        // Update history
        lastClipboardCandidate = text
        lastClipboardCandidateAt = Date.now()

        // 2. Refresh tasks if stale (to ensure duplicate check is accurate)
        const now = Date.now()
        if (now - lastTasksRefreshAt > 1500) {
          lastTasksRefreshAt = now
          await taskStore.fetchTasks()
        }

        // 3. Check for duplicates in existing tasks
        if (isDuplicateUri(text)) return

        // 4. Action
        if (trigger === 'auto') {
          // For auto-trigger, we jump to the downloads tab
          if (uiStore.activeTab !== 'downloads') {
            uiStore.setActiveTab('downloads')
          }
        }

        // If we are (or became) on the downloads tab, populate the field
        if (uiStore.activeTab === 'downloads') {
          uiStore.setPendingPasteUri(text)
        }
      } catch {
        // Permission denied or empty clipboard, silently ignore
      }
    }

    // Trigger 1: Window Focus (Smart Auto-Detection)
    unsubs.push(
      Events.On('common:WindowFocus', () => {
        processClipboard('auto')
      }),
    )

    // Trigger 2: Tab Switch (Context-Aware Detection)
    // When user manually enters Downloads tab, check clipboard
    // Reactively show/hide test simulator based on URL hash
    const updateSimulatorVisibility = () => {
      showTestSimulator.value = window.location.hash.includes('test-simulator')
    }
    window.addEventListener('hashchange', updateSimulatorVisibility)
    unsubs.push(() => window.removeEventListener('hashchange', updateSimulatorVisibility))

    watch(
      () => uiStore.activeTab,
      newTab => {
        if (newTab === 'downloads') {
          // Use 'manual' mode to be more permissive (allow current clipboard even if seen before)
          // But 'processClipboard' updates 'lastClipboardCandidate', so it syncs up.
          processClipboard('manual')
        }
      },
    )
  })

  onUnmounted(() => {
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    // Clean up Wails event listeners
    unsubs.forEach(unsub => unsub())
    // Clean up window lifecycle events
    unsubscribeFromWindowEvents()

    if (stopWindowTransparencyWatch) {
      stopWindowTransparencyWatch()
      stopWindowTransparencyWatch = null
    }
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
  <component v-if="TestSimulator && showTestSimulator" :is="TestSimulator" />

  <!-- Noise texture overlay for depth -->
  <div class="noise-overlay"></div>

  <div
    class="flex flex-col h-screen bg-[var(--color-app-bg)] text-[var(--color-app-text)] overflow-hidden"
  >
    <!-- Custom Frameless Titlebar -->
    <TitleBar />

    <!-- Main Layout Container -->
    <div class="flex flex-1 min-h-0">
      <!-- Sidebar: Fused with background, low contrast -->
      <Sidebar />

      <!-- Floating Glass Content Panel -->
      <main class="flex-1 flex flex-col min-w-0 p-3 pr-3 pb-3">
        <div
          :class="[
            'flex-1 flex flex-col min-h-0 glass-panel rounded-[var(--radius-squircle-xl)] overflow-hidden',
            'transition-opacity transition-transform duration-700 ease-out',
            isReady ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4',
          ]"
        >
          <!-- Content Area with KeepAlive to prevent remount on tab switch -->
          <KeepAlive>
            <component
              :is="uiStore.activeTab === 'settings' ? SettingsPanel : TaskList"
              :tab="uiStore.activeTab"
            />
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
</style>
