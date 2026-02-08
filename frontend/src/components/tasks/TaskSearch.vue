<script setup lang="ts">
  import { ref } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Search, X } from 'lucide-vue-next'

  const { t } = useI18n()
  const searchQuery = defineModel<string>({ default: '' })
  const inputFocused = ref(false)

  const clearSearch = () => {
    searchQuery.value = ''
  }
</script>

<template>
  <div class="p-5 pb-2 shrink-0">
    <div
      :class="[
        'flex items-center gap-3 p-2 rounded-[var(--radius-squircle-lg)] transition-all duration-300',
        'bg-[var(--input-bg)] backdrop-blur-md',
        'border border-[var(--input-border)]',
        inputFocused ? 'search-container-focused' : '',
      ]"
    >
      <!-- Search Icon -->
      <div
        class="pl-3 transition-colors duration-300"
        :class="inputFocused ? 'text-[var(--neon-primary)]/60' : 'text-[var(--app-text-subtle)]'"
      >
        <Search :size="16" />
      </div>

      <!-- Input Field -->
      <input
        v-model="searchQuery"
        type="text"
        :placeholder="t('taskSearch.placeholder')"
        :aria-label="t('taskSearch.placeholder')"
        class="flex-1 bg-transparent py-3 text-sm text-[var(--app-text)] font-medium focus:outline-none placeholder:text-[var(--input-placeholder)] placeholder:font-normal select-text"
        @focus="inputFocused = true"
        @blur="inputFocused = false"
      />

      <!-- Clear Button -->
      <button
        v-if="searchQuery"
        class="p-2 mr-1 rounded-lg text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:bg-[var(--btn-glass-bg)] transition-all duration-200"
        :aria-label="t('taskSearch.clear')"
        @click="clearSearch"
      >
        <X :size="14" />
      </button>

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
  </div>
</template>

<style scoped>
  /* Focus container glow effect */
  .search-container-focused {
    position: relative;
    border-color: color-mix(in srgb, var(--neon-primary) 25%, transparent) !important;
    box-shadow:
      0 0 0 1px color-mix(in srgb, var(--neon-primary) 15%, transparent),
      0 0 20px color-mix(in srgb, var(--neon-primary) 8%, transparent),
      inset 0 0 20px color-mix(in srgb, var(--neon-primary) 3%, transparent);
  }
</style>
