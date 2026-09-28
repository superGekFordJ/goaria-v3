<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Pause, Play, X, Copy, CheckCircle } from '@lucide/vue'
  import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
  import { useTaskStore } from '../../stores/task'
  import { copyToClipboard } from '../../utils/clipboard'
  import { formatDuration } from '../../composables/useTaskEta'
  import {
    formatAbsoluteTime,
    formatRelativeTime,
    formatSize,
    formatSpeed,
    packGridCells,
    parentDirectory,
    parseLiveThreadCount,
    sourceDisplay,
    speedUnit,
  } from '../../utils/taskDisplay'
  import { explainTaskErrorKey } from '../../utils/taskErrorExplain'

  const ENGINE_SURGE = 'Surge'
  const ENGINE_ARIA2 = 'Aria2'
  const GRID_COLUMNS = 4
  const GRID_MAX_ROWS = 2
  const ACTIVE_REFRESH_MS = 5000
  const RELATIVE_TICK_MS = 30_000
  const COPIED_FEEDBACK_MS = 1200

  const props = defineProps<{
    task: Task
    eta: string
  }>()

  const emit = defineEmits<{
    (e: 'close', restoreFocus: boolean): void
  }>()

  const i18n = useI18n()
  const { t } = i18n
  const taskStore = useTaskStore()

  const rootRef = ref<HTMLElement | null>(null)
  const closeButtonRef = ref<HTMLButtonElement | null>(null)
  const nowMs = ref(Date.now())
  const copiedKey = ref<string | null>(null)

  const localeTag = computed(() => i18n.locale.value || 'en')

  const status = computed(() => props.task.status)
  const isSurge = computed(() => props.task.gid.startsWith('sg_'))
  // Group-detail snapshot members are not in the store: no live telemetry.
  const isLive = computed(
    () =>
      status.value === 'active' &&
      taskStore.activeTasks.some((task: Task) => task.gid === props.task.gid),
  )

  const fileName = computed(() => {
    const path = props.task.files?.[0]?.path
    if (!path) return t('taskCard.parsing')
    return path.split(/[\\/]/).pop() || t('taskCard.unknownFile')
  })

  const statusMeta = computed(() => {
    switch (status.value) {
      case 'active':
        return { dot: 'status-active', label: t('taskCard.downloading') }
      case 'complete':
        return { dot: 'status-complete', label: t('taskCard.completed') }
      case 'paused':
        return { dot: 'status-paused', label: t('taskCard.paused') }
      case 'waiting':
        return { dot: 'status-waiting', label: t('taskCard.waiting') }
      case 'error':
        return { dot: 'status-error', label: t('taskCard.error') }
      default:
        return { dot: 'status-waiting', label: status.value || t('taskCard.unknown') }
    }
  })

  const hasKnownTotal = computed(() => {
    const total = Number(props.task.totalLength)
    return Number.isFinite(total) && total > 0
  })

  const downloadedText = computed(() => {
    const done = formatSize(props.task.completedLength)
    return `${done} / ${hasKnownTotal.value ? formatSize(props.task.totalLength) : '--'}`
  })

  const primaryMetric = computed(() => {
    if (status.value === 'active' && isLive.value) {
      return {
        kind: 'speed' as const,
        value: formatSpeed(props.task.downloadSpeed),
        unit: speedUnit(props.task.downloadSpeed),
      }
    }
    if (status.value === 'complete') {
      const size = hasKnownTotal.value ? props.task.totalLength : props.task.completedLength
      return { kind: 'text' as const, value: formatSize(size), unit: '' }
    }
    return { kind: 'text' as const, value: downloadedText.value, unit: '' }
  })

  const primaryAction = computed<'pause' | 'resume' | null>(() => {
    if (status.value === 'active') return 'pause'
    if (status.value === 'paused' || status.value === 'waiting' || status.value === 'error') {
      return 'resume'
    }
    return null
  })

  function runPrimaryAction() {
    if (primaryAction.value === 'pause') taskStore.pause(props.task.gid)
    else if (primaryAction.value === 'resume') taskStore.resume(props.task.gid)
  }

  // Only data fetched for this gid AND this status is trusted.
  const detailState = computed(() => {
    const state = taskStore.taskDetail
    if (!state || state.gid !== props.task.gid || state.status !== status.value) return null
    return state
  })
  const t1Phase = computed<'pending' | 'hidden' | 'ready'>(() => {
    const state = detailState.value
    if (!state || state.phase === 'pending') return 'pending'
    if (state.phase === 'failed' || !state.found || !state.detail) return 'hidden'
    return 'ready'
  })
  const detail = computed(() => (t1Phase.value === 'ready' ? detailState.value?.detail : undefined))

  const sourceUris = computed<string[]>(() => {
    const fromDetail = detail.value?.uris
    if (fromDetail && fromDetail.length > 0) return fromDetail
    return (props.task.files?.[0]?.uris ?? []).map(u => u.uri)
  })
  const source = computed(() => sourceDisplay(sourceUris.value))

  const saveLocation = computed(() => {
    return props.task.dir || parentDirectory(props.task.files?.[0]?.path)
  })

  const hasErrorInfo = computed(() => !!(props.task.errorCode || props.task.errorMessage))

  type CellKind =
    | 'remaining'
    | 'downloaded'
    | 'peak'
    | 'connections'
    | 'source'
    | 'saveLocation'
    | 'engine'
    | 'addedAt'
    | 'completedAt'
    | 'timeTaken'
    | 'avgSpeed'
    | 'error'

  interface Cell {
    kind: CellKind
    span: 1 | 2 | 4
    pending?: boolean
    value?: string
    unit?: string
    title?: string
  }

  function t1Cell(kind: CellKind, value: number | undefined, render: (v: number) => Cell) {
    if (t1Phase.value === 'pending') return { kind, span: 1, pending: true } as Cell
    if (t1Phase.value === 'hidden' || !value || value <= 0) return null
    return render(value)
  }

  function speedCell(kind: CellKind, bps: number): Cell {
    return { kind, span: 1, value: formatSpeed(bps), unit: speedUnit(bps) }
  }

  function timeCell(kind: CellKind, tsSec: number): Cell {
    return {
      kind,
      span: 1,
      value: formatRelativeTime(tsSec, nowMs.value, localeTag.value, t('taskDetail.justNow')),
      title: formatAbsoluteTime(tsSec, localeTag.value),
    }
  }

  const cells = computed<Cell[]>(() => {
    const d = detail.value
    const peak = () => t1Cell('peak', d?.peak_speed, v => speedCell('peak', v))
    const addedAt = () => t1Cell('addedAt', d?.added_at, v => timeCell('addedAt', v))
    const sourceCell = (): Cell | null => (source.value ? { kind: 'source', span: 2 } : null)
    const saveCell = (): Cell | null =>
      saveLocation.value ? { kind: 'saveLocation', span: 2 } : null
    const engine: Cell = {
      kind: 'engine',
      span: 1,
      value: isSurge.value ? ENGINE_SURGE : ENGINE_ARIA2,
    }

    let ordered: Array<Cell | null>
    switch (status.value) {
      case 'active': {
        const threads = parseLiveThreadCount(props.task)
        ordered = [
          isLive.value ? { kind: 'remaining', span: 1, value: props.eta } : null,
          { kind: 'downloaded', span: 2, value: downloadedText.value },
          peak(),
          isLive.value && isSurge.value && threads !== null
            ? { kind: 'connections', span: 1, value: String(threads) }
            : null,
          sourceCell(),
          engine,
          addedAt(),
        ]
        break
      }
      case 'waiting':
        ordered = [sourceCell(), saveCell(), engine, addedAt()]
        break
      case 'paused':
        ordered = [sourceCell(), saveCell(), peak(), engine, addedAt()]
        break
      case 'error':
        ordered = [hasErrorInfo.value ? { kind: 'error', span: 4 } : null, sourceCell(), saveCell()]
        break
      case 'complete':
        ordered = [
          t1Cell('timeTaken', d?.time_taken_ms, v => ({
            kind: 'timeTaken',
            span: 1,
            value: formatDuration(Math.max(1, Math.round(v / 1000))),
          })),
          t1Cell('avgSpeed', d?.avg_speed, v => speedCell('avgSpeed', v)),
          peak(),
          t1Cell('completedAt', d?.completed_at, v => timeCell('completedAt', v)),
          sourceCell(),
          saveCell(),
        ]
        break
      default:
        ordered = [sourceCell(), saveCell(), engine]
    }
    return packGridCells(
      ordered.filter((c): c is Cell => c !== null),
      GRID_COLUMNS,
      GRID_MAX_ROWS,
    )
  })

  const cellLabelKey: Record<Exclude<CellKind, 'error'>, string> = {
    remaining: 'taskCard.remaining',
    downloaded: 'taskDetail.downloaded',
    peak: 'taskDetail.peak',
    connections: 'taskDetail.connections',
    source: 'taskDetail.source',
    saveLocation: 'taskDetail.saveLocation',
    engine: 'taskDetail.engine',
    addedAt: 'taskDetail.addedAt',
    completedAt: 'taskDetail.completedAt',
    timeTaken: 'taskDetail.timeTaken',
    avgSpeed: 'taskDetail.avgSpeed',
  }

  const spanClass = (span: Cell['span']) =>
    span === 4 ? 'col-span-4' : span === 2 ? 'col-span-2' : 'col-span-1'

  const errorExplanation = computed(() =>
    t(explainTaskErrorKey(props.task.errorCode, props.task.errorMessage)),
  )

  // nowMs only feeds relative-time cells; without one the tick is wasted work.
  const hasTimeCell = computed(() =>
    cells.value.some(cell => cell.kind === 'addedAt' || cell.kind === 'completedAt'),
  )

  let copiedTimer: ReturnType<typeof setTimeout> | null = null
  async function copyValue(key: string, text: string | undefined) {
    if (!text) return
    const ok = await copyToClipboard(text)
    if (!ok) return
    copiedKey.value = key
    if (copiedTimer) clearTimeout(copiedTimer)
    copiedTimer = setTimeout(() => {
      copiedKey.value = null
      copiedTimer = null
    }, COPIED_FEEDBACK_MS)
  }
  const copyLabel = (key: string) =>
    copiedKey.value === key ? t('taskDetail.copied') : t('taskDetail.copy')

  function onCloseClick(event: MouseEvent) {
    // detail === 0 means keyboard activation (Enter/Space).
    emit('close', event.detail === 0)
  }

  function onDocumentPointerDown(event: PointerEvent) {
    const card = rootRef.value?.closest('.task-card')
    // A KeepAlive-suspended twin lives in a detached tree: ignore.
    if (!card || !card.isConnected) return
    if (event.target instanceof Node && card.contains(event.target)) return
    emit('close', false)
  }

  function fetchDetail() {
    void taskStore.fetchTaskDetail(props.task.gid, status.value)
  }

  let refreshTimer: ReturnType<typeof setInterval> | null = null
  let relativeTimer: ReturnType<typeof setInterval> | null = null

  function stopRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer)
      refreshTimer = null
    }
  }

  watch(status, () => fetchDetail())
  watch(
    () => status.value === 'active' && taskStore.isWindowVisible,
    shouldRefresh => {
      stopRefresh()
      if (shouldRefresh) refreshTimer = setInterval(fetchDetail, ACTIVE_REFRESH_MS)
    },
    { immediate: true },
  )

  onMounted(() => {
    fetchDetail()
    closeButtonRef.value?.focus({ preventScroll: true })
    document.addEventListener('pointerdown', onDocumentPointerDown, true)
    relativeTimer = setInterval(() => {
      if (hasTimeCell.value) nowMs.value = Date.now()
    }, RELATIVE_TICK_MS)
  })

  onUnmounted(() => {
    document.removeEventListener('pointerdown', onDocumentPointerDown, true)
    stopRefresh()
    if (relativeTimer) clearInterval(relativeTimer)
    if (copiedTimer) clearTimeout(copiedTimer)
    relativeTimer = null
    copiedTimer = null
  })
