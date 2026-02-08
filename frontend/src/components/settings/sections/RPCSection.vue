<script setup lang="ts">
  import { ref } from 'vue'
  import { useI18n } from 'vue-i18n'
  import { Zap, Globe, Shield, Eye, EyeOff } from 'lucide-vue-next'
  import SectionCard from './SectionCard.vue'

  const { t } = useI18n()

  defineProps<{
    port: string
    secret: string
  }>()

  const emit = defineEmits<{
    (e: 'update:port', value: string): void
    (e: 'update:secret', value: string): void
    (e: 'change'): void
  }>()

  const showSecret = ref(false)

  const updatePort = (event: Event) => {
    const value = (event.target as HTMLInputElement).value
    emit('update:port', value)
    emit('change')
  }

  const updateSecret = (event: Event) => {
    const value = (event.target as HTMLInputElement).value
    emit('update:secret', value)
    emit('change')
  }
</script>

<template>
  <SectionCard
    :title="t('rpc.title')"
    :description="t('rpc.description')"
    :icon="Zap"
    icon-class="bg-purple-500/10 text-purple-400"
  >
    <div class="grid grid-cols-2 gap-4">
      <!-- RPC Port -->
      <div class="space-y-2">
        <label
          class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
        >
          <Globe :size="10" />
          {{ t('rpc.port') }}
        </label>
        <input
          :value="port"
          type="text"
          class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 text-sm font-mono-data text-[var(--app-text)]/80 outline-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)]"
          @input="updatePort"
        />
      </div>

      <!-- RPC Secret -->
      <div class="space-y-2">
        <label
          class="flex items-center gap-2 text-[10px] font-bold uppercase tracking-widest text-[var(--app-text-subtle)]"
        >
          <Shield :size="10" />
          {{ t('rpc.secret') }}
        </label>
        <div class="relative">
          <input
            :value="secret"
            :type="showSecret ? 'text' : 'password'"
            :placeholder="t('rpc.optional')"
            class="w-full bg-[var(--input-bg)] border border-[var(--input-border)] rounded-xl px-4 py-3 pr-12 text-sm font-mono-data text-[var(--app-text)]/80 outline-none transition-all duration-200 focus:border-[var(--neon-primary)]/40 focus:shadow-[0_0_0_3px_var(--input-focus)] placeholder:text-[var(--input-placeholder)]"
            @input="updateSecret"
          />
          <button
            type="button"
            :title="showSecret ? t('rpc.hideSecret') : t('rpc.showSecret')"
            class="absolute right-3 top-1/2 -translate-y-1/2 p-1.5 rounded-lg text-[var(--app-text-subtle)] hover:text-[var(--neon-primary)] hover:bg-[var(--neon-primary)]/10 transition-all duration-200"
            @click="showSecret = !showSecret"
          >
            <Eye v-if="showSecret" :size="16" />
            <EyeOff v-else :size="16" />
          </button>
        </div>
      </div>
    </div>
  </SectionCard>
</template>
