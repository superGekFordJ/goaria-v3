<script setup lang="ts">
  import { ref } from 'vue'
  import { useTaskStore } from '../../stores/task'
  import { Link, Plus, Loader2 } from 'lucide-vue-next'

  const taskStore = useTaskStore()
  const urlInput = ref('')
  const isAdding = ref(false)
  const inputFocused = ref(false)

  const handleAdd = async () => {
    const url = urlInput.value.trim()
    if (!url || isAdding.value) return

    isAdding.value = true
    try {
      const res = await taskStore.addUri(url)
      if (res === 'success') {
        urlInput.value = ''
      } else {
        // Could implement toast notification here
        console.error('Failed to add task:', res)
      }
    } catch (err) {
      console.error('Failed to add task:', err)
    } finally {
      isAdding.value = false
    }
  }

  // Handle paste event for quick add
  const handlePaste = (e: ClipboardEvent) => {
    const text = e.clipboardData?.getData('text')
    if (text && (text.startsWith('http') || text.startsWith('magnet:'))) {
      // Auto-focus happens naturally, user can press Enter
    }
  }
</script>

<template>
  <header class="p-5 pb-2 shrink-0">
    <div
      :class="[
        'flex gap-3 p-2 rounded-[var(--radius-squircle-lg)] transition-all duration-300',
        'bg-black/20 backdrop-blur-md',
        'border border-white/[0.06]',
        inputFocused ? 'input-container-focused' : '',
      ]"
    >
      <!-- Input Container -->
      <div class="flex-1 relative">
        <!-- Icon -->
        <div
          class="absolute left-4 top-1/2 -translate-y-1/2 pointer-events-none transition-colors duration-300"
          :class="inputFocused ? 'text-[#06ffd5]/60' : 'text-white/20'"
        >
          <Link :size="16" />
        </div>

        <!-- Input Field -->
        <input
          v-model="urlInput"
          type="text"
          placeholder="粘贴下载链接 (HTTP / HTTPS / 磁力链接)..."
          class="w-full bg-transparent pl-11 pr-4 py-3 text-sm text-white/90 font-medium focus:outline-none placeholder:text-white/20 placeholder:font-normal select-text"
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
              ? 'bg-gradient-to-r from-transparent via-[#06ffd5]/40 to-transparent'
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
            : 'bg-white/5 text-white/30 border border-white/5',
        ]"
        @click="handleAdd"
      >
        <Loader2 v-if="isAdding" :size="16" class="animate-spin" />
        <Plus v-else :size="16" />
        <span>{{ isAdding ? '解析中...' : '开始下载' }}</span>
      </button>
    </div>

    <!-- Quick Tips (subtle) -->
    <div class="flex items-center gap-4 mt-3 px-2">
      <div class="flex items-center gap-2 text-[10px] text-white/20">
        <kbd class="px-1.5 py-0.5 rounded bg-white/5 border border-white/10 font-mono text-[9px]">
          Enter
        </kbd>
        <span>快速添加</span>
      </div>
      <div class="flex items-center gap-2 text-[10px] text-white/20">
        <kbd class="px-1.5 py-0.5 rounded bg-white/5 border border-white/10 font-mono text-[9px]">
          Ctrl+V
        </kbd>
        <span>粘贴链接</span>
      </div>
    </div>
  </header>
</template>

<style scoped>
  /* Focus container glow effect - uses pseudo element for smooth rounded glow */
  .input-container-focused {
    position: relative;
    border-color: rgba(6, 255, 213, 0.25) !important;
    box-shadow:
      0 0 0 1px rgba(6, 255, 213, 0.15),
      0 0 20px rgba(6, 255, 213, 0.08),
      inset 0 0 20px rgba(6, 255, 213, 0.03);
  }

  /* Input autofill styling override */
  input:-webkit-autofill,
  input:-webkit-autofill:hover,
  input:-webkit-autofill:focus {
    -webkit-text-fill-color: rgba(255, 255, 255, 0.9);
    -webkit-box-shadow: 0 0 0px 1000px transparent inset;
    transition: background-color 5000s ease-in-out 0s;
  }

  /* Keyboard shortcut styling */
  kbd {
    font-family: var(--font-family-mono);
  }
</style>
