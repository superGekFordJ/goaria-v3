<script setup lang="ts">
  import { ref, computed, onMounted, onUnmounted } from 'vue'
  import { useI18n } from 'vue-i18n'
  import {
    Loader2,
    RefreshCw,
    Download,
    RotateCcw,
    AlertCircle,
    CheckCircle,
  } from 'lucide-vue-next'
  import ThemeIcon from '../../common/ThemeIcon.vue'
  import {
    GetAppVersion,
    CheckForUpdate,
    ApplyUpdate,
    RestartApp,
  } from '../../../../bindings/goaria-v3/app.js'
  import { Events } from '@wailsio/runtime'
  import { useSmoothProgress } from '../../../composables/useSmoothProgress'

  interface ReleaseInfo {
    tag_name: string
    name: string
    body: string
    html_url: string
    asset_url: string
    asset_size: number
    prerelease: boolean
  }

  const { t } = useI18n()

  const currentVersion = ref('...')
  const status = ref<'idle' | 'checking' | 'available' | 'downloading' | 'ready' | 'error'>('idle')
  const latestVersion = ref('')
  const assetURL = ref('')
  const assetSize = ref(0)
  const errorMsg = ref('')
  const upToDate = ref(false)
  const includePreRelease = ref(false)
  const availableReleases = ref<ReleaseInfo[]>([])
  const downloadingAssetURL = ref('')

  // Keep smoothing for update download, but tune it to be more responsive than task cards
  const UPDATE_PROGRESS_CONFIG = {
    deviationDecay: 0.07,
    maxScaleDelta: 0.008,
  } as const

  const { displayDownloaded, totalBytes, updateStats } = useSmoothProgress(UPDATE_PROGRESS_CONFIG)
  const progressScale = computed(() => {
    if (totalBytes.value <= 0) return 0
    return Math.min(Math.max(displayDownloaded.value / totalBytes.value, 0), 1)
  })
  const progressPercent = computed(() => Math.round(progressScale.value * 100))

  const unsubs: Array<() => void> = []

  onMounted(async () => {
    try {
      const ver = await GetAppVersion()
      currentVersion.value = ver
    } catch {
      currentVersion.value = 'unknown'
    }

    // Listen for update status events
    unsubs.push(
      Events.On('update:status', ev => {
        const data = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as {
          status: string
          payload: unknown
        }
        if (data.status === 'downloading') {
          status.value = 'downloading'
        } else if (data.status === 'ready') {
          status.value = 'ready'
        } else if (data.status === 'error') {
          status.value = 'error'
          errorMsg.value = typeof data.payload === 'string' ? data.payload : t('update.error')
        }
      }),
    )

    // Listen for update progress events (bytes-level data for smooth algorithm)
    unsubs.push(
      Events.On('update:progress', ev => {
        const data = (ev && typeof ev === 'object' && 'data' in ev ? ev.data : ev) as {
          downloaded: number
          total: number
          speed: number
        }
        updateStats({
          downloaded: data.downloaded,
          total: data.total,
          speed: data.speed,
          status: 'active',
        })
      }),
    )
  })

  onUnmounted(() => {
    unsubs.forEach(unsub => unsub())
  })

  const checkUpdate = async () => {
    status.value = 'checking'
    upToDate.value = false
    errorMsg.value = ''
    availableReleases.value = []

    try {
      const result = await CheckForUpdate(includePreRelease.value)

      if (result.error) {
        status.value = 'error'
        errorMsg.value = result.error
        return
      }

      if (result.available && result.releases && result.releases.length > 0) {
        status.value = 'available'
        availableReleases.value = result.releases as ReleaseInfo[]
        latestVersion.value = result.latest
      } else {
        status.value = 'idle'
        upToDate.value = true
      }
    } catch {
      status.value = 'error'
      errorMsg.value = t('update.error')
    }
  }

  const startUpdate = async (release: ReleaseInfo) => {
    status.value = 'downloading'
    downloadingAssetURL.value = release.asset_url
    assetURL.value = release.asset_url
    assetSize.value = release.asset_size
    try {
      await ApplyUpdate(release.asset_url, release.asset_size)
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
  <div class="etched-panel p-5 mt-6 relative overflow-hidden">
    <!-- Top Row: App Identity & Global Actions/Status -->
    <div class="flex items-center justify-between relative z-10">
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

      <!-- Right: Toggle & Check / Status -->
      <div class="flex items-center gap-4">
        <!-- Pre-release Toggle Switch (only show when not checking, downloading, or ready to restart) -->
        <div
          v-if="status === 'idle' || status === 'error' || status === 'available'"
          class="flex items-center gap-2"
        >
          <span class="text-[10px] font-mono-data text-[var(--app-text-muted)]">
            {{ t('update.includePreRelease') }}
          </span>
          <div
            class="w-9 h-5 rounded-full relative transition-all duration-300 cursor-pointer shrink-0 border"
            :class="[
              includePreRelease
                ? 'bg-[var(--neon-primary)] border-transparent'
                : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)]',
            ]"
            role="switch"
            tabindex="0"
            :aria-checked="includePreRelease"
            @click="includePreRelease = !includePreRelease"
            @keydown.enter.prevent="includePreRelease = !includePreRelease"
            @keydown.space.prevent="includePreRelease = !includePreRelease"
          >
            <div
              class="absolute top-[2px] w-3.5 h-3.5 rounded-full bg-[var(--card-bg)] shadow-md transition-all duration-300"
              :class="[includePreRelease ? 'left-[18px]' : 'left-[2px]']"
            ></div>
          </div>
        </div>

        <!-- Check Button (Idle / Error) -->
        <template v-if="(status === 'idle' || status === 'error') && !upToDate">
          <button
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:border-[var(--neon-primary)]/30 transition-all duration-200 text-[10px] font-mono-data text-[var(--app-text-muted)] hover:text-[var(--neon-primary)]"
            @click="checkUpdate"
          >
            <RefreshCw :size="12" />
            {{ t('update.checkUpdate') }}
          </button>
        </template>

        <!-- Up to Date Status -->
        <template v-else-if="status === 'idle' && upToDate">
          <div
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--status-complete)]/10 border border-[var(--status-complete)]/20"
          >
            <CheckCircle :size="12" class="text-[var(--status-complete)]" />
            <span class="text-[10px] font-mono-data text-[var(--status-complete)]">
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

        <!-- Checking Status -->
        <template v-else-if="status === 'checking'">
          <div class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)]">
            <Loader2 :size="12" class="animate-spin text-[var(--neon-primary)]" />
            <span class="text-[10px] font-mono-data text-[var(--app-text-muted)]">
              {{ t('update.checking') }}
            </span>
          </div>
        </template>

        <!-- Error Status (when not showing release list) -->
        <template v-else-if="status === 'error' && availableReleases.length === 0">
          <div class="flex items-center gap-1.5 px-2 py-1 rounded-lg bg-[var(--status-error)]/10">
            <AlertCircle :size="12" class="text-[var(--status-error)]" />
            <span
              class="text-[10px] font-mono-data text-[var(--status-error)] max-w-[120px] truncate"
            >
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

        <!-- Recheck button when updates are already listed -->
        <template v-else-if="status === 'available'">
          <button
            class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] hover:border-[var(--neon-primary)]/30 transition-all duration-200 text-[10px] font-mono-data text-[var(--app-text-subtle)] hover:text-[var(--neon-primary)]"
            @click="checkUpdate"
          >
            <RefreshCw :size="12" />
            {{ t('update.checkUpdate') }}
          </button>
        </template>
      </div>
    </div>

    <!-- Error notice under the top row if listing releases is active but an operation failed -->
    <div
      v-if="status === 'error' && availableReleases.length > 0"
      class="mt-4 p-3 rounded-lg bg-[var(--status-error)]/10 border border-[var(--status-error)]/20 flex items-center gap-2 relative z-10"
    >
      <AlertCircle :size="14" class="text-[var(--status-error)] shrink-0" />
      <span class="text-[10px] font-mono-data text-[var(--status-error)]">
        {{ errorMsg || t('update.error') }}
      </span>
    </div>

    <!-- Releases List (shown when availableReleases has items) -->
    <div
      v-if="availableReleases.length > 0"
      class="mt-5 border-t border-[var(--glass-border)] pt-4 relative z-10"
    >
      <div class="flex flex-col gap-3">
        <div
          v-for="release in availableReleases"
          :key="release.tag_name"
          class="flex items-center justify-between p-3 rounded-xl bg-[var(--btn-glass-bg)]/30 border border-[var(--glass-border)]/50 hover:bg-[var(--btn-glass-bg)]/50 transition-all duration-200"
        >
          <!-- Left: Version info and Beta badge -->
          <div class="flex items-center gap-2">
            <span class="text-xs font-bold font-mono-data text-[var(--app-text)]">
              {{ release.tag_name }}
            </span>
            <span
              v-if="release.prerelease"
              class="px-1.5 py-0.5 rounded text-[8px] font-extrabold uppercase tracking-wider bg-[var(--neon-primary)]/10 text-[var(--neon-primary)] border border-[var(--neon-primary)]/20 font-mono-data"
            >
              {{ t('update.beta') }}
            </span>
          </div>

          <!-- Right: Actions / Download progress / Ready to restart -->
          <div class="flex items-center gap-3">
            <!-- Downloading Progress for this specific release -->
            <div
              v-if="status === 'downloading' && downloadingAssetURL === release.asset_url"
              class="flex items-center gap-3"
            >
              <span class="text-[10px] font-mono-data text-[var(--app-text-muted)]">
                {{ t('update.downloading') }}
              </span>
              <div class="w-24 h-1.5 rounded-full bg-[var(--btn-glass-bg)] overflow-hidden">
                <div
                  class="h-full rounded-full bg-[var(--neon-primary)]"
                  :style="{ transform: `scaleX(${progressScale})`, transformOrigin: 'left' }"
                ></div>
              </div>
              <span class="text-[10px] font-mono-data text-[var(--neon-primary)]">
                {{ progressPercent }}%
              </span>
            </div>

            <!-- Ready / Restart for this specific release -->
            <button
              v-else-if="status === 'ready' && downloadingAssetURL === release.asset_url"
              class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-[var(--status-complete)]/10 border border-[var(--status-complete)]/30 hover:bg-[var(--status-complete)]/20 transition-all duration-200 text-[10px] font-mono-data text-[var(--status-complete)]"
              @click="restartApp"
            >
              <RotateCcw :size="12" />
              {{ t('update.restart') }}
            </button>

            <!-- Update button (disabled for other releases when downloading/ready/checking) -->
            <button
              v-else
              class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg transition-all duration-200 text-[10px] font-mono-data"
              :class="[
                status === 'downloading' || status === 'ready' || status === 'checking'
                  ? 'bg-transparent border border-[var(--glass-border)] text-[var(--app-text-muted)] opacity-50 cursor-not-allowed'
                  : 'bg-[var(--neon-primary)]/10 border border-[var(--neon-primary)]/30 hover:bg-[var(--neon-primary)]/20 text-[var(--neon-primary)]',
              ]"
              :disabled="status === 'downloading' || status === 'ready' || status === 'checking'"
              @click="startUpdate(release)"
            >
              <Download :size="12" />
              {{ t('update.download') }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
