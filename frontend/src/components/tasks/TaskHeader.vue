<script setup lang="ts">
  import { ref, watch } from 'vue'
  import { useTaskStore } from '../../stores/task'
  import { useUIStore } from '../../stores/ui'
  import { Link, Plus, Loader2 } from 'lucide-vue-next'

  const taskStore = useTaskStore()
  const uiStore = useUIStore()
  const urlInput = ref('')
  const urlInputEl = ref<HTMLInputElement | null>(null)
  const isAdding = ref(false)
  const inputFocused = ref(false)
  const errorMessage = ref('')
  const clipboardHint = ref('')
  
  watch(
    () => uiStore.pendingPasteUri,
    (uri) => {
      const trimmed = (uri || '').trim()
      if (!trimmed) return

      if (!urlInput.value.trim()) {
        urlInput.value = trimmed
        clipboardHint.value = '已从剪贴板填入链接'
        setTimeout(() => {
          urlInputEl.value?.focus()
        }, 0)
      } else {
        clipboardHint.value = '剪贴板中有新链接'
      }
      setTimeout(() => {
        clipboardHint.value = ''
      }, 3000)
      uiStore.consumePendingPasteUri()
    },
    { immediate: true },
  )

  const handleAdd = async () => {
    const url = urlInput.value.trim()
    if (!url || isAdding.value) return

    errorMessage.value = ''
    isAdding.value = true
    try {
      const res = await taskStore.addUri(url)
      if (res === 'success') {
        urlInput.value = ''
      } else if (res === 'duplicate') {
        errorMessage.value = '存在重复任务，请检查下载列表或已完成的任务'
        setTimeout(() => { errorMessage.value = '' }, 3000)
      } else {
        errorMessage.value = `添加失败: ${res}`
        setTimeout(() => { errorMessage.value = '' }, 3000)
      }
    } catch (err) {
      errorMessage.value = '添加失败，请重试'
      setTimeout(() => { errorMessage.value = '' }, 3000)
    } finally {
      isAdding.value = false
    }
  }

  // Handle paste event for quick add
  const handlePaste = (e: ClipboardEvent) => {
    const text = e.clipboardData?.getData('text')
    if (text && (text.startsWith('http') || text.startsWith('https'))) {
      // Auto-focus happens naturally, user can press Enter
    }
  }
</script>

<template>
  <header class="p-5 pb-2 shrink-0">
    <div
      :class="[
        'flex gap-3 p-2 rounded-[var(--radius-squircle-lg)] transition-all duration-300',
        'bg-[var(--input-bg)] backdrop-blur-md',
        'border border-[var(--input-border)]',
        inputFocused ? 'input-container-focused' : '',
      ]"
    >
      <!-- Input Container -->
      <div class="flex-1 relative">
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
          placeholder="粘贴下载链接 (HTTP / HTTPS / FTP / SFTP)..."
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
              ? 'bg-gradient-to-r from-transparent via-[var(--neon-primary)]/40 to-transparent'
              : 'bg-transparent',
          ]"
        ></div>
      </div>

      <!-- Add Button -->
      <button
        :disabled="!urlInput.trim() || isAdding"
        :class="[
          'px-6 py-3 rounded-[var(--radius-squircle-md)] font-bold text-sm transition-all duration-300 flex items-center gap-2',
          'disabled:opacity-30 disabled:cursor-not-allowed disabled:transform-none',
          urlInput.trim() && !isAdding
            ? 'btn-neon'
            : 'bg-[var(--btn-glass-bg)] text-[var(--app-text-subtle)] border border-[var(--glass-border)]',
        ]"
        @click="handleAdd"
      >
        <Loader2 v-if="isAdding" :size="16" class="animate-spin" />
        <Plus v-else :size="16" />
        <span>{{ isAdding ? '解析中...' : '开始下载' }}</span>
      </button>
    </div>

    <!-- Error Message / Clipboard Hint / Quick Tips -->
    <div class="flex items-center gap-4 mt-3 px-2 min-h-[20px]">
      <Transition name="fade" mode="out-in">
        <div v-if="errorMessage" key="error" class="flex items-center gap-2 text-[11px] text-red-400 font-medium">
          <span>{{ errorMessage }}</span>
        </div>
        <div v-else-if="clipboardHint" key="hint" class="flex items-center gap-2 text-[11px] text-[var(--status-active)] font-medium">
          <span>{{ clipboardHint }}</span>
        </div>
        <div v-else key="tips" class="flex items-center gap-4">
          <div class="flex items-center gap-2 text-[10px] text-[var(--kbd-text)]">
            <kbd class="px-1.5 py-0.5 rounded bg-[var(--kbd-bg)] border border-[var(--kbd-border)] font-mono text-[9px]">
              Enter
            </kbd>
            <span>快速添加</span>
          </div>
          <div class="flex items-center gap-2 text-[10px] text-[var(--kbd-text)]">
            <kbd class="px-1.5 py-0.5 rounded bg-[var(--kbd-bg)] border border-[var(--kbd-border)] font-mono text-[9px]">
              Ctrl+V
            </kbd>
            <span>粘贴链接</span>
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
    border-color: color-mix(in srgb, var(--neon-primary) 25%, transparent) !important;
    box-shadow:
      0 0 0 1px color-mix(in srgb, var(--neon-primary) 15%, transparent),
      0 0 20px color-mix(in srgb, var(--neon-primary) 8%, transparent),
      inset 0 0 20px color-mix(in srgb, var(--neon-primary) 3%, transparent);
  }

  /* Input autofill styling override */
  input:-webkit-autofill,
  input:-webkit-autofill:hover,
  input:-webkit-autofill:focus {
    -webkit-text-fill-color: var(--app-text);
    -webkit-box-shadow: 0 0 0px 1000px transparent inset;
    transition: background-color 5000s ease-in-out 0s;
  }

  /* Keyboard shortcut styling */
  kbd {
    font-family: var(--font-family-mono);
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
</style>
