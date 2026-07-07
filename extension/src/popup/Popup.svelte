<script lang="ts">
  import { connectionState } from '../stores/connection.svelte'
  import { configState } from '../stores/config.svelte'
  import { unpairFromPopup } from '../stores/connection-popup'
  import { sendMessage } from 'webext-bridge/popup'
  import LiquidGlassPanel from '../lib/glass/LiquidGlassPanel.svelte'
  import type { InterceptionToggleMessage } from '../utils/messaging'

  let showSettings = $state(false)
  let showPairGuide = $state(false)
  let showUnpairConfirm = $state(false)

  let statusText = $derived.by(() => {
    if (connectionState.status === 'connected' && connectionState.paired) return '已配对 · 已连接'
    if (connectionState.status === 'connected' && !connectionState.paired) return '已连接 · 未配对'
    if (connectionState.status === 'connecting') return '连接中...'
    return '未连接'
  })

  let statusClass = $derived(connectionState.status)

  let portText = $derived(
    connectionState.wsPort > 0 ? `Port: ${connectionState.wsPort}` : 'Port: -',
  )

  let toggleDisabled = $derived(connectionState.status !== 'connected')

  async function toggleInterception() {
    if (toggleDisabled) return
    const newVal = !configState.autoCapture
    configState.autoCapture = newVal
    try {
      await sendMessage('interception:toggle', { enabled: newVal } satisfies InterceptionToggleMessage, 'background')
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
        <span class="popup-toggle-label">拦截下载</span>
        <span>{configState.autoCapture ? 'ON' : 'OFF'}</span>
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
        <span class="btn-inner">配对</span>
      </LiquidGlassPanel>
      {#if showPairGuide}
        <div class="popup-guide">请在 GoAria 设置中点击绑定扩展</div>
      {/if}
    </div>
  {:else}
    <div class="popup-section">
      {#if showUnpairConfirm}
        <div class="popup-confirm-row">
          <span class="popup-confirm-text">确定解绑？</span>
          <LiquidGlassPanel
            as="button"
            interactive={true}
            hoverEffect="scale"
            effects={configState.effects}
            class="popup-confirm-btn"
            onclick={handleUnpair}
          >
            <span class="btn-inner">确认</span>
          </LiquidGlassPanel>
          <LiquidGlassPanel
            as="button"
            interactive={true}
            hoverEffect="scale"
            effects={configState.effects}
            class="popup-confirm-cancel"
            onclick={() => (showUnpairConfirm = false)}
          >
            <span class="btn-inner">取消</span>
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
          <span class="btn-inner">解绑</span>
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
      <span class="btn-inner">设置</span>
    </LiquidGlassPanel>

    {#if showSettings}
      <div class="etched-panel popup-settings-panel">
        <div class="popup-settings-row">
          <span class="popup-settings-label">高级材质</span>
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
          <span class="popup-settings-label">端口</span>
          <span class="popup-settings-label">{configState.port}</span>
        </div>
      </div>
    {/if}
  </div>
</div>
