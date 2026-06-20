<script setup lang="ts">
  import { computed, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { AlertTriangle, Folder, Layers3, Pause, Play, Trash2, FolderOpen } from 'lucide-vue-next'
  import {
    TASK_PROGRESS_CONFIG,
    SURGE_TASK_PROGRESS_CONFIG,
    useSmoothProgress,
  } from '../../composables/useSmoothProgress'
  import {
    normalizeDownloadGroupWarningSummaries,
    type DownloadGroupMasterItem,
    type DownloadGroupOperationAction,
  } from '../../stores/downloadGroups'
  import { useTaskStore } from '../../stores/task'
  import type { DownloadGroupCard } from '../../../bindings/goaria-v3/internal/downloadgroups/models'
  import type { DownloadGroup, Task } from '../../../bindings/goaria-v3/internal/rpc/models'

  const props = defineProps<{
    item: DownloadGroupMasterItem
    readonlyActions?: boolean
    operationBusy?: Partial<Record<DownloadGroupOperationAction, boolean>>
  }>()

  const emit = defineEmits<{
    (e: 'open', groupKey: string): void
    (e: 'pause', groupKey: string): void
    (e: 'resume', groupKey: string): void
    (e: 'remove', groupKey: string): void
    (e: 'open-folder', groupKey: string): void
  }>()

  const { t } = useI18n()
  const taskStore = useTaskStore()

  const groupKey = computed(() => props.item.group_key)

  // Detect Surge group: check if any active/waiting task belonging to this group has sg_ prefix.
  // Evaluated once at setup time — config is fixed for the component's lifetime (same as TaskCard).
  const isSurgeGroup = (() => {
    const key = props.item.group_key
    if (!key) return false
    const check = (tasks: Task[]) =>
      tasks.some(t => t.download_group?.id === key && t.gid.startsWith('sg_'))
    return check(taskStore.activeTasks) || check(taskStore.waitingTasks)
  })()

  const { displayDownloaded, totalBytes, updateStats } = useSmoothProgress(
    isSurgeGroup ? SURGE_TASK_PROGRESS_CONFIG : TASK_PROGRESS_CONFIG,
  )

  const isPlaceholder = computed(() => props.item.type === 'placeholder')
  const card = computed<DownloadGroupCard | null>(() =>
    props.item.type === 'backend' ? props.item.card : null,
  )
  const placeholderGroup = computed<DownloadGroup | null>(() =>
    props.item.type === 'placeholder' ? props.item.placeholder.download_group : null,
  )

  const isBackendCard = computed(() => props.item.type === 'backend' && Boolean(groupKey.value))
  const canSelect = computed(() => isBackendCard.value)
  const isSelected = computed(
    () => canSelect.value && taskStore.isGroupSelected?.(groupKey.value) === true,
  )

  const displayName = computed(() => {
    if (card.value) {
      return (
        card.value.display_name || card.value.fallback_name || t('downloadGroups.placeholderTitle')
      )
    }

    const group = placeholderGroup.value
    return group?.name || group?.folder_name || t('downloadGroups.placeholderTitle')
  })

  const nameStatusLabel = computed(() => {
    if (isPlaceholder.value) return t('downloadGroups.nameStatus.pending')
    const status = card.value?.name_status || 'fallback'
    if (status === 'stable') return t('downloadGroups.nameStatus.stable')
    if (status === 'pending') return t('downloadGroups.nameStatus.pending')
    if (status === 'degraded') return t('downloadGroups.nameStatus.degraded')
    return t('downloadGroups.nameStatus.fallback')
  })

  const warningSummaries = computed(() =>
    card.value
      ? normalizeDownloadGroupWarningSummaries(
          card.value.warnings,
          card.value.name_status,
          card.value.degraded,
        )
      : [],
  )

  const primaryWarning = computed(() => warningSummaries.value[0])

  const warningTitle = computed(() => {
    if (!warningSummaries.value.length) return undefined
    return warningSummaries.value
      .map(summary =>
        t('downloadGroups.warning.tooltip', {
          label: t(summary.labelKey),
          description: t(summary.descriptionKey),
          count: summary.count,
        }),
      )
      .join('\n')
  })

  const nameStatusChipClass = computed(() => {
    const status = card.value?.name_status
    if (status === 'pending') return 'download-group-chip-info'
    if (status === 'degraded') return 'download-group-chip-degraded'
    return ''
  })

  const statusKey = computed(() => {
    if (isPlaceholder.value) return 'waiting'
    const status = card.value?.status || 'unknown'
    if (
      status === 'active' ||
      status === 'paused' ||
      status === 'waiting' ||
      status === 'error' ||
      status === 'complete'
    ) {
      return status
    }
    return 'unknown'
  })

  const statusLabel = computed(() => t(`downloadGroups.status.${statusKey.value}`))

  const statusDotClass = computed(() => {
    if (isPlaceholder.value) return 'status-waiting'
    switch (statusKey.value) {
      case 'active':
        return 'status-active'
      case 'paused':
        return 'status-paused'
      case 'complete':
        return 'status-complete'
      case 'error':
        return 'status-error'
      default:
        return 'status-waiting'
    }
  })

  const folderLabel = computed(() => {
    if (card.value) return card.value.folder_label || ''
    const group = placeholderGroup.value
    return group?.folder_name || ''
  })

  const folderTitle = computed(() => {
    if (card.value) return card.value.folder_path_hint || card.value.folder_label || undefined
    return placeholderGroup.value?.dir || folderLabel.value || undefined
  })

  const totalCount = computed(() => {
    if (card.value) return card.value.counts.expected || card.value.counts.resolved || 0
    return placeholderGroup.value?.item_count || 0
  })

  const activeCount = computed(() => card.value?.counts.active ?? 0)
  const waitingCount = computed(
    () => (card.value?.counts.waiting ?? 0) + (card.value?.counts.paused ?? 0),
  )
  const completeCount = computed(() => card.value?.counts.complete ?? 0)
  const errorCount = computed(() => card.value?.counts.error ?? 0)

  const isPauseBusy = computed(() => props.operationBusy?.pause === true)
  const isResumeBusy = computed(() => props.operationBusy?.resume === true)
  const isOpenFolderBusy = computed(() => props.operationBusy?.open_folder === true)
  const isRemoveBusy = computed(() => props.operationBusy?.remove === true)

  const canPause = computed(
    () =>
      isBackendCard.value &&
      !props.readonlyActions &&
      !isPauseBusy.value &&
      ((card.value?.counts.active ?? 0) > 0 || (card.value?.counts.waiting ?? 0) > 0),
  )
  const canResume = computed(
    () =>
      isBackendCard.value &&
      !props.readonlyActions &&
      !isResumeBusy.value &&
      (card.value?.counts.paused ?? 0) > 0,
  )
  const canOpenFolder = computed(
    () =>
      isBackendCard.value &&
      !props.readonlyActions &&
      !isOpenFolderBusy.value &&
      card.value?.has_folder === true,
  )
  const canRemove = computed(
    () => isBackendCard.value && !props.readonlyActions && !isRemoveBusy.value,
  )
  const canOpenDetail = computed(() => isBackendCard.value)

  const progressPercent = computed(() => {
    if (!card.value) return 0
    return Math.round(Math.min(Math.max(card.value.progress || 0, 0), 1) * 100)
  })

  const smoothProgressScale = computed(() => {
    if (!card.value || totalBytes.value <= 0) return 0
    const ratio = displayDownloaded.value / totalBytes.value
    return Math.min(Math.max(ratio, 0), 1)
  })

  watch(
    [
      () => card.value?.completed_length,
      () => card.value?.download_speed,
      () => card.value?.total_length,
      () => card.value?.status,
    ],
    ([downloaded, speed, total, status]) => {
      if (!card.value) return
      updateStats({
        downloaded: Number(downloaded),
        speed: Number(speed),
        total: Number(total),
        status: status as string,
      })
    },
    { immediate: true },
  )

  function formatBytes(value?: string): string {
    const bytes = Number(value || 0)
    if (!Number.isFinite(bytes) || bytes <= 0) {
      return t('downloadGroups.byteSize', { value: '0', unit: 'B' })
    }
    const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), 4)
    return t('downloadGroups.byteSize', {
      value: (bytes / Math.pow(1024, unitIndex)).toFixed(unitIndex === 0 ? 0 : 1),
      unit: ['B', 'KB', 'MB', 'GB', 'TB'][unitIndex],
    })
  }

  function formatSpeed(value?: string): string {
    return t('downloadGroups.byteSpeed', { size: formatBytes(value) })
  }

  function handleOpen() {
    if (!canOpenDetail.value) return
    emit('open', groupKey.value)
  }

  function toggleSelection() {
    if (!canSelect.value || !taskStore.toggleSelectGroup) return
    taskStore.toggleSelectGroup(groupKey.value)
  }

  function emitAction(action: DownloadGroupOperationAction) {
    if (!groupKey.value || isPlaceholder.value) return
    if (action === 'pause') emit('pause', groupKey.value)
    if (action === 'resume') emit('resume', groupKey.value)
    if (action === 'remove') emit('remove', groupKey.value)
    if (action === 'open_folder') emit('open-folder', groupKey.value)
  }

  function handleCardClick(event: MouseEvent) {
    const target = event.target as HTMLElement | null
    if (target?.closest('input, button')) return
    handleOpen()
  }
