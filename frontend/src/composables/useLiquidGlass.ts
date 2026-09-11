import { watch, onBeforeUnmount, ref, type Ref } from 'vue'

/* `backdrop-filter: url(#filter)` is a Chromium-engine capability, so gate on
 * the engine — never on window._wails.environment: Wails injects it via execJS
 * in NavigationCompleted, i.e. AFTER the Vue app has already mounted, and it is
 * absent entirely in plain-browser dev previews. A one-shot false here would
 * permanently disarm refraction until a remount. WebView2 reports Chromium/Edge
 * brands; WKWebView/WebKitGTK correctly fail this check. */
export function supportsUrlBackdropFilter(): boolean {
  try {
    const brands = (navigator as Navigator & { userAgentData?: { brands?: { brand: string }[] } })
      .userAgentData?.brands
    if (brands?.some(b => /chromium/i.test(b.brand))) return true
    return (
      /Chrom(e|ium)/.test(navigator.userAgent) ||
      Boolean((window as unknown as { chrome?: unknown }).chrome)
    )
  } catch {
    return false
  }
}

/* ================= Presets ================= */
export interface GlassParams {
  blur: number
  tint: number
  disp: number
  bezel: number
  ca: number
  sat: number
  spec: number
}

export const GLASS_PRESETS: Record<string, GlassParams> = {
  clear: { blur: 0, tint: 0.03, disp: 44, bezel: 24, ca: 0.07, sat: 1.05, spec: 0.6 },
}

/* ================= SDF Displacement Map Generator =================
 * Rounded-rect SDF: center = neutral gray (no displacement);
 * rim band = inward displacement along SDF normal with circular lens profile.
 * R encodes dx, G encodes dy; 0.5 (128) is neutral. Encoded at half amplitude. */
function canvasToBlobUrl(canvas: HTMLCanvasElement): Promise<string> {
  return new Promise((resolve, reject) => {
    canvas.toBlob(blob => {
      if (blob) resolve(URL.createObjectURL(blob))
      else reject(new Error('canvas.toBlob returned null'))
    })
  })
}

function buildDisplacementMap(
  w: number,
  h: number,
  radius: number,
  bezel: number,
  dpr: number,
): Promise<string> {
  const W = Math.max(2, Math.round(w * dpr))
  const H = Math.max(2, Math.round(h * dpr))
  const R = Math.min(radius * dpr, W / 2, H / 2)
  const B = Math.min(bezel * dpr, W / 2, H / 2)
  const canvas = document.createElement('canvas')
  canvas.width = W
  canvas.height = H
  const ctx = canvas.getContext('2d')!
  const img = ctx.createImageData(W, H)
  const data = img.data
  const hw = W / 2
  const hh = H / 2

  for (let y = 0; y < H; y++) {
    for (let x = 0; x < W; x++) {
      const px = x + 0.5 - hw
      const py = y + 0.5 - hh
      const qx = Math.abs(px) - (hw - R)
      const qy = Math.abs(py) - (hh - R)
      const ax = Math.max(qx, 0)
      const ay = Math.max(qy, 0)
      const sd = Math.hypot(ax, ay) + Math.min(Math.max(qx, qy), 0) - R
      const d = -sd

      let nx = 0
      let ny = 0
      let mag = 0
      if (d >= 0 && d < B) {
        if (qx > 0 && qy > 0) {
          const len = Math.hypot(ax, ay) || 1
          nx = (ax / len) * Math.sign(px)
          ny = (ay / len) * Math.sign(py)
        } else if (qx > qy) {
          nx = Math.sign(px)
        } else {
          ny = Math.sign(py)
        }
        const t = 1 - d / B
        mag = 1 - Math.sqrt(Math.max(0, 1 - t * t))
      }

      const i = (y * W + x) * 4
      data[i] = Math.round(127.5 - 127.5 * nx * mag)
      data[i + 1] = Math.round(127.5 - 127.5 * ny * mag)
      data[i + 2] = 0
      data[i + 3] = 255
    }
  }
  ctx.putImageData(img, 0, 0)
  return canvasToBlobUrl(canvas)
}

