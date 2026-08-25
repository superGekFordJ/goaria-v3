<script setup lang="ts">
  import { computed } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { System } from '@wailsio/runtime'
  import { useUIStore } from '../../stores/ui'
  import { useConfigStore } from '../../stores/config'
  import { useTaskStore } from '../../stores/task'
  import { useDownloadGroupStore } from '../../stores/downloadGroups'
  import { Download, CheckCircle, Settings as SettingsIcon, Activity } from '@lucide/vue'
  import ThemeIcon from '../common/ThemeIcon.vue'
  import LiquidGlassPanel from '../common/LiquidGlassPanel.vue'
  import StaticGlassPanel from '../common/StaticGlassPanel.vue'

  const { t } = useI18n()
  const uiStore = useUIStore()
  const configStore = useConfigStore()
  const taskStore = useTaskStore()
  const downloadGroupStore = useDownloadGroupStore()
  const isMac = System.IsMac()

  // Navigation items with dynamic counts
  const navItems = computed(() => [
    {
      id: 'downloads',
      name: t('sidebar.inProgress'),
      icon: Download,
      count: downloadGroupStore.inlineDownloadsCount,
      accent: 'cyan',
    },
    {
      id: 'stopped',
      name: t('sidebar.completed'),
      icon: CheckCircle,
      count: downloadGroupStore.inlineCompletedCount,
      accent: 'green',
    },
  ])

  function handleNavClick(itemId: string) {
    uiStore.setActiveTab(itemId)
  }

  // Calculate total download speed
  const totalSpeed = computed(() => {
    const bytes = taskStore.activeTasks.reduce((sum, t) => sum + Number(t.downloadSpeed || 0), 0)
    if (bytes === 0) return '0 B/s'
    const i = Math.floor(Math.log(bytes) / Math.log(1024))
    return (bytes / Math.pow(1024, i)).toFixed(1) + ' ' + ['B', 'KB', 'MB', 'GB', 'TB'][i] + '/s'
  })
</script>

