// Svelte 5 rune-based composable for liquid glass refraction.
// Ported from frontend/src/composables/useLiquidGlass.ts.

import { untrack } from 'svelte'
import {
  createGlassFilter,
  ensureDefs,
  GLASS_PRESETS,
  supportsUrlBackdropFilter,
  type GlassFilterHandle,
  type GlassParams,
} from './glassMaterial'

export type { GlassParams } from './glassMaterial'
export {
  GLASS_PRESETS,
  getStaticGlassFilterUrl,
  supportsUrlBackdropFilter,
  _resetSupportsUrlBackdropFilterForTest,
} from './glassMaterial'

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
  let handle = $state<GlassFilterHandle | null>(null)

  $effect(() => {
    const layer = layerGetter()
    if (!layer || !supportsUrlBackdropFilter()) return

    // Create the handle with the initial params once, avoiding re-creation when reactive params change.
    const params = untrack(() => options.params ?? GLASS_PRESETS.clear)
    const currentDispMul = untrack(() => options.dispMul ?? 1)
    const currentBezelMul = untrack(() => options.bezelMul ?? 1)

    const rootNode = layer.getRootNode() as Node
    const defs = ensureDefs(rootNode)
    const newHandle = createGlassFilter(defs, layer, params, currentDispMul, currentBezelMul, (url) => {
      filterUrl = url
    })
    handle = newHandle
    filterId = newHandle.key

    return () => {
      handle?.destroy()
      handle = null
      filterId = ''
      filterUrl = ''
    }
  })

  $effect(() => {
    if (!handle) return
    const layer = layerGetter()
    if (!layer) return
    const params = options.params ?? GLASS_PRESETS.clear
    const dm = options.dispMul ?? 1
    const bm = options.bezelMul ?? 1
    handle.update(params, layer, dm, bm)
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
