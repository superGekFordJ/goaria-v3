<script setup lang="ts">
  import { ref, onMounted, onBeforeUnmount, watch } from 'vue'

  const props = withDefaults(
    defineProps<{
      modelValue: number
      min?: number
      max?: number
      step?: number
      ariaLabel?: string
      ariaValuetext?: string
    }>(),
    {
      min: 0,
      max: 100,
      step: 1,
      ariaLabel: '',
      ariaValuetext: '',
    },
  )

  const emit = defineEmits<{
    'update:modelValue': [value: number]
    change: [value: number]
  }>()

  const localValue = ref(props.modelValue)

  const rootRef = ref<HTMLElement | null>(null)
  const railRef = ref<HTMLElement | null>(null)
  const fillRef = ref<HTMLElement | null>(null)
  const thumbRef = ref<HTMLElement | null>(null)
  const refractRef = ref<HTMLElement | null>(null)
  const rimRef = ref<SVGSVGElement | null>(null)
  const rimRectRef = ref<SVGRectElement | null>(null)

  const THUMB_W = 46
  const THUMB_H = 28
  const MAX_LIFT = 1.7
  const MAP_PAD = 14
  const LENS = { disp: 13, blur: 0, ca: 0, satBoost: 0.35 }
  const STIFF = 340
  const DAMP = 13
  const E_MAX = 0.32
  const E_GAIN = 1 / 2400

  const SUPPORTS_URL_FILTER = (() => {
    const brands = (navigator as Navigator & { userAgentData?: { brands?: { brand: string }[] } }).userAgentData
    if (brands?.brands?.some((b) => /chromium/i.test(b.brand))) return true
    return /Chrom(e|ium)/.test(navigator.userAgent) || !!(window as unknown as { chrome?: unknown }).chrome
  })()

  const filterId = ref('')
  let filterEl: SVGFilterElement | null = null
  let fMap: SVGFEImageElement | null = null
  let fMapBlur: SVGFEGaussianBlurElement | null = null
  let fDisp: SVGFEDisplacementMapElement | null = null
  let fSat: SVGFEColorMatrixElement | null = null
  let defsEl: SVGDefsElement | null = null
  let svgRoot: SVGSVGElement | null = null

  let pressed = false
  let p = 0
  let def = 0
  let defV = 0
  let vx = 0
  let lastX = 0
  let moveT = 0
  let raf = 0
  let frameT = 0
  let mapBucket = -1
  let railWidth = 0
  let ro: ResizeObserver | null = null
  const mapCache = new Map<string, { blurX: number; blurY: number; url: string }>()
  const mapDpr = Math.min(4, Math.max(3, (window.devicePixelRatio || 1) * 2))

  let currentW = THUMB_W
  let currentH = THUMB_H
  let currentCapturePad = 0



  function buildDisplacementMap(w: number, h: number, radius: number, bezel: number, pad: number, dpr: number): string {
    const lensW = Math.max(2, w * dpr)
    const lensH = Math.max(2, h * dpr)
    const mapPad = Math.max(2, pad * dpr)
    const mapW = Math.max(2, Math.ceil(lensW + mapPad * 2))
    const mapH = Math.max(2, Math.ceil(lensH + mapPad * 2))
    const r = Math.min(radius * dpr, lensW / 2, lensH / 2)
    const b = Math.min(bezel * dpr, lensW / 2, lensH / 2)
    const canvas = document.createElement('canvas')
    canvas.width = mapW
    canvas.height = mapH
    const ctx = canvas.getContext('2d')!
    const img = ctx.createImageData(mapW, mapH)
    const data = img.data
    const hw = lensW / 2
    const hh = lensH / 2
    const cx = mapW / 2
    const cy = mapH / 2

    for (let y = 0; y < mapH; y++) {
      for (let x = 0; x < mapW; x++) {
        const px = x + 0.5 - cx
        const py = y + 0.5 - cy
        const qx = Math.abs(px) - (hw - r)
        const qy = Math.abs(py) - (hh - r)
        const ax = Math.max(qx, 0)
        const ay = Math.max(qy, 0)
        const sd = Math.hypot(ax, ay) + Math.min(Math.max(qx, qy), 0) - r
        const d = -sd
        let nx = 0
        let ny = 0
        let mag = 0

        if (d >= 0 && d < b) {
          if (qx > 0 && qy > 0) {
            const len = Math.hypot(ax, ay) || 1
            nx = (ax / len) * Math.sign(px)
            ny = (ay / len) * Math.sign(py)
          } else if (qx > qy) {
            nx = Math.sign(px)
          } else {
            ny = Math.sign(py)
          }
          const t = 1 - d / b
          mag = 1 - Math.sqrt(Math.max(0, 1 - t * t))
        }

        const i = (y * mapW + x) * 4
        data[i] = Math.round(127.5 - 127.5 * nx * mag)
        data[i + 1] = Math.round(127.5 - 127.5 * ny * mag)
        data[i + 2] = 128
        data[i + 3] = 255
      }
    }
    ctx.putImageData(img, 0, 0)
    return canvas.toDataURL('image/png')
  }

  function ensureFilter() {
    const uid = `lgs-${Math.random().toString(36).slice(2, 9)}`
    filterId.value = uid

    svgRoot = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
    svgRoot.setAttribute('width', '0')
    svgRoot.setAttribute('height', '0')
    svgRoot.style.cssText = 'position:absolute;pointer-events:none'
    svgRoot.setAttribute('aria-hidden', 'true')
    defsEl = document.createElementNS('http://www.w3.org/2000/svg', 'defs')
    svgRoot.appendChild(defsEl)
    document.body.appendChild(svgRoot)

    filterEl = document.createElementNS('http://www.w3.org/2000/svg', 'filter')
    filterEl.id = uid
    filterEl.setAttribute('x', '0%')
    filterEl.setAttribute('y', '0%')
    filterEl.setAttribute('width', '100%')
    filterEl.setAttribute('height', '100%')
    filterEl.setAttribute('filterUnits', 'objectBoundingBox')
    filterEl.setAttribute('primitiveUnits', 'objectBoundingBox')
    filterEl.setAttribute('color-interpolation-filters', 'sRGB')
    filterEl.innerHTML = `
      <feImage x="0" y="0" width="1" height="1" preserveAspectRatio="none" result="rawMap" class="f-map"/>
      <feGaussianBlur in="rawMap" x="0" y="0" width="1" height="1" edgeMode="duplicate" result="map" class="f-map-blur"/>
      <feDisplacementMap in="SourceGraphic" in2="map" x="0" y="0" width="1" height="1" xChannelSelector="R" yChannelSelector="G" scale="0" result="refracted" class="f-disp"/>
      <feColorMatrix in="refracted" x="0" y="0" width="1" height="1" type="saturate" values="1" class="f-sat"/>`
    defsEl.appendChild(filterEl)

    fMap = filterEl.querySelector('.f-map') as SVGFEImageElement
    fMapBlur = filterEl.querySelector('.f-map-blur') as SVGFEGaussianBlurElement
    fDisp = filterEl.querySelector('.f-disp') as SVGFEDisplacementMapElement
    fSat = filterEl.querySelector('.f-sat') as SVGFEColorMatrixElement

    if (SUPPORTS_URL_FILTER && refractRef.value) {
      const style = refractRef.value.style as CSSStyleDeclaration & { webkitBackdropFilter: string }
      style.backdropFilter = `url(#${uid})`
      style.webkitBackdropFilter = `url(#${uid})`
    }
  }

  function syncMap(shapeDef: number) {
    if (!fMap || !fMapBlur) return
    const bucket = Math.round(Math.min(0.32, Math.abs(shapeDef)) / 0.025) * 0.025
    if (bucket === mapBucket) return
    mapBucket = bucket
    const key = bucket.toFixed(3)
    let entry = mapCache.get(key)
    if (!entry) {
      const mapShapeW = THUMB_W * MAX_LIFT * (1 + bucket)
      const mapShapeH = THUMB_H * MAX_LIFT * (1 - bucket * 0.55)
      entry = {
        blurX: 0.32 / (mapShapeW + MAP_PAD * 2),
        blurY: 0.32 / (mapShapeH + MAP_PAD * 2),
        url: '',
      }
      mapCache.set(key, entry)
      const url = buildDisplacementMap(mapShapeW, mapShapeH, mapShapeH / 2, mapShapeH / 2, MAP_PAD, mapDpr)
      entry.url = url
    }
    fMapBlur.setAttribute('stdDeviation', `${entry.blurX} ${entry.blurY}`)
    if (entry.url) {
      fMap.setAttribute('href', entry.url)
    }
  }

  function applyStyles() {
    const thumb = thumbRef.value
    const refract = refractRef.value
    if (!thumb || !refract) return

    const lift = 1 + (MAX_LIFT - 1) * p
    const scaleX = lift * (1 + def)
    const scaleY = lift * (1 - def * 0.55)

    currentW = THUMB_W * scaleX
    currentH = THUMB_H * scaleY
    currentCapturePad = (MAP_PAD * lift) / MAX_LIFT
    const currentTr = currentH / 2
    syncMap(def)

    thumb.style.width = `${currentW}px`
    thumb.style.height = `${currentH}px`
    thumb.style.borderRadius = `${currentTr}px`
    
    const pct = ((localValue.value - props.min) / (props.max - props.min))
    const x = pct * railWidth
    thumb.style.transform = `translate3d(calc(${x}px - 50%), -50%, 0)`
    
    refract.style.inset = `${-currentCapturePad}px`

    if (rimRef.value && rimRectRef.value) {
      rimRef.value.setAttribute('viewBox', `0 0 ${currentW} ${currentH}`)
      rimRectRef.value.setAttribute('width', String(Math.max(1, currentW - 1.2)))
      rimRectRef.value.setAttribute('height', String(Math.max(1, currentH - 1.2)))
      rimRectRef.value.setAttribute('rx', String(Math.max(0, currentTr - 0.6)))
    }
  }

  function applyLens() {
    const refract = refractRef.value
    if (!refract) return

    if (!SUPPORTS_URL_FILTER) {
      const fb = p > 0.01 ? `blur(${(2 * p).toFixed(2)}px) saturate(${(1 + LENS.satBoost * p).toFixed(2)})` : 'none'
      const style = refract.style as CSSStyleDeclaration & { webkitBackdropFilter: string }
      style.backdropFilter = fb
      style.webkitBackdropFilter = fb
      return
    }
    if (!fDisp || !fSat) return
    const captureW = currentW + currentCapturePad * 2
    const captureH = currentH + currentCapturePad * 2
    const diag = Math.sqrt((captureW * captureW + captureH * captureH) / 2)
    const dispPx = Math.min(LENS.disp, Math.min(THUMB_W, THUMB_H) * 0.35)
    const preciseScale = (2 * dispPx * p) / diag
    fDisp.setAttribute('scale', preciseScale.toFixed(5))
    fSat.setAttribute('values', (1 + LENS.satBoost * p).toFixed(3))
  }

  function frame(now: number) {
    const dt = Math.min(0.032, Math.max(0.001, (now - frameT) / 1000))
    frameT = now
    p += ((pressed ? 1 : 0) - p) * Math.min(1, dt * (pressed ? 16 : 10))
    vx *= Math.exp(-6 * dt)
    const target = pressed ? Math.min(E_MAX, Math.abs(vx) * E_GAIN) : 0
    defV += (target - def) * STIFF * dt
    defV *= Math.exp(-DAMP * dt)
    def += defV * dt

    if (!pressed && p < 0.002 && Math.abs(def) < 0.0008 && Math.abs(defV) < 0.004) {
      p = 0
      def = 0
      defV = 0
      raf = 0
      applyStyles()
      applyLens()
      return
    }
    applyStyles()
    applyLens()
    raf = requestAnimationFrame(frame)
  }

  function kick() {
    if (raf) return
    frameT = performance.now()
    raf = requestAnimationFrame(frame)
  }

  function valueFromEvent(e: PointerEvent): number {
    const rail = railRef.value
    if (!rail) return localValue.value
    const rect = rail.getBoundingClientRect()
    if (rect.width === 0) return localValue.value
    let pct = (e.clientX - rect.left) / rect.width
    pct = Math.max(0, Math.min(1, pct))
    const range = props.max - props.min
    const stepped = Math.round((props.min + pct * range) / props.step) * props.step
    return Math.max(props.min, Math.min(props.max, stepped))
  }

  function render() {
    const fill = fillRef.value
    const root = rootRef.value
    if (!fill || !root) return
    const pct = ((localValue.value - props.min) / (props.max - props.min))
    fill.style.width = `${pct * 100}%`
    root.setAttribute('aria-valuenow', String(Math.round(localValue.value)))
    
    if (!raf) {
      applyStyles()
    }
  }

  function onPointerDown(e: PointerEvent) {
    const root = rootRef.value
    if (!root) return
    root.setPointerCapture(e.pointerId)
    pressed = true
    thumbRef.value?.classList.add('pressed')
    lastX = e.clientX
    moveT = performance.now()
    localValue.value = valueFromEvent(e)
    emit('update:modelValue', localValue.value)
    kick()
  }

  function onPointerMove(e: PointerEvent) {
    if (!pressed) return
    localValue.value = valueFromEvent(e)
    emit('update:modelValue', localValue.value)
    render()
    const now = performance.now()
    const dt = Math.max(4, now - moveT) / 1000
    vx += ((e.clientX - lastX) / dt - vx) * 0.35
    lastX = e.clientX
    moveT = now
    kick()
  }

  function release() {
    if (!pressed) return
    pressed = false
    thumbRef.value?.classList.remove('pressed')
    emit('change', localValue.value)
    kick()
  }

  function onKeydown(e: KeyboardEvent) {
    const step = e.shiftKey ? props.step * 10 : props.step
    let v: number
    if (e.key === 'ArrowRight' || e.key === 'ArrowUp') v = Math.min(props.max, props.modelValue + step)
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') v = Math.max(props.min, props.modelValue - step)
    else if (e.key === 'PageUp') v = Math.min(props.max, props.modelValue + props.step * 10)
    else if (e.key === 'PageDown') v = Math.max(props.min, props.modelValue - props.step * 10)
    else if (e.key === 'Home') v = props.min
    else if (e.key === 'End') v = props.max
    else return
    e.preventDefault()
    localValue.value = v
    emit('update:modelValue', v)
    emit('change', v)
  }

  watch(() => props.modelValue, (v) => { localValue.value = v; render() })

  onMounted(() => {
    if (railRef.value) {
      railWidth = railRef.value.getBoundingClientRect().width
      ro = new ResizeObserver((entries) => {
        if (entries[0]) {
          railWidth = entries[0].contentRect.width
          if (!raf) applyStyles()
        }
      })
      ro.observe(railRef.value)
    }
    
    ensureFilter()
    render()
    applyLens()
  })

  onBeforeUnmount(() => {
    if (ro) ro.disconnect()
    if (raf) cancelAnimationFrame(raf)
    mapBucket = -1
    fMap = null
    fMapBlur = null
    fDisp = null
    fSat = null
    filterEl = null
    if (svgRoot) svgRoot.remove()
    svgRoot = null
    defsEl = null
    for (const entry of mapCache.values()) {
      if (entry.url) URL.revokeObjectURL(entry.url)
    }
    mapCache.clear()
  })
