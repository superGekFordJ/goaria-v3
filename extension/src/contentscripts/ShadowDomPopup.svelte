<script lang="ts">
  import { fly } from 'svelte/transition'
  import { popupQueue } from '../stores/popupQueue.svelte'
  import LiquidGlassPanel from '../lib/glass/LiquidGlassPanel.svelte'
  import { t } from '../lib/i18n'
  import { detectPageDarkness } from './pageTheme'

  let { effects = 'full' }: { effects?: 'full' | 'reduced' } = $props()

  let message = $derived(popupQueue.current)
  let dismissTimer: ReturnType<typeof setTimeout> | null = null

  let isDark = $state(true)

  $effect(() => {
    isDark = detectPageDarkness()
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const update = () => {
      isDark = detectPageDarkness()
    }
    mq.addEventListener('change', update)

    const observer = new MutationObserver(update)
    if (document.documentElement) {
      observer.observe(document.documentElement, {
        attributes: true,
        attributeFilter: ['class', 'data-theme', 'data-color-mode'],
      })
    }
    if (document.body) {
      observer.observe(document.body, {
        attributes: true,
        attributeFilter: ['class', 'data-theme', 'data-color-mode'],
      })
    }

    return () => {
      mq.removeEventListener('change', update)
      observer.disconnect()
    }
  })

  $effect(() => {
    if (message) {
      isDark = detectPageDarkness()
      if (message.success) {
        dismissTimer = setTimeout(() => popupQueue.dismiss(), 5000)
      }
    }
    return () => {
      if (dismissTimer) clearTimeout(dismissTimer)
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
  <div class="shadow-dom-popup-wrapper" data-theme={isDark ? 'dark' : 'light'} transition:fly={{ x: 300, duration: 300 }}>
    <LiquidGlassPanel
      radius="var(--radius-squircle-lg, 1.25rem)"
      {effects}
      class="shadow-dom-popup"
    >
      <div class="popup-inner">
        <div class="popup-header">
          <span class="popup-icon" class:success={message.success} class:error={!message.success}>
            {message.success ? '✓' : '✕'}
          </span>
          <span class="popup-title">
            {message.success ? t('shadow_popup_taken_over') : t('shadow_popup_takeover_failed')}
          </span>
        </div>

        <div class="popup-filename">
          {message.filename || hostUrl}
        </div>

        {#if message.filename}
          <div class="popup-url">{hostUrl}</div>
        {/if}

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
              <span class="btn-inner">{t('shadow_popup_btn_confirm')}</span>
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
              <span class="btn-inner">{t('shadow_popup_btn_close')}</span>
            </LiquidGlassPanel>
          {/if}
        </div>
      </div>
    </LiquidGlassPanel>
  </div>
{/if}
