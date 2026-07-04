<script lang="ts">
  import { connectionState } from '../stores/connection.svelte'
  import { configState } from '../stores/config.svelte'

  let statusText = $derived(connectionState.isConnected ? 'Connected' : 'Disconnected')

  let portText = $derived(`Port: ${configState.port}`)

  function simulateConnect() {
    connectionState.status = connectionState.isConnected ? 'disconnected' : 'connected'
  }
</script>

<div class="glass-panel popup-root">
  <h1>GoAria</h1>
  <p class="status">{statusText}</p>
  <p class="port">{portText}</p>
  <button class="test-btn" onclick={simulateConnect}>Toggle Connection</button>
</div>

<style>
  .popup-root {
    width: 280px;
    padding: 16px;
    border-radius: var(--radius-squircle-md, 16px);
  }

  h1 {
    margin: 0 0 8px;
    font-size: 18px;
    color: var(--color-neon-cyan, #00e5ff);
  }

  .status {
    margin: 4px 0;
    font-size: 14px;
  }

  .port {
    margin: 4px 0 12px;
    font-size: 12px;
    opacity: 0.7;
  }

  .test-btn {
    padding: 6px 12px;
    border: 1px solid var(--color-neon-cyan, #00e5ff);
    border-radius: 8px;
    background: transparent;
    color: var(--color-neon-cyan, #00e5ff);
    cursor: pointer;
    font-size: 13px;
  }

  .test-btn:hover {
    background: var(--color-glass-bg, rgba(255, 255, 255, 0.08));
  }
</style>
