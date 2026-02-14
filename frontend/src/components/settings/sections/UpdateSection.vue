<script setup lang="ts">
  import { ref, onMounted, onUnmounted } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Loader2, RefreshCw, Download, RotateCcw, AlertCircle, CheckCircle } from 'lucide-vue-next'
  import ThemeIcon from '../../common/ThemeIcon.vue'
  import { GetAppVersion, CheckForUpdate, ApplyUpdate, RestartApp } from '../../../../bindings/goaria-v3/app.js'
  import { Events } from '@wailsio/runtime'

  const { t } = useI18n()

  const currentVersion = ref('...')
  const status = ref<'idle' | 'checking' | 'available' | 'downloading' | 'ready' | 'error'>('idle')
  const progress = ref(0)
  const latestVersion = ref('')
  const assetURL = ref('')
  const assetSize = ref(0)
  const errorMsg = ref('')
  const upToDate = ref(false)

  const unsubs: Array<() => void> = []

  onMounted(async () => {
    try {
      const ver = await GetAppVersion()
      currentVersion.value = ver
    } catch {
      currentVersion.value = 'unknown'
    }

    // Listen for update status events
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    unsubs.push(Events.On('update:status', (ev: any) => {
      const data = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as {
        status: string
        payload: unknown
      }
      if (data.status === 'downloading') {
        status.value = 'downloading'
      } else if (data.status === 'ready') {
        status.value = 'ready'
        progress.value = 100
      } else if (data.status === 'error') {
        status.value = 'error'
        errorMsg.value = typeof data.payload === 'string' ? data.payload : t('update.error')
      }
    }))

    // Listen for update progress events
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    unsubs.push(Events.On('update:progress', (ev: any) => {
      const data = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as {
        percent: number
      }
      progress.value = data.percent
    }))
  })

  onUnmounted(() => {
    unsubs.forEach(unsub => unsub())
  })

  const checkUpdate = async () => {
    status.value = 'checking'
    upToDate.value = false
    errorMsg.value = ''

    try {
      const result = await CheckForUpdate()

      if (result.error) {
        status.value = 'error'
        errorMsg.value = result.error
        return
      }

      if (result.available && result.releaseInfo) {
        status.value = 'available'
        latestVersion.value = result.latest
        assetURL.value = result.releaseInfo.asset_url
        assetSize.value = result.releaseInfo.asset_size
      } else {
        status.value = 'idle'
        upToDate.value = true
      }
    } catch {
      status.value = 'error'
      errorMsg.value = t('update.error')
    }
  }

  const applyUpdate = async () => {
    status.value = 'downloading'
    progress.value = 0
    try {
      await ApplyUpdate(assetURL.value, assetSize.value)
    } catch {
      status.value = 'error'
      errorMsg.value = t('update.error')
    }
  }

  const restartApp = async () => {
    try {
      await RestartApp()
    } catch {
      // App should have exited
    }
  }
</script>

<template>
  <div class="glass-panel-subtle rounded-[var(--radius-squircle-lg)] p-5 mt-6">
    <div class="flex items-center justify-between">
      <!-- Left: App identity -->
      <div class="flex items-center gap-3">
        <ThemeIcon :size="32" />
        <div>
          <span class="text-sm font-bold text-[var(--app-text)]/60">GoAria</span>
          <span class="text-[10px] text-[var(--app-text-subtle)] ml-2 font-mono-data">
            v{{ currentVersion }}
          </span>
        </div>
      </div>

      <!-- Right: Status-dependent content -->
      <div class="flex items-center gap-2">
        <!-- idle -->
        <template v-if="status === 'idle' && !upToDate">
          <button
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:border-[var(--neon-primary)]/30 transition-all duration-200 text-[10px] font-mono-data text-[var(--app-text-muted)] hover:text-[var(--neon-primary)]"
            @click="checkUpdate"
          >
            <RefreshCw :size="12" />
            {{ t('update.checkUpdate') }}
          </button>
        </template>

        <!-- up to date -->
        <template v-else-if="status === 'idle' && upToDate">
          <div class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--status-active)]/10 border border-[var(--status-active)]/20">
            <CheckCircle :size="12" class="text-[var(--status-active)]" />
            <span class="text-[10px] font-mono-data text-[var(--status-active)]">
              {{ t('update.upToDate') }}
            </span>
          </div>
          <button
            class="flex items-center gap-1.5 px-2 py-1.5 rounded-lg bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:border-[var(--neon-primary)]/30 transition-all duration-200 text-[10px] text-[var(--app-text-subtle)] hover:text-[var(--neon-primary)]"
            @click="checkUpdate"
          >
            <RefreshCw :size="10" />
          </button>
        </template>

        <!-- checking -->
        <template v-else-if="status === 'checking'">
          <div class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)]">
            <Loader2 :size="12" class="animate-spin text-[var(--neon-primary)]" />
            <span class="text-[10px] font-mono-data text-[var(--app-text-muted)]">
              {{ t('update.checking') }}
            </span>
          </div>
        </template>

        <!-- available -->
        <template v-else-if="status === 'available'">
          <span class="text-[10px] font-mono-data text-[var(--neon-primary)]">
            {{ t('update.available', { version: latestVersion }) }}
          </span>
          <button
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--neon-primary)]/10 border border-[var(--neon-primary)]/30 hover:bg-[var(--neon-primary)]/20 transition-all duration-200 text-[10px] font-mono-data text-[var(--neon-primary)]"
            @click="applyUpdate"
          >
            <Download :size="12" />
            {{ t('update.download') }}
          </button>
        </template>

        <!-- downloading -->
        <template v-else-if="status === 'downloading'">
          <div class="flex items-center gap-3">
            <span class="text-[10px] font-mono-data text-[var(--app-text-muted)]">
              {{ t('update.downloading') }}
            </span>
            <div class="w-32 h-1.5 rounded-full bg-[var(--btn-glass-bg)] overflow-hidden">
              <div
                class="h-full rounded-full bg-[var(--neon-primary)] transition-all duration-300"
                :style="{ width: `${progress}%` }"
              ></div>
            </div>
            <span class="text-[10px] font-mono-data text-[var(--neon-primary)]">
              {{ progress }}%
            </span>
          </div>
        </template>

        <!-- ready -->
        <template v-else-if="status === 'ready'">
          <button
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--status-active)]/10 border border-[var(--status-active)]/30 hover:bg-[var(--status-active)]/20 transition-all duration-200 text-[10px] font-mono-data text-[var(--status-active)]"
            @click="restartApp"
          >
            <RotateCcw :size="12" />
            {{ t('update.restart') }}
          </button>
        </template>

        <!-- error -->
        <template v-else-if="status === 'error'">
          <div class="flex items-center gap-1.5 px-2 py-1 rounded-lg bg-[var(--status-error)]/10">
            <AlertCircle :size="12" class="text-[var(--status-error)]" />
            <span class="text-[10px] font-mono-data text-[var(--status-error)] max-w-[120px] truncate">
              {{ errorMsg || t('update.error') }}
            </span>
          </div>
          <button
            class="flex items-center gap-1.5 px-2 py-1.5 rounded-lg bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:border-[var(--neon-primary)]/30 transition-all duration-200 text-[10px] text-[var(--app-text-subtle)] hover:text-[var(--neon-primary)]"
            @click="checkUpdate"
          >
            <RefreshCw :size="10" />
            {{ t('update.retry') }}
          </button>
        </template>
      </div>
    </div>
  </div>
</template>