</script>

<template>
  <div
    ref="rootRef"
    class="lgs"
    role="slider"
    tabindex="0"
    :aria-label="ariaLabel"
    :aria-valuemin="min"
    :aria-valuemax="max"
    :aria-valuenow="modelValue"
    :aria-valuetext="ariaValuetext"
    @pointerdown="onPointerDown"
    @pointermove="onPointerMove"
    @pointerup="release"
    @pointercancel="release"
    @keydown="onKeydown"
  >
    <div ref="railRef" class="lgs-rail">
      <div class="lgs-track">
        <div ref="fillRef" class="lgs-fill"></div>
      </div>
      <div class="lgs-ticks"><i class="tick-30"></i><i class="tick-70"></i></div>
      <div ref="thumbRef" class="lgs-thumb">
        <div ref="refractRef" class="lgs-refract"></div>
        <div class="lgs-noise"></div>
        <svg ref="rimRef" class="lgs-rim" :viewBox="`0 0 ${THUMB_W} ${THUMB_H}`">
          <defs>
            <linearGradient :id="`rim-${filterId}`" x1="0" y1="0" x2="1" y2="1">
              <stop offset="0" stop-color="#fff" stop-opacity=".9" />
              <stop offset=".35" stop-color="#fff" stop-opacity=".22" />
              <stop offset=".7" stop-color="#fff" stop-opacity=".06" />
              <stop offset="1" stop-color="#fff" stop-opacity=".18" />
            </linearGradient>
          </defs>
          <rect
            ref="rimRectRef"
            x=".6"
            y=".6"
            :width="THUMB_W - 1.2"
            :height="THUMB_H - 1.2"
            :rx="THUMB_H / 2 - 0.6"
            fill="none"
            :stroke="`url(#rim-${filterId})`"
            stroke-width="1.2"
          />
        </svg>
      </div>
    </div>
  </div>
