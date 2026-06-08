<script setup lang="ts">
  import type { Component } from 'vue'
  import StaticGlassPanel from '../../common/StaticGlassPanel.vue'

  defineProps<{
    title: string
    description?: string
    icon?: Component
    iconClass?: string
  }>()
</script>

<template>
  <StaticGlassPanel
    radius="rounded-[var(--radius-squircle-lg)]"
    class="p-6"
    fallback-class="glass-panel"
  >
    <div class="flex items-center justify-between mb-4">
      <div class="flex items-center gap-3">
        <div
          v-if="icon"
          :class="['w-8 h-8 rounded-xl flex items-center justify-center', iconClass]"
        >
          <component :is="icon" :size="16" />
        </div>
        <div>
          <h3 class="text-sm font-semibold text-[var(--app-text)]/80">{{ title }}</h3>
          <p v-if="description" class="text-[10px] text-[var(--app-text-subtle)]">
            {{ description }}
          </p>
        </div>
      </div>
      <slot name="header-extra"></slot>
    </div>
    <slot></slot>
  </StaticGlassPanel>
</template>
