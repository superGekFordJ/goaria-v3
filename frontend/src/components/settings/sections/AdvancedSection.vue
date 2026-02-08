<script setup lang="ts">
  import { Layers, History } from 'lucide-vue-next'
  import { useI18n } from 'vue-i18n'
  import SectionCard from './SectionCard.vue'

  const { t } = useI18n()

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
      :title="t('advanced.transparencyTitle')"
      :description="t('advanced.transparencyDesc')"
      :icon="Layers"
      icon-class="bg-cyan-500/10 text-cyan-400"
    >
      <div class="grid grid-cols-2 gap-3">
        <button
          v-for="opt in [
            {
              value: 'none',
              label: t('advanced.transparencyClose'),
              desc: t('advanced.transparencyStandard'),
            },
            {
              value: 'acrylic',
              label: t('advanced.transparencyAcrylic'),
              desc: t('advanced.transparencyAcrylicBlur'),
            },
            {
              value: 'mica',
              label: t('advanced.transparencyMica'),
              desc: t('advanced.transparencyMicaAlt'),
            },
            { value: 'tabbed', label: 'Tabbed', desc: t('advanced.transparencyTabbed') },
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
            <h3 class="text-sm font-semibold text-[var(--app-text)]/80">
              {{ t('advanced.showHistory') }}
            </h3>
            <p class="text-[10px] text-[var(--app-text-subtle)]">
              {{ t('advanced.showHistoryDesc') }}
            </p>
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