/* feImage fetches blob: URLs asynchronously, and a url() backdrop-filter whose
 * first raster sees a still-loading feImage is never reliably re-invalidated by
 * Blink — the refraction stays dead until the backdrop-filter property itself
 * changes. Pre-decoding through Image.decode() places the pixels in Blink's
 * memory cache so the feImage fetch resolves synchronously at raster time. */
async function preloadMapImage(url: string): Promise<void> {
  try {
    const img = new Image()
    img.src = url
    await img.decode()
  } catch {
    /* best effort — a failed decode still leaves the href bound */
  }
}

/* ================= SVG Filter Pipeline ================= */
const FILTER_TEMPLATE = `
  <feImage x="0" y="0" width="1" height="1" result="map" preserveAspectRatio="none" class="f-map"/>
  <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" class="f-dr" result="dr"/>
  <feColorMatrix in="dr" type="matrix" values="1 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 0 1" result="chR"/>
  <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" class="f-dg" result="dg"/>
  <feColorMatrix in="dg" type="matrix" values="0 0 0 0 0  0 1 0 0 0  0 0 0 0 0  0 0 0 0 1" result="chG"/>
  <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" class="f-db" result="db"/>
  <feColorMatrix in="db" type="matrix" values="0 0 0 0 0  0 0 0 0 0  0 0 1 0 0  0 0 0 0 1" result="chB"/>
  <feComposite in="chR" in2="chG" operator="arithmetic" k1="0" k2="1" k3="1" k4="0" result="rg"/>
  <feComposite in="rg" in2="chB" operator="arithmetic" k1="0" k2="1" k3="1" k4="0" result="rgb_opaque"/>
  <feComposite in="rgb_opaque" in2="dg" operator="in" result="rgb"/>
  <feGaussianBlur in="rgb" stdDeviation="0" class="f-blur" result="soft"/>
  <feColorMatrix in="soft" type="saturate" values="1" class="f-sat"/>
`

interface CachedMapEntry {
  url: string
  refCount: number
  lastUsed: number
}

const globalMapCache = new Map<string, CachedMapEntry>()
const MAX_GLOBAL_CACHE_SIZE = 16

function getMapCacheKey(w: number, h: number, radius: number, bezel: number, dpr: number): string {
  return `${w}:${h}:${radius}:${bezel}:${dpr}`
}

function acquireDisplacementMap(
  w: number,
  h: number,
  radius: number,
  bezel: number,
  dpr: number,
): Promise<string> {
  const key = getMapCacheKey(w, h, radius, bezel, dpr)
  const cached = globalMapCache.get(key)
  if (cached) {
    cached.refCount++
    cached.lastUsed = Date.now()
    return preloadMapImage(cached.url).then(() => cached.url)
  }

  return buildDisplacementMap(w, h, radius, bezel, dpr).then(async url => {
    await preloadMapImage(url)
    globalMapCache.set(key, { url, refCount: 1, lastUsed: Date.now() })
    if (globalMapCache.size > MAX_GLOBAL_CACHE_SIZE) {
      let oldestKey: string | null = null
      let oldestTime = Infinity
      for (const [k, v] of globalMapCache.entries()) {
        if (v.refCount <= 0 && v.lastUsed < oldestTime) {
          oldestTime = v.lastUsed
          oldestKey = k
        }
      }
      if (oldestKey) {
        const evicted = globalMapCache.get(oldestKey)
        if (evicted) {
          URL.revokeObjectURL(evicted.url)
          globalMapCache.delete(oldestKey)
        }
      }
    }
    return url
  })
}

function releaseDisplacementMap(key: string | null) {
  if (!key) return
  const cached = globalMapCache.get(key)
  if (cached) {
    cached.refCount = Math.max(0, cached.refCount - 1)
    cached.lastUsed = Date.now()
  }
}

interface GlassFilterParts {
  filter: SVGFilterElement
  map: SVGFEImageElement
  dr: SVGFEDisplacementMapElement
  dg: SVGFEDisplacementMapElement
  db: SVGFEDisplacementMapElement
  blur: SVGFEGaussianBlurElement
  sat: SVGFEColorMatrixElement
}

