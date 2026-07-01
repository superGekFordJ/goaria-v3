<script setup lang="ts">
  import { computed, useId } from 'vue'
  import { categorizeByFileName, type FileIconCategoryId } from '../../utils/fileIconCategory'

  interface Props {
    fileName: string | null | undefined
    tier?: 'bare' | 'chipped'
    size?: number
    label?: string
  }

  const props = withDefaults(defineProps<Props>(), {
    tier: 'bare',
    size: undefined,
    label: undefined,
  })

  const category = computed<FileIconCategoryId>(() => categorizeByFileName(props.fileName))
  const isChipped = computed(() => props.tier === 'chipped')
  const resolvedSize = computed(() => props.size ?? (isChipped.value ? 40 : 18))

  // Stable unique id so multiple Chipped instances on one page don't clash.
  const uid = useId()
  const gradientId = computed(() => `file-icon-grad-${uid}`)
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    :width="resolvedSize"
    :height="resolvedSize"
    fill="none"
    :stroke="isChipped ? 'var(--neon-btn-text)' : 'currentColor'"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    :role="label ? 'img' : undefined"
    :aria-label="label"
    :aria-hidden="label ? undefined : 'true'"
  >
    <defs v-if="isChipped">
      <linearGradient
        :id="gradientId"
        x1="2"
        y1="2"
        x2="22"
        y2="22"
        gradientUnits="userSpaceOnUse"
      >
        <stop offset="0%" stop-color="var(--skin-accent-from)" />
        <stop offset="100%" stop-color="var(--skin-accent-to)" />
      </linearGradient>
    </defs>
    <rect
      v-if="isChipped"
      x="2"
      y="2"
      width="20"
      height="20"
      rx="6"
      :fill="`url(#${gradientId})`"
      stroke="var(--skin-surface-border)"
      stroke-width="1"
      stroke-opacity="0.6"
    />

    <!-- Default: pure three-signal carrier, no notch -->
    <template v-if="category === 'default'">
      <rect x="4" y="11" width="3" height="7" rx="1.5" />
      <rect x="10.5" y="7" width="3" height="11" rx="1.5" />
      <rect x="17" y="11" width="3" height="7" rx="1.5" />
    </template>

    <!-- Media: play triangle + equalizer waveform -->
    <template v-else-if="category === 'media'">
      <path d="M4 7 L4 17 L11 12 Z" />
      <line x1="15" y1="10" x2="15" y2="14" />
      <line x1="18" y1="8" x2="18" y2="16" />
      <line x1="21" y1="10" x2="21" y2="14" />
    </template>

    <!-- Archive: zip slider down the centre -->
    <template v-else-if="category === 'archive'">
      <path
        d="M11 3 H7 a2 2 0 0 0 -2 2 v14 a2 2 0 0 0 2 2 h10 a2 2 0 0 0 2 -2 V5 a2 2 0 0 0 -2 -2 h-4"
      />
      <line x1="12" y1="3" x2="12" y2="8" />
      <rect x="10.5" y="8" width="3" height="3" rx="0.5" />
      <line x1="12" y1="11" x2="12" y2="21" />
    </template>

    <!-- Executable: terminal window + prompt cursor -->
    <template v-else-if="category === 'executable'">
      <path
        d="M5 5 H10 M12 5 H19 a2 2 0 0 1 2 2 V17 a2 2 0 0 1 -2 2 H5 a2 2 0 0 1 -2 -2 V7 a2 2 0 0 1 2 -2"
      />
      <path d="M7 10 L10 12 L7 14" />
      <line x1="12" y1="14" x2="16" y2="14" />
    </template>

    <!-- Document: folded page + three reading lines -->
    <template v-else-if="category === 'document'">
      <path d="M6 3 H14 L18 7 V19 a2 2 0 0 1 -2 2 H6 a2 2 0 0 1 -2 -2 V5 a2 2 0 0 1 2 -2" />
      <path d="M14 3 V7 H18" />
      <line x1="9" y1="11" x2="14" y2="11" />
      <line x1="9" y1="14" x2="16" y2="14" />
      <line x1="9" y1="17" x2="13" y2="17" />
    </template>

    <!-- Disk: optical disc ring + centre hole -->
    <template v-else-if="category === 'disk'">
      <path d="M13.5 3.2 A9 9 0 1 1 10.5 3.2" />
      <circle cx="12" cy="12" r="2.5" />
    </template>
  </svg>
</template>
