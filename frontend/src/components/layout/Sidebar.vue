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
  <aside class="sidebar-container w-56 flex flex-col shrink-0 z-20 bg-[var(--sidebar-bg)]">
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
            class="sidebar-brand-text text-[10.5px] font-bold text-[var(--neon-primary)] tracking-wide leading-none mt-1.5 flex items-center gap-1"
          >
            <svg
              xmlns="http://www.w3.org/2000/svg"
              viewBox="42.2 -1530.0 6093.8 1992.0"
              fill="currentColor"
              class="h-[10.5px] w-auto shrink-0 select-none"
              aria-label="Surge"
            >
              <path
                d="M605 22Q426 22 297.5 -33.0Q169 -88 107.5 -196.0Q46 -304 68 -462H365Q354 -349 427.0 -292.5Q500 -236 624 -236Q746 -236 829.0 -288.5Q912 -341 926 -426Q939 -502 879.0 -543.5Q819 -585 701 -615L543 -656Q367 -700 273.5 -797.0Q180 -894 207 -1060Q228 -1195 312.5 -1296.0Q397 -1397 528.0 -1453.5Q659 -1510 821 -1510Q985 -1510 1104.0 -1453.5Q1223 -1397 1280.5 -1297.0Q1338 -1197 1319 -1065H1025Q1031 -1154 971.0 -1203.5Q911 -1253 796 -1253Q679 -1253 607.5 -1204.5Q536 -1156 523 -1081Q513 -1026 540.5 -990.0Q568 -954 618.5 -931.0Q669 -908 731 -893L859 -860Q979 -831 1071.0 -777.0Q1163 -723 1208.5 -636.5Q1254 -550 1233 -424Q1199 -221 1037.5 -99.5Q876 22 605 22ZM1820 14Q1646 14 1559.0 -98.0Q1472 -210 1504 -407L1622 -1118H1922L1813 -459Q1796 -355 1839.5 -296.0Q1883 -237 1979 -237Q2073 -237 2144.5 -297.5Q2216 -358 2234 -471L2341 -1118H2642L2457 0H2173L2208 -232Q2140 -114 2043.5 -50.0Q1947 14 1820 14ZM2774 0 2959 -1118H3249L3218 -923H3230Q3277 -1026 3358.0 -1079.5Q3439 -1133 3533 -1133Q3584 -1133 3629 -1123L3584 -855Q3563 -862 3526.0 -866.0Q3489 -870 3457 -870Q3353 -870 3274.5 -805.0Q3196 -740 3179 -636L3074 0ZM4109 442Q3892 442 3774.0 359.5Q3656 277 3640 158L3902 106Q3914 151 3961.0 187.5Q4008 224 4112 224Q4232 224 4312.0 166.0Q4392 108 4412 -2L4446 -198L4419 -196Q4376 -127 4301.0 -71.5Q4226 -16 4097 -16Q3929 -16 3818.0 -120.5Q3707 -225 3707 -436Q3707 -563 3743.5 -687.0Q3780 -811 3849.5 -911.5Q3919 -1012 4020.0 -1072.0Q4121 -1132 4250 -1132Q4349 -1132 4410.5 -1097.5Q4472 -1063 4505.0 -1014.0Q4538 -965 4551 -921L4565 -923L4598 -1118H4894L4708 0Q4682 152 4599.0 249.5Q4516 347 4390.0 394.5Q4264 442 4109 442ZM4196 -244Q4277 -244 4337.5 -283.0Q4398 -322 4437.5 -385.5Q4477 -449 4496.5 -525.0Q4516 -601 4516 -675Q4516 -776 4471.0 -834.0Q4426 -892 4335 -892Q4257 -892 4196.5 -852.0Q4136 -812 4095.0 -747.0Q4054 -682 4033.5 -605.0Q4013 -528 4013 -453Q4013 -354 4058.5 -299.0Q4104 -244 4196 -244ZM5541 24Q5317 24 5188.5 -96.5Q5060 -217 5060 -432Q5060 -573 5106.5 -700.0Q5153 -827 5239.0 -925.5Q5325 -1024 5443.0 -1080.5Q5561 -1137 5703 -1137Q5821 -1137 5914.5 -1097.5Q6008 -1058 6062.0 -985.0Q6116 -912 6116 -811Q6116 -685 6028.0 -613.0Q5940 -541 5769.0 -508.0Q5598 -475 5350 -469Q5349 -447 5349 -427Q5349 -331 5395.0 -266.5Q5441 -202 5563 -202Q5648 -202 5714.0 -237.5Q5780 -273 5808 -336L6077 -300Q6022 -154 5879.5 -65.0Q5737 24 5541 24ZM5381 -650Q5565 -653 5663.5 -668.0Q5762 -683 5799.5 -713.5Q5837 -744 5837 -796Q5837 -849 5793.0 -880.0Q5749 -911 5672 -911Q5589 -911 5531.5 -874.0Q5474 -837 5437.5 -777.0Q5401 -717 5381 -650Z"
              />
            </svg>
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
        class="w-full group"
        @click="handleNavClick(item.id)"
      >
        <div class="w-full h-full flex items-center justify-between px-4 py-3">
          <div class="flex items-center gap-3">
            <!-- Icon with conditional neon glow -->
            <div
              :class="[
                'w-8 h-8 rounded-xl flex items-center justify-center transition-all duration-200',
                uiStore.activeTab === item.id
                  ? 'bg-[var(--neon-primary)]/10 text-[var(--neon-primary)]'
                  : 'bg-[var(--btn-glass-bg)] text-[var(--app-text-muted)] group-hover:text-[var(--app-text)] group-hover:bg-[var(--btn-glass-hover)]',
              ]"
            >
              <component :is="item.icon" :size="16" />
            </div>
            <span
              :class="[
                'text-sm font-semibold transition-colors duration-200',
                uiStore.activeTab === item.id
                  ? 'text-[var(--app-text)]/90'
                  : 'text-[var(--app-text-muted)] group-hover:text-[var(--app-text)]',
              ]"
            >
              {{ item.name }}
            </span>
          </div>

          <!-- Task count badge -->
          <div
            v-if="item.count > 0"
            :class="[
              'min-w-[24px] h-6 px-2 rounded-lg flex items-center justify-center font-mono-data text-xs font-bold transition-all duration-200',
              uiStore.activeTab === item.id
                ? 'bg-[var(--neon-primary)]/20 text-[var(--neon-primary)]'
                : 'bg-[var(--btn-glass-bg)] text-[var(--app-text-subtle)] group-hover:bg-[var(--btn-glass-hover)] group-hover:text-[var(--app-text)]/80',
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
        class="w-full group"
        @click="uiStore.setActiveTab('settings')"
      >
        <div class="w-full h-full flex items-center gap-3 px-4 py-3">
          <div
            :class="[
              'w-8 h-8 rounded-xl flex items-center justify-center transition-all duration-200',
              uiStore.activeTab === 'settings'
                ? 'bg-[var(--btn-glass-hover)] text-[var(--app-text)]/80'
                : 'bg-[var(--btn-glass-bg)] text-[var(--app-text-muted)] group-hover:text-[var(--app-text)] group-hover:bg-[var(--btn-glass-hover)]',
            ]"
          >
            <SettingsIcon :size="16" />
          </div>
          <span
            :class="[
              'text-sm font-semibold transition-colors duration-200',
              uiStore.activeTab === 'settings'
                ? 'text-[var(--app-text)]/80'
                : 'text-[var(--app-text-muted)] group-hover:text-[var(--app-text)]',
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
                  ? configStore.isHydrated
                    ? configStore.settings.rpc_port
                    : t('sidebar.aria2PortPending')
                  : t('sidebar.aria2Offline')
              }}
            </span>
          </div>
        </div>
      </StaticGlassPanel>
    </div>
  </aside>
</template>
