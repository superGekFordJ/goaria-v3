<script lang="ts">
  import ShadowDomPopup from './ShadowDomPopup.svelte'
  import ExtractorCapsule from './ExtractorCapsule.svelte'
  import ExtractorPicker from './ExtractorPicker.svelte'
  import { capsuleView } from './capsuleView.svelte'
  import { pickerView } from './pickerView.svelte'

  const effects = $derived(capsuleView.effects)
  const pickerOpen = $derived(pickerView.state.phase !== 'closed')

  const HOST_CORNER = 'position:fixed;bottom:20px;right:20px;z-index:2147483647;pointer-events:none'
  const HOST_VIEWPORT = 'position:fixed;inset:0;z-index:2147483647;pointer-events:none'

  $effect(() => {
    const host = document.getElementById('goaria-shadow-host')
    if (!host) return
    host.style.cssText = pickerOpen ? HOST_VIEWPORT : HOST_CORNER
    return () => {
      host.style.cssText = HOST_CORNER
    }
  })
</script>

<div class="shadow-root-app" class:is-picker-open={pickerOpen} aria-hidden={pickerOpen || undefined} inert={pickerOpen || undefined}>
  {#if !pickerOpen}
    <ShadowDomPopup {effects} />
  {/if}
  <ExtractorCapsule {effects} />
</div>
{#if pickerOpen}
  <ExtractorPicker {effects} />
{/if}
