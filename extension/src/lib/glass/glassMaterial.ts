// Framework-agnostic glass material: SDF displacement map + SVG filter pipeline.
// Ported from frontend/src/composables/useLiquidGlass.ts (no Vue/Svelte deps).

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
  clear: { blur: 2, tint: 0.03, disp: 44, bezel: 24, ca: 0.07, sat: 1.05, spec: 0.6 },
}

// R encodes dx, G encodes dy; 0.5 (128) is neutral. Half amplitude encoding.
export function buildDisplacementMap(
  w: number,
  h: number,
  radius: number,
  bezel: number,
  dpr: number,
): string {
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
  return canvas.toDataURL()
}

const FILTER_TEMPLATE = `
  <feImage x="0" y="0" width="1" height="1" result="map" preserveAspectRatio="none" class="f-map"/>
  <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" class="f-dr" result="dr"/>
  <feColorMatrix in="dr" type="matrix" values="1 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 1 0" result="chR"/>
  <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" class="f-dg" result="dg"/>
  <feColorMatrix in="dg" type="matrix" values="0 0 0 0 0  0 1 0 0 0  0 0 0 0 0  0 0 0 1 0" result="chG"/>
  <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" class="f-db" result="db"/>
  <feColorMatrix in="db" type="matrix" values="0 0 0 0 0  0 0 0 0 0  0 0 1 0 0  0 0 0 1 0" result="chB"/>
  <feComposite in="chR" in2="chG" operator="arithmetic" k1="0" k2="1" k3="1" k4="0" result="rg"/>
  <feComposite in="rg" in2="chB" operator="arithmetic" k1="0" k2="1" k3="1" k4="0" result="rgb"/>
  <feGaussianBlur in="rgb" stdDeviation="0" class="f-blur" result="soft"/>
  <feColorMatrix in="soft" type="saturate" values="1" class="f-sat"/>
`

export interface GlassFilterHandle {
  key: string
  filter: SVGFilterElement
  update(params: GlassParams, layer: HTMLElement, dispMul: number, bezelMul: number): void
  destroy(): void
}

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
  geom: { w: number; h: number; bezel: number; radius: number; dpr: number }
  ro: ResizeObserver
}

// Per-root registries: each shadow root / document gets its own defs + registry.
const defsMap = new WeakMap<Node, SVGDefsElement>()
const registryMap = new WeakMap<Node, Map<string, GlassEntry>>()
let uidCounter = 0

// A Document node may only hold one element child (<html>); append SVG defs
// to <body> instead. ShadowRoot and other nodes accept children directly.
function appendTarget(root: Node): ParentNode {
  return root.nodeType === Node.DOCUMENT_NODE ? (root as Document).body : (root as ParentNode)
}

export function ensureDefs(root: Node): SVGDefsElement {
  const existing = defsMap.get(root)
  if (existing && root.contains(existing)) return existing
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.setAttribute('width', '0')
  svg.setAttribute('height', '0')
  svg.style.cssText = 'position:absolute;pointer-events:none'
  svg.setAttribute('aria-hidden', 'true')
  const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs')
  svg.appendChild(defs)
  appendTarget(root).appendChild(svg)
  defsMap.set(root, defs)
  registryMap.set(root, new Map())
  return defs
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

  const g = entry.geom
  if (g.w !== w || g.h !== h || g.bezel !== bezel || g.radius !== radius || g.dpr !== dpr) {
    entry.map.setAttribute('href', buildDisplacementMap(w, h, radius, bezel, dpr))
    entry.geom = { w, h, bezel, radius, dpr }
  }

  const dispPx = Math.min(params.disp * dispMul, minDim * 0.35)
  const diag = Math.sqrt((w * w + h * h) / 2)
  const scale = (2 * dispPx) / diag
  entry.dr.setAttribute('scale', (scale * (1 - params.ca)).toFixed(5))
  entry.dg.setAttribute('scale', scale.toFixed(5))
  entry.db.setAttribute('scale', (scale * (1 + params.ca)).toFixed(5))
  entry.blur.setAttribute('stdDeviation', `${(params.blur / w).toFixed(5)} ${(params.blur / h).toFixed(5)}`)
  entry.sat.setAttribute('values', params.sat.toFixed(2))
}

export function createGlassFilter(
  defs: SVGDefsElement,
  layer: HTMLElement,
  params: GlassParams,
  dispMul = 1,
  bezelMul = 1,
): GlassFilterHandle {
  const root = defs.getRootNode() as Node
  const registry = registryMap.get(root) ?? new Map()
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

  const entry: GlassEntry = {
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
    ro: new ResizeObserver(() => updateGlass(entry, params, dispMul, bezelMul)),
  }

  registry.set(key, entry)
  registryMap.set(root, registry)
  entry.ro.observe(layer)
  requestAnimationFrame(() => updateGlass(entry, params, dispMul, bezelMul))

  return {
    key,
    filter,
    update(p: GlassParams, l: HTMLElement, dm: number, bm: number) {
      entry.layer = l
      updateGlass(entry, p, dm, bm)
    },
    destroy() {
      entry.ro.disconnect()
      entry.filter.remove()
      registry.delete(entry.key)
    },
  }
}

// Static shared refraction: one fixed 256x256 SDF map, zero ongoing JS cost.
const staticFilterMap = new WeakMap<Node, string>()

export function getStaticGlassFilterId(root: Node): string {
  const existing = staticFilterMap.get(root)
  if (existing) {
    const el = (root as ParentNode).querySelector(`#${existing}`)
    if (el) return existing
  }

  const id = 'static-glass-refraction'
  const size = 256
  const mapUrl = buildDisplacementMap(size, size, 64, 36, 1)

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
    <feImage x="0" y="0" width="1" height="1" result="map" preserveAspectRatio="none" href="${mapUrl}"/>
    <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0.01" result="dr"/>
    <feGaussianBlur in="dr" stdDeviation="0.004" result="soft"/>
    <feColorMatrix in="soft" type="saturate" values="1.08"/>
  `
  defs.appendChild(filter)
  svg.appendChild(defs)
  appendTarget(root).appendChild(svg)

  staticFilterMap.set(root, id)
  return id
}

// Feature-detect whether backdrop-filter supports url() references.
// Firefox does not support SVG filters as backdrop-filter values.
let urlBackdropSupported: boolean | null = null
export function supportsUrlBackdropFilter(): boolean {
  if (urlBackdropSupported !== null) return urlBackdropSupported
  try {
    urlBackdropSupported = CSS.supports('backdrop-filter', 'url(#x)') || CSS.supports('-webkit-backdrop-filter', 'url(#x)')
  } catch {
    urlBackdropSupported = false
  }
  return urlBackdropSupported
}
