<script setup lang="ts">
  import { computed, onMounted, ref, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import {
    AlertTriangle,
    ArrowLeft,
    FolderOpen,
    Layers3,
    Pause,
    Play,
    RefreshCw,
    Trash2,
  } from '@lucide/vue'
  import {
    normalizeDownloadGroupWarningSummaries,
    type DownloadGroupOperationAction,
    useDownloadGroupStore,
  } from '../../stores/downloadGroups'
  import { useUIStore } from '../../stores/ui'
  import { useTaskStore } from '../../stores/task'
  import DownloadGroupOperationNotice from './DownloadGroupOperationNotice.vue'
  import DownloadGroupRemoveDialog from './DownloadGroupRemoveDialog.vue'
  import TaskList from '../tasks/TaskList.vue'
  import type { DownloadGroupCard as BackendDownloadGroupCard } from '../../../bindings/goaria-v3/internal/downloadgroups/models'
  import StaticGlassPanel from '../common/StaticGlassPanel.vue'
  import LiquidGlassPanel from '../common/LiquidGlassPanel.vue'

  const { t } = useI18n()
  const uiStore = useUIStore()
  const downloadGroupStore = useDownloadGroupStore()
  const taskStore = useTaskStore()

  const removeDialog = ref<{
    groupKey: string
    displayName: string
    fromDetail: boolean
  } | null>(null)

  const selectedKey = computed(() => uiStore.selectedDownloadGroupKey)
  const currentDetail = computed(() => {
    const detail = downloadGroupStore.currentDetail
    return detail?.group_key === selectedKey.value ? detail : null
  })
  const detailTasks = computed(
    () =>
      currentDetail.value?.tasks ?? {
        active: [],
        waiting: [],
        stopped: [],
      },
  )
  const isRefreshing = computed(
    () => downloadGroupStore.isLoading || downloadGroupStore.isDetailLoading,
  )
  const detailGroupName = computed(() => {
    const group = currentDetail.value?.group
    return (
      group?.display_name ||
      group?.fallback_name ||
      selectedKey.value ||
      t('downloadGroups.detailNotFoundTitle')
    )
  })
  const detailCard = computed(() => currentDetail.value?.group ?? null)
  const detailWarningSummaries = computed(() => {
    const detail = currentDetail.value
    if (!detail) return []
    return normalizeDownloadGroupWarningSummaries(
      [...(detail.group?.warnings ?? []), ...(detail.warnings ?? [])],
      detail.group?.name_status,
      detail.degraded || detail.group?.degraded === true,
    )
  })
  const detailWarningTitle = computed(() => {
    if (!detailWarningSummaries.value.length) return undefined
    return detailWarningSummaries.value
      .map(summary =>
        t('downloadGroups.warning.tooltip', {
          label: t(summary.labelKey),
          description: t(summary.descriptionKey),
          count: summary.count,
        }),
      )
      .join('\n')
  })

  const operationNotice = computed(() => downloadGroupStore.operationNotice)

  const backLabelKey = computed(() =>
    uiStore.activeTab === 'stopped'
      ? 'downloadGroups.backToCompleted'
      : 'downloadGroups.backToDownloads',
  )

  async function refreshGroups() {
    await downloadGroupStore.fetchGroups()
    if (selectedKey.value) {
      await downloadGroupStore.fetchGroupDetail(selectedKey.value)
    }
  }

  function closeDetail() {
    uiStore.closeDownloadGroupDetail()
  }

  function isBusy(groupKey: string, action: DownloadGroupOperationAction) {
    return downloadGroupStore.isGroupOperationBusy(groupKey, action)
  }

  function canPause(card?: BackendDownloadGroupCard | null) {
    if (!card?.group_key || isBusy(card.group_key, 'pause')) return false
    return (card.counts.active ?? 0) > 0 || (card.counts.waiting ?? 0) > 0
  }

  function canResume(card?: BackendDownloadGroupCard | null) {
    if (!card?.group_key || isBusy(card.group_key, 'resume')) return false
    return (card.counts.paused ?? 0) > 0
  }

  function canOpenFolder(card?: BackendDownloadGroupCard | null) {
    if (!card?.group_key || isBusy(card.group_key, 'open_folder')) return false
    return card.has_folder === true
  }

  function canRemove(card?: BackendDownloadGroupCard | null) {
    if (!card?.group_key || isBusy(card.group_key, 'remove')) return false
    return true
  }

  function pauseGroup(groupKey: string) {
    void downloadGroupStore.pauseGroup(groupKey)
  }

  function resumeGroup(groupKey: string) {
    void downloadGroupStore.resumeGroup(groupKey)
  }

  function openGroupFolder(groupKey: string) {
    void downloadGroupStore.openGroupFolder(groupKey)
  }

  function openRemoveDialog(groupKey: string, displayName = groupKey, fromDetail = false) {
    if (!groupKey) return
    removeDialog.value = { groupKey, displayName, fromDetail }
  }

  async function confirmRemoveGroup(deleteFiles: boolean) {
    if (!removeDialog.value) return
    const target = removeDialog.value
    await downloadGroupStore.removeGroup(target.groupKey, deleteFiles)
    if (target.fromDetail) {
      uiStore.closeDownloadGroupDetail()
      downloadGroupStore.clearCurrentDetailForGroup(target.groupKey)
      taskStore.clearSelection()
    }
    removeDialog.value = null
  }

  function cancelRemoveGroup() {
    removeDialog.value = null
  }

  watch(selectedKey, key => {
    if (!key && removeDialog.value?.fromDetail) {
      removeDialog.value = null
    }
  })

  onMounted(() => {
    void refreshGroups()
  })

  watch(
    () => uiStore.activeTab,
    tab => {
      if (selectedKey.value && (tab === 'downloads' || tab === 'stopped')) {
        void refreshGroups()
      }
    },
  )

  watch(selectedKey, key => {
    if (key) {
      void downloadGroupStore.fetchGroupDetail(key)
    }
  })
</script>

<template>
  <section class="download-group-shell flex-1 flex flex-col min-h-0">
    <header class="download-group-shell-header">
      <div class="flex items-center gap-3 min-w-0">
        <button
          type="button"
          class="btn-glass download-group-back rounded-[var(--radius-squircle-md)]"
          @click="closeDetail"
        >
          <ArrowLeft :size="16" />
          <span>{{ t(backLabelKey) }}</span>
        </button>
        <div class="download-group-shell-icon rounded-[var(--radius-squircle-md)]">
          <Layers3 :size="18" />
        </div>
        <div class="min-w-0">
          <h2 class="text-lg font-black text-[var(--app-text)] truncate">
            {{ detailGroupName }}
          </h2>
          <p class="text-xs text-[var(--app-text-subtle)] truncate">
            {{ t('downloadGroups.detailDescription') }}
          </p>
        </div>
      </div>

      <button
        type="button"
        class="btn-glass download-group-refresh rounded-[var(--radius-squircle-md)]"
        :disabled="isRefreshing"
        @click="refreshGroups"
      >
        <RefreshCw :size="15" :class="isRefreshing ? 'download-group-refreshing' : ''" />
        <span>{{
          isRefreshing ? t('downloadGroups.refreshing') : t('downloadGroups.refresh')
        }}</span>
      </button>
    </header>

    <DownloadGroupOperationNotice
      v-if="operationNotice"
      class="mx-5 mb-3"
      :notice="operationNotice"
      @dismiss="downloadGroupStore.clearOperationNotice()"
    />

    <main class="flex-1 min-h-0 overflow-hidden">
      <div v-if="selectedKey" class="h-full flex flex-col min-h-0">
        <div class="download-group-detail-summary px-5 pb-3">
          <StaticGlassPanel
            radius="rounded-[var(--radius-squircle-lg)]"
            class="p-4"
            fallback-class="glass-panel-subtle"
          >
            <div
              v-if="downloadGroupStore.isDetailLoading && !currentDetail"
              class="download-group-detail-state"
            >
              <Layers3 :size="15" />
              {{ t('downloadGroups.detailLoading') }}
            </div>
            <div
              v-else-if="downloadGroupStore.detailError"
              class="download-group-detail-state error"
            >
              <AlertTriangle :size="15" />
              <strong>{{ t('downloadGroups.detailErrorTitle') }}</strong>
              <span>{{ t('downloadGroups.detailErrorDescription') }}</span>
            </div>
            <div
              v-else-if="currentDetail && !currentDetail.found"
              class="download-group-detail-state degraded"
            >
              <AlertTriangle :size="15" />
              <strong>{{ t('downloadGroups.detailNotFoundTitle') }}</strong>
              <span>{{ t('downloadGroups.detailNotFoundDescription') }}</span>
              <span
                v-if="detailWarningSummaries.length"
                class="download-group-detail-warning-chip rounded-[var(--radius-squircle-sm)]"
                :title="detailWarningTitle"
              >
                {{ t('downloadGroups.warning.badge', { count: detailWarningSummaries.length }) }}
              </span>
            </div>
            <div v-else class="download-group-detail-meta">
              <span>
                {{ t('downloadGroups.memberCount') }}:
                {{ currentDetail?.group.counts.resolved ?? 0 }}
              </span>
              <span>
                {{ t('downloadGroups.progress') }}:
                {{
                  t('downloadGroups.percent', {
                    value: Math.round((currentDetail?.group.progress ?? 0) * 100),
                  })
                }}
              </span>
              <span
                v-if="detailWarningSummaries.length"
                class="download-group-detail-warning-chip rounded-[var(--radius-squircle-sm)]"
                :title="detailWarningTitle"
              >
                {{ t('downloadGroups.warning.badge', { count: detailWarningSummaries.length }) }}
              </span>
            </div>

            <div v-if="detailCard" class="download-group-detail-actions">
              <LiquidGlassPanel
                as="button"
                :interactive="true"
                hover-effect="all"
                base-color-class="bg-[var(--btn-glass-bg)]"
                fallback-class="btn-glass"
                type="button"
                class="download-group-detail-action rounded-[var(--radius-squircle-sm)] transition-colors hover:text-[var(--app-text)] disabled:opacity-50 disabled:cursor-not-allowed"
                :disabled="!canPause(detailCard)"
                @click="pauseGroup(detailCard.group_key)"
              >
                <span class="flex items-center justify-center gap-1.5 w-full h-full">
                  <Pause :size="14" />
                  <span>{{ t('downloadGroups.action.pause') }}</span>
                </span>
              </LiquidGlassPanel>
              <LiquidGlassPanel
                as="button"
                :interactive="true"
                hover-effect="all"
                base-color-class="bg-[var(--btn-glass-bg)]"
                fallback-class="btn-glass"
                type="button"
                class="download-group-detail-action rounded-[var(--radius-squircle-sm)] transition-colors hover:text-[var(--app-text)] disabled:opacity-50 disabled:cursor-not-allowed"
                :disabled="!canResume(detailCard)"
                @click="resumeGroup(detailCard.group_key)"
              >
                <span class="flex items-center justify-center gap-1.5 w-full h-full">
                  <Play :size="14" />
                  <span>{{ t('downloadGroups.action.resume') }}</span>
                </span>
              </LiquidGlassPanel>
              <button
                type="button"
                class="btn-glass download-group-detail-action rounded-[var(--radius-squircle-sm)]"
                :disabled="!canOpenFolder(detailCard)"
                @click="openGroupFolder(detailCard.group_key)"
              >
                <FolderOpen :size="14" />
                <span>{{ t('downloadGroups.action.openFolder') }}</span>
              </button>
              <button
                type="button"
                class="btn-glass download-group-detail-action download-group-detail-action-danger rounded-[var(--radius-squircle-sm)]"
                :disabled="!canRemove(detailCard)"
                @click="openRemoveDialog(detailCard.group_key, detailGroupName, true)"
              >
                <Trash2 :size="14" />
                <span>{{ t('downloadGroups.action.remove') }}</span>
              </button>
            </div>
          </StaticGlassPanel>
        </div>

        <TaskList mode="group-detail" :detail-tasks="detailTasks" :detail-key="selectedKey || ''" />
      </div>

      <div v-else class="h-full flex items-center justify-center p-8">
        <div
          class="download-group-detail-state degraded glass-panel-subtle rounded-[var(--radius-squircle-xl)] p-6"
        >
          <AlertTriangle :size="15" />
          <strong>{{ t('downloadGroups.detailNotFoundTitle') }}</strong>
          <span>{{ t('downloadGroups.detailNotFoundDescription') }}</span>
        </div>
      </div>
    </main>

    <DownloadGroupRemoveDialog
      :open="Boolean(removeDialog)"
      :group-key="removeDialog?.groupKey || ''"
      :display-name="removeDialog?.displayName || ''"
      :busy="
        removeDialog
          ? downloadGroupStore.isGroupOperationBusy(removeDialog.groupKey, 'remove')
          : false
      "
      @cancel="cancelRemoveGroup"
      @confirm="confirmRemoveGroup"
    />
  </section>
</template>

<style scoped>
  .download-group-shell-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    padding: 1.25rem 1.25rem 0.75rem;
  }

  .download-group-shell-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    border: 1px solid color-mix(in srgb, var(--neon-primary) 18%, transparent);
    background: color-mix(in srgb, var(--neon-primary) 8%, transparent);
    color: var(--neon-primary);
  }

  .download-group-shell-icon {
    width: 2.5rem;
    height: 2.5rem;
  }

  .download-group-back,
  .download-group-refresh {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.625rem 0.875rem;
    color: var(--app-text-muted);
    font-size: 0.75rem;
    font-weight: 800;
  }

  .download-group-refresh:disabled {
    cursor: wait;
    opacity: 0.65;
  }

  .download-group-refreshing {
    transform: rotate(12deg);
  }

  .download-group-alert {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 1rem;
    padding: 0.875rem 1rem;
    color: color-mix(in srgb, var(--status-paused) 74%, var(--app-text));
    font-size: 0.75rem;
    font-weight: 700;
  }

  .download-group-detail-warning-chip {
    display: inline-flex;
    align-items: center;
    border: 1px solid color-mix(in srgb, var(--neon-primary) 16%, transparent);
    background: color-mix(in srgb, var(--neon-primary) 7%, transparent);
    color: color-mix(in srgb, var(--neon-primary) 78%, var(--app-text));
    font-size: 0.6875rem;
    font-weight: 800;
  }

  .download-group-detail-state,
  .download-group-detail-meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem;
    color: var(--app-text-muted);
    font-size: 0.75rem;
    font-weight: 700;
  }

  .download-group-detail-warning-chip {
    padding: 0.125rem 0.5rem;
    border-color: color-mix(in srgb, var(--status-paused) 24%, transparent);
    background: color-mix(in srgb, var(--status-paused) 9%, transparent);
    color: color-mix(in srgb, var(--status-paused) 78%, var(--app-text));
  }

  .download-group-detail-actions {
    margin-top: 0.875rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .download-group-detail-action {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    padding: 0.5rem 0.75rem;
    color: var(--app-text-muted);
    font-size: 0.75rem;
    font-weight: 800;
  }

  .download-group-detail-action:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }

  .download-group-detail-action-danger:not(:disabled) {
    color: var(--status-error);
  }

  .download-group-detail-state.error {
    color: var(--status-error);
  }

  .download-group-detail-state.degraded {
    color: color-mix(in srgb, var(--status-paused) 76%, var(--app-text));
  }
</style>
