<script setup lang="ts">
  import { ref, onMounted } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Minus, Square, X, PanelBottomClose } from '@lucide/vue'
  import { Window, Application, System } from '@wailsio/runtime'
  import { useTaskStore } from '../../stores/task'

  const { t } = useI18n()
  const taskStore = useTaskStore()
  const isMac = System.IsMac()

  const isMaximized = ref(false)

  // Check initial maximized state on non-Mac platforms
  onMounted(async () => {
    if (isMac) return
    try {
      isMaximized.value = await Window.IsMaximised()
    } catch {
      // Ignore errors during development
    }
  })

  const handleMinimize = () => {
    Window.Minimise()
  }

  const handleMaximize = async () => {
    Window.ToggleMaximise()
    // Update state after toggle
    setTimeout(async () => {
      try {
        isMaximized.value = await Window.IsMaximised()
      } catch {
        isMaximized.value = !isMaximized.value
      }
    }, 100)
  }

  const handleClose = () => {
    Application.Quit()
  }

  // Minimize to system tray (true headless mode - destroys window)
  const handleHideToTray = () => {
    taskStore.minimizeToTray()
  }
</script>

<template>
  <header
    class="h-10 flex items-center justify-between px-4 bg-transparent relative z-50 shrink-0 select-none"
    style="--wails-draggable: drag"
  >
    <!-- Right: Window Controls (non-draggable, hidden on macOS as native traffic lights handle close/zoom) -->
    <div v-if="!isMac" class="flex items-center gap-0.5 ml-auto" style="--wails-draggable: no-drag">
      <!-- Minimize to Tray Button -->
      <button
        class="group w-9 h-9 flex items-center justify-center rounded-lg transition-all duration-200 hover:bg-[var(--neon-primary)]/10 active:bg-[var(--neon-primary)]/20"
        :title="t('titleBar.minimizeToTray')"
        @click="handleHideToTray"
      >
        <PanelBottomClose
          :size="14"
          class="text-[var(--app-text-subtle)] group-hover:text-[var(--neon-primary)] transition-colors duration-200"
        />
      </button>

      <!-- Minimize Button -->
      <button
        class="group w-9 h-9 flex items-center justify-center rounded-lg transition-all duration-200 hover:bg-[var(--btn-glass-hover)] active:bg-[var(--sidebar-active)]"
        :title="t('titleBar.minimize')"
        @click="handleMinimize"
      >
        <Minus
          :size="14"
          class="text-[var(--app-text-subtle)] group-hover:text-[var(--app-text)]/70 transition-colors duration-200"
        />
      </button>

      <!-- Maximize/Restore Button -->
      <button
        class="group w-9 h-9 flex items-center justify-center rounded-lg transition-all duration-200 hover:bg-[var(--btn-glass-hover)] active:bg-[var(--sidebar-active)]"
        :title="isMaximized ? t('titleBar.restore') : t('titleBar.maximize')"
        @click="handleMaximize"
      >
        <Square
          :size="11"
          :stroke-width="2"
          :class="[
            'transition-colors duration-200',
            isMaximized
              ? 'text-[var(--neon-primary)]/50 group-hover:text-[var(--neon-primary)]'
              : 'text-[var(--app-text-subtle)] group-hover:text-[var(--app-text)]/70',
          ]"
        />
      </button>

      <!-- Close Button -->
      <button
        class="group w-9 h-9 flex items-center justify-center rounded-lg transition-all duration-200 hover:bg-[var(--status-error)]/20 active:bg-[var(--status-error)]/30"
        :title="t('titleBar.close')"
        @click="handleClose"
      >
        <X
          :size="14"
          class="text-[var(--app-text-subtle)] group-hover:text-[var(--status-error)] transition-colors duration-200"
        />
      </button>
    </div>
  </header>
</template>

<style scoped>
  /* Ensure buttons don't interfere with drag */
  button {
    -webkit-app-region: no-drag;
    app-region: no-drag;
  }

  /* Hover effects animation */
  button:active {
    transform: scale(0.95);
  }
</style>
