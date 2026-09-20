<script lang="ts">
  import { fly } from 'svelte/transition'
  import LiquidGlassPanel from '../lib/glass/LiquidGlassPanel.svelte'
  import { t } from '../lib/i18n'
  import { sanitizeDisplayFilename } from '../background/extractorKeys'
  import type { I18nKey } from '../lib/i18n-keys'
  import { capsuleView } from './capsuleView.svelte'
  import { detectPageDarkness } from './pageTheme'
  import {
    EXTRACTOR_SUCCESS_HOLD_MS,
    EXTRACTOR_SUCCESS_OUT_MS,
  } from '../stores/config.svelte'

  let { effects = 'full' }: { effects?: 'full' | 'reduced' } = $props()

  let isDark = $state(true)
  let successTimer: ReturnType<typeof setTimeout> | null = null

  let snapshot = $derived(capsuleView.state)
  let visible = $derived(snapshot.ui !== 'hidden' && snapshot.ui !== 'success')
  let showSuccess = $derived(snapshot.ui === 'success')

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
    if (snapshot.ui !== 'success') {
      if (successTimer) {
        clearTimeout(successTimer)
        successTimer = null
      }
      return
    }
    successTimer = setTimeout(() => {
      capsuleView.apply({ type: 'hide', pageToken: snapshot.pageToken })
    }, EXTRACTOR_SUCCESS_HOLD_MS + EXTRACTOR_SUCCESS_OUT_MS)
    return () => {
      if (successTimer) clearTimeout(successTimer)
    }
  })

  function errorKey(code: string): I18nKey {
    switch (code) {
      case 'auth_expired':
        return 'capsule_error_auth_expired'
      case 'timeout':
        return 'capsule_error_timeout'
      case 'busy':
        return 'capsule_error_busy'
      case 'session_expired':
        return 'capsule_error_session_expired'
      case 'unavailable':
        return 'capsule_error_unavailable'
      case 'invalid_request':
        return 'capsule_error_invalid_request'
      case 'pack_error':
        return 'capsule_error_pack_error'
      case 'unsupported':
        return 'capsule_error_unsupported'
      case 'disconnected':
        return 'capsule_error_disconnected'
      case 'no_batch':
        return 'capsule_error_no_batch'
      case 'no_store':
        return 'capsule_error_no_store'
      case 'cookie_error':
        return 'capsule_error_cookie_error'
      case 'idempotency_conflict':
        return 'capsule_error_idempotency_conflict'
      default:
        return 'capsule_error_generic'
    }
  }

  function titleText(): string {
    switch (snapshot.ui) {
      case 'resolving':
      case 'committing':
        return t('capsule_resolving')
      case 'ready': {
        if (snapshot.count > 1) {
          return t('capsule_ready', [String(snapshot.count)])
        }
        const name = sanitizeDisplayFilename(snapshot.filename)
        return name || t('capsule_ready', [String(snapshot.count || 1)])
      }
      case 'success':
        return t('capsule_success')
      case 'error':
        return t(errorKey(snapshot.errorCode))
      default:
        return t('capsule_idle_title')
    }
  }

  function subtitleText(): string {
    switch (snapshot.ui) {
      case 'idle':
        return t('capsule_idle_action')
      case 'ready':
        return snapshot.count > 1 ? t('capsule_ready_action') : t('capsule_idle_action')
      default:
        return ''
    }
  }

  function onPrimary(event: MouseEvent) {
    if (!event.isTrusted) return
    capsuleView.onClick()
  }

  function onDismiss(event: MouseEvent) {
    event.stopPropagation()
    if (!event.isTrusted) return
    capsuleView.onIgnore()
  }
</script>

{#if visible || showSuccess}
  <div
    class="extractor-capsule-wrapper"
    class:extractor-capsule-success={showSuccess}
    data-extractor-capsule="1"
    data-theme={isDark ? 'dark' : 'light'}
    data-ui={snapshot.ui}
    in:fly={{ x: 280, duration: 300, opacity: 1 }}
    out:fly={{ x: 280, duration: EXTRACTOR_SUCCESS_OUT_MS, opacity: 1 }}
  >
    <LiquidGlassPanel radius="var(--radius-squircle-pill, 9999px)" {effects} class="extractor-capsule">
      <div class="extractor-capsule-inner">
        {#if snapshot.ui === 'resolving' || snapshot.ui === 'committing'}
          <div class="extractor-capsule-beam" aria-hidden="true"></div>
        {/if}
        {#if showSuccess}
          <div class="extractor-capsule-complete" aria-hidden="true"></div>
        {/if}

        <button
          type="button"
          class="extractor-capsule-action"
          disabled={snapshot.ui === 'resolving' || snapshot.ui === 'committing'}
          data-extractor-capsule-action="1"
          aria-busy={snapshot.ui === 'resolving' || snapshot.ui === 'committing'}
          onclick={onPrimary}
        >
          <div class="extractor-capsule-icon-box" aria-hidden="true">
            {#if snapshot.ui === 'resolving' || snapshot.ui === 'committing'}
              <svg class="extractor-capsule-spin" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">
                <path d="M12 2v4m0 12v4M4.93 4.93l2.83 2.83m8.48 8.48l2.83 2.83M2 12h4m12 0h4M4.93 19.07l2.83-2.83m8.48-8.48l2.83-2.83" />
              </svg>
            {:else if showSuccess}
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                <path d="M20 6L9 17l-5-5" />
              </svg>
            {:else if snapshot.ui === 'error'}
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
                <circle cx="12" cy="12" r="10" />
                <line x1="12" y1="8" x2="12" y2="12" />
                <line x1="12" y1="16" x2="12.01" y2="16" />
              </svg>
            {:else if snapshot.ui === 'ready' && snapshot.count > 1}
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z" />
              </svg>
            {:else}
              <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M12 3v12m0 0l-4-4m4 4l4-4" />
                <path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2" />
              </svg>
            {/if}
          </div>

          <div class="extractor-capsule-text">
            <span class="extractor-capsule-title">{titleText()}</span>
            {#if subtitleText()}
              <span class="extractor-capsule-hint">{subtitleText()}</span>
            {/if}
          </div>
        </button>

        <button
          type="button"
          class="extractor-capsule-dismiss"
          aria-label={t('capsule_dismiss_aria')}
          onclick={onDismiss}
        >
          ✕
        </button>
      </div>
    </LiquidGlassPanel>
  </div>
{/if}
