// Svelte 5 rune-based composable for liquid glass refraction.
// Ported from frontend/src/composables/useLiquidGlass.ts.

import {
  createGlassFilter,
  ensureDefs,
  getStaticGlassFilterId as getStaticGlassFilterIdImpl,
  GLASS_PRESETS,
  type GlassFilterHandle,
  type GlassParams,
} from './glassMaterial'

export type { GlassParams } from './glassMaterial'
export { GLASS_PRESETS, supportsUrlBackdropFilter } from './glassMaterial'

export interface UseLiquidGlassOptions {
  params?: GlassParams
  dispMul?: number
  bezelMul?: number
}

export function useLiquidGlass(
  layerGetter: () => HTMLElement | null,
  options: UseLiquidGlassOptions = {},
) {
  let filterId = $state('')
  let filterUrl = $state('')
  let handle: GlassFilterHandle | null = null

  $effect(() => {
    const layer = layerGetter()
    if (!layer) return

    const params = options.params ?? GLASS_PRESETS.clear
    const currentDispMul = options.dispMul ?? 1
    const currentBezelMul = options.bezelMul ?? 1

    const rootNode = layer.getRootNode() as Node
    const defs = ensureDefs(rootNode)
    handle = createGlassFilter(defs, layer, params, currentDispMul, currentBezelMul, (url) => {
      filterUrl = url
    })
    filterId = handle.key

    return () => {
      handle?.destroy()
      handle = null
      filterId = ''
      filterUrl = ''
    }
  })

  return {
    get filterId() {
      return filterId
    },
    get filterUrl() {
      return filterUrl
    }
  }
}

export function getStaticGlassFilterId(root: Node): string {
  return getStaticGlassFilterIdImpl(root)
}
