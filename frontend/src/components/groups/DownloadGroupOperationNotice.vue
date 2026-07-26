<script setup lang="ts">
  import { computed } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { X } from '@lucide/vue'
  import {
    OPERATION_WARNING_CODES,
    type DownloadGroupOperationNotice,
  } from '../../stores/downloadGroups'

  const props = defineProps<{
    notice: DownloadGroupOperationNotice
  }>()

  const emit = defineEmits<{
    (e: 'dismiss'): void
  }>()

  const { t } = useI18n()

  const operationCodeSet = new Set(OPERATION_WARNING_CODES)

  const noticeCodes = computed(() => {
    const result = props.notice.result
    const codes = new Set<string>()
    for (const warning of result.warnings ?? []) {
      if (warning?.code && operationCodeSet.has(warning.code)) {
        codes.add(warning.code)
      }
    }
    for (const item of result.items ?? []) {
      if (
        (item?.status === 'failed' || item?.status === 'skipped') &&
        item.code &&
        operationCodeSet.has(item.code)
      ) {
        codes.add(item.code)
      }
    }
    return Array.from(codes)
  })

  const operationStatusKey = computed(() => {
    if (props.notice.noop) return 'noop'
    if (props.notice.severity === 'success') return 'success'
    if (props.notice.severity === 'warning') return 'partialFailure'
    if (props.notice.severity === 'error') return 'failed'
    return 'noop'
  })

  const actionKey = computed(() =>
    props.notice.action === 'open_folder' ? 'openFolder' : props.notice.action,
  )
</script>

<template>
  <div
    class="download-group-operation-notice rounded-[var(--radius-squircle-lg)]"
    :class="`download-group-operation-notice-${notice.severity}`"
  >
    <div class="min-w-0 flex-1">
      <div class="flex flex-wrap items-center gap-2">
        <strong>{{ t('downloadGroups.operation.noticeTitle') }}</strong>
        <span class="download-group-operation-action">
          {{ t(`downloadGroups.action.${actionKey}`) }}
        </span>
        <span>{{ t(`downloadGroups.operation.${operationStatusKey}`) }}</span>
      </div>
      <div class="download-group-operation-counts">
        <span>{{ t('downloadGroups.operation.succeeded') }}: {{ notice.succeeded }}</span>
        <span>{{ t('downloadGroups.operation.skipped') }}: {{ notice.skipped }}</span>
        <span>{{ t('downloadGroups.operation.failedCount') }}: {{ notice.failed }}</span>
      </div>
      <div v-if="noticeCodes.length" class="download-group-operation-codes">
        <span
          v-for="code in noticeCodes"
          :key="code"
          class="download-group-operation-code rounded-[var(--radius-squircle-sm)]"
        >
          {{ t(`downloadGroups.operation.code.${code}`) }}
        </span>
      </div>
    </div>
    <button
      type="button"
      class="btn-glass download-group-notice-dismiss rounded-[var(--radius-squircle-sm)]"
      :aria-label="t('downloadGroups.operation.dismiss')"
      @click="emit('dismiss')"
    >
      <X :size="14" />
    </button>
  </div>
</template>

<style scoped>
  .download-group-operation-notice {
    display: flex;
    align-items: flex-start;
    gap: 1rem;
    border: 1px solid var(--glass-border);
    background: var(--btn-glass-bg);
    padding: 0.875rem 1rem;
    color: var(--app-text-muted);
    font-size: 0.75rem;
  }

  .download-group-operation-notice-success {
    border-color: color-mix(in srgb, var(--status-complete) 24%, var(--glass-border));
    background: color-mix(in srgb, var(--status-complete) 8%, transparent);
  }

  .download-group-operation-notice-info {
    border-color: color-mix(in srgb, var(--neon-primary) 20%, var(--glass-border));
    background: color-mix(in srgb, var(--neon-primary) 7%, transparent);
  }

  .download-group-operation-notice-warning {
    border-color: color-mix(in srgb, var(--status-paused) 24%, var(--glass-border));
    background: color-mix(in srgb, var(--status-paused) 8%, transparent);
  }

  .download-group-operation-notice-error {
    border-color: color-mix(in srgb, var(--status-error) 24%, var(--glass-border));
    background: color-mix(in srgb, var(--status-error) 8%, transparent);
  }

  .download-group-operation-action,
  .download-group-operation-code {
    display: inline-flex;
    align-items: center;
    border: 1px solid color-mix(in srgb, var(--neon-primary) 16%, transparent);
    background: color-mix(in srgb, var(--neon-primary) 7%, transparent);
    color: color-mix(in srgb, var(--neon-primary) 78%, var(--app-text));
    font-size: 0.6875rem;
    font-weight: 800;
  }

  .download-group-operation-action,
  .download-group-operation-code {
    padding: 0.125rem 0.5rem;
  }

  .download-group-operation-counts,
  .download-group-operation-codes {
    margin-top: 0.5rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .download-group-notice-dismiss {
    display: inline-flex;
    height: 2rem;
    width: 2rem;
    flex: 0 0 auto;
    align-items: center;
    justify-content: center;
    color: var(--app-text-muted);
  }
</style>
