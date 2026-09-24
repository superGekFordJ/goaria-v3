import {
  ref,
  computed,
  watch,
  onMounted,
  onUnmounted,
  getCurrentInstance,
  toValue,
  type MaybeRefOrGetter,
  type ComputedRef,
} from 'vue'

export interface UseTaskEtaOptions {
  hasKnownTotal: MaybeRefOrGetter<boolean>
  totalBytes: MaybeRefOrGetter<number>
  downloadedBytes: MaybeRefOrGetter<number>
  rawSpeed: MaybeRefOrGetter<number>
  isActive: MaybeRefOrGetter<boolean>
  isSurge?: MaybeRefOrGetter<boolean> // Optional, retained for interface compatibility
}

interface Sample {
  time: number
  bytes: number
}

/**
 * Format seconds into a compact human-readable duration string:
 * - < 60s: "35s"
 * - < 3600s: "4m 20s"
 * - >= 3600s: "1h 15m"
 * - invalid / unknown: "--"
 */
export function formatDuration(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds) || seconds < 0) return '--'
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  const hours = Math.floor(seconds / 3600)
  const mins = Math.floor((seconds % 3600) / 60)
  return `${hours}h ${mins}m`
}

export function useTaskEta(options: UseTaskEtaOptions): ComputedRef<string> {
  const {
    hasKnownTotal,
    totalBytes,
    downloadedBytes,
    rawSpeed,
    isActive,
  } = options

  // Universal path (Aria2 & Surge): 5s sliding window average + 1s stopwatch clock countdown
  const etaSeconds = ref<number | null>(null)
  const samples: Sample[] = []
  let timerId: ReturnType<typeof setInterval> | null = null

  const clearSamples = () => {
    samples.length = 0
  }

  const computeWindowSpeed = (now: number, currentDownloaded: number): number => {
    samples.push({ time: now, bytes: currentDownloaded })

    // Retain only samples within the last 5 seconds (5000ms)
    while (samples.length > 2 && now - samples[0].time > 5000) {
      samples.shift()
    }

    // Need at least 2 points to compute a delta
    if (samples.length < 2) {
      return toValue(rawSpeed)
    }

    const oldest = samples[0]
    const elapsedSeconds = (now - oldest.time) / 1000
    const deltaBytes = currentDownloaded - oldest.bytes

    if (elapsedSeconds <= 0 || deltaBytes <= 0) {
      return toValue(rawSpeed)
    }

    return deltaBytes / elapsedSeconds
  }

  const tick = () => {
    const active = toValue(isActive)
    const known = toValue(hasKnownTotal)
    const total = toValue(totalBytes)
    const downloaded = toValue(downloadedBytes)
    const remaining = total - downloaded

    if (!active || !known || remaining <= 0) {
      etaSeconds.value = null
      clearSamples()
      return
    }

    const now = Date.now()
    const speed = computeWindowSpeed(now, downloaded)

    if (speed <= 0) {
      etaSeconds.value = null
      return
    }

    const target = Math.floor(remaining / speed)
    if (!Number.isFinite(target) || target < 0) {
      etaSeconds.value = null
      return
    }

    if (etaSeconds.value === null) {
      // Cold start: immediately adopt target
      etaSeconds.value = target
      return
    }

    const current = etaSeconds.value
    const diff = target - current

    // If change is minor (within ±2s), countdown naturally by 1s (like a stopwatch)
    if (Math.abs(diff) <= 2) {
      const floor = target > 0 ? 1 : 0
      etaSeconds.value = Math.max(floor, current - 1)
    } else {
      // Significant change: follow the 5s window target directly.
      // Because the 5s window itself already smoothly filters out spikes,
      // target transitions smoothly without needing artificial clamp/damping.
      etaSeconds.value = target
    }
  }

  // React to status or totalLength transitions
  watch(
    [() => toValue(isActive), () => toValue(hasKnownTotal)],
    ([active, known]) => {
      if (!active || !known) {
        etaSeconds.value = null
        clearSamples()
      } else if (etaSeconds.value === null) {
        // Immediate initial target calculation when entering active state
        const speed = toValue(rawSpeed)
        const remaining = toValue(totalBytes) - toValue(downloadedBytes)
        if (speed > 0 && remaining > 0) {
          etaSeconds.value = Math.floor(remaining / speed)
          samples.push({ time: Date.now(), bytes: toValue(downloadedBytes) })
        }
      }
    },
    { immediate: true },
  )

  // Initialize target as soon as non-zero speed arrives
  watch(
    () => toValue(rawSpeed),
    (newSpeed) => {
      if (toValue(isActive) && toValue(hasKnownTotal) && etaSeconds.value === null && newSpeed > 0) {
        const remaining = toValue(totalBytes) - toValue(downloadedBytes)
        if (remaining > 0) {
          etaSeconds.value = Math.floor(remaining / newSpeed)
          samples.push({ time: Date.now(), bytes: toValue(downloadedBytes) })
        }
      }
    },
  )

  const startTimer = () => {
    if (timerId === null) {
      timerId = setInterval(tick, 1000)
    }
  }

  const stopTimer = () => {
    if (timerId !== null) {
      clearInterval(timerId)
      timerId = null
    }
    clearSamples()
  }

  if (getCurrentInstance()) {
    onMounted(startTimer)
    onUnmounted(stopTimer)
  } else {
    // Non-component testing context
    startTimer()
  }

  return computed(() => {
    if (!toValue(isActive) || !toValue(hasKnownTotal)) {
      return '--'
    }
    return formatDuration(etaSeconds.value)
  })
}
