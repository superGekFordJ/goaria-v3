<script setup lang="ts">
  import { computed } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { useUIStore } from '../../stores/ui'
  import { useConfigStore } from '../../stores/config'
  import { useTaskStore } from '../../stores/task'
  import { useDownloadGroupStore } from '../../stores/downloadGroups'
  import { Download, CheckCircle, Settings as SettingsIcon, Activity } from 'lucide-vue-next'
  import ThemeIcon from '../common/ThemeIcon.vue'
  import LiquidGlassPanel from '../common/LiquidGlassPanel.vue'
  import StaticGlassPanel from '../common/StaticGlassPanel.vue'

  const { t } = useI18n()
  const uiStore = useUIStore()
  const configStore = useConfigStore()
  const taskStore = useTaskStore()
  const downloadGroupStore = useDownloadGroupStore()

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

  const isConnected = computed(() => true) // Could be dynamic based on RPC status
</script>

<template>
  <aside
    class="sidebar-container w-56 flex flex-col shrink-0 z-20 bg-[var(--sidebar-bg)] border-r border-[var(--card-border)]"
  >
    <!-- Logo Section -->
    <div class="p-6 pt-2">
      <div class="flex items-center gap-3 group cursor-default select-none">
        <ThemeIcon :size="40" />
        <div class="flex flex-col">
          <span class="text-base font-black tracking-tight leading-none text-[var(--app-text)]/90">
            GoAria
          </span>
          <span
            class="sidebar-brand-text text-[9px] font-mono-data font-bold text-[var(--neon-primary)] tracking-widest mt-0.5"
          >
            LUMINOUS
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
          uiStore.activeTab === item.id
            ? 'border border-[var(--card-border)]'
            : 'hover:bg-[var(--sidebar-hover)] border border-transparent',
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
          uiStore.activeTab === 'settings'
            ? 'border border-[var(--card-border)]'
            : 'hover:bg-[var(--sidebar-hover)] border border-transparent',
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

    <!-- Connection Status Footer -->
    <div class="p-4 mt-auto">
      <StaticGlassPanel class="p-4" radius="rounded-[var(--radius-squircle-md)]">
        <div class="flex flex-col space-y-2 relative z-10">
          <div class="flex items-center gap-2">
            <!-- Status indicator dot with glow -->
            <div class="relative">
              <div
                :class="[
                  'w-2 h-2 rounded-full transition-colors',
                  isConnected ? 'bg-[var(--status-active)]' : 'bg-[var(--status-error)]',
                ]"
              ></div>
              <div
                v-if="isConnected"
                class="absolute inset-0 w-2 h-2 rounded-full bg-[var(--status-active)] animate-ping opacity-50"
              ></div>
            </div>
            <span
              class="text-[9px] font-bold uppercase tracking-[0.15em] text-[var(--app-text-subtle)]"
            >
              {{ isConnected ? t('sidebar.aria2Online') : t('sidebar.aria2Offline') }}
            </span>
          </div>
          <div class="font-mono-data text-[10px] text-[var(--app-text-subtle)] truncate">
            127.0.0.1:{{ configStore.settings.rpc_port }}
          </div>
        </div>
      </StaticGlassPanel>
    </div>
  </aside>
</template>
