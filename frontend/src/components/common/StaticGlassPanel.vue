<script setup lang="ts">
  import { useUIStore } from '../../stores/ui'

  withDefaults(
    defineProps<{
      as?: string
      interactive?: boolean
      radius?: string
      fallbackClass?: string
      baseColorClass?: string
    }>(),
    {
      as: 'div',
      interactive: false,
      radius: 'rounded-full',
      fallbackClass: '',
      baseColorClass: 'bg-white/20 dark:bg-black/20',
    },
  )

  const uiStore = useUIStore()
</script>

<template>
  <component
    :is="as"
    class="relative isolate transition-all duration-300 overflow-visible group"
    :class="[
      interactive ? 'cursor-pointer hover:scale-[1.01] active:scale-[0.99]' : '',
      radius,
      uiStore.effects === 'reduced' ? fallbackClass : '',
    ]"
  >
    <template v-if="uiStore.effects === 'full'">
      <!-- Background layer with blur -->
      <div
        class="absolute inset-0 -z-10 pointer-events-none transition-all duration-300 backdrop-blur-2xl dark:backdrop-blur-xl"
        :class="[radius, baseColorClass]"
      ></div>

      <!-- Soft Glass Edge & Shadow Layer -->
      <div
        class="absolute inset-0 z-0 pointer-events-none transition-all duration-300"
        :class="[
          radius,
          'shadow-[inset_0px_0px_0px_1px_rgba(0,0,0,0.08),inset_0px_1px_0px_rgba(255,255,255,0.8),inset_0px_-1px_1px_rgba(0,0,0,0.05),0px_8px_24px_rgba(0,0,0,0.08)] dark:shadow-[inset_0px_0px_0px_1px_rgba(0,0,0,0.2),inset_0px_1px_0px_rgba(255,255,255,0.15),0px_2px_8px_rgba(0,0,0,0.3)]',
        ]"
      ></div>
    </template>
    <template v-else-if="!fallbackClass">
      <!-- Lightweight fallback -->
      <div
        class="absolute inset-0 -z-10 pointer-events-none transition-all duration-300 backdrop-blur-md bg-white/10 dark:bg-black/10 border border-[var(--glass-border)]"
        :class="[radius]"
      ></div>
    </template>

    <!-- Content Slot Wrapper -->
    <div class="relative z-10 w-full h-full">
      <slot />
    </div>
  </component>
</template>
