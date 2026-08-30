<script lang="ts">
  import { t } from '../lib/i18n'
  import { pickerCatalogKey } from './pickerChrome'
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
</PickerShell>
