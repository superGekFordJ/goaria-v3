<script setup lang="ts">
  import { Layers, History } from 'lucide-vue-next'
  import SectionCard from './SectionCard.vue'

  const props = defineProps<{
    transparency: string
    showHistory: boolean
  }>()

  const emit = defineEmits<{
    (e: 'update:transparency', value: string): void
    (e: 'update:showHistory', value: boolean): void
    (e: 'change'): void
  }>()

  const updateTransparency = (value: string) => {
    emit('update:transparency', value)
    emit('change')
  }

  const toggleHistory = () => {
    emit('update:showHistory', !props.showHistory)
    emit('change')
  }
</script>

<template>
  <div class="space-y-4">
    <!-- Window Transparency Card -->
    <SectionCard
      title="窗口透明效果"
      description="仅 Windows 11 支持，更改后需重启应用"
      :icon="Layers"
      icon-class="bg-cyan-500/10 text-cyan-400"
    >
      <div class="grid grid-cols-2 gap-3">
        <button
          v-for="opt in [
            { value: 'none', label: '关闭', desc: '标准窗口' },
            { value: 'acrylic', label: '亚克力', desc: 'Acrylic 模糊' },
            { value: 'mica', label: '云母', desc: 'Mica 材质' },
            { value: 'tabbed', label: 'Tabbed', desc: '标签页风格' },
          ]"
          :key="opt.value"
          :class="[
            'flex flex-col items-start p-4 rounded-xl border transition-all duration-200',
            transparency === opt.value
              ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30'
              : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] hover:border-[var(--neon-primary)]/20',
          ]"
          @click="updateTransparency(opt.value)"
        >
          <span
            :class="[
              'text-xs font-semibold',
              transparency === opt.value
                ? 'text-[var(--neon-primary)]'
                : 'text-[var(--app-text)]/80',
            ]"
          >
            {{ opt.label }}
          </span>
          <span class="text-[9px] text-[var(--app-text-subtle)]">{{ opt.desc }}</span>
        </button>
      </div>
    </SectionCard>

    <!-- History Toggle Card -->
    <div
      class="glass-panel rounded-[var(--radius-squircle-lg)] p-6 cursor-pointer transition-all duration-300 hover:border-[var(--neon-primary)]/20"
      @click="toggleHistory"
    >
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <div
            :class="[
              'w-8 h-8 rounded-xl flex items-center justify-center transition-colors duration-300',
              showHistory ? 'bg-[var(--neon-primary)]/10' : 'bg-[var(--btn-glass-bg)]',
            ]"
          >
            <History
              :size="16"
              :class="showHistory ? 'text-[var(--neon-primary)]' : 'text-[var(--app-text-subtle)]'"
            />
          </div>
          <div>
            <h3 class="text-sm font-semibold text-[var(--app-text)]/80">显示下载历史</h3>
            <p class="text-[10px] text-[var(--app-text-subtle)]">在"已完成"标签页显示历史记录</p>
          </div>
        </div>

        <!-- Toggle Switch -->
        <div
          :class="[
            'w-12 h-7 rounded-full relative transition-all duration-300 cursor-pointer',
            showHistory ? 'bg-[var(--neon-primary)]' : 'bg-[var(--btn-glass-bg)]',
          ]"
        >
          <div
            :class="[
              'absolute top-1 w-5 h-5 rounded-full bg-[var(--card-bg)] shadow-lg ring-1 ring-[var(--glass-border)] transition-all duration-300',
              showHistory ? 'left-6' : 'left-1',
            ]"
          ></div>
        </div>
      </div>
    </div>
  </div>
</template>
