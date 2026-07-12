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
  dark: boolean
  tintColor: string
}

export const GLASS_PRESETS: Record<'clear' | 'dark', GlassParams> = {
  clear: {
    blur: 2,
    tint: 0.50,
    disp: 44,
    bezel: 24,
    ca: 0.07,
    sat: 1.05,
    spec: 0.9,
    dark: false,
    tintColor: '255, 255, 255',
  },
  dark: {
    blur: 2,
    tint: 0.60,
    disp: 44,
    bezel: 24,
    ca: 0.07,
    sat: 1.05,
    spec: 0.7,
    dark: true,
    tintColor: '24, 24, 30',
  },
}

// R encodes dx, G encodes dy; 0.5 (128) is neutral. Half amplitude encoding.
const displacementMapCache = new Map<string, string>();

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

  const cacheKey = `${W},${H},${R},${B}`
  let mapDataUri = displacementMapCache.get(cacheKey)
  if (!mapDataUri) {
    const canvas = document.createElement('canvas')
    canvas.width = W
    canvas.height = H
    const ctx = canvas.getContext('2d')
    if (!ctx) {
      throw new Error('Failed to get 2D canvas context')
    }
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
    mapDataUri = canvas.toDataURL()
    displacementMapCache.set(cacheKey, mapDataUri)
    if (displacementMapCache.size > 50) displacementMapCache.delete(displacementMapCache.keys().next().value!)
  }
  return mapDataUri
}

// Filter SVG cache excludes tint/tintColor/spec/dark because they don't affect the filter output.
const filterDataUriCache = new Map<string, string>()

export function buildFilterDataUri(
  w: number,
  h: number,
  radius: number,
  params: GlassParams,
  dispMul = 1,
  bezelMul = 1,
  dpr = 1,
): string {
  const cacheKey = `${w},${h},${radius},${dpr},${params.blur},${params.disp},${params.bezel},${params.ca},${params.sat},${dispMul},${bezelMul}`
  const cached = filterDataUriCache.get(cacheKey)
  if (cached) return cached

  const minDim = Math.min(w, h)
  const bezel = Math.min(Math.max(2, params.bezel * bezelMul), minDim * 0.5)
  const mapDataUri = buildDisplacementMap(w, h, radius, bezel, dpr)
  const dispPx = Math.min(params.disp * dispMul, minDim * 0.35)
  const diag = Math.sqrt((w * w + h * h) / 2)
  const scale = (2 * dispPx) / diag
  const scR = (scale * (1 - params.ca)).toFixed(5)
  const scG = scale.toFixed(5)
  const scB = (scale * (1 + params.ca)).toFixed(5)
  const bx = (params.blur / w).toFixed(5)
  const by = (params.blur / h).toFixed(5)
  const sat = params.sat.toFixed(2)

  const svg = `<svg xmlns="http://www.w3.org/2000/svg">
  <filter id="f" x="0" y="0" width="100%" height="100%" primitiveUnits="objectBoundingBox" color-interpolation-filters="sRGB">
    <feImage x="0" y="0" width="1" height="1" preserveAspectRatio="none" href="${mapDataUri}" result="map"/>
    <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="${scR}" result="dr"/>
    <feColorMatrix in="dr" type="matrix" values="1 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 0 0" result="chR"/>
    <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="${scG}" result="dg"/>
    <feColorMatrix in="dg" type="matrix" values="0 0 0 0 0  0 1 0 0 0  0 0 0 0 0  0 0 0 0 0" result="chG"/>
    <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="${scB}" result="db"/>
    <feColorMatrix in="db" type="matrix" values="0 0 0 0 0  0 0 0 0 0  0 0 1 0 0  0 0 0 1 0" result="chB"/>
    <feComposite in="chR" in2="chG" operator="arithmetic" k1="0" k2="1" k3="1" k4="0" result="rg"/>
    <feComposite in="rg" in2="chB" operator="arithmetic" k1="0" k2="1" k3="1" k4="0" result="rgb"/>
    <feGaussianBlur in="rgb" stdDeviation="${bx} ${by}" result="soft"/>
    <feColorMatrix in="soft" type="saturate" values="${sat}"/>
  </filter>
</svg>`
  const result = `url('data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}#f')`
  filterDataUriCache.set(cacheKey, result)
  if (filterDataUriCache.size > 50) filterDataUriCache.delete(filterDataUriCache.keys().next().value!)
  return result
}

