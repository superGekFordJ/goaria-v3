<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref, toRaw, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { useUIStore } from '../../stores/ui'
  import { formatSize } from '../../utils/taskDisplay'
  import {
    CHUNK_COMPLETE,
    CHUNK_DOWNLOADING,
    CHUNK_PENDING,
    bucketChunkProgress,
    bucketChunkStates,
    layoutChunkGrid,
    type ChunkMapGrid,
  } from '../../utils/chunkMap'

  const props = defineProps<{
    states: readonly number[]
    count?: number
    chunkSize?: number
    frozen?: boolean
    // Per-chunk downloaded bytes for the continuous byte-range track; absent
    // (Aria2, field-less frames) keeps the discrete three-state track.
    progress?: readonly number[]
    totalSize?: number
  }>()

  // The canvases overhang the box so the frontier glow is never cut flat by
  // the canvas edge; capsules are laid out inside the inner box only.
  const BLEED = 4
  const ROOT_ATTRS = ['data-theme', 'data-skin', 'data-window-transparency']

  interface Palette {
    pending: string
    downloading: string
    complete: string
    paused: string
    core: string
    light: boolean
  }

  interface Levels {
    pending: number
    complete: number
    halo: ReadonlyArray<readonly [spread: number, alpha: number]>
    core: number
  }

  // Three intensities that stay apart without relying on hue: a faint rail,
  // a steady mid fill, and a bright frontier with a stepped halo. Light
  // themes drop the hot core and keep the halo soft (no fluorescent glare).
  // dark.pending's alpha tops complete's on paper, but its --app-text-subtle
  // base is far dimmer than --status-complete, so the rail still reads lowest.
  const LEVELS: Record<'dark' | 'light' | 'frozenDark' | 'frozenLight', Levels> = {
    dark: {
      pending: 0.7,
      complete: 0.5,
      halo: [
        [1.5, 0.28],
        [3, 0.1],
      ],
      core: 0.55,
    },
    light: { pending: 0.45, complete: 0.5, halo: [[1.5, 0.14]], core: 0 },
    frozenDark: { pending: 0.2, complete: 0.5, halo: [[1.5, 0.16]], core: 0 },
    frozenLight: { pending: 0.2, complete: 0.45, halo: [[1.5, 0.1]], core: 0 },
  }

  const { t, locale } = useI18n()
  const uiStore = useUIStore()

  const rootRef = ref<HTMLElement | null>(null)
  const baseRef = ref<HTMLCanvasElement | null>(null)
  const frontierRef = ref<HTMLCanvasElement | null>(null)
  const probeRef = ref<HTMLElement | null>(null)

  const label = computed(() => {
    const total = props.count && props.count > 0 ? props.count : props.states.length
    const count = new Intl.NumberFormat(locale.value).format(total)
    return props.chunkSize && props.chunkSize > 0
      ? t('taskDetail.chunkMap', { count, size: formatSize(props.chunkSize) })
      : t('taskDetail.chunkMapNoSize', { count })
  })

  const layerStyle = {
    left: `-${BLEED}px`,
    top: `-${BLEED}px`,
    width: `calc(100% + ${BLEED * 2}px)`,
    height: `calc(100% + ${BLEED * 2}px)`,
  }

  let attached = false
  let rafId = 0
  let cssWidth = 0
  let cssHeight = 0
  let deviceWidth = 0
  let deviceHeight = 0
  let palette: Palette | null = null
  let resizeObserver: ResizeObserver | null = null
  let rootObserver: MutationObserver | null = null
  let dprQuery: MediaQueryList | null = null
  let sawDeviceBox = false

  function currentDpr(): number {
    return window.devicePixelRatio > 0 ? window.devicePixelRatio : 1
  }

  function schedule() {
    if (!attached || rafId) return
    rafId = requestAnimationFrame(paint)
  }

  function invalidatePalette() {
    palette = null
    schedule()
  }

  // canvas cannot read var(): resolve each token through a hidden probe so
  // oklch() / color-mix() skins come back as concrete colors.
  function resolvePalette(): Palette | null {
    const probe = probeRef.value
    if (!probe) return null
    const read = (name: string) => {
      probe.style.color = `var(${name})`
      return window.getComputedStyle(probe).color
    }
    const resolved: Palette = {
      pending: read('--app-text-subtle'),
      downloading: read('--neon-primary'),
      complete: read('--status-complete'),
      paused: read('--status-paused'),
      core: read('--app-text'),
      light: document.documentElement.getAttribute('data-theme') === 'light',
    }
    probe.style.color = ''
    const { pending, downloading, complete, paused, core } = resolved
    return pending && downloading && complete && paused && core ? resolved : null
  }

  function addCapsule(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number) {
    if (typeof ctx.roundRect === 'function') ctx.roundRect(x, y, w, h, Math.min(w, h) / 2)
    else ctx.rect(x, y, w, h)
  }

  function drawGrid(
    base: CanvasRenderingContext2D,
    glow: CanvasRenderingContext2D,
    buckets: Uint8Array,
    grid: ChunkMapGrid,
    colors: Palette,
    dpr: number,
  ) {
    const frozen = !!props.frozen
    const levels =
      LEVELS[
        frozen ? (colors.light ? 'frozenLight' : 'frozenDark') : colors.light ? 'light' : 'dark'
      ]
    const offset = Math.round(BLEED * dpr)

    // Geometry is integral in device pixels; the context is scaled by dpr.
    const fillState = (
      ctx: CanvasRenderingContext2D,
      state: number,
      color: string,
      alpha: number,
      grow = 0,
    ) => {
      ctx.globalAlpha = alpha
      ctx.fillStyle = frozen ? colors.paused : color
      ctx.beginPath()
      for (let b = 0; b < buckets.length; b++) {
        if (buckets[b] !== state) continue
        const x = offset + grid.columnX[Math.floor(b / grid.rows)] - grow
        const y = offset + grid.rowY[b % grid.rows] - grow
        addCapsule(
          ctx,
          x / dpr,
          y / dpr,
          (grid.columnWidth + grow * 2) / dpr,
          (grid.rowHeight + grow * 2) / dpr,
        )
      }
      ctx.fill()
    }

    fillState(base, CHUNK_PENDING, colors.pending, levels.pending)
    fillState(base, CHUNK_COMPLETE, colors.complete, levels.complete)
    fillState(base, CHUNK_DOWNLOADING, colors.downloading, 1)
    for (const [spread, alpha] of levels.halo) {
      fillState(glow, CHUNK_DOWNLOADING, colors.downloading, alpha, Math.round(spread * dpr))
    }

    if (levels.core <= 0) return
    const coreWidth = Math.max(1, Math.round(dpr))
    const inset = Math.max(1, Math.round(dpr))
    const coreHeight = grid.rowHeight - inset * 2
    if (coreHeight <= 0 || grid.columnWidth <= coreWidth) return
    glow.globalAlpha = levels.core
    glow.fillStyle = colors.core
    for (let b = 0; b < buckets.length; b++) {
      if (buckets[b] !== CHUNK_DOWNLOADING) continue
      const x =
        offset +
        grid.columnX[Math.floor(b / grid.rows)] +
        Math.floor((grid.columnWidth - coreWidth) / 2)
      const y = offset + grid.rowY[b % grid.rows] + inset
      glow.fillRect(x / dpr, y / dpr, coreWidth / dpr, coreHeight / dpr)
    }
  }

  function paint() {
    rafId = 0
    const baseCanvas = baseRef.value
    const frontierCanvas = frontierRef.value
    if (!baseCanvas || !frontierCanvas || deviceWidth <= 0 || deviceHeight <= 0) return
    for (const canvas of [baseCanvas, frontierCanvas]) {
      if (canvas.width !== deviceWidth) canvas.width = deviceWidth
      if (canvas.height !== deviceHeight) canvas.height = deviceHeight
    }
    const base = baseCanvas.getContext('2d')
    const glow = frontierCanvas.getContext('2d')
    if (!base || !glow) return

    const dpr = currentDpr()
    for (const ctx of [base, glow]) {
      ctx.setTransform(1, 0, 0, 1, 0, 0)
      ctx.clearRect(0, 0, deviceWidth, deviceHeight)
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    }

    const grid = layoutChunkGrid(cssWidth - BLEED * 2, cssHeight - BLEED * 2, dpr)
    palette ??= resolvePalette()
    if (!grid || !palette) return
    // toRaw: walk the plain arrays, not the reactive proxies, element by
    // element. Byte-level track needs real geometry; it returns null and the
    // discrete track takes over whenever progress or totals are unusable.
    const states = toRaw(props.states)
    const progress = props.progress ? toRaw(props.progress) : undefined
    const bucketCount = grid.columns * grid.rows
    const buckets = progress?.length
      ? (bucketChunkProgress(
          states,
          progress,
          props.chunkSize ?? 0,
          props.totalSize ?? 0,
          bucketCount,
        ) ?? bucketChunkStates(states, bucketCount))
      : bucketChunkStates(states, bucketCount)
    drawGrid(base, glow, buckets, grid, palette, dpr)
  }

  function onResize(entries: ResizeObserverEntry[]) {
    const entry = entries[entries.length - 1]
    if (!entry) return
    const dpr = currentDpr()
    cssWidth = entry.contentRect.width
    cssHeight = entry.contentRect.height
    const device = entry.devicePixelContentBoxSize?.[0]
    sawDeviceBox = !!device
    deviceWidth = device ? device.inlineSize : Math.round(cssWidth * dpr)
    deviceHeight = device ? device.blockSize : Math.round(cssHeight * dpr)
    schedule()
  }

  // Moving across monitors can change the scale without any CSS resize.
  function trackDpr() {
    dprQuery?.removeEventListener('change', onDprChange)
    dprQuery = window.matchMedia?.(`(resolution: ${currentDpr()}dppx)`) ?? null
    dprQuery?.addEventListener('change', onDprChange)
  }

  function onDprChange() {
    // With devicePixelContentBoxSize the observer re-fires with the exact box;
    // the estimate only runs where it is the sole device-pixel source.
    if (!sawDeviceBox) {
      const dpr = currentDpr()
      deviceWidth = Math.round(cssWidth * dpr)
      deviceHeight = Math.round(cssHeight * dpr)
    }
    trackDpr()
    schedule()
  }

  watch(() => props.states, schedule)
  watch(() => props.progress, schedule)
  watch(() => props.totalSize, schedule)
  watch(() => props.frozen, schedule)
  watch([() => uiStore.prismHue, () => uiStore.prismTone], invalidatePalette)

  onMounted(() => {
    // A copy rendered inside a detached (KeepAlive-suspended) tree stays inert.
    if (!rootRef.value?.isConnected || !baseRef.value) return
    attached = true
    if (typeof ResizeObserver !== 'undefined') {
      resizeObserver = new ResizeObserver(onResize)
      resizeObserver.observe(baseRef.value)
    }
    if (typeof MutationObserver !== 'undefined') {
      rootObserver = new MutationObserver(invalidatePalette)
      rootObserver.observe(document.documentElement, {
        attributes: true,
        attributeFilter: ROOT_ATTRS,
      })
    }
    trackDpr()
  })

  onUnmounted(() => {
    attached = false
    if (rafId) cancelAnimationFrame(rafId)
    rafId = 0
    resizeObserver?.disconnect()
    rootObserver?.disconnect()
    dprQuery?.removeEventListener('change', onDprChange)
    resizeObserver = null
    rootObserver = null
    dprQuery = null
  })
