import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { ref } from 'vue'
import { useTaskEta, formatDuration } from './useTaskEta'

describe('useTaskEta', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  describe('formatDuration', () => {
    it('returns -- for invalid or non-positive inputs', () => {
      expect(formatDuration(null)).toBe('--')
      expect(formatDuration(-1)).toBe('--')
      expect(formatDuration(NaN)).toBe('--')
      expect(formatDuration(Infinity)).toBe('--')
    })

    it('formats seconds correctly (< 60s)', () => {
      expect(formatDuration(0)).toBe('0s')
      expect(formatDuration(45)).toBe('45s')
      expect(formatDuration(59)).toBe('59s')
    })

    it('formats minutes and seconds correctly (< 3600s)', () => {
      expect(formatDuration(60)).toBe('1m 0s')
      expect(formatDuration(125)).toBe('2m 5s')
      expect(formatDuration(3599)).toBe('59m 59s')
    })

    it('formats hours and minutes correctly (>= 3600s)', () => {
      expect(formatDuration(3600)).toBe('1h 0m')
      expect(formatDuration(3665)).toBe('1h 1m')
      expect(formatDuration(7200)).toBe('2h 0m')
      expect(formatDuration(90000)).toBe('25h 0m')
    })
  })

  describe('Universal ETA prediction (5s sliding window + 1s stopwatch countdown)', () => {
    it('initializes immediately on first packet without waiting for 1s interval', () => {
      const hasKnownTotal = ref(true)
      const totalBytes = ref(100_000_000) // 100MB
      const downloadedBytes = ref(0)
      const rawSpeed = ref(10_000_000) // 10MB/s -> 10s
      const isActive = ref(true)

      const eta = useTaskEta({
        hasKnownTotal,
        totalBytes,
        downloadedBytes,
        rawSpeed,
        isActive,
      })

      // Immediate display at t=0
      expect(eta.value).toBe('10s')
    })

    it('shields UI from high-frequency sub-second speed oscillations', () => {
      const hasKnownTotal = ref(true)
      const totalBytes = ref(100_000_000)
      const downloadedBytes = ref(0)
      const rawSpeed = ref(10_000_000)
      const isActive = ref(true)

      const eta = useTaskEta({
        hasKnownTotal,
        totalBytes,
        downloadedBytes,
        rawSpeed,
        isActive,
      })

      expect(eta.value).toBe('10s')

      // High frequency updates arrive at 200ms and 400ms
      downloadedBytes.value = 2_000_000
      rawSpeed.value = 15_000_000
      vi.advanceTimersByTime(200)
      // ETA must not mutate at 200ms
      expect(eta.value).toBe('10s')

      downloadedBytes.value = 4_000_000
      rawSpeed.value = 8_000_000
      vi.advanceTimersByTime(200)
      expect(eta.value).toBe('10s')

      // At 1000ms tick, ETA updates smoothly
      downloadedBytes.value = 10_000_000
      vi.advanceTimersByTime(600) // Total 1000ms
      expect(eta.value).toBe('9s')
    })

    it('counts down naturally by 1s when speed is steady', () => {
      const hasKnownTotal = ref(true)
      const totalBytes = ref(500_000_000) // 500MB
      const downloadedBytes = ref(0)
      const rawSpeed = ref(10_000_000) // 10MB/s -> 50s
      const isActive = ref(true)

      const eta = useTaskEta({
        hasKnownTotal,
        totalBytes,
        downloadedBytes,
        rawSpeed,
        isActive,
      })

      expect(eta.value).toBe('50s')

      // Second 1
      downloadedBytes.value = 10_000_000
      vi.advanceTimersByTime(1000)
      expect(eta.value).toBe('49s')

      // Second 2
      downloadedBytes.value = 20_000_000
      vi.advanceTimersByTime(1000)
      expect(eta.value).toBe('48s')

      // Second 3
      downloadedBytes.value = 30_000_000
      vi.advanceTimersByTime(1000)
      expect(eta.value).toBe('47s')
    })

    it('adapts sliding window smoothly when download speed changes drastically', () => {
      const hasKnownTotal = ref(true)
      const totalBytes = ref(1_000_000_000) // 1GB
      const downloadedBytes = ref(0)
      const rawSpeed = ref(20_000_000) // 20MB/s -> 50s
      const isActive = ref(true)

      const eta = useTaskEta({
        hasKnownTotal,
        totalBytes,
        downloadedBytes,
        rawSpeed,
        isActive,
      })

      expect(eta.value).toBe('50s')

      // Run 3 seconds at 20MB/s
      downloadedBytes.value = 20_000_000
      vi.advanceTimersByTime(1000)
      downloadedBytes.value = 40_000_000
      vi.advanceTimersByTime(1000)
      downloadedBytes.value = 60_000_000
      vi.advanceTimersByTime(1000)

      // Speed drops: only 2MB in the next second
      downloadedBytes.value = 62_000_000
      rawSpeed.value = 2_000_000
      vi.advanceTimersByTime(1000)

      // The 4s window speed is (62MB - 0MB) / 4s = 15.5MB/s
      // remaining = 938MB, 938MB / 15.5MB/s = 60s (1m 0s)
      expect(eta.value).toBe('1m 0s')
    })

    it('returns -- when total is unknown', () => {
      const eta = useTaskEta({
        hasKnownTotal: ref(false),
        totalBytes: ref(0),
        downloadedBytes: ref(100),
        rawSpeed: ref(10),
        isActive: ref(true),
      })

      expect(eta.value).toBe('--')
    })

    it('immediately resets to -- when task is paused or stopped', () => {
      const hasKnownTotal = ref(true)
      const totalBytes = ref(100_000_000)
      const downloadedBytes = ref(10_000_000)
      const rawSpeed = ref(10_000_000)
      const isActive = ref(true)

      const eta = useTaskEta({
        hasKnownTotal,
        totalBytes,
        downloadedBytes,
        rawSpeed,
        isActive,
      })

      expect(eta.value).toBe('9s')

      // Pause task
      isActive.value = false
      expect(eta.value).toBe('--')

      // Resume task
      isActive.value = true
      expect(eta.value).toBe('9s')
    })
  })
})
