<script setup lang="ts">
  import { ref, watch, nextTick, computed } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { useTaskStore } from '../../stores/task'
  import { useUIStore } from '../../stores/ui'
  import { useDownloadGroupStore } from '../../stores/downloadGroups'
  import { isValidUrl, isDuplicateUri } from '../../utils/url'
  import { isInsufficientDiskSpaceMessage } from '../../utils/diskSpaceError'
  import { buildBatchGroupResultHints, type TaskGroupHint } from '../../stores/task/grouping'
  import { Link, Plus, Loader2, ChevronUp, Layers3 } from '@lucide/vue'
  import LiquidGlassPanel from '../common/LiquidGlassPanel.vue'

  const { t } = useI18n()
  const taskStore = useTaskStore()
  const uiStore = useUIStore()
  const downloadGroupStore = useDownloadGroupStore()

  // Single-line state
  const urlInput = ref('')
  const urlInputEl = ref<HTMLInputElement | null>(null)
  const isAdding = ref(false)
  const inputFocused = ref(false)
  const errorMessage = ref('')
  const clipboardHint = ref('')

  // Multi-line state
  const isMultiline = ref(false)
  const textareaValue = ref('')
  const textareaEl = ref<HTMLTextAreaElement | null>(null)
  const submitting = ref(false)
  const parsedStats = ref({
    valid: 0,
    duplicate: 0,
    invalid: 0,
    uniqueAddable: 0,
    urls: [] as string[],
  })
  const batchResult = ref<{
    succeeded: number
    duplicates: number
    errors: number
    diskErrors: number
    groupHints: TaskGroupHint[]
  } | null>(null)
  const BATCH_AUTO_FOLDER_THRESHOLD = 5

  // Debounced textarea parsing
  let parseTimer: ReturnType<typeof setTimeout> | null = null

  function parseTextarea(text: string) {
    const lines = text
      .split('\n')
      .map(l => l.trim())
      .filter(Boolean)
    let valid = 0
    let duplicate = 0
    let invalid = 0
    const validUrls: string[] = []
    const uniqueAddableUrls = new Set<string>()

    for (const line of lines) {
      if (isValidUrl(line)) {
        if (isDuplicateUri(line, taskStore)) {
          duplicate++
        } else {
          valid++
          uniqueAddableUrls.add(line)
        }
        // Push ALL valid URLs to array! Backend BatchAddUri is the authority for deduplication.
        validUrls.push(line)
      } else {
        invalid++
      }
    }

    parsedStats.value = {
      valid,
      duplicate,
      invalid,
      uniqueAddable: uniqueAddableUrls.size,
      urls: validUrls,
    }
  }

  watch(textareaValue, val => {
    if (parseTimer) clearTimeout(parseTimer)
    parseTimer = setTimeout(() => parseTextarea(val), 300)
  })

  // Computed textarea rows
  const textareaRows = computed(() => {
    const lineCount = textareaValue.value.split('\n').length
    return Math.min(Math.max(lineCount, 2), 6)
  })

  // Computed button label
  const buttonLabel = computed(() => {
    if (submitting.value) return t('taskHeader.parsing')
    if (isAdding.value) return t('taskHeader.parsing')
    if (isMultiline.value && parsedStats.value.valid > 0) {
      return t('taskHeader.addAll', { count: parsedStats.value.valid })
    }
    return t('taskHeader.startDownload')
  })

  // Active neon state: valid input and not mid-submit (mirrors :disabled inverse)
  const canSubmit = computed(() =>
    isMultiline.value
      ? parsedStats.value.valid > 0 && !submitting.value
      : Boolean(urlInput.value.trim()) && !isAdding.value,
  )

  const preSubmitGroupHint = computed(() => {
    if (!isMultiline.value || parsedStats.value.uniqueAddable < BATCH_AUTO_FOLDER_THRESHOLD)
      return null
    return {
      count: parsedStats.value.uniqueAddable,
      label: t('taskHeader.autoFolderHintWithCount', { count: parsedStats.value.uniqueAddable }),
    }
  })

  const primaryBatchGroupHint = computed(() => batchResult.value?.groupHints[0] ?? null)

  // Watch pendingPasteUri (single URL — existing behavior)
  watch(
    () => uiStore.pendingPasteUri,
    uri => {
      const trimmed = (uri || '').trim()
      if (!trimmed) return

      if (!urlInput.value.trim()) {
        urlInput.value = trimmed
        clipboardHint.value = t('taskHeader.clipboardFilled')
        setTimeout(() => {
          urlInputEl.value?.focus()
        }, 0)
      } else {
        clipboardHint.value = t('taskHeader.clipboardNew')
      }
      setTimeout(() => {
        clipboardHint.value = ''
      }, 3000)
      uiStore.consumePendingPasteUri()
    },
    { immediate: true },
  )

  // Watch pendingPasteUris (multi-URL — new batch behavior)
  watch(
    () => uiStore.pendingPasteUris,
    uris => {
      if (!uris.length) return
      isMultiline.value = true
      textareaValue.value = uris.join('\n')
      nextTick(() => textareaEl.value?.focus())
      uiStore.consumePendingPasteUris()
    },
    { immediate: true },
  )

  // Single-line add
  const handleAdd = async () => {
    const url = urlInput.value.trim()
    if (!url || isAdding.value) return

    errorMessage.value = ''
    isAdding.value = true
    try {
      const res = await taskStore.addUri(url)
      if (res === 'success') {
        urlInput.value = ''
        void downloadGroupStore.fetchGroups()
      } else if (res === 'duplicate') {
        errorMessage.value = t('taskHeader.duplicateTask')
        setTimeout(() => {
          errorMessage.value = ''
        }, 3000)
      } else if (isInsufficientDiskSpaceMessage(res)) {
        errorMessage.value = t('taskHeader.insufficientDiskSpace')
        setTimeout(() => {
          errorMessage.value = ''
        }, 3000)
      } else {
        errorMessage.value = `${t('taskHeader.addFailed')}: ${res}`
        setTimeout(() => {
          errorMessage.value = ''
        }, 3000)
      }
    } catch {
      errorMessage.value = t('taskHeader.addFailedRetry')
      setTimeout(() => {
        errorMessage.value = ''
      }, 3000)
    } finally {
      isAdding.value = false
    }
  }

  // Batch add
  const handleBatchAdd = async () => {
    const urls = parsedStats.value.urls
    if (!urls.length || submitting.value) return

    submitting.value = true
    try {
      const res = await taskStore.batchAddUri(urls)
      const succeeded = res.succeeded?.length || 0
      const duplicates = res.duplicates?.length || 0
      const errorMap = res.errors || {}
      const errorKeys = Object.keys(errorMap)
      const errors = errorKeys.length
      const diskErrors = errorKeys.filter(key =>
        isInsufficientDiskSpaceMessage(errorMap[key]),
      ).length

      const groupHints = buildBatchGroupResultHints(res.groups)
      // Redundant placeholder registration is now handled inside taskStore.batchAddUri before fetchTasks
      // downloadGroupStore.addPlaceholdersFromDownloadGroups(res.groups, 'batch-add')
      void downloadGroupStore.fetchGroups()

      batchResult.value = { succeeded, duplicates, errors, diskErrors, groupHints }

      if (errors > 0) {
        // Keep only failed URLs in textarea
        textareaValue.value = errorKeys.join('\n')
      } else {
        // All succeeded or duplicated — collapse
        textareaValue.value = ''
        isMultiline.value = false
      }

      setTimeout(() => {
        batchResult.value = null
      }, 3000)
    } catch {
      errorMessage.value = t('taskHeader.addFailedRetry')
      setTimeout(() => {
        errorMessage.value = ''
      }, 3000)
    } finally {
      submitting.value = false
    }
  }

  // Paste handler — detect multi-line and switch mode
  const handlePaste = (e: ClipboardEvent) => {
    const text = e.clipboardData?.getData('text') || ''
    if (text.includes('\n')) {
      e.preventDefault()
      isMultiline.value = true
      textareaValue.value = text
      nextTick(() => textareaEl.value?.focus())
    }
  }

  // Collapse back to single-line
  const collapseToSingleLine = () => {
    isMultiline.value = false
    textareaValue.value = ''
    parsedStats.value = { valid: 0, duplicate: 0, invalid: 0, uniqueAddable: 0, urls: [] }
  }
