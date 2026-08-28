<script lang="ts">
  import { t } from '../lib/i18n'
  import { sanitizeDisplayFilename } from '../background/extractorKeys'
  import type { DomLinkKind } from '../utils/messaging'
  import { formatPickerBytes, pickerCatalogKey } from './pickerChrome'
  import { domPickerView } from './domPickerView.svelte'
  import PickerShell from './PickerShell.svelte'

  let { effects = 'full' }: { effects?: 'full' | 'reduced' } = $props()

  let snapshot = $derived(domPickerView.state)
  let submitting = $derived(snapshot.phase === 'submitting')
  let catalogKey = $derived(
    pickerCatalogKey(
      snapshot.catalogId,
      snapshot.items.map(row => row.index),
    ),
  )

  function kindLabel(kind: DomLinkKind | undefined): string {
    if (kind === 'image') return t('dom_kind_image')
    if (kind === 'video') return t('dom_kind_video')
    if (kind === 'audio') return t('dom_kind_audio')
    if (kind === 'source') return t('dom_kind_source')
    return t('dom_kind_link')
  }
</script>

<PickerShell
  {effects}
  source="dom"
  title={t('dom_picker_title')}
  items={snapshot.items}
  {catalogKey}
  selectPolicy="empty"
  {submitting}
  cancelDisabled={submitting && snapshot.banner !== 'pending'}
  folderPrefill={snapshot.folderPrefill}
  restoreFocus={false}
  onCancel={() => domPickerView.onCancel()}
  onSubmit={payload => domPickerView.onSubmit(payload)}
>
  {#snippet banner()}
    {#if snapshot.truncated}
      <div class="extractor-picker-hint">{t('dom_truncated')}</div>
    {/if}
    {#if snapshot.storeUnproven}
      <div class="extractor-picker-hint">{t('dom_store_unproven')}</div>
    {/if}
    {#if snapshot.banner === 'busy'}
      <div class="extractor-picker-hint">{t('dom_busy')}</div>
    {/if}
    {#if snapshot.banner === 'pending'}
      <div class="extractor-picker-hint">{t('dom_ack_pending')}</div>
    {/if}
  {/snippet}
  {#snippet rowMeta(item)}
    {sanitizeDisplayFilename(item.origin) || ''}
    {item.origin ? ' · ' : ''}
    {sanitizeDisplayFilename(item.path) || ''}
    {item.path ? ' · ' : ''}
    {kindLabel(item.kind)}
    {typeof item.size_bytes === 'number' ? ` · ${formatPickerBytes(item.size_bytes)}` : ''}
  {/snippet}
</PickerShell>