/* Filter primitives stay null until a decoded map is bound; the consumer's
 * backdrop-filter only references the filter after bind (see expose()). */
interface GlassEntry extends Partial<Omit<GlassFilterParts, 'filter'>> {
  key: string
  filter: SVGFilterElement | null
  layer: HTMLElement
  /** Geometry matching the currently bound map (attrs-safe). */
  geom: { w: number; h: number; bezel: number; radius: number; dpr: number }
  /** Target of an in-flight blob rebuild; null when idle. */
  pendingGeom: { w: number; h: number; bezel: number; radius: number; dpr: number } | null
  ro: ResizeObserver
  mapCacheKey: string | null
  mapGen: number
  /** Publishes the live filter id to the bound element (or clears it). */
  expose: () => void
}

/* Global singleton: one SVG <defs> for all glass elements */
let defsEl: SVGDefsElement | null = null
let registry: Map<string, GlassEntry> | null = null
let uidCounter = 0

function ensureDefs(): SVGDefsElement {
  if (defsEl && document.body.contains(defsEl)) return defsEl
  if (registry) {
    for (const stale of registry.values()) {
      stale.mapGen++
      stale.pendingGeom = null
      if (stale.mapCacheKey) {
        releaseDisplacementMap(stale.mapCacheKey)
        stale.mapCacheKey = null
      }
      stale.filter = null
      stale.map = stale.dr = stale.dg = stale.db = stale.blur = stale.sat = undefined
      stale.geom = { w: 0, h: 0, bezel: 0, radius: 0, dpr: 0 }
      stale.expose()
    }
  }
  for (const item of globalMapCache.values()) {
    URL.revokeObjectURL(item.url)
  }
  globalMapCache.clear()

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.setAttribute('width', '0')
  svg.setAttribute('height', '0')
  svg.style.cssText = 'position:absolute;pointer-events:none'
  svg.setAttribute('aria-hidden', 'true')
  const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs')
  svg.appendChild(defs)
  document.body.appendChild(svg)
  defsEl = defs
  registry = new Map()
  return defs
}

function geomEquals(
  a: { w: number; h: number; bezel: number; radius: number; dpr: number },
  w: number,
  h: number,
  bezel: number,
  radius: number,
  dpr: number,
): boolean {
  return a.w === w && a.h === h && a.bezel === bezel && a.radius === radius && a.dpr === dpr
}

function createGlassFilter(defs: SVGDefsElement, id: string): GlassFilterParts {
  const filter = document.createElementNS('http://www.w3.org/2000/svg', 'filter')
  filter.id = id
  filter.setAttribute('x', '0%')
  filter.setAttribute('y', '0%')
  filter.setAttribute('width', '100%')
  filter.setAttribute('height', '100%')
  filter.setAttribute('primitiveUnits', 'objectBoundingBox')
  filter.setAttribute('color-interpolation-filters', 'sRGB')
  filter.innerHTML = FILTER_TEMPLATE
  defs.appendChild(filter)
  return {
    filter,
    map: filter.querySelector('.f-map') as unknown as SVGFEImageElement,
    dr: filter.querySelector('.f-dr') as unknown as SVGFEDisplacementMapElement,
    dg: filter.querySelector('.f-dg') as unknown as SVGFEDisplacementMapElement,
    db: filter.querySelector('.f-db') as unknown as SVGFEDisplacementMapElement,
    blur: filter.querySelector('.f-blur') as unknown as SVGFEGaussianBlurElement,
    sat: filter.querySelector('.f-sat') as unknown as SVGFEColorMatrixElement,
  }
}