</script>

<template>
  <article
    :class="[
      'download-group-card glass-panel rounded-[var(--radius-squircle-xl)] p-4 transition-all duration-300 hover-reveal-container',
      isPlaceholder ? 'download-group-card-placeholder' : '',
      card?.degraded ? 'download-group-card-degraded' : '',
      isSelected ? 'download-group-card-selected task-card-selected' : '',
      canOpenDetail ? 'download-group-card-openable' : 'download-group-card-disabled',
    ]"
    :data-group-key="groupKey"
    :data-placeholder="isPlaceholder ? 'true' : 'false'"
    @click="handleCardClick"
  >
    <div class="download-group-card-main">
      <div class="checkbox-container" :class="{ 'always-visible': isSelected }">
        <input
          type="checkbox"
          :checked="isSelected"
          :disabled="!canSelect"
          class="task-checkbox"
          @click.stop="toggleSelection"
        />
      </div>

      <div class="download-group-card-icon rounded-[var(--radius-squircle-md)]">
        <Layers3 :size="20" />
      </div>

      <div class="min-w-0 flex-1 text-left">
        <div class="flex items-center gap-2 mb-1">
          <h3 class="download-group-card-title truncate" :title="displayName">
            {{ displayName }}
          </h3>
          <span v-if="isPlaceholder" class="download-group-chip download-group-chip-pending">
            {{ t('downloadGroups.pendingCard') }}
          </span>
          <span
            v-else-if="primaryWarning"
            class="download-group-chip"
            :class="`download-group-chip-${primaryWarning.severity}`"
            :title="warningTitle"
          >
            <AlertTriangle :size="11" />
            {{ t('downloadGroups.warning.badge', { count: warningSummaries.length }) }}
          </span>
          <span
            v-if="
              !isPlaceholder &&
              (card?.name_status === 'pending' || card?.name_status === 'degraded')
            "
            class="download-group-chip"
            :class="nameStatusChipClass"
            :title="warningTitle"
          >
            {{ nameStatusLabel }}
          </span>
        </div>

        <div class="flex flex-wrap items-center gap-2 text-[11px] text-[var(--app-text-subtle)]">
          <span class="inline-flex items-center gap-1.5">
            <span class="status-dot" :class="statusDotClass"></span>
            <span class="font-bold uppercase tracking-[0.14em]">{{ statusLabel }}</span>
          </span>
          <span>{{ nameStatusLabel }}</span>
          <span v-if="folderLabel" class="download-group-folder" :title="folderTitle">
            <Folder :size="12" />
            {{ t('downloadGroups.folderLabel', { folder: folderLabel }) }}
          </span>
        </div>
      </div>
    </div>

    <div class="download-group-card-body">
      <p v-if="isPlaceholder" class="download-group-card-description">
        {{ t('downloadGroups.placeholderDescription') }}
      </p>

      <div class="download-group-card-metrics">
        <div class="download-group-metric">
          <span>{{ t('downloadGroups.memberCount') }}</span>
          <strong class="font-mono-data">{{ totalCount }}</strong>
        </div>
        <template v-if="!isPlaceholder">
          <div class="download-group-metric">
            <span>{{ t('downloadGroups.activeCount') }}</span>
            <strong class="font-mono-data">{{ activeCount }}</strong>
          </div>
          <div class="download-group-metric">
            <span>{{ t('downloadGroups.waitingCount') }}</span>
            <strong class="font-mono-data">{{ waitingCount }}</strong>
          </div>
          <div class="download-group-metric">
            <span>{{ t('downloadGroups.completeCount') }}</span>
            <strong class="font-mono-data">{{ completeCount }}</strong>
          </div>
          <div v-if="errorCount > 0" class="download-group-metric download-group-metric-error">
            <span>{{ t('downloadGroups.errorCount') }}</span>
            <strong class="font-mono-data">{{ errorCount }}</strong>
          </div>
        </template>
      </div>

      <div v-if="!isPlaceholder && statusKey !== 'complete'" class="download-group-progress">
        <div class="progress-bar-container mt-2">
          <div
            class="progress-bar-fill"
            :style="{ transform: `scaleX(${smoothProgressScale})` }"
          ></div>
        </div>
      </div>

      <div v-if="!isPlaceholder" class="download-group-card-meta">
        <span>{{ t('downloadGroups.totalSize') }}: {{ formatBytes(card?.total_length) }}</span>
        <span
          >{{ t('downloadGroups.downloadSpeed') }}: {{ formatSpeed(card?.download_speed) }}</span
        >
        <span
          >{{ t('downloadGroups.progress') }}:
          {{ t('downloadGroups.percent', { value: progressPercent }) }}</span
        >
      </div>

      <div class="download-group-actions" :aria-label="t('downloadGroups.action.groupActions')">
        <span v-if="isPlaceholder" class="download-group-actions-label">
          {{ t('downloadGroups.action.placeholderDisabled') }}
        </span>
        <span
          v-else-if="isPauseBusy || isResumeBusy || isOpenFolderBusy || isRemoveBusy"
          class="download-group-actions-label"
        >
          {{ t('downloadGroups.action.busy') }}
        </span>
        <button
          class="btn-glass download-group-action-btn"
          type="button"
          :disabled="!canPause"
          :title="t('downloadGroups.action.pause')"
          @click.stop="emitAction('pause')"
        >
          <Pause :size="14" />
        </button>
        <button
          class="btn-glass download-group-action-btn"
          type="button"
          :disabled="!canResume"
          :title="t('downloadGroups.action.resume')"
          @click.stop="emitAction('resume')"
        >
          <Play :size="14" />
        </button>
        <button
          class="btn-glass download-group-action-btn"
          type="button"
          :disabled="!canOpenFolder"
          :title="t('downloadGroups.action.openFolder')"
          @click.stop="emitAction('open_folder')"
        >
          <FolderOpen :size="14" />
        </button>
        <button
          class="btn-glass download-group-action-btn"
          type="button"
          :disabled="!canRemove"
          :title="t('downloadGroups.action.remove')"
          @click.stop="emitAction('remove')"
        >
          <Trash2 :size="14" />
        </button>
      </div>
    </div>
  </article>
