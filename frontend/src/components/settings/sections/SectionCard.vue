<script setup lang="ts">
  import type { Component } from 'vue'

  defineProps<{
    title: string
    description?: string
    icon?: Component
    iconClass?: string
  }>()
</script>

<template>
  <div class="glass-panel rounded-[var(--radius-squircle-lg)] p-6">
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
  </div>
</template>
