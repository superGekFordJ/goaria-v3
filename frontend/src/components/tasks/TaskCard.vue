<script setup lang="ts">
  import { computed } from 'vue'
  import { Task } from '../../../bindings/goaria-v3/internal/rpc/models'
  import { useTaskStore } from '../../stores/task'
  import { Pause, Play, FolderOpen, Trash2, FileDown, Clock, Zap } from 'lucide-vue-next'

  const props = defineProps<{
    task: Task
  }>()

  const emit = defineEmits<{
    (e: 'confirm-delete', task: Task): void
  }>()

  const taskStore = useTaskStore()

  // Extract filename from path
  const fileName = computed(() => {
    const path = props.task.files?.[0]?.path
    if (!path) return '正在解析资源...'
    return path.split(/[\\/]/).pop() || '未知文件'
  })

  // Calculate progress percentage
  const progress = computed(() => {
    const total = Number(props.task.totalLength)
    const completed = Number(props.task.completedLength)
    if (total <= 0) return 0
    return Math.min((completed / total) * 100, 100)
  })

  // Format bytes to human readable
  const formatSize = (b: string | number | undefined) => {
    if (!b || b === '0') return '0 B'
    const bytes = Number(b)
    if (bytes === 0) return '0 B'
    const i = Math.floor(Math.log(bytes) / Math.log(1024))
    return (bytes / Math.pow(1024, i)).toFixed(2) + ' ' + ['B', 'KB', 'MB', 'GB', 'TB'][i]
  }

  // Format speed with neon styling consideration
  const formatSpeed = (b: string | number | undefined) => {
    if (!b || b === '0') return '0'
    const bytes = Number(b)
    if (bytes === 0) return '0'
    const i = Math.floor(Math.log(bytes) / Math.log(1024))
    return (bytes / Math.pow(1024, i)).toFixed(1)
  }

  const speedUnit = (b: string | number | undefined) => {
    if (!b || b === '0') return 'B/s'
    const bytes = Number(b)
    if (bytes === 0) return 'B/s'
    const i = Math.floor(Math.log(bytes) / Math.log(1024))
    return ['B', 'KB', 'MB', 'GB', 'TB'][i] + '/s'
  }

  // Calculate ETA
  const estimatedTime = computed(() => {
    const speed = Number(props.task.downloadSpeed)
    const remaining = Number(props.task.totalLength) - Number(props.task.completedLength)
    if (speed <= 0 || remaining <= 0) return '--'

    const seconds = Math.floor(remaining / speed)
    if (seconds < 60) return `${seconds}s`
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
    const hours = Math.floor(seconds / 3600)
    const mins = Math.floor((seconds % 3600) / 60)
    return `${hours}h ${mins}m`
  })

  // Status styling with neon effects
  const statusConfig = computed(() => {
    switch (props.task.status) {
      case 'active':
        return {
          dotClass: 'status-active',
          label: '下载中',
          labelClass: 'text-[var(--status-active)]',
          showProgress: true,
        }
      case 'complete':
        return {
          dotClass: 'status-complete',
          label: '已完成',
          labelClass: 'text-[var(--status-complete)]',
          showProgress: false,
        }
      case 'paused':
        return {
          dotClass: 'status-paused',
          label: '已暂停',
          labelClass: 'text-amber-400',
          showProgress: true,
        }
      case 'waiting':
        return {
          dotClass: 'status-waiting',
          label: '等待中',
          labelClass: 'text-[var(--app-text-muted)]',
          showProgress: true,
        }
      case 'error':
        return {
          dotClass: 'status-error',
          label: '错误',
          labelClass: 'text-red-400',
          showProgress: false,
        }
      default:
        return {
          dotClass: 'status-waiting',
          label: props.task.status || '未知',
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
</script>

<template>
  <div
    :class="[
      'task-card glass-panel rounded-[var(--radius-squircle-xl)] p-5 group hover-reveal-container',
      cardGlowClass,
    ]"
  >
    <!-- Top Row: Filename & Actions -->
    <div class="flex items-start justify-between gap-4 mb-4">
      <!-- File Info -->
      <div class="flex-1 min-w-0">
        <div class="flex items-center gap-3 mb-2">
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
            <FileDown :size="18" />
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
            :title="isActive ? '暂停' : '继续'"
            @click="isActive ? taskStore.pause(task.gid) : taskStore.resume(task.gid)"
          >
            <Pause v-if="isActive" :size="16" />
            <Play v-else :size="16" class="ml-0.5" />
          </button>
        </template>

        <!-- Open Folder Button -->
        <button
          class="btn-glass w-10 h-10 rounded-xl flex items-center justify-center text-[var(--app-text-muted)] hover:text-[var(--app-text)] hover:border-[var(--glass-border)]"
          title="打开文件夹"
          @click="taskStore.openTaskFolder(task)"
        >
          <FolderOpen :size="16" />
        </button>

        <!-- Delete Button -->
        <button
          class="btn-glass w-10 h-10 rounded-xl flex items-center justify-center text-[var(--app-text-muted)] hover:text-[var(--status-error)] hover:bg-[var(--status-error)]/10 hover:border-[var(--status-error)]/30"
          title="删除任务"
          @click="emit('confirm-delete', task)"
        >
          <Trash2 :size="16" />
        </button>
      </div>
    </div>

    <!-- Progress Bar (Neon Style with Energy Flow) -->
    <div v-if="statusConfig.showProgress" class="mb-4">
      <div class="progress-bar-container">
        <div
          :class="[
            'progress-bar-fill',
            { 'opacity-50': isPaused },
            { 'progress-bar-energy': isActive },
          ]"
          :style="{ width: progress + '%' }"
        ></div>
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
            进度
          </span>
          <div class="font-mono-data text-xs text-[var(--app-text-muted)]">
            <span class="text-[var(--app-text)]/70">{{ formatSize(task.completedLength) }}</span>
            <span class="mx-1 text-[var(--app-text-subtle)]">/</span>
            <span>{{ formatSize(task.totalLength) }}</span>
          </div>
        </div>

        <!-- Progress Percentage -->
        <div class="flex flex-col">
          <span
            class="text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-1"
          >
            完成
          </span>
          <div class="font-mono-data text-xs text-[var(--app-text-muted)]">
            {{ progress.toFixed(1)
            }}<span class="text-[10px] text-[var(--app-text-subtle)]">%</span>
          </div>
        </div>

        <!-- ETA (only when downloading) -->
        <div v-if="isActive" class="flex flex-col">
          <span
            class="text-[9px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-1"
          >
            剩余
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
          速度
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
            下载完成
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

  /* Override progress bar for paused state */
  .task-card .progress-bar-fill.opacity-50 {
    box-shadow: none;
    background: linear-gradient(
      90deg,
      var(--status-paused),
      color-mix(in srgb, var(--status-paused) 80%, #000)
    );
  }

  .task-card .progress-bar-fill.opacity-50::after {
    display: none;
  }
</style>
