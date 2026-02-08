<script setup lang="ts">
  import { Cpu } from 'lucide-vue-next'
  import { useI18n } from 'vue-i18n'
  import SectionCard from './SectionCard.vue'

  const { t } = useI18n()

  const props = defineProps<{
    connections: string
    concurrentDownloads: string
    connectionOptions: string[]
    smartThreadMode: boolean
  }>()

  const emit = defineEmits<{
    (e: 'update:connections', value: string): void
    (e: 'update:concurrentDownloads', value: string): void
    (e: 'update:smartThreadMode', value: boolean): void
    (e: 'change'): void
  }>()

  const toggleSmartThreadMode = () => {
    emit('update:smartThreadMode', !props.smartThreadMode)
    emit('change')
  }

  const updateConnections = (event: Event) => {
    const value = (event.target as HTMLSelectElement).value
    emit('update:connections', value)
    emit('change')
  }

  const updateConcurrentDownloads = (event: Event) => {
    const value = (event.target as HTMLInputElement).value
    emit('update:concurrentDownloads', value)
    emit('change')
  }
</script>

<template>
  <SectionCard
    :title="t('performance.title')"
    :description="t('performance.description')"
    :icon="Cpu"
    icon-class="bg-amber-500/10 text-amber-400"
  >
    <!-- Smart Thread Mode Toggle -->
    <div
      class="mb-4 flex items-center justify-between p-3 bg-[var(--input-bg)] rounded-xl border border-[var(--input-border)]"
    >
      <div>
        <div class="text-sm font-medium text-[var(--app-text)]">
          {{ t('performance.smartThreadMode') }}
        </div>
        <div class="text-xs text-[var(--app-text-subtle)]">
          {{ t('performance.smartThreadDesc') }}
        </div>
      </div>
      <button
        type="button"
        :class="[
          'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none',
          smartThreadMode ? 'bg-[var(--neon-primary)]' : 'bg-[var(--input-border)]',
        ]"
        @click="toggleSmartThreadMode"
      >
        <span
          :class="[
            'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-[var(--card-bg)] shadow ring-1 ring-[var(--glass-border)] transition duration-200 ease-in-out',
            smartThreadMode ? 'translate-x-5' : 'translate-x-0',
          ]"
        />
      </button>
    </div>

    <div class="grid grid-cols-2 gap-4">
      <!-- Max Connections Per Server -->
      <div class="space-y-2">
        <label
          class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
        >
          {{ t('performance.maxConnections') }}
        </label>
        <div class="relative">
          <select
            :value="connections"
            class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none appearance-none cursor-pointer transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)]"
            @change="updateConnections"
          >
            <option v-for="n in connectionOptions" :key="n" :value="n">
              {{ n }} {{ t('performance.threads') }}
            </option>
          </select>
          <!-- Custom dropdown arrow -->
          <div class="absolute right-4 top-1/2 -translate-y-1/2 pointer-events-none">
            <svg
              width="10"
              height="6"
              viewBox="0 0 10 6"
              fill="none"
              class="text-[var(--app-text-subtle)]"
            >
              <path
                d="M1 1L5 5L9 1"
                stroke="currentColor"
                stroke-width="1.5"
                stroke-linecap="round"
                stroke-linejoin="round"
              />
            </svg>
          </div>
        </div>
      </div>

      <!-- Max Concurrent Downloads -->
      <div class="space-y-2">
        <label
          class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
        >
          {{ t('performance.maxConcurrent') }}
        </label>
        <input
          :value="concurrentDownloads"
          type="number"
          min="1"
          max="10"
          class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)]"
          @input="updateConcurrentDownloads"
        />
      </div>
    </div>
  </SectionCard>
</template>

<style scoped>
  /* Select option styling (limited support) */
  select option {
    background: var(--glass-bg);
    color: var(--app-text);
    padding: 8px;
  }

  /* Hide number input spinners */
  input[type='number']::-webkit-inner-spin-button,
  input[type='number']::-webkit-outer-spin-button {
    -webkit-appearance: none;
    margin: 0;
  }

  input[type='number'] {
    appearance: textfield;
    -moz-appearance: textfield;
  }
</style>
