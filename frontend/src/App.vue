<script setup lang="ts">
  import { onMounted, onUnmounted, ref } from 'vue'
  import Sidebar from './components/layout/Sidebar.vue'
  import TitleBar from './components/layout/TitleBar.vue'
  import TaskList from './components/tasks/TaskList.vue'
  import SettingsPanel from './components/settings/SettingsPanel.vue'
  import { useUIStore } from './stores/ui'
  import { useConfigStore } from './stores/config'
  import { useTaskStore } from './stores/task'
  import { Events } from '@wailsio/runtime'

  const uiStore = useUIStore()
  const configStore = useConfigStore()
  const taskStore = useTaskStore()
  const isReady = ref(false)
  const unsubs: Array<() => void> = []

  onMounted(async () => {
    // Global initialization: fetch config from Go backend
    await configStore.fetchConfig()
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
    unsubs.push(Events.On('common:WindowFocus', () => taskStore.setWindowVisibility(true)))
  })

  onUnmounted(() => {
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    // Clean up Wails event listeners
    unsubs.forEach(unsub => unsub())
  })

  // Pause polling when window is hidden to save CPU and reduce log growth
  const handleVisibilityChange = () => {
    const isVisible = document.visibilityState === 'visible'
    taskStore.setWindowVisibility(isVisible)
  }
</script>

<template>
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
            'transition-all duration-700 ease-out',
            isReady ? 'opacity-100 translate-y-0' : 'opacity-0 translate-y-4',
          ]"
        >
          <!-- Content Area with smooth transitions -->
          <Transition name="panel-fade" mode="out-in">
            <SettingsPanel v-if="uiStore.activeTab === 'settings'" key="settings" />
            <TaskList v-else :key="uiStore.activeTab" />
          </Transition>
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
