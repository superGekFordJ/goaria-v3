<script setup lang="ts">
  import { computed } from 'vue'
  import { useUIStore } from '../../stores/ui'
  import { useConfigStore } from '../../stores/config'
  import { useTaskStore } from '../../stores/task'
  import { Download, CheckCircle, Settings as SettingsIcon, Zap, Activity } from 'lucide-vue-next'

  const uiStore = useUIStore()
  const configStore = useConfigStore()
  const taskStore = useTaskStore()

  // Navigation items with dynamic counts
  const navItems = computed(() => [
    {
      id: 'downloads',
      name: '进行中',
      icon: Download,
      count: taskStore.activeTasks.length + taskStore.waitingTasks.length,
      accent: 'cyan',
    },
    {
      id: 'stopped',
      name: '已完成',
      icon: CheckCircle,
      count: taskStore.stoppedTasks.length,
      accent: 'green',
    },
  ])

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
    class="w-56 flex flex-col shrink-0 z-20 bg-[var(--sidebar-bg)] border-r border-[var(--card-border)]"
  >
    <!-- Logo Section -->
    <div class="p-6 pt-2">
      <div class="flex items-center gap-3 group cursor-default select-none">
        <div
          class="w-10 h-10 rounded-[var(--radius-squircle-sm)] flex items-center justify-center relative overflow-hidden"
        >
          <!-- Animated gradient background -->
          <div
            class="absolute inset-0 bg-gradient-to-br from-[var(--neon-primary)] to-[var(--neon-secondary)] opacity-90"
          ></div>
          <div
            class="absolute inset-0 bg-gradient-to-tr from-transparent via-white/20 to-transparent animate-shimmer"
          ></div>
          <Zap class="relative text-[var(--app-bg)] fill-current" :size="20" />
        </div>
        <div class="flex flex-col">
          <span class="text-base font-black tracking-tight leading-none text-[var(--app-text)]/90">
            GoAria
          </span>
          <span
            class="text-[9px] font-mono-data font-bold text-[var(--neon-primary)]/70 tracking-widest mt-0.5"
          >
            LUMINOUS
          </span>
        </div>
      </div>
    </div>

    <!-- Live Stats Card -->
    <div class="px-4 mb-4">
      <div class="glass-panel-subtle rounded-[var(--radius-squircle-md)] p-4 space-y-3">
        <div class="flex items-center gap-2">
          <Activity :size="12" class="text-[var(--neon-primary)]/60" />
          <span
            class="text-[9px] font-bold uppercase tracking-[0.15em] text-[var(--app-text-subtle)]"
          >
            实时速度
          </span>
        </div>
        <div class="font-mono-data text-xl font-bold text-neon leading-none">
          {{ totalSpeed }}
        </div>
      </div>
    </div>

    <!-- Navigation -->
    <nav class="flex-1 px-3 space-y-1">
      <button
        v-for="item in navItems"
        :key="item.id"
        :class="[
          'w-full flex items-center justify-between px-4 py-3 rounded-[var(--radius-squircle-md)] transition-all duration-300 group',
          uiStore.activeTab === item.id
            ? 'bg-[var(--sidebar-active)] border border-[var(--card-border)]'
            : 'hover:bg-[var(--sidebar-hover)] border border-transparent',
        ]"
        @click="uiStore.setActiveTab(item.id)"
      >
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
      </button>

      <!-- Divider -->
      <div class="py-3 px-2">
        <div class="divider-glow"></div>
      </div>

      <!-- Settings Button -->
      <button
        :class="[
          'w-full flex items-center gap-3 px-4 py-3 rounded-[var(--radius-squircle-md)] transition-all duration-300 group',
          uiStore.activeTab === 'settings'
            ? 'bg-[var(--sidebar-active)] border border-[var(--card-border)]'
            : 'hover:bg-[var(--sidebar-hover)] border border-transparent',
        ]"
        @click="uiStore.setActiveTab('settings')"
      >
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
          偏好设置
        </span>
      </button>
    </nav>

    <!-- Connection Status Footer -->
    <div class="p-4 mt-auto">
      <div class="glass-panel-subtle rounded-[var(--radius-squircle-md)] p-4 space-y-2">
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
            {{ isConnected ? 'Aria2 在线' : 'Aria2 离线' }}
          </span>
        </div>
        <div class="font-mono-data text-[10px] text-[var(--app-text-subtle)] truncate">
          127.0.0.1:{{ configStore.settings.rpc_port }}
        </div>
      </div>
    </div>
  </aside>
</template>
