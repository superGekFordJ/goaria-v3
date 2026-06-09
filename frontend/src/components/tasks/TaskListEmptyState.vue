<script setup lang="ts">
  import type { Component } from 'vue'

  defineProps<{
    show: boolean
    config: {
      icon: Component
      title: string
      description: string
      accent: string
    }
  }>()
</script>

<template>
  <Transition name="fade">
    <div
      v-if="show"
      class="absolute inset-0 flex flex-col items-center justify-center p-8"
    >
      <div class="empty-state animate-fade-in-up">
        <!-- Animated Icon Container -->
        <div
          class="w-24 h-24 rounded-[var(--radius-squircle-xl)] flex items-center justify-center mb-6 relative overflow-hidden"
          :style="{
            background: `color-mix(in srgb, ${config.accent} 3%, transparent)`,
          }"
        >
          <!-- Subtle glow effect -->
          <div
            class="absolute inset-0 opacity-30"
            :style="{
              background: `radial-gradient(circle at center, color-mix(in srgb, ${config.accent} 12%, transparent) 0%, transparent 70%)`,
            }"
          ></div>
          <component
            :is="config.icon"
            :size="40"
            :style="{
              color: `color-mix(in srgb, ${config.accent} 38%, transparent)`,
            }"
            class="animate-float"
          />
        </div>

        <h3 class="text-lg font-bold text-[var(--app-text-muted)] mb-2">
          {{ config.title }}
        </h3>
        <p class="text-sm text-[var(--app-text-subtle)]">
          {{ config.description }}
        </p>
      </div>
    </div>
  </Transition>
</template>

<style scoped>
  /* Fade transition for empty state */
  .fade-enter-active,
  .fade-leave-active {
    transition: opacity 0.3s ease;
  }

  .fade-enter-from,
  .fade-leave-to {
    opacity: 0;
  }

  /* Empty state centering fix */
  .empty-state {
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
  }
</style>
