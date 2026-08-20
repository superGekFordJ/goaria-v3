<script lang="ts">
  import { fly } from 'svelte/transition'
  import LiquidGlassPanel from '../lib/glass/LiquidGlassPanel.svelte'
  import { t } from '../lib/i18n'
  import { sanitizeDisplayFilename } from '../background/extractorKeys'
  import type { I18nKey } from '../lib/i18n-keys'
  import { capsuleView } from './capsuleView.svelte'
  import {
    EXTRACTOR_SUCCESS_HOLD_MS,
    EXTRACTOR_SUCCESS_OUT_MS,
  } from '../stores/config.svelte'

  let { effects = 'full' }: { effects?: 'full' | 'reduced' } = $props()

  let isSystemDark = $state(true)
  let successTimer: ReturnType<typeof setTimeout> | null = null

  let snapshot = $derived(capsuleView.state)
  let visible = $derived(snapshot.ui !== 'hidden' && snapshot.ui !== 'success')
  let showSuccess = $derived(snapshot.ui === 'success')

  $effect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    isSystemDark = mq.matches
    const listener = (e: MediaQueryListEvent) => {
      isSystemDark = e.matches
    }
    mq.addEventListener('change', listener)
    return () => mq.removeEventListener('change', listener)
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
      case 'ready':
        return snapshot.count > 1 ? t('capsule_ready_action') : t('capsule_ready', [String(snapshot.count || 0)])
      case 'success':
        return t('capsule_success')
      case 'error':
        return t(errorKey(snapshot.errorCode))
      default:
        return t('capsule_idle_title')
    }
  }

  function chipText(): string {
    const name = sanitizeDisplayFilename(snapshot.filename)
    if (name) return name
    if (snapshot.ui === 'ready' && snapshot.count > 1) {
      return t('capsule_ready', [String(snapshot.count)])
    }
    return t('capsule_item_generic')
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
    data-theme={isSystemDark ? 'dark' : 'light'}
    data-ui={snapshot.ui}
    in:fly={{ x: 280, duration: 300, opacity: 1 }}
    out:fly={{ x: 280, duration: EXTRACTOR_SUCCESS_OUT_MS, opacity: 1 }}
  >
    <LiquidGlassPanel radius="var(--radius-squircle-lg, 2rem)" {effects} class="extractor-capsule">
      <div class="extractor-capsule-inner">
        {#if snapshot.ui === 'resolving' || snapshot.ui === 'committing'}
          <div class="extractor-capsule-beam" aria-hidden="true"></div>
        {/if}
        {#if showSuccess}
          <div class="extractor-capsule-complete" aria-hidden="true"></div>
        {/if}

        <div class="extractor-capsule-row">
          <button
            type="button"
            class="extractor-capsule-action"
            disabled={snapshot.ui === 'resolving' || snapshot.ui === 'committing'}
            data-extractor-capsule-action="1"
            aria-busy={snapshot.ui === 'resolving' || snapshot.ui === 'committing'}
            onclick={onPrimary}
          >
            {titleText()}
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

        {#if snapshot.ui === 'idle'}
          <div class="extractor-capsule-hint">{t('capsule_idle_action')}</div>
        {/if}

        <div class="etched-panel extractor-capsule-chip">{chipText()}</div>
      </div>
    </LiquidGlassPanel>
  </div>
{/if}