</script>

<template>
  <div
    :id="`task-detail-${task.gid}`"
    ref="rootRef"
    data-detail-open
    role="region"
    :aria-label="t('taskDetail.regionLabel', { name: fileName })"
    class="task-detail-overlay absolute inset-0 z-10 p-3.5 flex flex-col"
  >
    <!-- Identity strip -->
    <div class="flex items-center gap-2 h-7 shrink-0">
      <span class="status-dot shrink-0" :class="statusMeta.dot" :title="statusMeta.label"></span>
      <span class="sr-only">{{ statusMeta.label }}</span>
      <h3
        class="flex-1 min-w-0 font-semibold text-sm text-[var(--app-text)]/90 truncate"
        :title="fileName"
      >
        {{ fileName }}
      </h3>
      <div data-detail-primary class="shrink-0 flex items-baseline gap-1">
        <template v-if="primaryMetric.kind === 'speed'">
          <span class="font-mono-data text-sm font-bold text-neon leading-none">
            {{ primaryMetric.value }}
          </span>
          <span class="font-mono-data text-[10px] text-[var(--neon-primary)]/60">
            {{ primaryMetric.unit }}
          </span>
        </template>
        <span v-else class="font-mono-data text-xs text-[var(--app-text-muted)]">
          {{ primaryMetric.value }}
        </span>
      </div>
      <button
        v-if="primaryAction"
        type="button"
        data-detail-action
        class="btn-glass w-7 h-7 rounded-[var(--radius-squircle-sm)] flex items-center justify-center shrink-0 text-[var(--app-text-muted)] hover:text-[var(--neon-primary)]"
        :title="primaryAction === 'pause' ? t('taskCard.pause') : t('taskCard.resume')"
        :aria-label="primaryAction === 'pause' ? t('taskCard.pause') : t('taskCard.resume')"
        @click="runPrimaryAction"
      >
        <Pause v-if="primaryAction === 'pause'" :size="14" />
        <Play v-else :size="14" class="ml-0.5" />
      </button>
      <button
        ref="closeButtonRef"
        type="button"
        data-detail-close
        class="btn-glass w-7 h-7 rounded-[var(--radius-squircle-sm)] flex items-center justify-center shrink-0 text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
        :title="t('taskDetail.close')"
        :aria-label="t('taskDetail.close')"
        @click="onCloseClick"
      >
        <X :size="14" />
      </button>
    </div>

    <!-- Fact grid: at most two rows, never scrolls -->
    <div class="grid grid-cols-4 gap-x-3 gap-y-1.5 mt-2 min-w-0">
      <div
        v-for="cell in cells"
        :key="cell.kind"
        :data-cell="cell.kind"
        :class="[spanClass(cell.span), 'min-w-0']"
      >
        <template v-if="cell.kind === 'error'">
          <div class="text-xs leading-4 text-[var(--app-text)] truncate">
            {{ errorExplanation }}
          </div>
          <div v-if="task.errorMessage" class="flex items-center gap-1 min-w-0">
            <span
              class="font-mono-data text-[11px] leading-4 text-[var(--app-text-subtle)] truncate"
              :title="task.errorMessage"
            >
              {{ task.errorMessage }}
            </span>
            <button
              type="button"
              class="task-detail-copy"
              :title="copyLabel('error')"
              :aria-label="copyLabel('error')"
              @click="copyValue('error', task.errorMessage)"
            >
              <CheckCircle v-if="copiedKey === 'error'" :size="11" class="task-detail-copied" />
              <Copy v-else :size="11" />
            </button>
          </div>
        </template>

        <template v-else>
          <span
            class="block text-[9px] leading-3 font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-0.5 truncate"
          >
            {{ t(cellLabelKey[cell.kind]) }}
          </span>

          <span
            v-if="cell.pending"
            data-detail-placeholder
            class="task-detail-placeholder"
            aria-hidden="true"
          ></span>

          <div v-else-if="cell.kind === 'source' && source" class="flex items-center gap-1 min-w-0">
            <span class="text-xs leading-4 text-[var(--app-text-muted)] truncate" :title="source.title">
              {{ source.label }}
            </span>
            <span
              v-if="source.extraCount > 0"
              class="font-mono-data text-[10px] leading-4 text-[var(--app-text-subtle)] shrink-0"
              :title="t('taskDetail.mirrorCount', { count: source.extraCount })"
            >
              {{ t('taskDetail.mirrorBadge', { count: source.extraCount }) }}
            </span>
            <button
              type="button"
              class="task-detail-copy"
              :title="copyLabel('source')"
              :aria-label="copyLabel('source')"
              @click="copyValue('source', source.raw)"
            >
              <CheckCircle v-if="copiedKey === 'source'" :size="11" class="task-detail-copied" />
              <Copy v-else :size="11" />
            </button>
          </div>

          <div v-else-if="cell.kind === 'saveLocation'" class="flex items-center gap-1 min-w-0">
            <span class="text-xs leading-4 text-[var(--app-text-muted)] truncate" :title="saveLocation">
              {{ saveLocation }}
            </span>
            <button
              type="button"
              class="task-detail-copy"
              :title="copyLabel('saveLocation')"
              :aria-label="copyLabel('saveLocation')"
              @click="copyValue('saveLocation', saveLocation)"
            >
              <CheckCircle
                v-if="copiedKey === 'saveLocation'"
                :size="11"
                class="task-detail-copied"
              />
              <Copy v-else :size="11" />
            </button>
          </div>

          <div
            v-else
            class="font-mono-data text-xs leading-4 text-[var(--app-text-muted)] truncate"
            :title="cell.title"
          >
            {{ cell.value }}
            <span v-if="cell.unit" class="text-[10px] text-[var(--app-text-subtle)]">
              {{ cell.unit }}
            </span>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<style scoped>
  .task-detail-placeholder {
    display: block;
    width: 3rem;
    height: 0.75rem;
    margin-top: 0.125rem;
    border-radius: var(--radius-squircle-sm);
    background: color-mix(in srgb, var(--app-text) 10%, transparent);
  }

  .task-detail-copy {
    display: inline-flex;
    flex: 0 0 auto;
    align-items: center;
    justify-content: center;
    width: 1rem;
    height: 1rem;
    border-radius: var(--radius-squircle-sm);
    color: var(--app-text-subtle);
    transition: color 150ms ease;
  }

  .task-detail-copy:hover,
  .task-detail-copy:focus-visible {
    color: var(--neon-primary);
  }

  .task-detail-copied {
    color: var(--status-complete);
  }
</style>
