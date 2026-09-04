<script setup lang="ts">
  import { computed, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
  import type { TaskGroupHint } from '../../stores/task/grouping'
  import { useTaskStore } from '../../stores/task'
  import { Pause, Play, FolderOpen, Trash2, Clock, Zap, Layers3, Share2 } from '@lucide/vue'
  import FileIcon from '../common/FileIcon.vue'
  import {
    TASK_PROGRESS_CONFIG,
    SURGE_TASK_PROGRESS_CONFIG,
    useSmoothProgress,
  } from '../../composables/useSmoothProgress'
  import { isInsufficientDiskSpaceFailure } from '../../utils/diskSpaceError'

  const { t } = useI18n()

  const props = defineProps<{
    task: Task
    groupHint?: TaskGroupHint | null
  }>()

  const emit = defineEmits<{
    (e: 'confirm-delete', task: Task): void
  }>()

  const taskStore = useTaskStore()

  const isSurgeTask = props.task.gid.startsWith('sg_')
  const { displayDownloaded, totalBytes, updateStats } = useSmoothProgress(
    isSurgeTask ? SURGE_TASK_PROGRESS_CONFIG : TASK_PROGRESS_CONFIG,
  )

  const nonNegativeFinite = (value: unknown): number => {
    const parsed = Number(value)
    return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
  }

  const hasKnownTotal = computed(() => {
    const total = Number(props.task.totalLength)
    return Number.isFinite(total) && total > 0
  })

  const taskNumbers = computed(() => {
    const downloaded = nonNegativeFinite(props.task.completedLength)
    const speed = nonNegativeFinite(props.task.downloadSpeed)
    const total = hasKnownTotal.value ? Number(props.task.totalLength) : 0
    return { downloaded, speed, total }
  })

  // Sync with prop updates - optimized to watch specific fields
  watch(
    [
      () => props.task.completedLength,
      () => props.task.downloadSpeed,
      () => props.task.totalLength,
      () => props.task.status,
    ],
    () => {
      const { downloaded, speed, total } = taskNumbers.value
      // While visual total is unknown, supply max(downloaded, 1) as internal
      // smoothing denominator so displayDownloaded does not collapse to 1 byte.
      const internalTotal = hasKnownTotal.value ? total : Math.max(downloaded, 1)
      updateStats({
        downloaded,
        speed,
        total: internalTotal,
        status: props.task.status as string,
      })
    },
    { immediate: true },
  )

  // When totalLength transitions from unknown to known, re-anchor displayDownloaded
  // to avoid catching up from arbitrary smoothing lag.
  watch(hasKnownTotal, (known, prevKnown) => {
    if (known && !prevKnown) {
      displayDownloaded.value = taskNumbers.value.downloaded
    }
  })

  // Extract filename from path
  const fileName = computed(() => {
    const path = props.task.files?.[0]?.path
    if (!path) return t('taskCard.parsing')
    return path.split(/[\\/]/).pop() || t('taskCard.unknownFile')
  })

  // Calculate progress percentage for display (null when total is unknown)
  const progress = computed<number | null>(() => {
    if (isCompleted.value) return 100
    if (!hasKnownTotal.value) return null
    const { downloaded, total } = taskNumbers.value
    if (total <= 0) return null
    return Math.min(Math.max((downloaded / total) * 100, 0), 100)
  })

  // Calculate smooth progress scale (0-1) for GPU animation
  const progressScale = computed(() => {
    if (!hasKnownTotal.value) return 0
    if (totalBytes.value <= 0) return 0
    const ratio = displayDownloaded.value / totalBytes.value
    return Math.min(Math.max(ratio, 0), 1)
  })

  // Format bytes to human readable with bounds safety
  const formatSize = (b: string | number | undefined) => {
    if (b === undefined || b === null || b === '') return '0 B'
    const bytes = Number(b)
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
    const clampedI = Math.max(0, i)
    return (bytes / Math.pow(1024, clampedI)).toFixed(2) + ' ' + units[clampedI]
  }

  // Format speed with neon styling consideration
  const formatSpeed = (b: string | number | undefined) => {
    if (!b || b === '0') return '0'
    const bytes = Number(b)
    if (!Number.isFinite(bytes) || bytes <= 0) return '0'
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
    const clampedI = Math.max(0, i)
    return (bytes / Math.pow(1024, clampedI)).toFixed(1)
  }

  const speedUnit = (b: string | number | undefined) => {
    if (!b || b === '0') return 'B/s'
    const bytes = Number(b)
    if (!Number.isFinite(bytes) || bytes <= 0) return 'B/s'
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
    const clampedI = Math.max(0, i)
    return units[clampedI] + '/s'
  }

  // Calculate ETA
  const estimatedTime = computed(() => {
    if (!hasKnownTotal.value) return '--'
    const speed = taskNumbers.value.speed
    const remaining = taskNumbers.value.total - taskNumbers.value.downloaded
    if (speed <= 0 || remaining <= 0 || !Number.isFinite(remaining)) return '--'

    const seconds = Math.floor(remaining / speed)
    if (!Number.isFinite(seconds) || seconds < 0) return '--'
    if (seconds < 60) return `${seconds}s`
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
    const hours = Math.floor(seconds / 3600)
    const mins = Math.floor((seconds % 3600) / 60)
    return `${hours}h ${mins}m`
  })

  // Concurrency threads count (real runtime telemetry only, no fake fallbacks)
  const threadCount = computed<number | null>(() => {
    const raw = (props.task as unknown as { threads?: string | number }).threads
    if (raw !== undefined && raw !== null && raw !== '') {
      const parsed = Number(raw)
      if (Number.isFinite(parsed) && parsed > 0) return parsed
    }
    return null
  })

  // Status styling with neon effects
  const statusConfig = computed(() => {
    switch (props.task.status) {
      case 'active':
        return {
          dotClass: 'status-active',
          label: t('taskCard.downloading'),
          labelClass: 'text-[var(--status-active)]',
          showProgress: true,
        }
      case 'complete':
        return {
          dotClass: 'status-complete',
          label: t('taskCard.completed'),
          labelClass: 'text-[var(--status-complete)]',
          showProgress: false,
        }
      case 'paused':
        return {
          dotClass: 'status-paused',
          label: t('taskCard.paused'),
          labelClass: 'text-amber-400',
          showProgress: true,
        }
      case 'waiting':
        return {
          dotClass: 'status-waiting',
          label: t('taskCard.waiting'),
          labelClass: 'text-[var(--app-text-muted)]',
          showProgress: true,
        }
      case 'error':
        return {
          dotClass: 'status-error',
          label: isInsufficientDiskSpaceFailure(props.task.errorCode, props.task.errorMessage)
            ? t('taskCard.insufficientDiskSpace')
            : t('taskCard.error'),
          labelClass: 'text-red-400',
          showProgress: false,
        }
      default:
        return {
          dotClass: 'status-waiting',
          label: props.task.status || t('taskCard.unknown'),
          labelClass: 'text-[var(--app-text-muted)]',
          showProgress: false,
        }
    }
  })

  // Check if task is active/downloading
  const isActive = computed(() => props.task.status === 'active')
  const isPaused = computed(() => props.task.status === 'paused' || props.task.status === 'waiting')
  const isCompleted = computed(() => props.task.status === 'complete')
  const isError = computed(() => props.task.status === 'error')

  // Status-based card glow class
  const cardGlowClass = computed(() => {
    if (isActive.value) return 'task-card-active'
    if (isPaused.value) return 'task-card-paused'
    if (isError.value) return 'task-card-error'
    if (isCompleted.value) return 'task-card-complete'
    return ''
  })

  // Selection state from store
  const isSelected = computed(() => taskStore.isSelected(props.task.gid))

  const groupChipTitle = computed(() => {
    if (!props.groupHint) return undefined
    const count = props.groupHint.totalCount || props.groupHint.visibleCount || 0
    const label = t('taskCard.groupHintLabel', { count })
    if (!props.groupHint.folderPath) return label
    return `${label} · ${props.groupHint.folderPath}`
  })

  const groupCountText = computed(() => {
    const hint = props.groupHint
    if (!hint) return ''
    if (hint.ordinal && hint.totalCount)
      return t('taskCard.groupOrdinal', { index: hint.ordinal, count: hint.totalCount })
    if (hint.ordinal && hint.visibleCount)
      return t('taskCard.groupOrdinal', { index: hint.ordinal, count: hint.visibleCount })
    if (hint.totalCount) return t('taskCard.groupItemCount', { count: hint.totalCount })
    if (hint.visibleCount) return t('taskCard.groupItemCount', { count: hint.visibleCount })
    return ''
  })
</script>

<template>
  <div
    :class="[
      'task-card glass-panel rounded-[var(--radius-squircle-xl)] p-5 group hover-reveal-container',
      cardGlowClass,
      { 'task-card-selected': isSelected },
    ]"
  >
    <!-- Top Row: Filename & Actions -->
    <div class="flex items-start justify-between gap-4 mb-4">
      <!-- File Info -->
      <div class="flex-1 min-w-0">
        <div class="flex items-center gap-3 mb-2">
          <!-- Checkbox (Hover Reveal) -->
          <div class="checkbox-container" :class="{ 'always-visible': isSelected }">
            <input
              type="checkbox"
              :checked="isSelected"
              class="task-checkbox"
              @click.stop="taskStore.toggleSelect(task.gid)"
            />
          </div>

          <!-- File Icon -->
          <div
            :class="[
              'w-10 h-10 rounded-[var(--radius-squircle-sm)] flex items-center justify-center shrink-0 transition-all duration-300',
              isActive
                ? 'bg-[var(--status-active)]/10 text-[var(--status-active)]'
                : isCompleted
                  ? 'bg-[var(--status-complete)]/10 text-[var(--status-complete)]'
                  : 'bg-[var(--btn-glass-bg)] text-[var(--app-text-subtle)]',
            ]"
          >
            <FileIcon :file-name="fileName" :size="18" />
          </div>

          <!-- Filename -->
          <div class="min-w-0 flex-1">
            <h3
              class="font-semibold text-sm text-[var(--app-text)]/90 truncate leading-tight mb-1"
              :title="fileName"
            >
              {{ fileName }}
            </h3>
            <!-- Status Badge -->
            <div class="flex items-center gap-2">
              <div class="status-dot" :class="statusConfig.dotClass"></div>
              <span
                class="text-[10px] font-bold uppercase tracking-widest"
                :class="statusConfig.labelClass"
              >
                {{ statusConfig.label }}
              </span>
              <!-- Threads Chip (Active only, real telemetry only) -->
              <span
                v-if="isActive && threadCount !== null"
                class="task-threads-chip"
                :title="t('taskCard.threadsTooltip', { count: threadCount })"
              >
                <Share2 :size="10" class="task-threads-chip-icon" />
                <span class="font-mono-data task-threads-chip-count">{{ threadCount }}</span>
              </span>
              <span
                v-if="groupHint"
                class="task-group-chip"
                :title="groupChipTitle"
                :aria-label="groupChipTitle"
              >
                <span class="task-group-chip-dot"></span>
                <Layers3 :size="11" class="task-group-chip-icon" />
                <span v-if="groupHint.folderLabel" class="task-group-chip-folder">
                  {{ t('taskCard.groupFolder', { folder: groupHint.folderLabel }) }}
                </span>
                <span v-else class="task-group-chip-label">
                  {{ t('taskCard.groupStack') }}
                </span>
                <span v-if="groupCountText" class="task-group-chip-count font-mono-data">
                  {{ groupCountText }}
                </span>
              </span>
            </div>
          </div>
        </div>
      </div>

      <!-- Action Buttons (Hover Reveal) -->
      <div class="flex gap-1.5 hover-reveal-target shrink-0">
        <!-- Pause/Resume Button -->
        <template v-if="!isCompleted">
          <button
            class="btn-glass w-10 h-10 rounded-xl flex items-center justify-center text-[var(--app-text-muted)] hover:text-[var(--neon-primary)] hover:border-[var(--neon-primary)]/30"
            :title="isActive ? t('taskCard.pause') : t('taskCard.resume')"
            @click="isActive ? taskStore.pause(task.gid) : taskStore.resume(task.gid)"
          >
            <Pause v-if="isActive" :size="16" />
            <Play v-else :size="16" class="ml-0.5" />
          </button>
        </template>

        <!-- Open Folder Button -->
        <button
          class="btn-glass w-10 h-10 rounded-xl flex items-center justify-center text-[var(--app-text-muted)] hover:text-[var(--app-text)] hover:border-[var(--glass-border)]"
          :title="t('taskCard.openFolder')"
          @click="taskStore.openTaskFolder(task)"
        >
          <FolderOpen :size="16" />
        </button>

        <!-- Delete Button -->
        <button
          class="btn-glass w-10 h-10 rounded-xl flex items-center justify-center text-[var(--app-text-muted)] hover:text-[var(--status-error)] hover:bg-[var(--status-error)]/10 hover:border-[var(--status-error)]/30"
          :title="t('taskCard.delete')"
          @click="emit('confirm-delete', task)"
        >
          <Trash2 :size="16" />
        </button>
      </div>
    </div>

    <!-- Progress Bar (Neon Style with Energy Flow / Indeterminate) -->
    <div
      v-if="statusConfig.showProgress"
      class="mb-4"
      role="progressbar"
      :aria-label="t('taskCard.progress')"
      :aria-valuemin="0"
      :aria-valuemax="100"
      :aria-valuenow="
        hasKnownTotal && progress !== null ? Math.min(Math.round(progress), 99) : undefined
      "
    >
      <div class="progress-bar-container">
        <template v-if="hasKnownTotal">
          <div
            :class="[
              'progress-bar-fill',
              { 'opacity-50': isPaused },
              { 'progress-bar-energy': isActive },
            ]"
            :style="{ transform: `scaleX(${progressScale})` }"
          ></div>
        </template>
        <template v-else>
          <div :class="['progress-bar-indeterminate', { 'opacity-50': isPaused }]"></div>
        </template>
      </div>
    </div>

    <!-- Stats Row -->
    <div class="flex items-end justify-between gap-4">
      <!-- Left: Progress Stats -->
      <div class="flex items-center gap-6">
        <!-- Size Progress -->
        <div class="flex flex-col">
          <span
            class="text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-1"
          >
            {{ t('taskCard.progress') }}
          </span>
          <div class="font-mono-data text-xs text-[var(--app-text-muted)]">
            <span class="text-[var(--app-text)]/70">{{ formatSize(task.completedLength) }}</span>
            <span class="mx-1 text-[var(--app-text-subtle)]">/</span>
            <span>{{
              hasKnownTotal
                ? formatSize(task.totalLength)
                : isCompleted
                  ? formatSize(task.completedLength)
                  : '--'
            }}</span>
          </div>
        </div>

        <!-- Progress Percentage -->
        <div class="flex flex-col">
          <span
            class="text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-1"
          >
            {{ t('taskCard.done') }}
          </span>
          <div class="font-mono-data text-xs text-[var(--app-text-muted)]">
            <template v-if="isCompleted">
              100.0<span class="text-[10px] text-[var(--app-text-subtle)]">%</span>
            </template>
            <template v-else-if="hasKnownTotal && progress !== null">
              {{ progress.toFixed(1)
              }}<span class="text-[10px] text-[var(--app-text-subtle)]">%</span>
            </template>
            <template v-else>
              --<span class="text-[10px] text-[var(--app-text-subtle)]">%</span>
            </template>
          </div>
        </div>

        <!-- ETA (only when downloading) -->
        <div v-if="isActive" class="flex flex-col">
          <span
            class="text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-1"
          >
            {{ t('taskCard.remaining') }}
          </span>
          <div class="flex items-center gap-1 font-mono-data text-xs text-[var(--app-text-muted)]">
            <Clock :size="10" class="text-[var(--app-text-subtle)]" />
            {{ estimatedTime }}
          </div>
        </div>
      </div>

      <!-- Right: Speed Display (Neon) -->
      <div v-if="isActive" class="flex flex-col items-end">
        <span
          class="text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-1"
        >
          {{ t('taskCard.speed') }}
        </span>
        <div class="flex items-baseline gap-1">
          <Zap :size="12" class="text-[var(--neon-primary)]/60 mb-0.5" />
          <span class="font-mono-data text-xl font-bold text-neon leading-none">
            {{ formatSpeed(task.downloadSpeed) }}
          </span>
          <span class="font-mono-data text-[10px] text-[var(--neon-primary)]/60">
            {{ speedUnit(task.downloadSpeed) }}
          </span>
        </div>
      </div>

      <!-- Completed Badge -->
      <div v-else-if="isCompleted" class="flex items-center gap-2">
        <div
          class="px-3 py-1.5 rounded-lg bg-[var(--status-complete)]/10 border border-[var(--status-complete)]/20 flex items-center gap-2"
        >
          <div class="w-1.5 h-1.5 rounded-full bg-[var(--status-complete)]"></div>
          <span class="font-mono-data text-[10px] font-bold text-[var(--status-complete)]">
            {{ t('taskCard.downloadComplete') }}
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
  /* Card hover glow effect */
  .task-card {
    position: relative;
  }

  .task-card::before {
    content: '';
    position: absolute;
    inset: 0;
    border-radius: inherit;
    background: radial-gradient(ellipse at top left, var(--neon-glow) 0%, transparent 50%);
    opacity: 0;
    transition: opacity 0.5s ease;
    pointer-events: none;
  }

  .task-card:hover::before {
    opacity: 1;
  }

  .task-group-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    min-width: 0;
    max-width: min(18rem, 46vw);
    padding: 0.125rem 0.4375rem;
    border: 1px solid color-mix(in srgb, var(--neon-primary) 18%, transparent);
    border-radius: var(--radius-squircle-sm);
    background: color-mix(in srgb, var(--neon-primary) 6%, transparent);
    color: color-mix(in srgb, var(--app-text-muted) 88%, var(--neon-primary));
    box-shadow:
      inset 0 0 0 1px color-mix(in srgb, var(--glass-highlight) 14%, transparent),
      0 0 14px color-mix(in srgb, var(--neon-primary) 6%, transparent);
    font-size: 0.625rem;
    font-weight: 700;
    line-height: 1.1;
  }

  .task-group-chip-dot {
    width: 0.3125rem;
    height: 0.3125rem;
    flex: 0 0 auto;
    border-radius: 999px;
    background: var(--neon-primary);
    box-shadow: 0 0 8px color-mix(in srgb, var(--neon-primary) 36%, transparent);
  }

  .task-group-chip-icon {
    flex: 0 0 auto;
    color: color-mix(in srgb, var(--neon-primary) 70%, var(--app-text));
  }

  .task-group-chip-folder,
  .task-group-chip-label {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .task-group-chip-count {
    flex: 0 0 auto;
    color: color-mix(in srgb, var(--neon-primary) 78%, var(--app-text));
  }

  .task-threads-chip {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.09375rem 0.375rem;
    border: 1px solid color-mix(in srgb, var(--glass-border) 60%, transparent);
    border-radius: var(--radius-squircle-sm);
    background: color-mix(in srgb, var(--app-text) 5%, transparent);
    color: color-mix(in srgb, var(--app-text-muted) 90%, var(--neon-primary));
    box-shadow:
      inset 0 0 0 1px color-mix(in srgb, var(--glass-highlight) 14%, transparent),
      0 0 8px color-mix(in srgb, var(--neon-primary) 4%, transparent);
    font-size: 0.625rem;
    font-weight: 700;
    line-height: 1;
    transition: all 0.2s ease;
  }

  .task-threads-chip:hover {
    border-color: color-mix(in srgb, var(--neon-primary) 35%, transparent);
    background: color-mix(in srgb, var(--neon-primary) 8%, transparent);
    color: var(--app-text);
    box-shadow:
      inset 0 0 0 1px color-mix(in srgb, var(--glass-highlight) 25%, transparent),
      0 0 12px color-mix(in srgb, var(--neon-primary) 12%, transparent);
  }

  .task-threads-chip-icon {
    flex: 0 0 auto;
    color: color-mix(in srgb, var(--neon-primary) 75%, var(--app-text-muted));
  }

  .task-threads-chip-count {
    font-size: 0.625rem;
    line-height: 1;
  }
</style>
