import { watch, onBeforeUnmount, ref, type Ref } from 'vue'

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
    canvas.toBlob((blob) => {
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

interface GlassEntry {
  key: string
  layer: HTMLElement
  filter: SVGFilterElement
  map: SVGFEImageElement
  dr: SVGFEDisplacementMapElement
  dg: SVGFEDisplacementMapElement
  db: SVGFEDisplacementMapElement
  blur: SVGFEGaussianBlurElement
  sat: SVGFEColorMatrixElement
  /** Geometry matching the currently bound map (attrs-safe). */
  geom: { w: number; h: number; bezel: number; radius: number; dpr: number }
  /** Target of an in-flight blob rebuild; null when idle. */
  pendingGeom: { w: number; h: number; bezel: number; radius: number; dpr: number } | null
  ro: ResizeObserver
  blobUrl: string | null
  mapGen: number
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
      if (stale.blobUrl) {
        URL.revokeObjectURL(stale.blobUrl)
        stale.blobUrl = null
      }
    }
  }
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

function applyGlassAttrs(
  entry: GlassEntry,
  params: GlassParams,
  dispMul: number,
  w: number,
  h: number,
) {
  const minDim = Math.min(w, h)
  const dispPx = Math.min(params.disp * dispMul, minDim * 0.35)
  const diag = Math.sqrt((w * w + h * h) / 2)
  const scale = (2 * dispPx) / diag
  entry.dr.setAttribute('scale', (scale * (1 - params.ca)).toFixed(5))
  entry.dg.setAttribute('scale', scale.toFixed(5))
  entry.db.setAttribute('scale', (scale * (1 + params.ca)).toFixed(5))
  entry.blur.setAttribute('stdDeviation', `${(params.blur / w).toFixed(5)} ${(params.blur / h).toFixed(5)}`)
  entry.sat.setAttribute('values', params.sat.toFixed(2))
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
  if (geomEquals(entry.geom, w, h, bezel, radius, dpr)) {
    applyGlassAttrs(entry, params, dispMul, w, h)
    return
  }

  // Same target already rebuilding — keep last coherent map+attrs.
  if (entry.pendingGeom && geomEquals(entry.pendingGeom, w, h, bezel, radius, dpr)) {
    return
  }

  const target = { w, h, bezel, radius, dpr }
  entry.pendingGeom = target
  entry.mapGen++
  const gen = entry.mapGen
  buildDisplacementMap(w, h, radius, bezel, dpr)
    .then((url) => {
      if (gen !== entry.mapGen || !registry?.has(entry.key)) {
        URL.revokeObjectURL(url)
        return
      }
      const prev = entry.blobUrl
      entry.blobUrl = url
      entry.geom = target
      entry.pendingGeom = null
      entry.map.setAttribute('href', url)
      applyGlassAttrs(entry, params, dispMul, w, h)
      if (prev) URL.revokeObjectURL(prev)
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
    const layer = layerRef.value
    if (!layer) return
    const defs = ensureDefs()
    if (!registry) registry = new Map()

    const key = `lg-${++uidCounter}`
    const filter = document.createElementNS('http://www.w3.org/2000/svg', 'filter')
    filter.id = key
    filter.setAttribute('x', '0%')
    filter.setAttribute('y', '0%')
    filter.setAttribute('width', '100%')
    filter.setAttribute('height', '100%')
    filter.setAttribute('primitiveUnits', 'objectBoundingBox')
    filter.setAttribute('color-interpolation-filters', 'sRGB')
    filter.innerHTML = FILTER_TEMPLATE
    defs.appendChild(filter)

    entry = {
      key,
      layer,
      filter,
      map: filter.querySelector('.f-map') as unknown as SVGFEImageElement,
      dr: filter.querySelector('.f-dr') as unknown as SVGFEDisplacementMapElement,
      dg: filter.querySelector('.f-dg') as unknown as SVGFEDisplacementMapElement,
      db: filter.querySelector('.f-db') as unknown as SVGFEDisplacementMapElement,
      blur: filter.querySelector('.f-blur') as unknown as SVGFEGaussianBlurElement,
      sat: filter.querySelector('.f-sat') as unknown as SVGFEColorMatrixElement,
      geom: { w: 0, h: 0, bezel: 0, radius: 0, dpr: 0 },
      pendingGeom: null,
      ro: new ResizeObserver(() => {
        if (entry) updateGlass(entry, params, dispMul, bezelMul)
      }),
      blobUrl: null,
      mapGen: 0,
    }

    registry.set(key, entry)
    filterId.value = key
    entry.ro.observe(layer)
    requestAnimationFrame(() => {
      if (entry) updateGlass(entry, params, dispMul, bezelMul)
    })
  }

  function unregister() {
    if (!entry) return
    entry.mapGen++
    entry.ro.disconnect()
    if (entry.blobUrl) {
      URL.revokeObjectURL(entry.blobUrl)
      entry.blobUrl = null
    }
    entry.filter.remove()
    registry?.delete(entry.key)
    entry = null
    filterId.value = ''
  }

  watch(
    layerRef,
    (newEl) => {
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

export function getStaticGlassFilterId(): string {
  if (staticFilterId && document.getElementById(staticFilterId)) return staticFilterId

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
    .then((url) => {
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
      const prev = staticBlobUrl
      staticBlobUrl = url
      mapEl.setAttribute('href', url)
      dispEl.setAttribute('scale', '0.01')
      if (prev) URL.revokeObjectURL(prev)
    })
    .catch(() => {
      if (gen === staticMapGen) {
        staticFilterId = null
        svg.remove()
      }
    })

  return id
}
