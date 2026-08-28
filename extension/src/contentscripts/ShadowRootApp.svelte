<script lang="ts">
  import ShadowDomPopup from './ShadowDomPopup.svelte'
  import ExtractorCapsule from './ExtractorCapsule.svelte'
  import ExtractorPicker from './ExtractorPicker.svelte'
  import DomLinkPicker from './DomLinkPicker.svelte'
  import { capsuleView } from './capsuleView.svelte'
  import { pickerView } from './pickerView.svelte'
  import { domPickerView } from './domPickerView.svelte'
  import { overlayHostOpen, paintOverlayKind } from './overlayCoordinator'

  const effects = $derived(capsuleView.effects)
  const paint = $derived(
    paintOverlayKind({
      extractorPhase: pickerView.state.phase,
      domPhase: domPickerView.state.phase,
    }),
  )
  const overlayOpen = $derived(overlayHostOpen(paint))

  const HOST_CORNER = 'position:fixed;bottom:20px;right:20px;z-index:2147483647;pointer-events:none'
  const HOST_VIEWPORT = 'position:fixed;inset:0;z-index:2147483647;pointer-events:none'

  $effect(() => {
    const host = document.getElementById('goaria-shadow-host')
    if (!host) return
    host.style.cssText = overlayOpen ? HOST_VIEWPORT : HOST_CORNER
    return () => {
      host.style.cssText = HOST_CORNER
    }
  })
</script>

<div class="shadow-root-app" class:is-picker-open={overlayOpen} aria-hidden={overlayOpen || undefined} inert={overlayOpen || undefined}>
  {#if !overlayOpen}
    <ShadowDomPopup {effects} />
  {/if}
  <ExtractorCapsule {effects} />
</div>
{#if paint === 'extractor'}
  <ExtractorPicker {effects} />
{/if}
{#if paint === 'dom'}
  <DomLinkPicker {effects} />
{/if}