</script>

<template>
  <header class="p-5 pb-2 shrink-0">
    <div
      :class="[
        'flex gap-3 p-2 rounded-[var(--radius-squircle-lg)] transition-all duration-300',
        'bg-[var(--input-bg)]',
        'border border-[var(--input-border)]',
        inputFocused ? 'input-container-focused' : '',
      ]"
    >
      <!-- Single-line mode: input (existing design) -->
      <div v-if="!isMultiline" class="flex-1 relative">
        <!-- Icon -->
        <div
          class="absolute left-4 top-1/2 -translate-y-1/2 pointer-events-none transition-colors duration-300"
          :class="inputFocused ? 'text-[var(--neon-primary)]/60' : 'text-[var(--app-text-subtle)]'"
        >
          <Link :size="16" />
        </div>

        <!-- Input Field -->
        <input
          ref="urlInputEl"
          v-model="urlInput"
          type="text"
          :placeholder="t('taskHeader.placeholder')"
          class="w-full bg-transparent pl-11 pr-4 py-3 text-sm text-[var(--app-text)] font-medium focus:outline-none placeholder:text-[var(--input-placeholder)] placeholder:font-normal select-text"
          @focus="inputFocused = true"
          @blur="inputFocused = false"
          @keyup.enter="handleAdd"
          @paste="handlePaste"
        />

        <!-- Subtle glow line at bottom when focused -->
        <div
          :class="[
            'absolute bottom-0 left-4 right-4 h-px transition-all duration-500',
            inputFocused
              ? 'bg-gradient-to-r from-transparent via-[var(--skin-focus-beam)]/40 to-transparent'
              : 'bg-transparent',
          ]"
        ></div>
      </div>

      <!-- Multi-line mode: textarea -->
      <div v-else class="flex-1 relative">
        <!-- Icon -->
        <div
          class="absolute left-4 top-4 pointer-events-none transition-colors duration-300"
          :class="inputFocused ? 'text-[var(--neon-primary)]/60' : 'text-[var(--app-text-subtle)]'"
        >
          <Link :size="16" />
        </div>

        <textarea
          ref="textareaEl"
          v-model="textareaValue"
          :placeholder="t('taskHeader.multilinePlaceholder')"
          :rows="textareaRows"
          class="w-full bg-transparent pl-11 pr-10 py-3 text-sm text-[var(--app-text)] font-medium focus:outline-none placeholder:text-[var(--input-placeholder)] placeholder:font-normal select-text resize-none overflow-auto"
          @focus="inputFocused = true"
          @blur="inputFocused = false"
          @keydown.ctrl.enter.prevent="handleBatchAdd"
          @keydown.escape="collapseToSingleLine"
          @paste="handlePaste"
        />

        <!-- Collapse button -->
        <button
          class="absolute top-2 right-2 p-1 rounded text-[var(--app-text-subtle)] hover:text-[var(--app-text)] transition-colors duration-200"
          @click="collapseToSingleLine"
        >
          <ChevronUp :size="14" />
        </button>
      </div>

      <!-- Add Button -->
      <LiquidGlassPanel
        as="button"
        :interactive="true"
        hover-effect="all"
        :disabled="!canSubmit"
        :base-color-class="canSubmit ? 'bg-neon-glass' : 'bg-[var(--btn-glass-bg)]'"
        :fallback-class="canSubmit ? 'btn-neon' : 'btn-glass'"
        :class="[
          'task-header-add-btn px-6 h-11 box-border rounded-[var(--radius-squircle-md)] font-bold text-sm',
          'flex items-center justify-center self-start shrink-0',
          'disabled:opacity-30 disabled:cursor-not-allowed disabled:transform-none',
          'before:pointer-events-none before:absolute before:inset-0 before:z-[2] before:rounded-[inherit]',
          'before:ring-1 before:ring-inset before:ring-[var(--glass-border)] before:transition-opacity before:duration-300',
          canSubmit
            ? 'btn-neon-glass before:opacity-0'
            : 'text-[var(--app-text-subtle)] before:opacity-100',
        ]"
        @click="isMultiline ? handleBatchAdd() : handleAdd()"
      >
        <span class="flex items-center justify-center gap-2 w-full h-full">
          <Loader2 v-if="isAdding || submitting" :size="16" class="animate-spin" />
          <Plus v-else :size="16" />
          <span>{{ buttonLabel }}</span>
        </span>
      </LiquidGlassPanel>
    </div>

    <!-- Stats / Error / Clipboard Hint / Quick Tips -->
    <div class="flex items-center gap-4 mt-3 px-2 min-h-[20px]">
      <Transition name="fade" mode="out-in">
        <!-- Batch result feedback -->
        <div
          v-if="batchResult"
          key="result"
          class="flex items-center gap-3 text-[11px] font-medium"
        >
          <span class="text-[var(--status-complete)]">
            {{ t('taskHeader.batchSucceeded', { count: batchResult.succeeded }) }}
          </span>
          <span v-if="batchResult.duplicates > 0" class="text-amber-400 dark:text-amber-400">
            {{ t('taskHeader.batchDuplicates', { count: batchResult.duplicates }) }}
          </span>
          <span v-if="batchResult.diskErrors > 0" class="text-[var(--status-error)]">
            {{ t('taskHeader.insufficientDiskSpaceBatch', { count: batchResult.diskErrors }) }}
          </span>
          <span v-else-if="batchResult.errors > 0" class="text-[var(--status-error)]">
            {{ t('taskHeader.batchErrors', { count: batchResult.errors }) }}
          </span>
          <span
            v-if="batchResult.errors > batchResult.diskErrors && batchResult.diskErrors > 0"
            class="text-[var(--status-error)]"
          >
            {{
              t('taskHeader.batchErrors', {
                count: batchResult.errors - batchResult.diskErrors,
              })
            }}
          </span>
          <span
            v-if="primaryBatchGroupHint"
            class="task-header-group-hint"
            :title="
              primaryBatchGroupHint.folderPath || primaryBatchGroupHint.folderLabel || undefined
            "
          >
            <Layers3 :size="12" />
            <span>
              {{
                t('taskHeader.batchGroupCreated', {
                  count:
                    primaryBatchGroupHint.totalCount ||
                    primaryBatchGroupHint.visibleCount ||
                    batchResult.succeeded,
                })
              }}
            </span>
            <span v-if="primaryBatchGroupHint.folderLabel" class="task-header-group-folder">
              {{ t('taskHeader.batchGroupFolder', { folder: primaryBatchGroupHint.folderLabel }) }}
            </span>
          </span>
        </div>

        <!-- Multi-line mode real-time stats -->
        <div
          v-else-if="isMultiline"
          key="stats"
          class="flex items-center gap-3 text-[11px] font-medium w-full"
        >
          <span class="text-[var(--status-complete)]">
            {{ t('taskHeader.validLinks', { count: parsedStats.valid }) }}
          </span>
          <span v-if="parsedStats.duplicate > 0" class="text-amber-400 dark:text-amber-400">
            {{ t('taskHeader.duplicateLinks', { count: parsedStats.duplicate }) }}
          </span>
          <span v-if="parsedStats.invalid > 0" class="text-[var(--app-text-subtle)]">
            {{ t('taskHeader.invalidLinks', { count: parsedStats.invalid }) }}
          </span>
          <span v-if="preSubmitGroupHint" class="task-header-group-hint">
            <Layers3 :size="12" />
            <span>{{ preSubmitGroupHint.label }}</span>
          </span>
          <div class="flex items-center gap-2 text-[10px] text-[var(--kbd-text)] ml-auto">
            <kbd
              class="px-1.5 py-0.5 rounded bg-[var(--kbd-bg)] border border-[var(--kbd-border)] font-mono text-[9px]"
            >
              Ctrl+Enter
            </kbd>
            <span>{{ t('taskHeader.submitAll') }}</span>
            <kbd
              class="px-1.5 py-0.5 rounded bg-[var(--kbd-bg)] border border-[var(--kbd-border)] font-mono text-[9px]"
            >
              Esc
            </kbd>
            <span>{{ t('taskHeader.collapse') }}</span>
          </div>
        </div>

        <!-- Error message -->
        <div
          v-else-if="errorMessage"
          key="error"
          class="flex items-center gap-2 text-[11px] text-red-400 font-medium"
        >
          <span>{{ errorMessage }}</span>
        </div>

        <!-- Clipboard hint -->
        <div
          v-else-if="clipboardHint"
          key="hint"
          class="flex items-center gap-2 text-[11px] text-[var(--status-complete)] font-medium"
        >
          <span>{{ clipboardHint }}</span>
        </div>

        <!-- Default tips -->
        <div v-else key="tips" class="flex items-center gap-4">
          <div class="flex items-center gap-2 text-[10px] text-[var(--kbd-text)]">
            <kbd
              class="px-1.5 py-0.5 rounded bg-[var(--kbd-bg)] border border-[var(--kbd-border)] font-mono text-[9px]"
            >
              Enter
            </kbd>
            <span>{{ t('taskHeader.quickAdd') }}</span>
          </div>
          <div class="flex items-center gap-2 text-[10px] text-[var(--kbd-text)]">
            <kbd
              class="px-1.5 py-0.5 rounded bg-[var(--kbd-bg)] border border-[var(--kbd-border)] font-mono text-[9px]"
            >
              Ctrl+V
            </kbd>
            <span>{{ t('taskHeader.pasteLink') }}</span>
          </div>
        </div>
      </Transition>
    </div>
  </header>
