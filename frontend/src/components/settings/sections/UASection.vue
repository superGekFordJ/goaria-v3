<script setup lang="ts">
  import { Globe, Loader2, Download, X } from 'lucide-vue-next'
  import SectionCard from './SectionCard.vue'
  import { useUserAgent } from '../../common/useUserAgent'

  defineProps<{
    modelValue: string
  }>()

  const emit = defineEmits<{
    (e: 'update:modelValue', value: string): void
    (e: 'change'): void
  }>()

  const {
    userAgentPresets,
    isFetchingUA,
    showUADropdown,
    uaFetchError,
    fetchUserAgents,
    closeUADropdown,
  } = useUserAgent()

  const selectUserAgent = (ua: string) => {
    emit('update:modelValue', ua)
    closeUADropdown()
    emit('change')
  }

  const updateModelValue = (event: Event) => {
    const value = (event.target as HTMLTextAreaElement).value
    emit('update:modelValue', value)
    emit('change')
  }
</script>

<template>
  <SectionCard
    title="User-Agent"
    description="自定义浏览器标识"
    :icon="Globe"
    icon-class="bg-blue-500/10 text-blue-400"
  >
    <template #header-extra>
      <button
        type="button"
        class="flex items-center gap-2 px-3 py-1.5 rounded-lg text-[10px] font-semibold bg-[var(--btn-glass-bg)] border border-[var(--glass-border)] text-[var(--app-text-muted)] hover:border-[var(--neon-primary)]/30 hover:text-[var(--neon-primary)] transition-all duration-200 disabled:opacity-50 disabled:cursor-not-allowed"
        :disabled="isFetchingUA"
        @click="fetchUserAgents"
      >
        <Loader2 v-if="isFetchingUA" :size="12" class="animate-spin" />
        <Download v-else :size="12" />
        获取预设
      </button>
    </template>

    <!-- Error Message -->
    <div
      v-if="uaFetchError"
      class="mb-3 px-3 py-2 rounded-lg bg-red-500/10 border border-red-500/20 text-red-400 text-[10px]"
    >
      {{ uaFetchError }}
    </div>

    <!-- UA Dropdown -->
    <Transition name="slide-fade">
      <div v-if="showUADropdown && userAgentPresets.length > 0" class="mb-4 relative">
        <div class="flex items-center justify-between mb-2">
          <span class="text-[10px] font-semibold text-[var(--app-text-subtle)]">选择预设</span>
          <button
            type="button"
            class="p-1 rounded-md text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:bg-[var(--btn-glass-bg)] transition-all duration-200"
            @click="closeUADropdown"
          >
            <X :size="12" />
          </button>
        </div>
        <div class="grid grid-cols-2 gap-2 max-h-48 overflow-y-auto custom-scrollbar">
          <button
            v-for="(item, index) in userAgentPresets"
            :key="index"
            type="button"
            class="flex flex-col items-start p-3 rounded-xl bg-[var(--input-bg)] border border-[var(--input-border)] text-left hover:border-[var(--neon-primary)]/30 hover:bg-[var(--neon-primary)]/5 transition-all duration-200"
            @click="selectUserAgent(item.ua)"
          >
            <div class="flex items-center gap-1.5 mb-1">
              <span class="text-[10px] font-bold text-[var(--app-text)]/90">{{
                item.browser
              }}</span>
              <span
                class="px-1 py-0.5 rounded-md bg-[var(--app-text-subtle)]/10 text-[8px] font-semibold text-[var(--app-text-subtle)] border border-[var(--app-text-subtle)]/20"
                >{{ item.os }}</span
              >
            </div>
            <span
              class="text-[9px] text-[var(--app-text-subtle)] line-clamp-2 break-all leading-relaxed"
              >{{ item.ua }}</span
            >
          </button>
        </div>
      </div>
    </Transition>

    <textarea
      :value="modelValue"
      rows="2"
      placeholder="留空使用默认值"
      class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-[11px] font-mono-data text-[var(--app-text)]/70 outline-none resize-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)] placeholder:text-[var(--input-placeholder)]"
      @input="updateModelValue"
    ></textarea>
  </SectionCard>
</template>

<style scoped>
  /* Textarea scrollbar */
  textarea::-webkit-scrollbar {
    width: 4px;
  }

  textarea::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.1);
    border-radius: 4px;
  }

  /* Slide-fade transition for UA dropdown */
  .slide-fade-enter-active,
  .slide-fade-leave-active {
    transition: all 0.2s ease;
  }

  .slide-fade-enter-from,
  .slide-fade-leave-to {
    opacity: 0;
    transform: translateY(-8px);
  }

  /* Custom scrollbar for UA dropdown */
  .custom-scrollbar::-webkit-scrollbar {
    width: 4px;
  }

  .custom-scrollbar::-webkit-scrollbar-track {
    background: transparent;
  }

  .custom-scrollbar::-webkit-scrollbar-thumb {
    background: rgba(255, 255, 255, 0.1);
    border-radius: 4px;
  }

  .custom-scrollbar::-webkit-scrollbar-thumb:hover {
    background: rgba(255, 255, 255, 0.2);
  }
</style>