function applyGlassAttrs(
  entry: GlassEntry,
  params: GlassParams,
  dispMul: number,
  w: number,
  h: number,
) {
  const { dr, dg, db, blur, sat } = entry
  if (!dr || !dg || !db || !blur || !sat) return
  const minDim = Math.min(w, h)
  const dispPx = Math.min(params.disp * dispMul, minDim * 0.35)
  const diag = Math.sqrt((w * w + h * h) / 2)
  const scale = (2 * dispPx) / diag
  dr.setAttribute('scale', (scale * (1 - params.ca)).toFixed(5))
  dg.setAttribute('scale', scale.toFixed(5))
  db.setAttribute('scale', (scale * (1 + params.ca)).toFixed(5))
  blur.setAttribute(
    'stdDeviation',
    `${(params.blur / w).toFixed(5)} ${(params.blur / h).toFixed(5)}`,
  )
  sat.setAttribute('values', params.sat.toFixed(2))
}

function updateGlass(entry: GlassEntry, params: GlassParams, dispMul: number, bezelMul: number) {
  const rect = entry.layer.getBoundingClientRect()
  const w = Math.round(rect.width)
  const h = Math.round(rect.height)
  if (w < 2 || h < 2) return

  const style = getComputedStyle(entry.layer)
  const radius = parseFloat(style.borderTopLeftRadius) || Math.min(w, h) / 2
  const minDim = Math.min(w, h)
  const bezel = Math.min(Math.max(2, params.bezel * bezelMul), minDim * 0.5)
  const dpr = Math.min(window.devicePixelRatio || 1, 2)

  // Attrs are only safe when the bound map matches this geometry.
  if (entry.filter && geomEquals(entry.geom, w, h, bezel, radius, dpr)) {
    applyGlassAttrs(entry, params, dispMul, w, h)
    return
  }

  /* One map build in flight per element: during size animations the RO fires
   * every frame with a different target, so naive per-tick rebuilds would run
   * an O(W·H) canvas loop per frame. The completion path below re-measures and
   * starts a single catch-up build for the settled size (usually a cache hit
   * on the pre-baked geometry). */
  if (entry.pendingGeom) return

  const target = { w, h, bezel, radius, dpr }
  const cacheKey = getMapCacheKey(w, h, radius, bezel, dpr)
  entry.pendingGeom = target
  entry.mapGen++
  const gen = entry.mapGen

  acquireDisplacementMap(w, h, radius, bezel, dpr)
    .then(url => {
      if (gen !== entry.mapGen || !registry?.has(entry.key)) {
        releaseDisplacementMap(cacheKey)
        return
      }
      /* Swap in a fresh filter element whose feImage is already bound to a
       * decoded map. The consumer's `backdrop-filter: url(#id)` string changes,
       * forcing Blink to re-resolve and raster with the real map on the first
       * pass — mutating href on an already-referenced filter races its async
       * blob fetch and can leave the refraction stuck empty. */
      const parts = createGlassFilter(ensureDefs(), `lgf-${++uidCounter}`)
      parts.map.setAttribute('href', url)
      const old = entry.filter
      entry.filter = parts.filter
      entry.map = parts.map
      entry.dr = parts.dr
      entry.dg = parts.dg
      entry.db = parts.db
      entry.blur = parts.blur
      entry.sat = parts.sat
      const prevKey = entry.mapCacheKey
      entry.mapCacheKey = cacheKey
      entry.geom = target
      entry.pendingGeom = null
      applyGlassAttrs(entry, params, dispMul, w, h)
      entry.expose()
      old?.remove()
      if (prevKey && prevKey !== cacheKey) {
        releaseDisplacementMap(prevKey)
      }
      // Re-measure: if geometry drifted while this map was building, start one
      // catch-up build for the settled size; otherwise this returns via the
      // geom-equal attrs path at trivial cost.
      updateGlass(entry, params, dispMul, bezelMul)
    })
    .catch(() => {
      if (gen === entry.mapGen) {
        entry.pendingGeom = null
      }
    })
}

/* ================= Composable ================= */
export interface UseLiquidGlassOptions {
  params?: GlassParams
  dispMul?: number
  bezelMul?: number
}