</script>

<template>
  <div
    ref="rootRef"
    data-chunk-map
    :data-frozen="frozen ? '' : undefined"
    class="task-chunk-map"
    role="img"
    :title="label"
    :aria-label="label"
  >
    <canvas ref="baseRef" class="task-chunk-map-layer" :style="layerStyle" aria-hidden="true" />
    <canvas
      ref="frontierRef"
      class="task-chunk-map-layer task-chunk-map-frontier"
      :style="layerStyle"
      aria-hidden="true"
    />
    <span ref="probeRef" class="task-chunk-map-probe" aria-hidden="true"></span>
  </div>
</template>

<style scoped>
  .task-chunk-map {
    position: relative;
    width: 100%;
    height: 100%;
  }

  .task-chunk-map-layer {
    position: absolute;
    display: block;
    pointer-events: none;
  }

  /* The frontier glow breathes on the compositor; the canvas itself is only
     repainted when data, size, scale or theme change. */
  .task-chunk-map-frontier {
    opacity: 0.85;
  }

  [data-effects-glow='breathe'] .task-chunk-map:not([data-frozen]) .task-chunk-map-frontier {
    animation: chunk-frontier-breathe 2.4s ease-in-out infinite;
  }

  [data-effects='reduced'] .task-chunk-map-frontier {
    animation: none;
  }

  @keyframes chunk-frontier-breathe {
    0%,
    100% {
      opacity: 0.5;
    }
    50% {
      opacity: 1;
    }
  }

  .task-chunk-map-probe {
    position: absolute;
    width: 0;
    height: 0;
    overflow: hidden;
    visibility: hidden;
  }
</style>