</template>

<style scoped>
  .download-group-card {
    position: relative;
    overflow: hidden;
  }

  .download-group-card::before {
    content: '';
    position: absolute;
    inset: 0;
    border-radius: inherit;
    background: linear-gradient(
      135deg,
      color-mix(in srgb, var(--skin-surface-tint) 80%, transparent),
      transparent 58%
    );
    pointer-events: none;
  }

  .download-group-card-main {
    position: relative;
    display: flex;
    width: 100%;
    align-items: flex-start;
    gap: 1rem;
  }

  .download-group-card-openable .download-group-card-main {
    cursor: pointer;
  }

  .download-group-card-disabled .download-group-card-main {
    cursor: default;
  }

  .download-group-card-icon {
    display: flex;
    width: 2.75rem;
    height: 2.75rem;
    flex: 0 0 auto;
    align-items: center;
    justify-content: center;
    border: 1px solid color-mix(in srgb, var(--neon-primary) 18%, transparent);
    background: color-mix(in srgb, var(--neon-primary) 8%, transparent);
    color: var(--neon-primary);
  }

  .download-group-card-title {
    font-size: 0.9375rem;
    font-weight: 800;
    color: color-mix(in srgb, var(--app-text) 90%, var(--neon-primary));
  }

  .download-group-chip,
  .download-group-folder,
  .download-group-actions-label {
    display: inline-flex;
    align-items: center;
    gap: 0.3125rem;
    border-radius: var(--radius-squircle-sm);
  }

  .download-group-chip {
    padding: 0.125rem 0.5rem;
    border: 1px solid color-mix(in srgb, var(--neon-primary) 18%, transparent);
    background: color-mix(in srgb, var(--neon-primary) 7%, transparent);
    color: color-mix(in srgb, var(--neon-primary) 78%, var(--app-text));
    font-size: 0.625rem;
    font-weight: 800;
  }

  .download-group-chip-degraded {
    border-color: color-mix(in srgb, var(--status-paused) 24%, transparent);
    background: color-mix(in srgb, var(--status-paused) 9%, transparent);
    color: color-mix(in srgb, var(--status-paused) 78%, var(--app-text));
  }

  .download-group-chip-info {
    border-color: color-mix(in srgb, var(--neon-primary) 18%, transparent);
    background: color-mix(in srgb, var(--neon-primary) 7%, transparent);
    color: color-mix(in srgb, var(--neon-primary) 78%, var(--app-text));
  }

  .download-group-chip-warning {
    border-color: color-mix(in srgb, var(--status-paused) 24%, transparent);
    background: color-mix(in srgb, var(--status-paused) 9%, transparent);
    color: color-mix(in srgb, var(--status-paused) 78%, var(--app-text));
  }

  .download-group-chip-error {
    border-color: color-mix(in srgb, var(--status-error) 24%, transparent);
    background: color-mix(in srgb, var(--status-error) 9%, transparent);
    color: color-mix(in srgb, var(--status-error) 78%, var(--app-text));
  }

  .download-group-folder {
    min-width: 0;
    max-width: 24rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .download-group-card-body {
    position: relative;
    margin-top: 0.875rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  .download-group-card-description {
    color: var(--app-text-muted);
    font-size: 0.75rem;
  }

  .download-group-card-metrics {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(5.5rem, 1fr));
    gap: 0.5rem;
  }

  .download-group-metric {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    border: 1px solid var(--glass-border);
    border-radius: var(--radius-squircle-md);
    background: var(--btn-glass-bg);
    padding: 0.5rem 0.625rem;
  }

  .download-group-metric span,
  .download-group-card-meta {
    color: var(--app-text-subtle);
    font-size: 0.625rem;
    font-weight: 700;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }

  .download-group-metric strong {
    color: var(--app-text);
    font-size: 0.875rem;
  }

  .download-group-metric-error strong {
    color: var(--status-error);
  }

  .download-group-card-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 0.875rem;
    text-transform: none;
  }

  .download-group-actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.5rem;
  }

  .download-group-actions-label {
    margin-right: auto;
    color: var(--app-text-subtle);
    font-size: 0.6875rem;
    font-weight: 700;
  }

  .download-group-action-btn {
    display: inline-flex;
    width: 2rem;
    height: 2rem;
    align-items: center;
    justify-content: center;
    border-radius: var(--radius-squircle-sm);
    color: var(--app-text-subtle);
  }

  .download-group-action-btn:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .download-group-action-btn:not(:disabled) {
    color: var(--app-text-muted);
    opacity: 1;
    cursor: pointer;
  }

  .download-group-action-btn:not(:disabled):hover {
    color: var(--app-text);
  }

  .download-group-card-placeholder {
    border-color: color-mix(in srgb, var(--neon-primary) 20%, var(--glass-border));
  }

  .download-group-card-degraded {
    border-color: color-mix(in srgb, var(--status-paused) 24%, var(--glass-border));
  }
</style>