export function useLiquidGlass(
  layerRef: Ref<HTMLElement | null>,
  options: UseLiquidGlassOptions = {},
) {
  const filterId = ref<string>('')
  const params = options.params ?? GLASS_PRESETS.clear
  const dispMul = options.dispMul ?? 1
  const bezelMul = options.bezelMul ?? 1

  let entry: GlassEntry | null = null

  function register() {
    if (!supportsUrlBackdropFilter()) return
    const layer = layerRef.value
    if (!layer) return
    ensureDefs()
    if (!registry) registry = new Map()

    const key = `lg-${++uidCounter}`

    entry = {
      key,
      layer,
      filter: null,
      geom: { w: 0, h: 0, bezel: 0, radius: 0, dpr: 0 },
      pendingGeom: null,
      ro: new ResizeObserver(() => {
        if (entry) updateGlass(entry, params, dispMul, bezelMul)
      }),
      mapCacheKey: null,
      mapGen: 0,
      expose: () => {
        filterId.value = entry?.filter?.id ?? ''
      },
    }

    registry.set(key, entry)
    entry.ro.observe(layer)
    requestAnimationFrame(() => {
      if (entry) updateGlass(entry, params, dispMul, bezelMul)
    })
  }

  function unregister() {
    if (!entry) return
    entry.mapGen++
    entry.ro.disconnect()
    if (entry.mapCacheKey) {
      releaseDisplacementMap(entry.mapCacheKey)
      entry.mapCacheKey = null
    }
    entry.filter?.remove()
    registry?.delete(entry.key)
    entry = null
    filterId.value = ''
  }

  watch(
    layerRef,
    newEl => {
      if (entry) unregister()
      if (newEl) register()
    },
    { immediate: true, flush: 'post' },
  )

  onBeforeUnmount(unregister)

  return { filterId }
}

/* ================= Static Glass Refraction (lightweight, shared) =================
 * One fixed 256×256 SDF map + one shared SVG filter for all StaticGlassPanel instances.
 * No ResizeObserver, no per-element canvas — just a single filter element in the DOM.
 * Produces a subtle edge bend with slight blur and saturation boost. */
let staticFilterId: string | null = null
let staticBlobUrl: string | null = null
let staticMapGen = 0
let staticBuilding = false
/* Readiness gate: consumers read this ref inside computed(), so flipping it
 * re-applies `url(#id)` only after the map is bound and decoded — the same
 * bind-before-expose rule as the dynamic pipeline. */
const staticReady = ref(false)

export function getStaticGlassFilterId(): string {
  if (!supportsUrlBackdropFilter()) return ''
  if (staticReady.value && staticFilterId && document.getElementById(staticFilterId)) {
    return staticFilterId
  }
  if (staticBuilding) return ''
  staticBuilding = true
  staticReady.value = false

  const size = 256
  const id = 'static-glass-refraction'
  staticMapGen++
  const gen = staticMapGen

  if (staticBlobUrl) {
    URL.revokeObjectURL(staticBlobUrl)
    staticBlobUrl = null
  }

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.setAttribute('width', '0')
  svg.setAttribute('height', '0')
  svg.style.cssText = 'position:absolute;pointer-events:none'
  svg.setAttribute('aria-hidden', 'true')

  const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs')
  const filter = document.createElementNS('http://www.w3.org/2000/svg', 'filter')
  filter.id = id
  filter.setAttribute('x', '0%')
  filter.setAttribute('y', '0%')
  filter.setAttribute('width', '100%')
  filter.setAttribute('height', '100%')
  filter.setAttribute('primitiveUnits', 'objectBoundingBox')
  filter.setAttribute('color-interpolation-filters', 'sRGB')
  filter.innerHTML = `
    <feImage x="0" y="0" width="1" height="1" result="map" preserveAspectRatio="none" class="f-static-map"/>
    <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" result="dr" class="f-static-disp"/>
    <feGaussianBlur in="dr" stdDeviation="0.004" result="soft"/>
    <feColorMatrix in="soft" type="saturate" values="1.08"/>
  `
  defs.appendChild(filter)
  svg.appendChild(defs)
  document.body.appendChild(svg)

  staticFilterId = id

  buildDisplacementMap(size, size, 64, 36, 1)
    .then(async url => {
      if (gen !== staticMapGen || staticFilterId !== id) {
        URL.revokeObjectURL(url)
        return
      }
      const mapEl = filter.querySelector('.f-static-map') as SVGFEImageElement | null
      const dispEl = filter.querySelector('.f-static-disp') as SVGFEDisplacementMapElement | null
      if (!mapEl || !dispEl) {
        URL.revokeObjectURL(url)
        return
      }
      await preloadMapImage(url)
      if (gen !== staticMapGen || staticFilterId !== id) {
        URL.revokeObjectURL(url)
        return
      }
      const prev = staticBlobUrl
      staticBlobUrl = url
      mapEl.setAttribute('href', url)
      dispEl.setAttribute('scale', '0.01')
      if (prev) URL.revokeObjectURL(prev)
      staticReady.value = true
    })
    .catch(() => {
      if (gen === staticMapGen) {
        staticFilterId = null
        svg.remove()
      }
    })
    .finally(() => {
      if (gen === staticMapGen) staticBuilding = false
    })

  return ''
}

