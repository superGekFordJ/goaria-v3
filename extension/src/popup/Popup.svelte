<script lang="ts">
  import { connectionState } from '../stores/connection.svelte'
  import { configState } from '../stores/config.svelte'
  import { onPopupConnectionSignal, unpairFromPopup } from '../stores/connection-popup'
  import { sendMessage } from 'webext-bridge/popup'
  import LiquidGlassPanel from '../lib/glass/LiquidGlassPanel.svelte'
  import type { InterceptionToggleMessage, CaptureArmMessage, CaptureArmReply } from '../utils/messaging'
  import { t } from '../lib/i18n'

  let showSettings = $state(false)
  let showPairGuide = $state(false)
  let showUnpairConfirm = $state(false)

  let statusText = $derived.by(() => {
    if (connectionState.status === 'connected' && connectionState.paired) return t('popup_status_connected_paired')
    if (connectionState.status === 'connected' && !connectionState.paired) return t('popup_status_connected_unpaired')
    if (connectionState.status === 'connecting') return t('popup_status_connecting')
    return t('popup_status_disconnected')
  })

  let statusClass = $derived(connectionState.status)

  let portText = $derived(
    connectionState.wsPort > 0 ? `Port: ${connectionState.wsPort}` : 'Port: -',
  )

  let toggleDisabled = $derived(connectionState.status !== 'connected')
  let sessionArmed = $state(false)
  let armDisabled = $derived(
    sessionArmed ||
      connectionState.status !== 'connected' ||
      !connectionState.paired ||
      !configState.autoCapture,
  )

  $effect(() => {
    if (
      connectionState.status !== 'connected' ||
      !connectionState.paired ||
      !configState.autoCapture
    ) {
      sessionArmed = false
    }
  })

  onPopupConnectionSignal(() => {
    sessionArmed = false
  })

  async function armCapture() {
    if (armDisabled) return
    try {
      const reply = (await sendMessage(
        'capture:arm',
        {} satisfies CaptureArmMessage,
        'background',
      )) as CaptureArmReply
      if (reply?.ok === true) sessionArmed = true
    } catch {
      // background unavailable
    }
  }

  async function toggleInterception() {
    if (toggleDisabled) return
    const newVal = !configState.autoCapture
    configState.autoCapture = newVal
    try {
      await sendMessage(
        'interception:toggle',
        { enabled: newVal } satisfies InterceptionToggleMessage,
        'background',
      )
    } catch {
      // Revert on failure.
      configState.autoCapture = !newVal
    }
  }

  async function handleUnpair() {
    showUnpairConfirm = false
    await unpairFromPopup()
  }

  async function toggleEffects() {
    configState.effects = configState.effects === 'full' ? 'reduced' : 'full'
    await configState.persistEffects()
  }
</script>

<div class="popup-root">
  <div class="popup-header-row">
    <h1 class="popup-brand">GoAria</h1>
  </div>

  <div class="popup-status-row">
    <span class="popup-status-dot {statusClass}"></span>
    <span class="popup-status-text">{statusText}</span>
  </div>

  <div class="popup-port-row">{portText}</div>

  {#if connectionState.lastError && connectionState.status === 'disconnected'}
    <div class="popup-error-text">{connectionState.lastError}</div>
  {/if}

  {#if connectionState.status === 'connected' && connectionState.legacyHost === true}
    <div class="etched-panel popup-guide">{t('popup_legacy_host_hint')}</div>
  {/if}

  <div class="popup-section">
    <LiquidGlassPanel
      as="button"
      interactive={true}
      hoverEffect="all"
      effects={configState.effects}
      class="popup-toggle-row"
      disabled={toggleDisabled}
      onclick={toggleInterception}
    >
      <span class="btn-inner">
        <span class="popup-toggle-label">{t('popup_toggle_intercept')}</span>
        <span>{configState.autoCapture ? 'ON' : 'OFF'}</span>
      </span>
    </LiquidGlassPanel>
  </div>

  <div class="popup-section">
    <LiquidGlassPanel
      as="button"
      interactive={true}
      hoverEffect="all"
      effects={configState.effects}
      class="popup-toggle-row"
      disabled={armDisabled}
      onclick={armCapture}
    >
      <span class="btn-inner">
        <span class="popup-toggle-label">{t('popup_btn_capture_arm')}</span>
      </span>
    </LiquidGlassPanel>
  </div>

  {#if !connectionState.paired}
    <div class="popup-section">
      <LiquidGlassPanel
        as="button"
        interactive={true}
        hoverEffect="glow"
        effects={configState.effects}
        class="popup-pair-btn"
        onclick={() => (showPairGuide = !showPairGuide)}
      >
        <span class="btn-inner">{t('popup_btn_pair')}</span>
      </LiquidGlassPanel>
      {#if showPairGuide}
        <div class="popup-guide">{t('popup_pair_guide')}</div>
      {/if}
    </div>
  {:else}
    <div class="popup-section">
      {#if showUnpairConfirm}
        <div class="popup-confirm-row">
          <span class="popup-confirm-text">{t('popup_confirm_unpair')}</span>
          <LiquidGlassPanel
            as="button"
            interactive={true}
            hoverEffect="scale"
            effects={configState.effects}
            class="popup-confirm-btn"
            onclick={handleUnpair}
          >
            <span class="btn-inner">{t('popup_btn_confirm')}</span>
          </LiquidGlassPanel>
          <LiquidGlassPanel
            as="button"
            interactive={true}
            hoverEffect="scale"
            effects={configState.effects}
            class="popup-confirm-cancel"
            onclick={() => (showUnpairConfirm = false)}
          >
            <span class="btn-inner">{t('popup_btn_cancel')}</span>
          </LiquidGlassPanel>
        </div>
      {:else}
        <LiquidGlassPanel
          as="button"
          interactive={true}
          hoverEffect="glow"
          effects={configState.effects}
          class="popup-unpair-btn"
          onclick={() => (showUnpairConfirm = true)}
        >
          <span class="btn-inner">{t('popup_btn_unpair')}</span>
        </LiquidGlassPanel>
      {/if}
    </div>
  {/if}

  <div class="popup-section">
    <LiquidGlassPanel
      as="button"
      interactive={true}
      hoverEffect="glow"
      effects={configState.effects}
      class="popup-settings-toggle"
      onclick={() => (showSettings = !showSettings)}
    >
      <span class="btn-inner">{t('popup_btn_settings')}</span>
    </LiquidGlassPanel>

    {#if showSettings}
      <div class="etched-panel popup-settings-panel">
        <div class="popup-settings-row">
          <span class="popup-settings-label">{t('popup_settings_advanced_materials')}</span>
          <LiquidGlassPanel
            as="button"
            interactive={true}
            hoverEffect="scale"
            effects={configState.effects}
            class="popup-toggle-btn"
            onclick={toggleEffects}
          >
            <span class="btn-inner">{configState.effects === 'full' ? 'ON' : 'OFF'}</span>
          </LiquidGlassPanel>
        </div>
        <div class="popup-settings-row">
          <span class="popup-settings-label">{t('popup_settings_port')}</span>
          <span class="popup-settings-label">{configState.port}</span>
        </div>
      </div>
    {/if}
  </div>
</div>
