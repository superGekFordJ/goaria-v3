<script lang="ts">
  import { fly } from 'svelte/transition'
  import { popupQueue } from '../stores/popupQueue.svelte'
  import StaticGlassPanel from '../lib/glass/StaticGlassPanel.svelte'
  import LiquidGlassPanel from '../lib/glass/LiquidGlassPanel.svelte'

  let { effects = 'full' }: { effects?: 'full' | 'reduced' } = $props()

  let message = $derived(popupQueue.current)
  let dismissTimer: ReturnType<typeof setTimeout> | null = null

  $effect(() => {
    if (message && message.success) {
      dismissTimer = setTimeout(() => popupQueue.dismiss(), 5000)
      return () => {
        if (dismissTimer) clearTimeout(dismissTimer)
      }
    }
  })

  function dismiss() {
    popupQueue.dismiss()
  }

  let hostUrl = $derived.by(() => {
    if (!message) return ''
    try {
      return new URL(message.url).host
    } catch {
      return message.url.slice(0, 40)
    }
  })
</script>

{#if message}
  <div
    class="shadow-dom-popup-wrapper"
    transition:fly={{ x: 300, duration: 300 }}
  >
    <StaticGlassPanel
      refraction={effects === 'full'}
      radius="var(--radius-squircle-lg, 2rem)"
      {effects}
      class="shadow-dom-popup"
    >
      <div class="popup-inner">
        <div class="popup-header">
          <span class="popup-icon" class:success={message.success} class:error={!message.success}>
            {message.success ? '✓' : '✕'}
          </span>
          <span class="popup-title">
            {message.success ? 'GoAria 已接管下载' : '接管失败'}
          </span>
        </div>

        <div class="etched-panel popup-filename">
          {message.filename}
        </div>

        <div class="popup-url">{hostUrl}</div>

        {#if !message.success && message.error}
          <div class="popup-error">{message.error}</div>
        {/if}

        <div class="popup-actions">
          {#if message.success}
            <LiquidGlassPanel
              as="button"
              interactive={true}
              hoverEffect="all"
              {effects}
              class="popup-btn"
              onclick={dismiss}
            >
              <span class="btn-inner">确认</span>
            </LiquidGlassPanel>
          {:else}
            <LiquidGlassPanel
              as="button"
              interactive={true}
              hoverEffect="glow"
              {effects}
              class="popup-btn"
              onclick={dismiss}
            >
              <span class="btn-inner">关闭</span>
            </LiquidGlassPanel>
          {/if}
        </div>
      </div>
    </StaticGlassPanel>
  </div>
{/if}


