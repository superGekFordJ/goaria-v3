<script lang="ts">
  import { useLiquidGlass, supportsUrlBackdropFilter, GLASS_PRESETS } from './useLiquidGlass.svelte'

  let {
    as = 'div',
    active = true,
    interactive = false,
    hoverEffect = 'none',
    preset = 'auto',
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
    preset?: 'auto' | 'dark' | 'clear'
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

  let isSystemDark = $state(true)

  $effect(() => {
    if (preset !== 'auto') return
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    isSystemDark = mq.matches
    const listener = (e: MediaQueryListEvent) => {
      isSystemDark = e.matches
    }
    mq.addEventListener('change', listener)
    return () => mq.removeEventListener('change', listener)
  })

  let activePreset = $derived(preset === 'auto' ? (isSystemDark ? 'dark' : 'clear') : preset)

  const glassState = useLiquidGlass(() => layerEl, {
    get params() {
      return GLASS_PRESETS[activePreset]
    }
  })

  let backdropStyle = $derived.by(() => {
    if (!glassState.filterId || !supportsUrlBackdropFilter()) {
      return 'backdrop-filter: blur(2px) saturate(1.05); -webkit-backdrop-filter: blur(2px) saturate(1.05)'
    }
    const filter = glassState.filterUrl || `url(#${glassState.filterId})`
    return `backdrop-filter: ${filter}; -webkit-backdrop-filter: ${filter}`
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

    <div class="liquid-glass-content">
      {@render children?.()}
    </div>
  </div>
{/if}
