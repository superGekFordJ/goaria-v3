<script setup lang="ts">
  import { computed, onMounted, onUnmounted, ref } from 'vue'
  import darkIcon from '../../assets/icons/dark.svg'
  import lightIcon from '../../assets/icons/light.svg'

  interface Props {
    size?: number
  }

  const props = withDefaults(defineProps<Props>(), {
    size: 20,
  })

  // Computed for template usage
  const iconSize = computed(() => props.size)

  // Track current resolved theme
  const resolvedTheme = ref<'light' | 'dark'>('dark')

  const updateTheme = () => {
    const theme = document.documentElement.getAttribute('data-theme')
    resolvedTheme.value = theme === 'light' ? 'light' : 'dark'
  }

  // Observe data-theme attribute changes
  let observer: MutationObserver | null = null

  onMounted(() => {
    updateTheme()
    observer = new MutationObserver(mutations => {
      for (const mutation of mutations) {
        if (mutation.attributeName === 'data-theme') {
          updateTheme()
        }
      }
    })
    observer.observe(document.documentElement, { attributes: true })
  })

  onUnmounted(() => {
    observer?.disconnect()
  })
</script>

<template>
  <span
    class="theme-icon-wrapper"
    :style="{ width: `${iconSize}px`, height: `${iconSize}px` }"
    role="img"
    aria-label="GoAria Logo"
  >
    <img
      :src="darkIcon"
      class="theme-icon"
      :class="resolvedTheme === 'dark' ? 'is-active' : ''"
      alt=""
      aria-hidden="true"
    />
    <img
      :src="lightIcon"
      class="theme-icon"
      :class="resolvedTheme === 'light' ? 'is-active' : ''"
      alt=""
      aria-hidden="true"
    />
  </span>
</template>

<style scoped>
  .theme-icon-wrapper {
    position: relative;
    display: inline-block;
    flex-shrink: 0;
    border-radius: var(--radius-squircle-sm);
    box-shadow:
      0 0 0 1px color-mix(in srgb, var(--glass-border) 70%, transparent),
      0 10px 30px rgba(0, 0, 0, 0.18);
  }

  .theme-icon {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    border-radius: inherit;
    opacity: 0;
    transition: opacity 180ms ease;
    filter: drop-shadow(0 6px 16px rgba(0, 0, 0, 0.18));
  }

  .theme-icon.is-active {
    opacity: 1;
  }

  :global(:root[data-effects='reduced']) .theme-icon {
    transition: none !important;
  }
</style>
