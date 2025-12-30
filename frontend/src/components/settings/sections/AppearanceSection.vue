<script setup lang="ts">
  import { Palette, Monitor, Sun, Moon } from 'lucide-vue-next'
  import SectionCard from './SectionCard.vue'
  import { useUIStore, type ThemeMode, type SkinId } from '../../../stores/ui'

  const uiStore = useUIStore()
</script>

<template>
  <SectionCard 
    title="外观设置" 
    description="主题模式与皮肤风格" 
    :icon="Palette" 
    icon-class="bg-indigo-500/10 text-indigo-400"
  >
    <!-- Theme Mode Selector -->
    <div class="mb-6">
      <label
        class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
      >
        主题模式
      </label>
      <div class="grid grid-cols-3 gap-2">
        <button
          v-for="mode in ['system', 'light', 'dark'] as ThemeMode[]"
          :key="mode"
          :class="[
            'flex flex-col items-center gap-2 p-4 rounded-xl border transition-all duration-200',
            uiStore.themeMode === mode
              ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30 text-[var(--neon-primary)]'
              : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] text-[var(--app-text-muted)] hover:border-[var(--neon-primary)]/20',
          ]"
          @click="uiStore.setTheme(mode)"
        >
          <Monitor v-if="mode === 'system'" :size="20" />
          <Sun v-else-if="mode === 'light'" :size="20" />
          <Moon v-else :size="20" />
          <span class="text-[10px] font-semibold">
            {{ mode === 'system' ? '跟随系统' : mode === 'light' ? '亮色' : '暗色' }}
          </span>
        </button>
      </div>
    </div>

    <!-- Skin Selector -->
    <div>
      <label
        class="text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)] mb-3 block"
      >
        皮肤风格
      </label>
      <div class="grid grid-cols-2 gap-3">
        <button
          v-for="skin in ['obsidian', 'ceramic'] as SkinId[]"
          :key="skin"
          :class="[
            'flex items-center gap-3 p-4 rounded-xl border transition-all duration-200',
            uiStore.skinId === skin
              ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30'
              : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] hover:border-[var(--neon-primary)]/20',
          ]"
          @click="uiStore.setSkin(skin)"
        >
          <div
            :class="[
              'w-8 h-8 rounded-lg',
              skin === 'obsidian'
                ? 'bg-gradient-to-br from-gray-800 to-gray-900'
                : 'bg-gradient-to-br from-gray-100 to-white border border-gray-200',
            ]"
          ></div>
          <div class="text-left">
            <span
              :class="[
                'text-xs font-semibold block',
                uiStore.skinId === skin
                  ? 'text-[var(--neon-primary)]'
                  : 'text-[var(--app-text)]/80',
              ]"
            >
              {{ skin === 'obsidian' ? 'Obsidian' : 'Ceramic' }}
            </span>
            <span class="text-[9px] text-[var(--app-text-subtle)]">
              {{ skin === 'obsidian' ? '深邃黑曜石' : '温润陶瓷白' }}
            </span>
          </div>
        </button>
      </div>
    </div>
  </SectionCard>
</template>
