<script setup lang="ts">
  import { computed, ref, onMounted, onUnmounted } from 'vue'
  import { Cpu, ChevronDown, Check, Info } from '@lucide/vue'
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

  const displayedConnectionOptions = computed(() => {
    const current = Number.parseInt(props.connections, 10)
    if (!Number.isInteger(current) || current < 1 || current > 256) {
      return props.connectionOptions
    }
    return props.connectionOptions.includes(String(current))
      ? props.connectionOptions
      : [...props.connectionOptions, String(current)]
  })

  const isSurgeExclusive = (value: string) => {
    const n = Number.parseInt(value, 10)
    return Number.isInteger(n) && n > 16
  }

  const toggleSmartThreadMode = () => {
    emit('update:smartThreadMode', !props.smartThreadMode)
    emit('change')
  }

  const showConnectionsDropdown = ref(false)
  const connectionsDropdownRef = ref<HTMLElement | null>(null)

  const handleClickOutsideConnections = (event: MouseEvent) => {
    if (
      showConnectionsDropdown.value &&
      connectionsDropdownRef.value &&
      !connectionsDropdownRef.value.contains(event.target as Node)
    ) {
      showConnectionsDropdown.value = false
    }
  }

  onMounted(() => {
    document.addEventListener('click', handleClickOutsideConnections)
  })

  onUnmounted(() => {
    document.removeEventListener('click', handleClickOutsideConnections)
  })

  const selectConnections = (value: string) => {
    emit('update:connections', value)
    emit('change')
    showConnectionsDropdown.value = false
  }

  const updateConcurrentDownloads = (event: Event) => {
    const value = (event.target as HTMLInputElement).value
    emit('update:concurrentDownloads', value)
    emit('change')
  }
</script>

<template>
  <SectionCard
    class="relative z-[80]"
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
          class="flex items-center gap-1.5 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
        >
          {{ t('performance.maxConnections') }}
          <div class="group relative flex items-center">
            <Info :size="12" class="cursor-help hover:text-[var(--app-text)] transition-colors" />
            <!-- Tooltip Popup -->
            <div
              class="absolute bottom-full left-1/2 -translate-x-1/2 mb-2 w-56 p-2.5 rounded-lg glass-panel-solid opacity-0 invisible group-hover:opacity-100 group-hover:visible transition-all duration-200 z-[100] text-center text-[10px] font-medium normal-case tracking-normal pointer-events-none"
            >
              {{ t('performance.surgeExclusiveTooltip') }}
              <div
                class="absolute -bottom-1 left-1/2 -translate-x-1/2 w-2 h-2 glass-panel-solid-arrow transform rotate-45"
              ></div>
            </div>
          </div>
        </label>
        <div ref="connectionsDropdownRef" class="relative z-50">
          <button
            type="button"
            class="w-full flex items-center justify-between bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none cursor-pointer transition-all duration-200 hover:border-[var(--neon-primary)]/30"
            @click="showConnectionsDropdown = !showConnectionsDropdown"
          >
            <span>{{ connections }} {{ t('performance.threads') }}</span>
            <ChevronDown
              :size="16"
              class="text-[var(--app-text-subtle)] transition-transform duration-200"
              :class="{ 'rotate-180': showConnectionsDropdown }"
            />
          </button>

          <!-- Dropdown Menu -->
          <Transition name="slide-fade">
            <div
              v-if="showConnectionsDropdown"
              class="absolute z-50 top-full left-0 right-0 mt-2 p-1 rounded-xl glass-panel-solid origin-top max-h-72 overflow-y-auto"
            >
              <button
                v-for="n in displayedConnectionOptions"
                :key="n"
                type="button"
                class="w-full flex items-center justify-between p-3 rounded-lg transition-all duration-200 group"
                :class="[
                  connections === n
                    ? 'bg-[var(--neon-primary)]/10 text-[var(--neon-primary)]'
                    : 'text-[var(--app-text)] hover:bg-[var(--app-text)]/5',
                ]"
                @click="selectConnections(n)"
              >
                <div class="flex items-center gap-2">
                  <span class="text-sm font-mono-data font-medium">
                    {{ n }} {{ t('performance.threads') }}
                  </span>
                  <span
                    v-if="isSurgeExclusive(n)"
                    class="flex items-center gap-1 text-[9px] font-bold text-[var(--neon-primary)] bg-[var(--neon-primary)]/10 px-1.5 py-0.5 rounded uppercase tracking-wider"
                  >
                    Surge
                  </span>
                </div>
                <Check v-if="connections === n" :size="14" />
              </button>
            </div>
          </Transition>
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
          max="32"
          class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)]"
          @input="updateConcurrentDownloads"
        />
      </div>
    </div>
  </SectionCard>
</template>

<style scoped>
  .slide-fade-enter-active,
  .slide-fade-leave-active {
    transition: all 0.2s ease;
  }
  .slide-fade-enter-from,
  .slide-fade-leave-to {
    opacity: 0;
    transform: translateY(-8px) scale(0.98);
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