/* ================= Pipeline Warmup =================
 * Triggers GPU shader compilation and creates shared SVG filter definitions during app idle time.
 * Prevents initial shader compilation stutter when opening modals or navigating to settings. */
let hasWarmedUp = false

export function warmupLiquidGlassPipeline(): void {
  if (hasWarmedUp || typeof document === 'undefined' || !supportsUrlBackdropFilter()) return
  hasWarmedUp = true
  try {
    // 1. Pre-warm static glass filter and its 256x256 SDF map
    getStaticGlassFilterId()

    // 2. Pre-warm full liquid glass filter template in DOM
    const defs = ensureDefs()
    const warmupId = 'lg-warmup-probe'
    if (!document.getElementById(warmupId)) {
      createGlassFilter(defs, warmupId)
    }

    // 3. Trigger GPU shader compilation with an offscreen probe element.
    // The probe binds a real decoded map + nonzero displacement so the warmup
    // exercises the true pipeline instead of rasterizing an empty feImage.
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    acquireDisplacementMap(32, 32, 8, 8, dpr)
      .then(url => {
        const filter = document.getElementById(warmupId)
        const mapEl = filter?.querySelector('.f-map')
        if (!filter || !mapEl) return
        mapEl.setAttribute('href', url)
        for (const cls of ['.f-dr', '.f-dg', '.f-db']) {
          filter.querySelector(cls)?.setAttribute('scale', '0.02')
        }
        const probe = document.createElement('div')
        probe.style.cssText =
          'position:fixed;top:-9999px;left:-9999px;width:16px;height:16px;opacity:0.001;pointer-events:none;backdrop-filter:blur(2px) url(#lg-warmup-probe);-webkit-backdrop-filter:blur(2px) url(#lg-warmup-probe);'
        document.body.appendChild(probe)
        requestAnimationFrame(() => {
          probe.remove()
        })
      })
      .catch(() => {})
  } catch {
    // Pipeline warmup is strictly best-effort
  }
}

/** Pre-warms a specific geometry displacement map into the global LRU cache during idle time.
 * Returns a disposer that releases the acquired cache entry; consumers must invoke it on
 * unmount, otherwise the pre-baked map stays at refCount >= 1 and can never be evicted. */
export function preloadDisplacementMap(
  w: number,
  h: number,
  radius: number,
  bezel: number,
): () => void {
  if (typeof window === 'undefined' || !supportsUrlBackdropFilter()) return () => {}
  const dpr = Math.min(window.devicePixelRatio || 1, 2)
  const key = getMapCacheKey(w, h, radius, bezel, dpr)
  let released = false
  acquireDisplacementMap(w, h, radius, bezel, dpr)
    .then(() => {
      /* The cache entry lands asynchronously — if the consumer already disposed
       * before the build resolved, release it here so it isn't pinned forever. */
      if (released) releaseDisplacementMap(key)
    })
    .catch(() => {})
  return () => {
    if (released) return
    released = true
    releaseDisplacementMap(key)
  }
}
