<script lang="ts">
  import { t } from '../lib/i18n'
  import { pickerCatalogKey } from './pickerChrome'
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
/>