</template>

<style scoped>
  /* Focus container glow effect - uses pseudo element for smooth rounded glow */
  .input-container-focused {
    position: relative;
    border-color: color-mix(in srgb, var(--skin-focus-beam) 25%, transparent) !important;
    box-shadow:
      0 0 0 1px color-mix(in srgb, var(--skin-focus-beam) 15%, transparent),
      0 0 20px color-mix(in srgb, var(--skin-focus-beam) 8%, transparent),
      inset 0 0 20px color-mix(in srgb, var(--skin-focus-beam) 3%, transparent);
  }

  /* Input autofill styling override */
  input:-webkit-autofill,
  input:-webkit-autofill:hover,
  input:-webkit-autofill:focus {
    -webkit-text-fill-color: var(--app-text);
    -webkit-box-shadow: 0 0 0px 1000px transparent inset;
    transition: background-color 5000s ease-in-out 0s;
  }

  /* Prevent box-shadow transition to avoid visual bloat during active/inactive switch */
  .task-header-add-btn {
    transition-property:
      color, background-color, border-color, text-decoration-color, fill, stroke, opacity,
      transform, filter, backdrop-filter;
    transition-timing-function: cubic-bezier(0.4, 0, 0.2, 1);
    transition-duration: 300ms;
  }

  /* Keyboard shortcut styling */
  kbd {
    font-family: var(--font-family-mono);
  }

  .task-header-group-hint {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    max-width: min(28rem, 52vw);
    padding: 0.125rem 0.5rem;
    border: 1px solid color-mix(in srgb, var(--neon-primary) 18%, transparent);
    border-radius: var(--radius-squircle-sm);
    background: color-mix(in srgb, var(--neon-primary) 7%, transparent);
    color: color-mix(in srgb, var(--neon-primary) 74%, var(--app-text));
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--glass-highlight) 16%, transparent);
  }

  .task-header-group-folder {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--app-text-muted);
  }

  /* Fade transition for error message */
  .fade-enter-active,
  .fade-leave-active {
    transition: opacity 0.2s ease;
  }
  .fade-enter-from,
  .fade-leave-to {
    opacity: 0;
  }

  /*
   * Override LiquidGlassPanel's transition-all so neon box-shadow / ring does not
   * interpolate into a visual width/height pulse when canSubmit toggles.
   */
  .task-header-add-btn {
    transition-property: color, transform, opacity;
    transition-duration: 300ms;
  }
</style>
