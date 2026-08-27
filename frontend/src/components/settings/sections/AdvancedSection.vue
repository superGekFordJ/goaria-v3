<script setup lang="ts">
  import { Layers, History, PanelBottomClose } from '@lucide/vue'
  import { useI18n } from 'vue-i18n'
  import { computed } from 'vue'
  import { System } from '@wailsio/runtime'
  import SectionCard from './SectionCard.vue'

  const { t } = useI18n()
  const isMac = System.IsMac()
  const isLinux = System.IsLinux()

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

  const isOptionActive = (optValue: string) => {
    if (isMac) {
      if (optValue === 'none') {
        return props.transparency === 'none'
      }
      return props.transparency !== 'none'
    }
    return props.transparency === optValue
  }

  const transparencyOptions = computed(() => {
    if (isMac) {
      return [
        {
          value: 'none',
          label: t('advanced.transparencyClose'),
          desc: t('advanced.transparencyStandard'),
        },
        {
          value: 'acrylic',
          label: t('advanced.transparencyMacVibrancy'),
          desc: t('advanced.transparencyMacVibrancyDesc'),
        },
      ]
    }
    return [
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
        desc: t('advanced.transparencyMicaDesc'),
      },
      {
        value: 'tabbed',
        label: t('advanced.transparencyTabbed'),
        desc: t('advanced.transparencyTabbedDesc'),
      },
    ]
  })

  const toggleHistory = () => {
    emit('update:showHistory', !props.showHistory)
    emit('change')
  }
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- Window Transparency Card (Hidden on Linux as desktop compositors lack unified blur APIs) -->
    <SectionCard
      v-if="!isLinux"
      :title="t('advanced.transparencyTitle')"
      :icon="Layers"
      icon-class="bg-cyan-500/10 text-cyan-400"
    >
      <template #description>
        <span v-if="isMac" class="leading-relaxed">
          {{ t('advanced.transparencyDescMac') }}
        </span>
        <span v-else class="inline-flex items-center gap-1 flex-wrap leading-relaxed">
          <span>{{ t('advanced.transparencyDescWinPrefix') }}</span>
          <span
            class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-md bg-[var(--neon-primary)]/10 text-[var(--neon-primary)] text-[9px] font-medium border border-[var(--neon-primary)]/20 align-middle select-none"
          >
            <PanelBottomClose :size="11" class="shrink-0" />
            <span>{{ t('titleBar.minimizeToTray') }}</span>
          </span>
          <span>{{ t('advanced.transparencyDescWinSuffix') }}</span>
        </span>
      </template>

      <div class="grid grid-cols-2 gap-3">
        <button
          v-for="opt in transparencyOptions"
          :key="opt.value"
          :class="[
            'flex flex-col items-start p-4 rounded-xl border transition-all duration-200',
            isOptionActive(opt.value)
              ? 'bg-[var(--neon-primary)]/10 border-[var(--neon-primary)]/30'
              : 'bg-[var(--btn-glass-bg)] border-[var(--glass-border)] hover:border-[var(--neon-primary)]/20',
          ]"
          @click="updateTransparency(opt.value)"
        >
          <span
            :class="[
              'text-xs font-semibold',
              isOptionActive(opt.value)
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
    <div class="etched-panel p-6">
      <div class="flex items-center justify-between relative z-10">
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
          @click="toggleHistory"
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
