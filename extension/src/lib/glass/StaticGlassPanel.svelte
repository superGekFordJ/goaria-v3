<script lang="ts">
  import { getStaticGlassFilterUrl, supportsUrlBackdropFilter } from './useLiquidGlass.svelte'

  let {
    as = 'div',
    interactive = false,
    radius = '9999px',
    fallbackClass = '',
    overlayColor = 'rgba(255,255,255,0.2)',
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
    overlayColor?: string
    disabled?: boolean
    refraction?: boolean
    effects?: 'full' | 'reduced'
    children?: import('svelte').Snippet
    class?: string
  } = $props()

  let rootEl = $state<HTMLElement | null>(null)
  let refractionFilterUrl = $state('')

  let isInteractive = $derived(interactive && !disabled)

  $effect(() => {
    if (!rootEl) return
    if (!refraction || effects !== 'full') {
      refractionFilterUrl = ''
      return
    }
    if (!supportsUrlBackdropFilter()) {
      refractionFilterUrl = ''
      return
    }
    refractionFilterUrl = getStaticGlassFilterUrl()
    return () => { refractionFilterUrl = '' }
  })

  let backdropStyle = $derived.by(() => {
    if (refractionFilterUrl) {
      return `backdrop-filter: blur(16px) ${refractionFilterUrl}; -webkit-backdrop-filter: blur(16px) ${refractionFilterUrl}`
    }
    return 'backdrop-filter: blur(16px) saturate(1.4); -webkit-backdrop-filter: blur(16px) saturate(1.4)'
  })
</script>

{#if as === 'button'}
  <button
    bind:this={rootEl}
    class="static-glass-root lg-group {extraClass}"
    class:lg-interactive={isInteractive}
    style="border-radius: {radius}"
    {disabled}
  >
    {#if effects === 'full'}
      <div
        class="static-glass-bg"
        style="{backdropStyle}; border-radius: {radius}; --overlay-color: {overlayColor}"
      ></div>
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
      <div
        class="static-glass-bg"
        style="{backdropStyle}; border-radius: {radius}; --overlay-color: {overlayColor}"
      ></div>
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
