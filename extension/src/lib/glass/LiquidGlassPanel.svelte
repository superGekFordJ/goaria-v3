<script lang="ts">
  import { useLiquidGlass, supportsUrlBackdropFilter } from './useLiquidGlass.svelte'

  let {
    as = 'div',
    active = true,
    interactive = false,
    hoverEffect = 'none',
    radius = 'var(--radius-squircle-md, 1.5rem)',
    fallbackClass = '',
    baseColor = 'var(--app-liquid-glass-bg, rgba(0,0,0,0.2))',
    disabled = false,
    effects = 'full',
    children,
    class: extraClass = '',
    onclick,
  }: {
    as?: string
    active?: boolean
    interactive?: boolean
    hoverEffect?: 'none' | 'glow' | 'scale' | 'all'
    radius?: string
    fallbackClass?: string
    baseColor?: string
    disabled?: boolean
    effects?: 'full' | 'reduced'
    children?: import('svelte').Snippet
    class?: string
    onclick?: (e: MouseEvent) => void
  } = $props()

  let layerEl = $state<HTMLElement | null>(null)

  let isInteractive = $derived(interactive && !disabled)
  let hasGlow = $derived(isInteractive && (hoverEffect === 'all' || hoverEffect === 'glow'))
  let hasScale = $derived(
    isInteractive && effects === 'full' && (hoverEffect === 'all' || hoverEffect === 'scale'),
  )

  const { filterId } = useLiquidGlass(() => layerEl)

  let backdropStyle = $derived.by(() => {
    if (!filterId || !supportsUrlBackdropFilter()) {
      return 'backdrop-filter: blur(2px) saturate(1.05); -webkit-backdrop-filter: blur(2px) saturate(1.05)'
    }
    return `backdrop-filter: url(#${filterId}); -webkit-backdrop-filter: url(#${filterId})`
  })
</script>

{#if as === 'button'}
  <button
    class="liquid-glass-root lg-group {extraClass}"
    style="border-radius: {radius}"
    class:lg-interactive={isInteractive}
    class:lg-scale={hasScale}
    {disabled}
    {onclick}
  >
    {#if effects === 'full' && active}
      <div
        bind:this={layerEl}
        class="liquid-glass-refraction"
        style="{backdropStyle}; border-radius: {radius}; background: {baseColor}"
      >
        {#if hasGlow}
          <div class="liquid-glass-glow"></div>
        {/if}
      </div>
    {:else if effects === 'full' && isInteractive}
      <div class="liquid-glass-placeholder" style="border-radius: {radius}"></div>
    {:else if effects === 'reduced' && !fallbackClass}
      <div
        class="liquid-glass-fallback"
        class:lg-fallback-active={active}
        style="border-radius: {radius}; background: {baseColor}"
      ></div>
    {/if}

    {#if effects === 'full' && active}
      <div class="lg-bevel" style="border-radius: {radius}"></div>
      <div class="lg-specular" style="border-radius: {radius}"></div>
    {/if}

    <div class="liquid-glass-content">
      {@render children?.()}
    </div>
  </button>
{:else}
  <div
    class="liquid-glass-root lg-group {extraClass}"
    style="border-radius: {radius}"
    class:lg-interactive={isInteractive}
    class:lg-scale={hasScale}
  >
    {#if effects === 'full' && active}
      <div
        bind:this={layerEl}
        class="liquid-glass-refraction"
        style="{backdropStyle}; border-radius: {radius}; background: {baseColor}"
      >
        {#if hasGlow}
          <div class="liquid-glass-glow"></div>
        {/if}
      </div>
    {:else if effects === 'full' && isInteractive}
      <div class="liquid-glass-placeholder" style="border-radius: {radius}"></div>
    {:else if effects === 'reduced' && !fallbackClass}
      <div
        class="liquid-glass-fallback"
        class:lg-fallback-active={active}
        style="border-radius: {radius}; background: {baseColor}"
      ></div>
    {/if}

    {#if effects === 'full' && active}
      <div class="lg-bevel" style="border-radius: {radius}"></div>
      <div class="lg-specular" style="border-radius: {radius}"></div>
    {/if}

    <div class="liquid-glass-content">
      {@render children?.()}
    </div>
  </div>
{/if}