</template>

<style scoped>
  .lgs {
    position: relative;
    display: flex;
    align-items: center;
    height: 44px;
    padding: 0 24px;
    touch-action: none;
    cursor: pointer;
    outline: none;
  }

  .lgs:focus-visible {
    outline: 2px solid var(--neon-primary);
    outline-offset: 4px;
    border-radius: 8px;
  }

  .lgs-rail {
    position: relative;
    z-index: 1;
    flex: 1;
    height: 44px;
  }

  .lgs-track {
    position: absolute;
    left: 0;
    right: 0;
    top: 50%;
    height: 5px;
    transform: translateY(-50%);
    border-radius: 999px;
    background: var(--glass-border);
    overflow: hidden;
  }

  .lgs-fill {
    position: absolute;
    left: 0;
    top: 0;
    bottom: 0;
    border-radius: inherit;
    background: var(--neon-primary);
  }

  .lgs-ticks {
    position: absolute;
    left: 0;
    right: 0;
    top: calc(50% + 9px);
    pointer-events: none;
  }

  .lgs-ticks i {
    position: absolute;
    width: 2px;
    height: 8px;
    border-radius: 1px;
    background: var(--glass-border);
    transform: translateX(-50%);
  }

  .lgs-ticks .tick-30 {
    left: 30%;
  }

  .lgs-ticks .tick-70 {
    left: 70%;
  }

  .lgs-thumb {
    position: absolute;
    top: 50%;
    left: 0;
    width: 46px;
    height: 28px;
    border-radius: 14px;
    transform: translate3d(-50%, -50%, 0);
    isolation: isolate;
    overflow: hidden;
    background: #fff;
    box-shadow:
      0 1px 2px rgba(0, 0, 10, 0.22),
      0 0 0 0.5px rgba(0, 0, 10, 0.06);
    transition:
      background-color 0.22s ease,
      box-shadow 0.22s ease;
  }

  .lgs-refract {
    position: absolute;
    inset: 0;
    z-index: -1;
    border-radius: 0;
  }

  .lgs-noise {
    position: absolute;
    inset: 0;
    z-index: 1;
    border-radius: inherit;
    pointer-events: none;
    background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='120' height='120'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='0.9' numOctaves='2' stitchTiles='stitch'/%3E%3CfeColorMatrix type='saturate' values='0'/%3E%3C/filter%3E%3Crect width='120' height='120' filter='url(%23n)'/%3E%3C/svg%3E");
    background-size: 120px 120px;
    opacity: 0;
    transition: opacity 0.2s ease;
  }

  .lgs-rim {
    position: absolute;
    inset: 0;
    z-index: 2;
    width: 100%;
    height: 100%;
    pointer-events: none;
    overflow: visible;
    opacity: 0;
    transition: opacity 0.2s ease;
  }

  .lgs-thumb.pressed {
    background: rgba(255, 255, 255, 0.06);
    box-shadow:
      0 10px 24px rgba(0, 0, 10, 0.3),
      0 3px 8px rgba(0, 0, 10, 0.18),
      inset 0 1px 1px rgba(255, 255, 255, 0.4),
      inset 0 -8px 14px -10px rgba(10, 15, 40, 0.35);
  }

  .lgs-thumb.pressed .lgs-noise {
    opacity: 0.05;
  }

  .lgs-thumb.pressed .lgs-rim {
    opacity: 1;
  }
</style>

<style>
  /* 全局注入暗色模式样式，确保覆盖 scoped 隔离 */
  [data-theme='dark'] .lgs-thumb {
    background: #606068; /* 不透明的金属深灰，增强向液态玻璃转化时的惊艳反差 */
    box-shadow:
      inset 0 0 0 1px rgba(255, 255, 255, 0.12),
      0 1px 3px rgba(0, 0, 0, 0.5);
  }

  /* 恢复完美暗色折射质感，避免浑浊发白 */
  [data-theme='dark'] .lgs-thumb.pressed {
    background: rgba(20, 20, 26, 0.35);
    box-shadow:
      0 10px 24px rgba(0, 0, 10, 0.5),
      0 3px 8px rgba(0, 0, 10, 0.4),
      inset 0 1px 1px rgba(255, 255, 255, 0.2),
      inset 0 -8px 14px -10px rgba(10, 15, 40, 0.6);
  }

  [data-theme='dark'] .lgs-thumb.pressed .lgs-rim {
    opacity: 0.85;
  }
</style>
