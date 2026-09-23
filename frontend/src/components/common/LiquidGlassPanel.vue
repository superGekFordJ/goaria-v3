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
  const staticFilterId = computed(() => getStaticGlassFilterId())
</script>

<template>
  <component
    :is="as"
    :disabled="props.disabled ? true : undefined"
    class="relative isolate [transform:translateZ(0)] [backface-visibility:hidden] transition-all duration-300 overflow-visible group/liquid outline-none focus:outline-none focus-visible:outline-none"
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
      <!-- Layer 1: Central Translucency + Refraction (backdrop-filter → SVG SDF displacement or WebKit blur fallback) -->
      <div
        v-if="active"
        ref="refractionLayer"
        class="absolute top-0 left-0 -z-10 h-full w-full overflow-hidden transition-all duration-300 pointer-events-none"
        :class="[radius, baseColorClass]"
        :style="
          filterId
            ? {
                backdropFilter: `blur(var(--glass-blur)) url(#${filterId})`,
                WebkitBackdropFilter: `blur(var(--glass-blur)) url(#${filterId})`,
              }
            : {
                backdropFilter: `blur(var(--glass-blur))`,
                WebkitBackdropFilter: `blur(var(--glass-blur))`,
              }
        "
      >
        <!-- Interactive Hover Glow -->
        <div
          v-if="isInteractive && (hoverEffect === 'all' || hoverEffect === 'glow')"
          class="absolute inset-0 bg-gradient-to-t from-transparent to-white/20 dark:to-white/10 opacity-0 group-hover/liquid:opacity-100 transition-opacity duration-300 pointer-events-none"
        ></div>
      </div>
      <!-- Non-active interactive: micro-glass hover reveal (zero layer-demotion, anti-flicker) -->
      <div v-else-if="isInteractive" class="glass-hover-surface -z-10" :class="[radius]"></div>

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
      <!-- Balanced: static glass + bevel + shared static refraction (no dynamic SDF, no specular ring) -->
      <div
        v-if="active"
        class="absolute top-0 left-0 -z-10 h-full w-full overflow-hidden transition-all duration-300 pointer-events-none"
        :class="[radius, baseColorClass]"
        :style="
          staticFilterId
            ? {
                backdropFilter: `blur(var(--glass-blur)) url(#${staticFilterId})`,
                WebkitBackdropFilter: `blur(var(--glass-blur)) url(#${staticFilterId})`,
              }
            : {
                backdropFilter: `blur(var(--glass-blur))`,
                WebkitBackdropFilter: `blur(var(--glass-blur))`,
              }
        "
      ></div>
      <div v-else-if="isInteractive" class="glass-hover-surface -z-10" :class="[radius]"></div>
      <div
        v-if="active"
        class="absolute inset-0 z-0 pointer-events-none transition-all duration-300 lg-bevel"
        :class="[radius]"
      ></div>
    </template>
    <template v-else-if="!fallbackClass">
      <!-- Lightweight fallback for reduced mode -->
      <div
        class="absolute top-0 left-0 -z-10 h-full w-full overflow-hidden transition-all duration-200 pointer-events-none"
        :style="{
          backdropFilter: `blur(var(--glass-blur))`,
          WebkitBackdropFilter: `blur(var(--glass-blur))`,
        }"
        :class="[
          radius,
          active
            ? `${baseColorClass} opacity-100 shadow-[inset_0_0_0_1px_var(--glass-border)]`
            : isInteractive
              ? 'glass-hover-surface'
              : 'bg-transparent',
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
