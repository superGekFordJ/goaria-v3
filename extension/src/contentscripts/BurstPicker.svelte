<script lang="ts">
  import { t } from '../lib/i18n'
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
</PickerShell>
