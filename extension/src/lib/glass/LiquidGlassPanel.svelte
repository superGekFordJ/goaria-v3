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
    overlayColor = '',
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
    overlayColor?: string
    disabled?: boolean
    effects?: 'full' | 'reduced'
    children?: import('svelte').Snippet
    class?: string
    onclick?: (e: MouseEvent) => void
  } = $props()

  let layerEl = $state<HTMLElement | null>(null)
  let theme = $state<'dark' | 'clear'>('dark')

  function resolveTheme(): 'dark' | 'clear' {
    if (preset !== 'auto') return preset
    const el = layerEl
    const themed = el?.closest('[data-theme]') as HTMLElement | null
    if (themed) return themed.dataset.theme === 'dark' ? 'dark' : 'clear'
    const root = el?.getRootNode()
    if (root instanceof ShadowRoot) {
      const host = root.host as HTMLElement | null
      const hostTheme = host?.dataset.theme
      if (hostTheme === 'dark' || hostTheme === 'light') return hostTheme === 'dark' ? 'dark' : 'clear'
    }
    const html = document.documentElement?.dataset.theme
    if (html === 'dark' || html === 'light') return html === 'dark' ? 'dark' : 'clear'
    return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'clear'
  }

  $effect(() => {
    theme = resolveTheme()
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const update = () => { theme = resolveTheme() }
    const observer = new MutationObserver(update)
    const el = layerEl
    const themed = el?.closest('[data-theme]') as HTMLElement | null
    if (themed) observer.observe(themed, { attributes: true, attributeFilter: ['data-theme'] })
    const root = el?.getRootNode()
    if (root instanceof ShadowRoot) {
      const host = root.host as HTMLElement | null
      if (host) observer.observe(host, { attributes: true, attributeFilter: ['data-theme'] })
    }
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    mq.addEventListener('change', update)
    return () => {
      observer.disconnect()
      mq.removeEventListener('change', update)
    }
  })

  let isInteractive = $derived(interactive && !disabled)
  let hasGlow = $derived(isInteractive && (hoverEffect === 'all' || hoverEffect === 'glow'))
  let hasScale = $derived(
    isInteractive && effects === 'full' && (hoverEffect === 'all' || hoverEffect === 'scale'),
  )

  let params = $derived(GLASS_PRESETS[theme])

  const glassState = useLiquidGlass(() => layerEl, {
    get params() {
      return params
    },
  })

  let isFallback = $derived(!glassState.filterUrl || !supportsUrlBackdropFilter())

  let backdropStyle = $derived.by(() => {
    if (!glassState.filterUrl || !supportsUrlBackdropFilter()) {
      return `backdrop-filter: blur(${params.blur}px) saturate(${params.sat}); -webkit-backdrop-filter: blur(${params.blur}px) saturate(${params.sat})`
    }
    return `backdrop-filter: ${glassState.filterUrl}; -webkit-backdrop-filter: ${glassState.filterUrl}`
  })
</script>

{#if as === 'button'}
  <button
    class="liquid-glass-root lg-group {extraClass}"
    style="border-radius: {radius}; --tint: {params.tint}; --tint-rgb: {params.tintColor}; --spec: {params.spec}"
    data-active={active}
    data-effects={effects}
    data-fallback={isFallback}
    class:lg-interactive={isInteractive}
    class:lg-scale={hasScale}
    {disabled}
    {onclick}
  >
    {#if effects === 'full' && active}
      <div
        bind:this={layerEl}
        class="liquid-glass-refraction"
        style="{backdropStyle}; border-radius: {radius}; {overlayColor ? `background: ${overlayColor};` : ''}"
      >
        {#if hasGlow}
          <div class="liquid-glass-glow"></div>
        {/if}
      </div>
    {:else if effects === 'full' && isInteractive}
      <div class="liquid-glass-placeholder" style="border-radius: {radius}"></div>
    {:else if effects === 'reduced'}
      <div
        class="liquid-glass-fallback {fallbackClass}"
        class:lg-fallback-active={active}
        style="border-radius: {radius}"
      ></div>
    {/if}

    <div class="liquid-glass-content">
      {@render children?.()}
    </div>
  </button>
{:else}
  <div
    class="liquid-glass-root lg-group {extraClass}"
    style="border-radius: {radius}; --tint: {params.tint}; --tint-rgb: {params.tintColor}; --spec: {params.spec}"
    data-active={active}
    data-effects={effects}
    data-fallback={isFallback}
    class:lg-interactive={isInteractive}
    class:lg-scale={hasScale}
  >
    {#if effects === 'full' && active}
      <div
        bind:this={layerEl}
        class="liquid-glass-refraction"
        style="{backdropStyle}; border-radius: {radius}; {overlayColor ? `background: ${overlayColor};` : ''}"
      >
        {#if hasGlow}
          <div class="liquid-glass-glow"></div>
        {/if}
      </div>
    {:else if effects === 'full' && isInteractive}
      <div class="liquid-glass-placeholder" style="border-radius: {radius}"></div>
    {:else if effects === 'reduced'}
      <div
        class="liquid-glass-fallback {fallbackClass}"
        class:lg-fallback-active={active}
        style="border-radius: {radius}"
      ></div>
    {/if}

    <div class="liquid-glass-content">
      {@render children?.()}
    </div>
  </div>
{/if}
