<script setup lang="ts">
  import { computed } from 'vue'
  import { useUIStore } from '../../stores/ui'
  import { getStaticGlassFilterId } from '../../composables/useLiquidGlass'

  const props = withDefaults(
    defineProps<{
      as?: string
      interactive?: boolean
      radius?: string
      fallbackClass?: string
      baseColorClass?: string
      disabled?: boolean
      refraction?: boolean
    }>(),
    {
      as: 'div',
      interactive: false,
      radius: 'rounded-full',
      fallbackClass: '',
      baseColorClass: 'bg-[var(--app-static-glass-bg)]',
      disabled: false,
      refraction: false,
    },
  )

  const uiStore = useUIStore()

  const isInteractive = computed(() => {
    return props.interactive && !props.disabled
  })

  const refractionFilter = computed(() => {
    if (!props.refraction || uiStore.effectsTier === 'reduced') return ''
    return getStaticGlassFilterId()
  })
</script>

<template>
  <component
    :is="as"
    :disabled="props.disabled ? true : undefined"
    class="relative isolate [transform:translateZ(0)] [backface-visibility:hidden] transition-all duration-300 overflow-visible group"
    :class="[
      isInteractive ? 'cursor-pointer hover:scale-[1.01] active:scale-[0.99]' : '',
      radius,
      uiStore.effectsTier === 'reduced' ? fallbackClass : '',
    ]"
  >
    <template v-if="uiStore.effectsTier !== 'reduced'">
      <!-- Background layer with blur (+ optional static refraction) -->
      <div
        class="absolute inset-0 -z-10 pointer-events-none transition-all duration-300 static-glass-backdrop"
        :class="[radius, baseColorClass]"
        :style="{
          backdropFilter: refractionFilter
            ? `blur(max(var(--glass-blur), 12px)) url(#${refractionFilter})`
            : 'blur(max(var(--glass-blur), 12px))',
          WebkitBackdropFilter: refractionFilter
            ? `blur(max(var(--glass-blur), 12px)) url(#${refractionFilter})`
            : 'blur(max(var(--glass-blur), 12px))',
        }"
      ></div>

      <!-- Soft Glass Edge & Shadow Layer -->
      <div
        class="absolute inset-0 z-0 pointer-events-none transition-all duration-300 static-glass-edge"
        :class="[radius]"
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