const FILTER_TEMPLATE = `
  <feImage x="0" y="0" width="1" height="1" result="map" preserveAspectRatio="none" class="f-map"/>
  <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" class="f-dr" result="dr"/>
  <feColorMatrix in="dr" type="matrix" values="1 0 0 0 0  0 0 0 0 0  0 0 0 0 0  0 0 0 0 0" result="chR"/>
  <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0" class="f-dg" result="dg"/>
  <feColorMatrix in="dg" type="matrix" values="0 0 0 0 0  0 1 0 0 0  0 0 0 0 0  0 0 0 0 0" result="chG"/>
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
  onUrlChange?: (url: string) => void
  update(params: GlassParams, layer: HTMLElement, dispMul: number, bezelMul: number): void
  destroy(): void
}

interface GlassEntry {
  key: string
  layer: HTMLElement
  filter: SVGFilterElement
  map: SVGFEImageElement | null
  dr: SVGFEDisplacementMapElement | null
  dg: SVGFEDisplacementMapElement | null
  db: SVGFEDisplacementMapElement | null
  blur: SVGFEGaussianBlurElement | null
  sat: SVGFEColorMatrixElement | null
  host?: HTMLElement
  noise?: HTMLElement
  rimSvg?: SVGSVGElement
  rimPath?: SVGPathElement
  params: GlassParams
  dispMul: number
  bezelMul: number
  geom: { w: number; h: number; bezel: number; radius: number; dpr: number }
  ro: ResizeObserver
  onUrlChange?: (url: string) => void
}

// Per-root registries: each shadow root / document gets its own defs + registry.
const defsMap = new WeakMap<Node, SVGDefsElement>()
const registryMap = new WeakMap<Node, Map<string, GlassEntry>>()
let uidCounter = 0

function appendTarget(root: Node): ParentNode {
  // Chromium Bug: backdrop-filter: url(#id) inside Shadow DOM cannot find SVG filters
  // defined within the same Shadow DOM. They MUST be in the host document.
  if (root.nodeType === Node.DOCUMENT_NODE) return (root as Document).body
  const doc = root.ownerDocument
  if (doc && doc.body) return doc.body
  return root as ParentNode
}

export function ensureDefs(root: Node): SVGDefsElement {
  const existing = defsMap.get(root)
  const target = appendTarget(root)
  if (existing && target.contains(existing)) return existing
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

function cornerParams(r: number, s: number, minDim: number) {
  r = Math.min(r, minDim / 2)
  let p = (1 + s) * r
  if (p > minDim / 2) {
    p = minDim / 2
    s = Math.max(0, p / r - 1)
  }
  const arcMeasure = 90 * (1 - s)
  const arcLen = Math.sin(((arcMeasure / 2) * Math.PI) / 180) * r * Math.SQRT2
  const angleAlpha = (90 - arcMeasure) / 2
  const p34 = r * Math.tan(((angleAlpha / 2) * Math.PI) / 180)
  const angleBeta = 45 * s
  const c = p34 * Math.cos((angleBeta * Math.PI) / 180)
  const d = c * Math.tan((angleBeta * Math.PI) / 180)
  const b = Math.max(0, (p - arcLen - c - d) / 3)
  return { r, p, a: 2 * b, b, c, d, arcLen }
}

function squirclePath(w: number, h: number, radius: number, smooth = 0.6, inset = 0): string {
  w -= inset * 2
  h -= inset * 2
  const q = cornerParams(Math.max(0, radius - inset), smooth, Math.min(w, h))
  const { r, p, a, b, c, d, arcLen } = q
  const f = (n: number) => +n.toFixed(2)
  const i = inset
  return [
    `M ${f(i + w - p)} ${f(i)}`,
    `c ${f(a)} 0 ${f(a + b)} 0 ${f(a + b + c)} ${f(d)}`,
    `a ${f(r)} ${f(r)} 0 0 1 ${f(arcLen)} ${f(arcLen)}`,
    `c ${f(d)} ${f(c)} ${f(d)} ${f(b + c)} ${f(d)} ${f(a + b + c)}`,
    `L ${f(i + w)} ${f(i + h - p)}`,
    `c 0 ${f(a)} 0 ${f(a + b)} ${f(-d)} ${f(a + b + c)}`,
    `a ${f(r)} ${f(r)} 0 0 1 ${f(-arcLen)} ${f(arcLen)}`,
    `c ${f(-c)} ${f(d)} ${f(-(b + c))} ${f(d)} ${f(-(a + b + c))} ${f(d)}`,
    `L ${f(i + p)} ${f(i + h)}`,
    `c ${f(-a)} 0 ${f(-(a + b))} 0 ${f(-(a + b + c))} ${f(-d)}`,
    `a ${f(r)} ${f(r)} 0 0 1 ${f(-arcLen)} ${f(-arcLen)}`,
    `c ${f(-d)} ${f(-c)} ${f(-d)} ${f(-(b + c))} ${f(-d)} ${f(-(a + b + c))}`,
    `L ${f(i)} ${f(i + p)}`,
    `c 0 ${f(-a)} 0 ${f(-(a + b))} ${f(d)} ${f(-(a + b + c))}`,
    `a ${f(r)} ${f(r)} 0 0 1 ${f(arcLen)} ${f(-arcLen)}`,
    `c ${f(c)} ${f(-d)} ${f(b + c)} ${f(-d)} ${f(a + b + c)} ${f(-d)}`,
    'Z',
  ].join(' ')
}

function updateGlass(entry: GlassEntry, params: GlassParams, dispMul: number, bezelMul: number) {
  if (!entry.map || !entry.dr || !entry.dg || !entry.db || !entry.blur || !entry.sat) return

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
    if (entry.host) {
      entry.host.style.setProperty('--squircle', `path('${squirclePath(w, h, radius)}')`)
    }
    if (entry.rimSvg && entry.rimPath) {
      entry.rimSvg.setAttribute('viewBox', `0 0 ${w} ${h}`)
      entry.rimPath.setAttribute('d', squirclePath(w, h, radius, 0.6, 0.6))
    }
    entry.geom = { w, h, bezel, radius, dpr }
  }

  entry.rimPath?.setAttribute('stroke', params.dark ? 'url(#rim-dark)' : 'url(#rim-light)')

  const dispPx = Math.min(params.disp * dispMul, minDim * 0.35)
  const diag = Math.sqrt((w * w + h * h) / 2)
  const scale = (2 * dispPx) / diag
  entry.dr.setAttribute('scale', (scale * (1 - params.ca)).toFixed(5))
  entry.dg.setAttribute('scale', scale.toFixed(5))
  entry.db.setAttribute('scale', (scale * (1 + params.ca)).toFixed(5))
  entry.blur.setAttribute(
    'stdDeviation',
    `${(params.blur / w).toFixed(5)} ${(params.blur / h).toFixed(5)}`,
  )
  entry.sat.setAttribute('values', params.sat.toFixed(2))

  if (entry.onUrlChange) {
    try {
      entry.onUrlChange(buildFilterDataUri(w, h, radius, params, dispMul, bezelMul, dpr))
    } catch {
      entry.onUrlChange('')
    }
  }
}

function buildChrome(host: HTMLElement) {
  const noise = document.createElement('div')
  noise.className = 'glass-noise'
  host.appendChild(noise)

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.setAttribute('class', 'glass-rim')

  const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs')
  defs.innerHTML = `
    <linearGradient id="rim-light" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#fff" stop-opacity=".85"/>
      <stop offset=".3" stop-color="#fff" stop-opacity=".20"/>
      <stop offset=".65" stop-color="#fff" stop-opacity=".06"/>
      <stop offset="1" stop-color="#fff" stop-opacity=".16"/>
    </linearGradient>
    <linearGradient id="rim-dark" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="#fff" stop-opacity=".32"/>
      <stop offset=".28" stop-color="#fff" stop-opacity=".05"/>
      <stop offset=".6" stop-color="#000" stop-opacity=".18"/>
      <stop offset="1" stop-color="#000" stop-opacity=".30"/>
    </linearGradient>
  `
  svg.appendChild(defs)

  const path = document.createElementNS('http://www.w3.org/2000/svg', 'path')
  path.setAttribute('fill', 'none')
  path.setAttribute('stroke', 'url(#rim-light)')
  path.setAttribute('stroke-width', '1.2')
  svg.appendChild(path)

  host.appendChild(svg)
  return { noise, rimSvg: svg, rimPath: path }
}

export function createGlassFilter(
  defs: SVGDefsElement,
  layer: HTMLElement,
  params: GlassParams,
  dispMul = 1,
  bezelMul = 1,
  onUrlChange?: (url: string) => void
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

  const host = layer.parentElement
  const chrome = host ? buildChrome(host) : undefined

  const entry: GlassEntry = {
    key,
    layer,
    filter,
    host: host || undefined,
    noise: chrome?.noise,
    rimSvg: chrome?.rimSvg,
    rimPath: chrome?.rimPath,
    map: filter.querySelector('.f-map') as SVGFEImageElement | null,
    dr: filter.querySelector('.f-dr') as SVGFEDisplacementMapElement | null,
    dg: filter.querySelector('.f-dg') as SVGFEDisplacementMapElement | null,
    db: filter.querySelector('.f-db') as SVGFEDisplacementMapElement | null,
    blur: filter.querySelector('.f-blur') as SVGFEGaussianBlurElement | null,
    sat: filter.querySelector('.f-sat') as SVGFEColorMatrixElement | null,
    params,
    dispMul,
    bezelMul,
    geom: { w: 0, h: 0, bezel: 0, radius: 0, dpr: 0 },
    ro: new ResizeObserver(() => updateGlass(entry, entry.params, entry.dispMul, entry.bezelMul)),
    onUrlChange,
  }

  registry.set(key, entry)
  registryMap.set(root, registry)
  entry.ro.observe(layer)
  requestAnimationFrame(() => updateGlass(entry, params, dispMul, bezelMul))

  return {
    key,
    filter,
    update(p: GlassParams, l: HTMLElement, dm: number, bm: number) {
      entry.params = p
      entry.layer = l
      entry.dispMul = dm
      entry.bezelMul = bm
      updateGlass(entry, p, dm, bm)
    },
    destroy() {
      entry.ro.disconnect()
      entry.filter.remove()
      if (entry.noise) entry.noise.remove()
      if (entry.rimSvg) entry.rimSvg.remove()
      registry.delete(entry.key)
    },
  }
}

// Static shared refraction: one fixed 256x256 SDF map, returned as a self-contained SVG filter Data URI.
let staticGlassFilterUrl: string | null = null

export function getStaticGlassFilterUrl(): string {
  if (staticGlassFilterUrl) return staticGlassFilterUrl

  const mapUrl = buildDisplacementMap(256, 256, 64, 36, 1)
  const svg = `<svg xmlns="http://www.w3.org/2000/svg">
  <filter id="static-glass-refraction" x="0%" y="0%" width="100%" height="100%" primitiveUnits="objectBoundingBox" color-interpolation-filters="sRGB">
    <feImage x="0" y="0" width="1" height="1" preserveAspectRatio="none" href="${mapUrl}" result="map"/>
    <feDisplacementMap in="SourceGraphic" in2="map" xChannelSelector="R" yChannelSelector="G" scale="0.01" result="dr"/>
    <feGaussianBlur in="dr" stdDeviation="0.004" result="soft"/>
    <feColorMatrix in="soft" type="saturate" values="1.08"/>
  </filter>
</svg>`
  staticGlassFilterUrl = `url('data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}#static-glass-refraction')`
  return staticGlassFilterUrl
}

// Feature-detect whether backdrop-filter supports url() references.
// Firefox does not support SVG filters as backdrop-filter values.
let urlBackdropSupported: boolean | null = null
export function supportsUrlBackdropFilter(): boolean {
  if (urlBackdropSupported !== null) return urlBackdropSupported
  try {
    urlBackdropSupported =
      CSS.supports('backdrop-filter', 'url(#x)') ||
      CSS.supports('-webkit-backdrop-filter', 'url(#x)')
  } catch {
    urlBackdropSupported = false
  }
  return urlBackdropSupported
}
