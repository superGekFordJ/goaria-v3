<script lang="ts">
  import { t } from '../lib/i18n'
  import { formatPickerBytes, pickerCatalogKey } from './pickerChrome'
  import { pickerView } from './pickerView.svelte'
  import PickerShell from './PickerShell.svelte'

  let { effects = 'full' }: { effects?: 'full' | 'reduced' } = $props()

  let snapshot = $derived(pickerView.state)
  let submitting = $derived(snapshot.phase === 'submitting')
  let catalogKey = $derived(
    pickerCatalogKey(
      snapshot.pageToken,
      snapshot.items.map(row => row.index),
    ),
  )
</script>

<PickerShell
  {effects}
  source="extractor"
  title={t('picker_title')}
  items={snapshot.items}
  {catalogKey}
  selectPolicy="window"
  {submitting}
  cancelDisabled={submitting}
  folderPrefill=""
  restoreFocus={true}
  onCancel={() => pickerView.onCancel()}
  onSubmit={payload => pickerView.onSubmit(payload)}
>
  {#snippet rowMeta(item)}
    {typeof item.size_bytes === 'number' ? formatPickerBytes(item.size_bytes) : t('picker_size_unknown')}
  {/snippet}
</PickerShell>
