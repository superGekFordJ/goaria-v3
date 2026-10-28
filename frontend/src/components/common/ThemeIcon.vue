<script lang="ts">
  import { ref as sharedRef } from 'vue'

  const sharedResolvedTheme = sharedRef<'light' | 'dark'>('dark')
  let sharedThemeObserver: MutationObserver | null = null
  let sharedObserverSubscribers = 0

  function syncSharedTheme() {
    if (typeof document === 'undefined') return
    const theme = document.documentElement.getAttribute('data-theme')
    sharedResolvedTheme.value = theme === 'light' ? 'light' : 'dark'
  }

  function retainThemeObserver() {
    if (typeof document === 'undefined') return
    if (sharedObserverSubscribers === 0) {
      syncSharedTheme()
      sharedThemeObserver = new MutationObserver(mutations => {
        for (const mutation of mutations) {
          if (mutation.attributeName === 'data-theme') {
            syncSharedTheme()
          }
        }
      })
      sharedThemeObserver.observe(document.documentElement, {
        attributes: true,
        attributeFilter: ['data-theme'],
      })
    }
    sharedObserverSubscribers++
  }

  function releaseThemeObserver() {
    sharedObserverSubscribers = Math.max(0, sharedObserverSubscribers - 1)
    if (sharedObserverSubscribers === 0 && sharedThemeObserver) {
      sharedThemeObserver.disconnect()
      sharedThemeObserver = null
    }
  }
</script>

<script setup lang="ts">
  import { computed, onMounted, onUnmounted } from 'vue'
  import LiquidGlassPanel from './LiquidGlassPanel.vue'
  import darkIcon from '../../assets/icons/dark.svg'
  import lightIcon from '../../assets/icons/light.svg'

  interface Props {
    size?: number
    interactive?: boolean
    radius?: string
  }

  const props = withDefaults(defineProps<Props>(), {
    size: 20,
    interactive: false,
    radius: 'rounded-[var(--radius-squircle-sm)]',
  })

  // Computed for template usage
  const iconSize = computed(() => props.size)
  const resolvedTheme = sharedResolvedTheme

  onMounted(() => {
    retainThemeObserver()
  })

  onUnmounted(() => {
    releaseThemeObserver()
  })
</script>

<template>
  <LiquidGlassPanel
    as="span"
    :interactive="interactive"
    :radius="radius"
    base-color-class="bg-[var(--app-liquid-glass-bg)] dark:bg-[rgba(255,255,255,0.05)]"
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
  </LiquidGlassPanel>
</template>

<style scoped>
  .theme-icon-wrapper {
    position: relative;
    display: inline-block;
    flex-shrink: 0;
    overflow: hidden;
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
    pointer-events: none;
  }

  .theme-icon.is-active {
    opacity: 1;
  }

  [data-effects='reduced'] .theme-icon {
    transition: none !important;
  }
</style>
