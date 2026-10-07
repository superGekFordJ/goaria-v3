<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Pause, Play, X, Copy, CheckCircle } from '@lucide/vue'
  import { Task } from '../../../bindings/goaria-v3/internal/rpc/models.js'
  import { useTaskStore } from '../../stores/task'
  import TaskChunkMap from './TaskChunkMap.vue'
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
  const LIVE_REFRESH_MS = 1000
  const RELATIVE_TICK_MS = 30_000
  const COPIED_FEEDBACK_MS = 1200

  const props = defineProps<{
    task: Task
    // Single source of truth from the card: active and backed by live telemetry.
    live: boolean
    // One-shot claim armed by a user-initiated open on the card; a mount that
    // finds it may move focus, then reports back so it cannot fire twice.
    focusOnMount?: boolean
  }>()

  const emit = defineEmits<{
    (e: 'close', restoreFocus: boolean): void
    (e: 'focus-claimed'): void
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
  const isLive = computed(() => props.live && status.value === 'active')

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

  // The grid only carries facts the card face cannot show. Live cards reveal
  // their own stats row, so progress numbers stay out of the grid; peak is a
  // terminal-only fact (it appears late and would reflow a running task).
  const cells = computed<Cell[]>(() => {
    const d = detail.value
    const addedAt = () => t1Cell('addedAt', d?.added_at, v => timeCell('addedAt', v))
    const sourceCell = (): Cell | null => (source.value ? { kind: 'source', span: 2 } : null)
    const saveCell = (): Cell | null =>
      saveLocation.value ? { kind: 'saveLocation', span: 2 } : null
    const downloaded: Cell = { kind: 'downloaded', span: 2, value: downloadedText.value }
    const engine: Cell = {
      kind: 'engine',
      span: 1,
      value: isSurge.value ? ENGINE_SURGE : ENGINE_ARIA2,
    }

    let ordered: Array<Cell | null>
    switch (status.value) {
      case 'active': {
        if (!isLive.value) {
          ordered = [downloaded, sourceCell(), engine, addedAt()]
          break
        }
        const threads = parseLiveThreadCount(props.task)
        ordered = [
          sourceCell(),
          saveCell(),
          isSurge.value && threads !== null
            ? { kind: 'connections', span: 1, value: String(threads) }
            : null,
          engine,
          addedAt(),
        ]
        break
      }
      case 'waiting':
      case 'paused':
        ordered = [downloaded, sourceCell(), saveCell(), engine, addedAt()]
        break
      case 'error':
        ordered = [
          hasErrorInfo.value ? { kind: 'error', span: 4 } : null,
          downloaded,
          sourceCell(),
          saveCell(),
        ]
        break
      case 'complete':
        ordered = [
          t1Cell('timeTaken', d?.time_taken_ms, v => ({
            kind: 'timeTaken',
            span: 1,
            value: formatDuration(Math.max(1, Math.round(v / 1000))),
          })),
          t1Cell('avgSpeed', d?.avg_speed, v => speedCell('avgSpeed', v)),
          t1Cell('peak', d?.peak_speed, v => speedCell('peak', v)),
          t1Cell('completedAt', d?.completed_at, v => timeCell('completedAt', v)),
          sourceCell(),
          saveCell(),
        ]
        break
      default:
        ordered = [sourceCell(), saveCell(), engine]
    }
    const kept = ordered.filter((c): c is Cell => c !== null)
    return packGridCells(kept, GRID_COLUMNS, GRID_MAX_ROWS)
  })

  const cellLabelKey: Record<Exclude<CellKind, 'error'>, string> = {
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

  const spanClass = (span: Cell['span']) => {
    return span === 4 ? 'col-span-4' : span === 2 ? 'col-span-2' : 'col-span-1'
  }

  // A twin mounted inside a KeepAlive-suspended (detached) card must not fetch,
  // tick, listen, or draw: it would race the visible copy's request ordering.
  const detachedMount = ref(false)

  interface ChunkFrame {
    states: readonly number[]
    count?: number
    size?: number
  }

  // Chunk maps exist for Surge tasks that are downloading or paused (frozen).
  // Snapshot members qualify too: their frames come from the same monitor cache.
  const chunkEligible = computed(
    () => isSurge.value && (status.value === 'active' || status.value === 'paused'),
  )
  const readyChunkFrame = computed<ChunkFrame | null>(() => {
    const d = detail.value
    const states = d?.chunk_states
    if (!d || !states || states.length === 0) return null
    return { states, count: d.chunk_count, size: d.chunk_size }
  })
  // The last trusted frame bridges fetch gaps — a status refetch's pending
  // phase and the empty window right after a resume or silent retry clears
  // the engine's snapshot — so the map recolors in place instead of blinking.
  const lastChunkFrame = shallowRef<ChunkFrame | null>(null)
  watch(
    [t1Phase, readyChunkFrame],
    ([phase, frame]) => {
      // Only a real frame may replace the fallback; an empty ready response
      // must not erase it.
      if (phase !== 'pending' && frame) lastChunkFrame.value = frame
    },
    { immediate: true },
  )
  // undefined: no chunk column. null: box reserved while the first fetch is
  // pending, so the fact grid does not shift sideways when data lands.
  const chunkFrame = computed<ChunkFrame | null | undefined>(() => {
    if (!chunkEligible.value || detachedMount.value) return undefined
    if (t1Phase.value === 'pending') return lastChunkFrame.value
    if (t1Phase.value !== 'ready') return undefined
    // A ready pull that comes back empty keeps the last real frame; a task
    // that never produced one keeps the column hidden.
    return readyChunkFrame.value ?? lastChunkFrame.value ?? undefined
  })

  const errorExplanation = computed(() =>
    t(explainTaskErrorKey(props.task.errorCode, props.task.errorMessage)),
  )

  // nowMs only feeds relative-time cells; without one the tick is wasted work.
  const hasTimeCell = computed(() =>
    cells.value.some(cell => cell.kind === 'addedAt' || cell.kind === 'completedAt'),
  )

  let copiedTimer: ReturnType<typeof setTimeout> | null = null
  let disposed = false
  async function copyValue(key: string, text: string | undefined) {
    if (!text) return
    const ok = await copyToClipboard(text)
    // A resolve landing after unmount must not arm a dangling timer.
    if (!ok || disposed) return
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
    if (detachedMount.value) return
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
  // Live overlays pull near the engine's own snapshot cadence so the chunk
  // map keeps moving; snapshot members keep the slower rhythm. Paused and
  // hidden windows never poll.
  const refreshMs = computed(() => {
    if (status.value !== 'active' || !taskStore.isWindowVisible) return 0
    return isLive.value ? LIVE_REFRESH_MS : ACTIVE_REFRESH_MS
  })
  watch(
    refreshMs,
    ms => {
      stopRefresh()
      if (ms > 0 && !detachedMount.value) {
        refreshTimer = setInterval(fetchDetail, ms)
      }
    },
    { immediate: true },
  )
  // Hidden windows pause polling; on return, pull once so the overlay does
  // not serve the stale frame until the next interval tick.
  watch(
    () => taskStore.isWindowVisible,
    visible => {
      if (visible && refreshMs.value > 0) fetchDetail()
    },
  )
  // A time cell appearing while the tick was gated needs a fresh baseline.
  watch(hasTimeCell, v => {
    if (v) nowMs.value = Date.now()
  })

  onMounted(() => {
    const card = rootRef.value?.closest('.task-card')
    if (card && !card.isConnected) {
      detachedMount.value = true
      stopRefresh()
      return
    }
    fetchDetail()
    // Steal focus only when the card armed this mount from a user open (and
    // focus is free or inside this card); recycled remounts arrive with the
    // claim consumed, so they never move focus.
    if (props.focusOnMount) {
      emit('focus-claimed')
      const active = document.activeElement
      if (!active || active === document.body || !!(card && card.contains(active))) {
        closeButtonRef.value?.focus({ preventScroll: true })
      }
    }
    document.addEventListener('pointerdown', onDocumentPointerDown, true)
    relativeTimer = setInterval(() => {
      if (hasTimeCell.value) nowMs.value = Date.now()
    }, RELATIVE_TICK_MS)
  })

  onUnmounted(() => {
    disposed = true
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
    :data-detail-reveal="isLive ? '' : undefined"
    class="task-detail-overlay absolute inset-0 z-10"
  >
    <!-- Surface: always the whole card. Live cards fade it out over the stats
         labels so the card's own running numbers show through the bottom.
         Both variants stay mounted so a status flip crossfades. -->
    <div class="task-detail-surface" aria-hidden="true">
      <div class="task-detail-slab task-detail-slab-solid" :class="{ 'is-shown': !isLive }"></div>
      <div class="task-detail-slab task-detail-slab-reveal" :class="{ 'is-shown': isLive }"></div>
      <div class="task-detail-bottom-glow" :class="{ 'is-shown': isLive }"></div>
    </div>

    <div class="relative h-full p-3.5 flex flex-col">
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

      <div class="flex items-start gap-3 mt-2 min-w-0">
        <!-- Fact grid: at most two rows, never scrolls -->
        <div
          data-detail-facts
          class="flex-1 min-w-0 grid grid-cols-4 gap-x-3 gap-y-1.5"
        >
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

              <div
                v-else-if="cell.kind === 'source' && source"
                class="flex items-center gap-1 min-w-0"
              >
                <span
                  class="text-xs leading-4 text-[var(--app-text-muted)] truncate"
                  :title="source.title"
                >
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
                  <CheckCircle
                    v-if="copiedKey === 'source'"
                    :size="11"
                    class="task-detail-copied"
                  />
                  <Copy v-else :size="11" />
                </button>
              </div>

              <div v-else-if="cell.kind === 'saveLocation'" class="flex items-center gap-1 min-w-0">
                <span
                  class="text-xs leading-4 text-[var(--app-text-muted)] truncate"
                  :title="saveLocation"
                >
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

        <div v-if="chunkFrame !== undefined" data-chunk-slot class="task-detail-chunk-slot">
          <TaskChunkMap
            v-if="chunkFrame"
            :states="chunkFrame.states"
            :count="chunkFrame.count"
            :chunk-size="chunkFrame.size"
            :frozen="status === 'paused'"
          />
          <span
            v-else
            data-chunk-reserve
            class="task-detail-chunk-reserve"
            aria-hidden="true"
          ></span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
  .task-detail-overlay {
    /* Inner radius of the card's padding box: card squircle minus its border. */
    --detail-radius: calc(var(--radius-squircle-xl) - 1px);
    /* Live reveal, anchored to the card bottom because the stats row hugs it.
       The mask stays fully transparent for the first 1.75rem (bottom padding
       20px + digit bodies), so the numbers read sharp; a soft ramp to ~25%
       at --detail-clear only grazes the digit tops, then --detail-fade
       (~18px) veils the labels row above the figures. */
    --detail-clear: 2.375rem;
    --detail-fade: 1.125rem;
    /* Obsidian glass: deep smoked tone that shares the card's glass DNA rather
       than opaque flat black paint. No blur. */
    --detail-slab-fill:
      linear-gradient(color-mix(in srgb, var(--overlay-bg) 75%, var(--card-bg)) 0 0), var(--card-bg);
    /* Light caught by the glass: lip rim, faint full edge, inner top glow, and
       one soft specular sweep from the top-left. */
    --detail-rim: color-mix(in srgb, var(--app-text) 38%, transparent);
    --detail-edge: color-mix(in srgb, var(--app-text) 7%, transparent);
    --detail-glow: color-mix(in srgb, var(--app-text) 12%, transparent);
    --detail-sheen: color-mix(in srgb, var(--app-text) 6%, transparent);
    --detail-surface:
      linear-gradient(160deg, var(--detail-sheen), transparent 45%), var(--detail-slab-fill);
    border-radius: var(--detail-radius);
  }

  /* Ceramic glass: a clean, denser white than the card (no grey wash), defined
     by its edge rather than by tint. */
  [data-theme='light'] .task-detail-overlay {
    --detail-slab-fill: linear-gradient(var(--card-bg) 0 0), var(--glass-bg);
    --detail-rim: var(--glass-border-highlight);
    --detail-edge: color-mix(in srgb, var(--app-text) 9%, transparent);
    --detail-glow: color-mix(in srgb, var(--glass-border-highlight) 70%, transparent);
    --detail-sheen: transparent;
  }

  .task-detail-surface {
    position: absolute;
    inset: 0;
    border-radius: inherit;
    pointer-events: none;
  }

  .task-detail-slab {
    position: absolute;
    inset: 0;
    border-radius: inherit;
    background: var(--detail-surface);
    box-shadow:
      inset 0 0 0 1px var(--detail-edge),
      inset 0 14px 22px -18px var(--detail-glow);
  }

  /* Specular lip: bright along the top edge and corners, fading down the
     walls. This also restores the card's own 12 o'clock hairline. */
  .task-detail-slab::before {
    content: '';
    position: absolute;
    inset: 0;
    padding: 1px;
    border-radius: inherit;
    background: linear-gradient(
      180deg,
      var(--detail-rim),
      color-mix(in srgb, var(--detail-rim) 20%, transparent) 45%,
      transparent 80%
    );
    mask:
      linear-gradient(black 0 0) content-box,
      linear-gradient(black 0 0);
    mask-composite: exclude;
  }

  /* Status flips crossfade the two surfaces: the incoming one rises quickly
     while the outgoing one lingers briefly, so the panel never thins out. */
  .task-detail-slab-solid,
  .task-detail-slab-reveal {
    opacity: 0;
    transition: opacity 220ms ease 60ms;
  }

  .task-detail-slab-solid.is-shown,
  .task-detail-slab-reveal.is-shown {
    opacity: 1;
    transition: opacity 200ms cubic-bezier(0.16, 1, 0.3, 1);
  }

  [data-effects='reduced'] .task-detail-slab-solid,
  [data-effects='reduced'] .task-detail-slab-reveal {
    transition: none;
  }

  /* Live: liquid meniscus transition. Solid across the upper panel and fact
     grid, easing over the stats labels into a translucent glass floor at the
     bottom figures. An elliptical mask gently arches the meniscus, retaining
     the organic liquid lens boundary. */
  .task-detail-slab-reveal {
    mask-image:
      linear-gradient(
        to top,
        transparent 0,
        transparent 1.75rem,
        color-mix(in srgb, black 25%, transparent) var(--detail-clear),
        color-mix(in srgb, black 65%, transparent)
          calc(var(--detail-clear) + var(--detail-fade) * 0.4),
        color-mix(in srgb, black 90%, transparent)
          calc(var(--detail-clear) + var(--detail-fade) * 0.8),
        black calc(var(--detail-clear) + var(--detail-fade))
      ),
      radial-gradient(110% 100% at 50% 0, black 82%, transparent);
    mask-composite: intersect;
  }

  /* Fluid colored aura at the card bottom, radiating upwards into the glass
     like an ambient light pool. Kept soft and subtle to prevent text glare. */
  .task-detail-bottom-glow {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    height: 3.25rem;
    border-radius: inherit;
    /* The light pool breathes up from the card edge, not from its center. */
    transform-origin: bottom;
    pointer-events: none;
    opacity: 0;
    transition: opacity 220ms ease 60ms;
    background: radial-gradient(
      ellipse 80% 100% at 50% 100%,
      color-mix(in srgb, var(--neon-primary) 18%, transparent),
      color-mix(in srgb, var(--neon-primary) 5%, transparent) 55%,
      transparent 85%
    );
  }

  .task-detail-bottom-glow.is-shown {
    opacity: 0.35;
    transition: opacity 200ms cubic-bezier(0.16, 1, 0.3, 1);
    animation: detail-bottom-breathe 4s ease-in-out infinite;
  }

  [data-theme='light'] .task-detail-bottom-glow {
    background: radial-gradient(
      ellipse 80% 100% at 50% 100%,
      color-mix(in srgb, var(--neon-primary) 10%, transparent),
      transparent 65%
    );
  }

  [data-theme='light'] .task-detail-bottom-glow.is-shown {
    opacity: 0.18;
  }

  @keyframes detail-bottom-breathe {
    0%,
    100% {
      opacity: 0.25;
      transform: scaleY(0.95);
    }
    50% {
      opacity: 0.42;
      transform: scaleY(1.04);
    }
  }

  [data-theme='light'] .task-detail-bottom-glow.is-shown {
    animation-name: detail-bottom-breathe-light;
  }

  @keyframes detail-bottom-breathe-light {
    0%,
    100% {
      opacity: 0.12;
      transform: scaleY(0.95);
    }
    50% {
      opacity: 0.22;
      transform: scaleY(1.04);
    }
  }

  [data-effects='reduced'] .task-detail-bottom-glow {
    animation: none;
    transition: none;
  }

  /* Static tier only silences the breathe; opacity stays with the .is-shown
     rules above so a hidden glow never paints on non-live panels. */
  [data-effects-glow='static'] .task-detail-bottom-glow.is-shown {
    animation: none;
  }

  .task-detail-chunk-slot {
    flex: 0 0 auto;
    /* Elastic between ~120px and 176px; at the 800px minimum window the fact
       grid keeps enough width for a full downloaded/total pair. */
    width: clamp(7.5rem, 24%, 11rem);
    /* Just under the two-row fact band (~66px), so on live cards the map's
       bottom edge clears the revealed stats row. */
    height: 3.5rem;
  }

  .task-detail-chunk-reserve {
    display: block;
    width: 100%;
    height: 100%;
    border-radius: var(--radius-squircle-sm);
    background: color-mix(in srgb, var(--app-text) 6%, transparent);
  }

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