<template>
  <aside
    class="sidebar-container w-56 flex flex-col shrink-0 z-20 bg-[var(--sidebar-bg)] border-r border-[var(--card-border)]"
  >
    <!-- Logo Section (Draggable window anchor; on macOS, top padding ensures safe clearance for native traffic lights) -->
    <div
      :class="[
        'px-5 flex shrink-0 select-none',
        isMac ? 'h-32 pt-8 pb-3 items-end' : 'h-28 items-center',
      ]"
      style="--wails-draggable: drag"
    >
      <div class="flex items-center gap-3.5 group cursor-default select-none">
        <ThemeIcon :size="42" />
        <div class="flex flex-col">
          <span
            class="text-[17px] font-extrabold tracking-tight leading-none text-[var(--app-text)]/90"
          >
            GoAria
          </span>
          <span
            class="sidebar-brand-text text-[9.5px] font-mono-data font-bold text-[var(--neon-primary)] tracking-wider leading-none mt-1.5 flex items-baseline gap-1"
          >
            <span>SURGE</span>
            <span class="text-[10.5px] font-normal tracking-normal leading-none select-none"
              >𝓥𝓮𝓻.</span
            >
          </span>
        </div>
      </div>
    </div>

    <!-- Live Stats Card -->
    <div class="px-4 mb-4">
      <StaticGlassPanel class="p-4" radius="rounded-[var(--radius-squircle-md)]">
        <div class="flex flex-col space-y-3 relative z-10">
          <div class="flex items-center gap-2">
            <Activity :size="12" class="text-[var(--neon-primary)]/60" />
            <span
              class="text-[9px] font-bold uppercase tracking-[0.15em] text-[var(--app-text-subtle)]"
            >
              {{ t('sidebar.liveSpeed') }}
            </span>
          </div>
          <div class="font-mono-data text-xl font-bold text-neon leading-none">
            {{ totalSpeed }}
          </div>
        </div>
      </StaticGlassPanel>
    </div>

    <!-- Navigation -->
    <nav class="flex-1 px-3 space-y-1">
      <LiquidGlassPanel
        v-for="item in navItems"
        :key="item.id"
        as="button"
        :active="uiStore.activeTab === item.id"
        :interactive="true"
        :class="[
          'w-full transition-all duration-300 group',
          uiStore.activeTab === item.id ? '' : 'hover:bg-[var(--sidebar-hover)]',
        ]"
        @click="handleNavClick(item.id)"
      >
        <div class="w-full h-full flex items-center justify-between px-4 py-3">
          <div class="flex items-center gap-3">
            <!-- Icon with conditional neon glow -->
            <div
              :class="[
                'w-8 h-8 rounded-xl flex items-center justify-center transition-all duration-300',
                uiStore.activeTab === item.id
                  ? 'bg-[var(--neon-primary)]/10 text-[var(--neon-primary)]'
                  : 'bg-[var(--btn-glass-bg)] text-[var(--app-text-muted)] group-hover:text-[var(--app-text)]/60',
              ]"
            >
              <component :is="item.icon" :size="16" />
            </div>
            <span
              :class="[
                'text-sm font-semibold transition-colors duration-300',
                uiStore.activeTab === item.id
                  ? 'text-[var(--app-text)]/90'
                  : 'text-[var(--app-text-muted)] group-hover:text-[var(--app-text)]/60',
              ]"
            >
              {{ item.name }}
            </span>
          </div>

          <!-- Task count badge -->
          <div
            v-if="item.count > 0"
            :class="[
              'min-w-[24px] h-6 px-2 rounded-lg flex items-center justify-center font-mono-data text-xs font-bold transition-all duration-300',
              uiStore.activeTab === item.id
                ? 'bg-[var(--neon-primary)]/20 text-[var(--neon-primary)]'
                : 'bg-[var(--btn-glass-bg)] text-[var(--app-text-subtle)]',
            ]"
          >
            {{ item.count }}
          </div>
        </div>
      </LiquidGlassPanel>

      <!-- Divider -->
      <div class="py-3 px-2">
        <div class="divider-glow"></div>
      </div>

      <!-- Settings Button -->
      <LiquidGlassPanel
        as="button"
        :active="uiStore.activeTab === 'settings'"
        :interactive="true"
        :class="[
          'w-full transition-all duration-300 group',
          uiStore.activeTab === 'settings' ? '' : 'hover:bg-[var(--sidebar-hover)]',
        ]"
        @click="uiStore.setActiveTab('settings')"
      >
        <div class="w-full h-full flex items-center gap-3 px-4 py-3">
          <div
            :class="[
              'w-8 h-8 rounded-xl flex items-center justify-center transition-all duration-300',
              uiStore.activeTab === 'settings'
                ? 'bg-[var(--btn-glass-hover)] text-[var(--app-text)]/80'
                : 'bg-[var(--btn-glass-bg)] text-[var(--app-text-muted)] group-hover:text-[var(--app-text)]/60',
            ]"
          >
            <SettingsIcon :size="16" />
          </div>
          <span
            :class="[
              'text-sm font-semibold transition-colors duration-300',
              uiStore.activeTab === 'settings'
                ? 'text-[var(--app-text)]/80'
                : 'text-[var(--app-text-muted)] group-hover:text-[var(--app-text)]/60',
            ]"
          >
            {{ t('sidebar.settings') }}
          </span>
        </div>
      </LiquidGlassPanel>
    </nav>

    <!-- Dual Engine Status Footer -->
    <div class="p-4 mt-auto">
      <StaticGlassPanel class="p-3" radius="rounded-[var(--radius-squircle-md)]">
        <div class="flex flex-col space-y-1.5 relative z-10 select-none">
          <!-- Surge (In-Process Native) -->
          <div class="flex items-center justify-between text-[11px]">
            <div class="flex items-center gap-2 text-[var(--app-text-subtle)]">
              <div class="w-1.5 h-1.5 rounded-full bg-[var(--status-active)]"></div>
              <span class="font-medium">Surge</span>
            </div>
            <span class="text-[10px] text-[var(--app-text-subtle)]/60">
              {{ t('sidebar.surgeReady') }}
            </span>
          </div>

          <!-- Aria2 (External Daemon) -->
          <div class="flex items-center justify-between text-[11px]">
            <div class="flex items-center gap-2 text-[var(--app-text-subtle)]">
              <div
                :class="[
                  'w-1.5 h-1.5 rounded-full transition-colors duration-300',
                  configStore.aria2Connected
                    ? 'bg-[var(--status-active)]'
                    : 'bg-[var(--status-error)]',
                ]"
              ></div>
              <span class="font-medium">Aria2</span>
            </div>
            <span
              :class="[
                'text-[10px] font-mono-data transition-colors duration-300',
                configStore.aria2Connected
                  ? 'text-[var(--app-text-subtle)]/60'
                  : 'text-[var(--status-error)] font-medium',
              ]"
            >
              {{
                configStore.aria2Connected
                  ? configStore.settings.rpc_port
                  : t('sidebar.aria2Offline')
              }}
            </span>
          </div>
        </div>
      </StaticGlassPanel>
    </div>
  </aside>
</template>
