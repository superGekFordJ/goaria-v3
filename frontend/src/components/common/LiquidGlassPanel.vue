<script setup lang="ts">
  import { computed, ref } from 'vue'
  import { useUIStore } from '../../stores/ui'
  import { useLiquidGlass, getStaticGlassFilterId } from '../../composables/useLiquidGlass'

  const props = withDefaults(
    defineProps<{
      as?: string
      active?: boolean
      interactive?: boolean
      hoverEffect?: 'none' | 'glow' | 'scale' | 'all'
      radius?: string
      fallbackClass?: string
      baseColorClass?: string
      disabled?: boolean
      /** Balanced-tier static refraction. Default true preserves sidebar etc. */
      refraction?: boolean
    }>(),
    {
      as: 'div',
      active: true,
      interactive: false,
      hoverEffect: 'none',
      radius: 'rounded-[var(--radius-squircle-md)]',
      fallbackClass: '',
      baseColorClass: 'bg-[var(--app-liquid-glass-bg)]',
      disabled: false,
      refraction: true,
    },
  )

  const uiStore = useUIStore()

  const isInteractive = computed(() => {
    return props.interactive && !props.disabled
  })

  // Refraction layer element — the composable registers/unregisters the SVG filter
  // automatically when this ref changes (e.g. effects toggle, active state change).
  const refractionLayer = ref<HTMLElement | null>(null)
  const { filterId } = useLiquidGlass(refractionLayer)

  // Shared static refraction filter id for the balanced tier (no dynamic SDF).
  const staticFilterId = computed(() => {
    if (!props.refraction || uiStore.effectsTier === 'reduced') return ''
    return getStaticGlassFilterId()
  })

  const balancedBackdrop = computed(() => {
    if (staticFilterId.value) {
      return {
        backdropFilter: `blur(var(--glass-blur)) url(#${staticFilterId.value})`,
        WebkitBackdropFilter: `blur(var(--glass-blur)) url(#${staticFilterId.value})`,
      }
    }
    return {
      backdropFilter: 'blur(var(--glass-blur))',
      WebkitBackdropFilter: 'blur(var(--glass-blur))',
    }
  })
</script>

<template>
  <component
    :is="as"
    :disabled="props.disabled ? true : undefined"
    class="relative isolate transition-all duration-300 overflow-visible group/liquid"
    :class="[
      isInteractive ? 'cursor-pointer' : '',
      isInteractive &&
      uiStore.effectsTier !== 'reduced' &&
      (hoverEffect === 'all' || hoverEffect === 'scale')
        ? 'hover:scale-[1.02] active:scale-[0.98]'
        : '',
      radius,
      uiStore.effectsTier === 'reduced' ? fallbackClass : '',
    ]"
  >
    <template v-if="uiStore.effectsTier === 'full'">
      <!-- Layer 1: Central Translucency + Refraction (backdrop-filter → SVG SDF displacement) -->
      <div
        v-if="active"
        ref="refractionLayer"
        class="absolute top-0 left-0 -z-10 h-full w-full overflow-hidden transition-all duration-300 pointer-events-none"
        :class="[radius, baseColorClass]"
        :style="
          filterId
            ? { backdropFilter: `blur(var(--glass-blur)) url(#${filterId})`, WebkitBackdropFilter: `blur(var(--glass-blur)) url(#${filterId})` }
            : { backdropFilter: `blur(var(--glass-blur))`, WebkitBackdropFilter: `blur(var(--glass-blur))` }
        "
      >
        <!-- Interactive Hover Glow -->
        <div
          v-if="isInteractive && (hoverEffect === 'all' || hoverEffect === 'glow')"
          class="absolute inset-0 bg-gradient-to-t from-transparent to-white/20 dark:to-white/10 opacity-0 group-hover/liquid:opacity-100 transition-opacity duration-300 pointer-events-none"
        ></div>
      </div>
      <!-- Non-active interactive: transparent placeholder for hover reveal -->
      <div
        v-else-if="isInteractive"
        class="absolute top-0 left-0 -z-10 h-full w-full overflow-hidden transition-all duration-300 pointer-events-none bg-transparent opacity-0 group-hover/liquid:bg-[var(--app-liquid-glass-hover)] group-hover/liquid:opacity-100"
        :class="[radius]"
      ></div>

      <!-- Layer 2: Specular Bevel — inner sheen + inset shadows + outer drop shadow -->
      <div
        v-if="active"
        class="absolute inset-0 z-0 pointer-events-none transition-all duration-300 lg-bevel"
        :class="[radius]"
      ></div>

      <!-- Layer 2b: Hairline Specular Ring — conic-gradient masked border (bright top, dim bottom) -->
      <div
        v-if="active"
        class="absolute inset-0 z-[1] pointer-events-none transition-all duration-300 lg-specular"
        :class="[radius]"
      ></div>
    </template>
    <template v-else-if="uiStore.effectsTier === 'balanced'">
      <!-- Balanced: blur + tint + bevel; optional static refraction (off for floating overlays) -->
      <div
        v-if="active"
        class="absolute top-0 left-0 -z-10 h-full w-full overflow-hidden transition-all duration-300 pointer-events-none"
        :class="[radius, baseColorClass]"
        :style="balancedBackdrop"
      ></div>
      <div
        v-else-if="isInteractive"
        class="absolute top-0 left-0 -z-10 h-full w-full overflow-hidden transition-all duration-300 pointer-events-none bg-transparent opacity-0 group-hover/liquid:bg-[var(--app-liquid-glass-hover)] group-hover/liquid:opacity-100"
        :class="[radius]"
      ></div>
      <div
        v-if="active"
        class="absolute inset-0 z-0 pointer-events-none transition-all duration-300 lg-bevel"
        :class="[radius]"
      ></div>
    </template>
    <template v-else-if="!fallbackClass">
      <!-- Lightweight fallback for reduced mode -->
      <div
        class="absolute top-0 left-0 -z-10 h-full w-full overflow-hidden transition-all duration-300 pointer-events-none"
        :style="{ backdropFilter: `blur(var(--glass-blur))`, WebkitBackdropFilter: `blur(var(--glass-blur))` }"
        :class="[
          radius,
          active
            ? `${baseColorClass} opacity-100 border border-[var(--glass-border)]`
            : isInteractive
              ? 'bg-transparent opacity-0 group-hover:bg-[var(--app-liquid-glass-hover)]'
              : 'bg-transparent opacity-0',
        ]"
      ></div>
    </template>

    <!-- Content Slot Wrapper -->
    <div
      class="relative z-10 w-full h-full flex items-[inherit] justify-[inherit] flex-col-[inherit] flex-row-[inherit]"
    >
      <slot />
    </div>
  </component>
</template>
