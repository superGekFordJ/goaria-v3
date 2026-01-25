<script setup lang="ts">
  import { FolderOpen } from 'lucide-vue-next'
  import SectionCard from './SectionCard.vue'

  defineProps<{
    modelValue: string
  }>()

  defineEmits<{
    (e: 'pick'): void
  }>()
</script>

<template>
  <SectionCard
    title="下载目录"
    description="文件默认保存位置"
    :icon="FolderOpen"
    icon-class="bg-[var(--neon-primary)]/10 text-[var(--neon-primary)]"
  >
    <div
      class="flex gap-3 p-1.5 bg-[var(--input-bg)] rounded-[var(--radius-squircle-md)] border border-[var(--input-border)]"
    >
      <input
        :value="modelValue"
        readonly
        class="flex-1 bg-transparent px-4 py-2.5 text-sm font-mono-data text-[var(--app-text)]/70 outline-none cursor-default truncate"
        :title="modelValue"
      />
      <button
        class="btn-neon px-5 py-2 rounded-xl text-xs font-bold shrink-0"
        @click="$emit('pick')"
      >
        浏览
      </button>
    </div>
  </SectionCard>
</template>
