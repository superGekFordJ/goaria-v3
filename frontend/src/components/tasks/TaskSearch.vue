<script setup lang="ts">
  import { ref } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Search, X } from 'lucide-vue-next'
  import StaticGlassPanel from '../common/StaticGlassPanel.vue'
  const { t } = useI18n()
  const searchQuery = defineModel<string>({ default: '' })
  const inputFocused = ref(false)

  const clearSearch = () => {
    searchQuery.value = ''
  }
</script>

<template>
  <div class="p-5 pb-2 shrink-0">
    <StaticGlassPanel
      radius="rounded-[var(--radius-squircle-lg)]"
      :class="[
        'transition-all duration-300',
        'border border-[var(--input-border)]',
        inputFocused ? 'search-container-focused' : '',
      ]"
    >
      <div class="flex items-center gap-3 p-2 w-full h-full">
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
            ? 'bg-gradient-to-r from-transparent via-[var(--skin-focus-beam)]/40 to-transparent'
            : 'bg-transparent',
        ]"
      ></div>
      </div>
    </StaticGlassPanel>
  </div>
</template>

<style scoped>
  /* Focus container glow effect */
  .search-container-focused {
    position: relative;
    border-color: color-mix(in srgb, var(--skin-focus-beam) 25%, transparent) !important;
    box-shadow:
      0 0 0 1px color-mix(in srgb, var(--skin-focus-beam) 15%, transparent),
      0 0 20px color-mix(in srgb, var(--skin-focus-beam) 8%, transparent),
      inset 0 0 20px color-mix(in srgb, var(--skin-focus-beam) 3%, transparent);
  }
</style>
