<script lang="ts">
  import { t } from '../lib/i18n'
  import { sanitizeDisplayFilename } from '../background/extractorKeys'
  import { formatPickerBytes } from './pickerChrome'
  import { burstPickerView } from './burstPickerView.svelte'
  import PickerShell from './PickerShell.svelte'

  let { effects = 'full' }: { effects?: 'full' | 'reduced' } = $props()

  let snapshot = $derived(burstPickerView.state)
  let submitting = $derived(snapshot.phase === 'submitting')
</script>

<PickerShell
  {effects}
  source="burst"
  title={t('burst_picker_title')}
  items={snapshot.items}
  catalogKey={snapshot.captureId}
  selectPolicy="window"
  {submitting}
  cancelDisabled={submitting && snapshot.banner !== 'pending'}
  folderPrefill=""
  restoreFocus={false}
  onCancel={() => burstPickerView.onCancel()}
  onSubmit={payload => burstPickerView.onSubmit(payload)}
>
  {#snippet banner()}
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
    {typeof item.size_bytes === 'number' ? formatPickerBytes(item.size_bytes) : t('picker_size_unknown')}
  {/snippet}
</PickerShell>
