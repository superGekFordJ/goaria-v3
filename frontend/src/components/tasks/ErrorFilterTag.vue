<script setup lang="ts">
  import { useI18n } from 'vue-i18n'
  import LiquidGlassPanel from '../common/LiquidGlassPanel.vue'

  const { t } = useI18n()

  defineProps<{
    errorCount: number
    active: boolean
  }>()

  const emit = defineEmits<{
    (e: 'toggle'): void
  }>()
</script>

<template>
  <div class="error-filter-tag-wrapper">
    <LiquidGlassPanel
      as="button"
      :interactive="true"
      hover-effect="scale"
      radius="rounded-[var(--radius-squircle-md)]"
      base-color-class="bg-white/10 dark:bg-white/5"
      class="error-filter-tag"
      :class="{ 'error-filter-tag-active': active, 'error-filter-tag-inactive': !active }"
      :aria-label="t('errorFilter.tagAriaLabel', { count: errorCount })"
      :aria-pressed="active"
      @click="emit('toggle')"
    >
      <span class="flex items-center gap-2 px-3 py-1.5 w-full h-full">
        <!-- Error dot (Light is Status) -->
        <span class="error-status-dot" :class="{ 'error-status-dot-active': active }"></span>

        <!-- Label + count -->
        <span
          class="text-xs font-medium transition-colors duration-200"
          :class="
            active
              ? 'text-[var(--status-error)]'
              : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
          "
        >
          {{ t('errorFilter.tagLabel') }}
        </span>
        <span class="font-mono-data text-[var(--status-error)]" aria-hidden="true">{{
          errorCount
        }}</span>
      </span>
    </LiquidGlassPanel>
  </div>
</template>

<style scoped>
  .error-filter-tag-wrapper {
    pointer-events: auto;
  }

  /* Inactive (quiet) state — low-key, just a hint with red dot */
  .error-filter-tag-inactive {
    border: 1px solid color-mix(in srgb, var(--glass-border) 80%, transparent);
    box-shadow: none;
    transition: all 0.3s ease;
  }

  .error-filter-tag-inactive:hover {
    border-color: color-mix(in srgb, var(--glow-error) 20%, transparent);
  }

  /* Active (lit) state
     Dark mode: full error glow (Obsidian & Laser — glow is identity)
     Light mode: flat ceramic — no shadow, just tinted bg + thin border */
  .error-filter-tag-active {
    border: 1px solid color-mix(in srgb, var(--glow-error) 35%, transparent);
    background-color: color-mix(in srgb, var(--status-error) 8%, transparent);
    transition: all 0.3s ease;
  }

  :global([data-theme='dark']) .error-filter-tag-active,
  :global(html:not([data-theme='light'])) .error-filter-tag-active {
    background-color: transparent;
    box-shadow:
      0 0 16px color-mix(in srgb, var(--glow-error) 12%, transparent),
      0 0 32px color-mix(in srgb, var(--glow-error) 6%, transparent),
      0 4px 16px color-mix(in srgb, var(--app-bg) 12%, transparent);
  }

  /* Error status dot — 绯红色实心点 per visual_system.md "Light is Status" */
  .error-status-dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--status-error);
    box-shadow: 0 0 6px color-mix(in srgb, var(--status-error) 30%, transparent);
    flex-shrink: 0;
    transition: all 0.3s ease;
  }

  .error-status-dot-active {
    box-shadow: 0 0 10px color-mix(in srgb, var(--status-error) 60%, transparent);
  }

  /* Light mode: reduce dot glow to stay flat/ceramic */
  :global([data-theme='light']) .error-status-dot {
    box-shadow: none;
  }

  :global([data-theme='light']) .error-status-dot-active {
    box-shadow: none;
  }

  /* reduced effects: disable glow, keep static border */
  :global([data-effects='reduced']) .error-filter-tag-active {
    box-shadow: none;
  }

  :global([data-effects='reduced']) .error-status-dot,
  :global([data-effects='reduced']) .error-status-dot-active {
    box-shadow: none;
  }
</style>
