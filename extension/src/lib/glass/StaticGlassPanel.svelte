<script lang="ts">
  import { getStaticGlassFilterId, supportsUrlBackdropFilter } from './useLiquidGlass.svelte'

  let {
    as = 'div',
    interactive = false,
    radius = '9999px',
    fallbackClass = '',
    baseColor = 'rgba(255,255,255,0.2)',
    disabled = false,
    refraction = false,
    effects = 'full',
    children,
    class: extraClass = '',
  }: {
    as?: string
    interactive?: boolean
    radius?: string
    fallbackClass?: string
    baseColor?: string
    disabled?: boolean
    refraction?: boolean
    effects?: 'full' | 'reduced'
    children?: import('svelte').Snippet
    class?: string
  } = $props()

  let rootEl = $state<HTMLElement | null>(null)

  let isInteractive = $derived(interactive && !disabled)

  let refractionFilterId = $derived.by(() => {
    if (!refraction || effects !== 'full' || !rootEl) return ''
    if (!supportsUrlBackdropFilter()) return ''
    const rootNode = rootEl.getRootNode() as Node
    return getStaticGlassFilterId(rootNode)
  })

  let backdropStyle = $derived.by(() => {
    if (refractionFilterId) {
      return `backdrop-filter: blur(16px) url(#${refractionFilterId}); -webkit-backdrop-filter: blur(16px) url(#${refractionFilterId})`
    }
    // Firefox lacks url() backdrop-filter support; degrade with saturate.
    return 'backdrop-filter: blur(16px) saturate(1.4); -webkit-backdrop-filter: blur(16px) saturate(1.4)'
  })
</script>

{#if as === 'button'}
  <button
    bind:this={rootEl}
    class="static-glass-root lg-group {extraClass}"
    class:lg-interactive={isInteractive}
    style="border-radius: {radius}"
    disabled={disabled}
  >
    {#if effects === 'full'}
      <div class="static-glass-bg" style="{backdropStyle}; border-radius: {radius}; --base-color: {baseColor}"></div>
      <div class="static-glass-shadow" style="border-radius: {radius}"></div>
    {:else if !fallbackClass}
      <div class="static-glass-fallback" style="border-radius: {radius}"></div>
    {/if}
    {#if fallbackClass && effects === 'reduced'}
      <div class={fallbackClass} style="border-radius: {radius}"></div>
    {/if}
    <div class="static-glass-content">
      {@render children?.()}
    </div>
  </button>
{:else}
  <div
    bind:this={rootEl}
    class="static-glass-root lg-group {extraClass}"
    class:lg-interactive={isInteractive}
    style="border-radius: {radius}"
  >
    {#if effects === 'full'}
      <div class="static-glass-bg" style="{backdropStyle}; border-radius: {radius}; --base-color: {baseColor}"></div>
      <div class="static-glass-shadow" style="border-radius: {radius}"></div>
    {:else if !fallbackClass}
      <div class="static-glass-fallback" style="border-radius: {radius}"></div>
    {/if}
    {#if fallbackClass && effects === 'reduced'}
      <div class={fallbackClass} style="border-radius: {radius}"></div>
    {/if}
    <div class="static-glass-content">
      {@render children?.()}
    </div>
  </div>
{/if}


