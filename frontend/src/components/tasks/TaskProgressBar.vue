<script setup lang="ts">
  import { computed, watch } from 'vue'
  import {
    TASK_PROGRESS_CONFIG,
    SURGE_TASK_PROGRESS_CONFIG,
    useSmoothProgress,
  } from '../../composables/useSmoothProgress'

  const props = defineProps<{
    downloaded: number
    speed: number
    total: number
    status: string
    hasKnownTotal: boolean
    isSurge: boolean
    isActive: boolean
    isPaused: boolean
    progress: number | null
  }>()

  const { displayDownloaded, totalBytes, updateStats } = useSmoothProgress(
    props.isSurge ? SURGE_TASK_PROGRESS_CONFIG : TASK_PROGRESS_CONFIG,
  )

  watch(
    [
      () => props.downloaded,
      () => props.speed,
      () => props.total,
      () => props.status,
      () => props.hasKnownTotal,
    ],
    () => {
      // While visual total is unknown, supply max(downloaded, 1) as internal
      // smoothing denominator so displayDownloaded does not collapse to 1 byte.
      const internalTotal = props.hasKnownTotal ? props.total : Math.max(props.downloaded, 1)
      updateStats({
        downloaded: props.downloaded,
        speed: props.speed,
        total: internalTotal,
        status: props.status,
      })
    },
    { immediate: true },
  )

  // When totalLength transitions from unknown to known, re-anchor displayDownloaded
  // to avoid catching up from arbitrary smoothing lag.
  watch(
    () => props.hasKnownTotal,
    (known, prevKnown) => {
      if (known && !prevKnown) {
        displayDownloaded.value = props.downloaded
      }
    },
  )

  // Calculate smooth progress scale (0-1) for GPU animation
  const progressScale = computed(() => {
    if (!props.hasKnownTotal) return 0
    if (totalBytes.value <= 0) return 0
    const ratio = displayDownloaded.value / totalBytes.value
    return Math.min(Math.max(ratio, 0), 1)
  })
</script>

<template>
  <div
    class="mb-4"
    role="progressbar"
    :aria-valuemin="0"
    :aria-valuemax="100"
    :aria-valuenow="
      hasKnownTotal && progress !== null ? Math.min(Math.round(progress), 99) : undefined
    "
  >
    <div class="progress-bar-container">
      <template v-if="hasKnownTotal">
        <div
          :class="[
            'progress-bar-fill',
            { 'opacity-50': isPaused },
            { 'progress-bar-energy': isActive },
          ]"
          :style="{ transform: `scaleX(${progressScale})` }"
        ></div>
      </template>
      <template v-else>
        <div :class="['progress-bar-indeterminate', { 'opacity-50': isPaused }]"></div>
      </template>
    </div>
  </div>
</template>
